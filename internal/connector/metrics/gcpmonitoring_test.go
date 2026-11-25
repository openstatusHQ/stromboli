package metrics

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openstatushq/stromboli/internal/connector/mcpconn/mcptest"
)

func TestGCPMonitoringRange(t *testing.T) {
	srv := mcptest.New(t, map[string]mcptest.Handler{
		"query_range": func(args map[string]any) (any, error) {
			if strings.Contains(args["query"].(string), "run_googleapis_com") {
				return nil, errors.New("") // what the MCP server sends for a query it rejects
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
	if err == nil || !strings.Contains(err.Error(), "monitored_resource") {
		t.Errorf("err = %v, want the rejected query hint", err)
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

// A tool error with a message is passed on, without the hint.
func TestGCPMonitoringRangeError(t *testing.T) {
	srv := mcptest.New(t, map[string]mcptest.Handler{
		"query_range":             func(map[string]any) (any, error) { return nil, errors.New("permission denied") },
		"list_metric_descriptors": func(map[string]any) (any, error) { return map[string]any{}, nil },
	}, false)
	g := NewGCPMonitoring("p", srv.Client(t, GCPMonitoringTools...))
	end := time.Unix(1793698295, 0)
	_, err := g.Range(context.Background(), "vector(1)", end.Add(-time.Hour), end)
	if err == nil || strings.Contains(err.Error(), "monitored_resource") || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("err = %v, want permission denied", err)
	}
}

func TestGCPMonitoringServicesNotCapped(t *testing.T) {
	const n = maxSeries + 4
	srv := mcptest.New(t, map[string]mcptest.Handler{
		"query_range": func(args map[string]any) (any, error) {
			if !strings.Contains(args["query"].(string), "pubsub_googleapis_com") {
				return nil, errors.New("") // a rejected product does not hide the others
			}
			var result []any
			for i := range n {
				result = append(result, map[string]any{
					"metric": map[string]string{"subscription_id": fmt.Sprintf("checker-%02d", i)},
					"values": []any{[]any{1793694695.0, "1"}},
				})
			}
			return map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": result}}, nil
		},
		"list_metric_descriptors": func(map[string]any) (any, error) { return map[string]any{}, nil },
	}, false)
	g := NewGCPMonitoring("p", srv.Client(t, GCPMonitoringTools...))

	services, err := g.Services(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != n {
		t.Fatalf("got %d services, want %d", len(services), n)
	}
	if last := services[n-1]; last.Name != "checker-53" || last.Selector != `subscription_id="checker-53"` {
		t.Errorf("last = %+v", last)
	}

	end := time.Unix(1793698295, 0)
	series, err := g.Range(context.Background(), "pubsub_googleapis_com:x", end.Add(-time.Hour), end)
	if err != nil || len(series) != maxSeries {
		t.Errorf("Range returned %d series, %v; want the cap of %d", len(series), err, maxSeries)
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

// Cloud Monitoring rejects Cloud Run queries that do not pick a resource type.
func TestGCPCloudRunResourceType(t *testing.T) {
	exprs := map[string]string{}
	for _, p := range GCPPresets {
		exprs[p.Name] = p.Expr
	}
	for _, d := range gcpDiscovery {
		exprs["discovery by "+d.label] = d.expr
	}
	found := 0
	for name, expr := range exprs {
		n := strings.Count(expr, "run_googleapis_com:")
		found += n
		if strings.Count(expr, `monitored_resource="cloud_run_revision"`) != n {
			t.Errorf("%s: no monitored_resource matcher in %s", name, expr)
		}
	}
	if found != 8 {
		t.Errorf("checked %d Cloud Run metrics, want 8", found)
	}
}

func TestApplicablePresets(t *testing.T) {
	for matchers, prefixes := range map[string][]string{
		`subscription_id="checker-ams"`:  {"pubsub_"},
		`subscription_id=~"checker-.*"`:  {"pubsub_"},
		`topic_id="checker-ams"`:         {"pubsub_publish"},
		`queue_id="alerting"`:            {"tasks_"},
		`service_name="api",region="eu"`: {"http_", "instances", "cpu_", "memory_", "startup_"},
	} {
		presets := ApplicablePresets("gcp_monitoring", matchers)
		if len(presets) == 0 {
			t.Errorf("%s: no presets", matchers)
		}
		for _, p := range presets {
			if !slices.ContainsFunc(prefixes, func(s string) bool { return strings.HasPrefix(p.Name, s) }) {
				t.Errorf("%s: unexpected preset %s", matchers, p.Name)
			}
		}
	}
	if got := ApplicablePresets("gcp_monitoring", `project_id="p"`); len(got) != 0 {
		t.Errorf("presets without a product label: %v", got)
	}
}

// rate() is for DELTA metrics, and DISTRIBUTION ones need a histogram suffix.
// Cloud Monitoring answers the wrong ones too, with wrong or no values.
func TestGCPPubSubPresetTypes(t *testing.T) {
	const (
		gauge = iota
		delta
		distribution
	)
	// https://cloud.google.com/monitoring/api/metrics_gcp_p_z#gcp-pubsub
	types := map[string]int{
		"subscription_num_undelivered_messages":    gauge,
		"subscription_oldest_unacked_message_age":  gauge,
		"subscription_open_streaming_pulls":        gauge,
		"subscription_nack_requests":               gauge,
		"subscription_ack_message_count":           delta,
		"subscription_sent_message_count":          delta,
		"subscription_expired_ack_deadlines_count": delta,
		"subscription_dead_letter_message_count":   delta,
		"topic_send_request_count":                 delta,
		"subscription_ack_latencies_bucket":        distribution,
		"topic_message_sizes_count":                distribution,
		"topic_send_request_latencies_bucket":      distribution,
	}
	re := regexp.MustCompile(`(rate\()?pubsub_googleapis_com:(\w+)\{\$selector`)
	for _, p := range GCPPresets {
		if !strings.HasPrefix(p.Name, "pubsub_") {
			continue
		}
		found := re.FindAllStringSubmatch(p.Expr, -1)
		if len(found) == 0 {
			t.Errorf("%s: no metric in %s", p.Name, p.Expr)
		}
		for _, m := range found {
			typ, ok := types[m[2]]
			if !ok {
				t.Errorf("%s: unknown metric %s", p.Name, m[2])
			} else if rated := m[1] != ""; rated != (typ != gauge) {
				t.Errorf("%s: rate() = %v on %s", p.Name, rated, m[2])
			}
			if want := p.Requires[:len(p.Requires)-len("_id")] + "_"; !strings.HasPrefix(m[2], want) {
				t.Errorf("%s: requires %s but queries %s", p.Name, p.Requires, m[2])
			}
		}
	}
}
