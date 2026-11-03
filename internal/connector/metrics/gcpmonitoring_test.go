package metrics

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openstatushq/opensre/internal/connector/mcpconn/mcptest"
)

func TestGCPMonitoringRange(t *testing.T) {
	srv := mcptest.New(t, map[string]mcptest.Handler{
		"query_range": func(args map[string]any) (any, error) {
			if strings.Contains(args["query"].(string), "run_googleapis_com") {
				return nil, errors.New("") // what Cloud Monitoring sends for a metric without data
			}
			return map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{
				map[string]any{"metric": map[string]string{"subscription_id": "checker-ams"}, "values": []any{[]any{1793694695.0, "7"}, []any{1793694995.0, "18"}}},
			}}}, nil
		},
		"list_metric_descriptors": func(args map[string]any) (any, error) {
			return map[string]any{"metricDescriptors": []map[string]string{
				{"type": "cloudtasks.googleapis.com/queue/depth", "valueType": "INT64"},
				{"type": "cloudtasks.googleapis.com/queue/task_attempt_delays", "valueType": "DISTRIBUTION"},
			}}, nil
		},
	}, false)
	g := NewGCPMonitoring("p", srv.Client(t, GCPMonitoringTools...))
	ctx := context.Background()
	end := time.Unix(1793698295, 0)

	series, err := g.Range(ctx, `max by (subscription_id) (pubsub_googleapis_com:subscription_num_undelivered_messages{subscription_id="checker-ams"})`, end.Add(-time.Hour), end)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || len(series[0].Samples) != 2 || series[0].Samples[1].Value != 18 {
		t.Fatalf("series = %+v", series)
	}
	args := srv.Calls()[0].Args
	if args["name"] != "projects/p" || args["location"] != "global" || args["step"] != "15s" {
		t.Errorf("args = %v", args)
	}

	_, err = g.Range(ctx, `run_googleapis_com:request_count`, end.Add(-time.Hour), end)
	if err == nil || !strings.Contains(err.Error(), "has no data in this project") {
		t.Errorf("err = %v, want the no data hint", err)
	}

	names, err := g.MetricNames(ctx, `queue_id="1m"`, end, end)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "cloudtasks_googleapis_com:queue_depth,cloudtasks_googleapis_com:queue_task_attempt_delays_bucket" {
		t.Errorf("names = %v", names)
	}
	if f := srv.Calls()[2].Args["filter"]; f != `metric.type = starts_with("cloudtasks.googleapis.com/")` {
		t.Errorf("filter = %v", f)
	}
}

func TestPromQLName(t *testing.T) {
	for in, want := range map[string]string{
		"run.googleapis.com/request_count":                            "run_googleapis_com:request_count",
		"pubsub.googleapis.com/subscription/num_undelivered_messages": "pubsub_googleapis_com:subscription_num_undelivered_messages",
		"prometheus.googleapis.com/http_requests_total/counter":       "http_requests_total",
	} {
		if got := PromQLName(in); got != want {
			t.Errorf("PromQLName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPresetApplies(t *testing.T) {
	p, _ := FindPreset("gcp_monitoring", "pubsub_backlog_messages")
	if !p.Applies(`subscription_id="checker-ams"`) || p.Applies(`service_name="api"`) {
		t.Error("pubsub presets should only apply to subscriptions")
	}
	f, _ := FindPreset("fly", "cpu_percent")
	if !f.Applies(`app="x"`) {
		t.Error("presets without Requires always apply")
	}
}
