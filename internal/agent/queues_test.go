package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
	"github.com/openstatushq/stromboli/internal/connector/metrics"
)

// fakeQueueSource answers per subscription, by the metric in the query and
// whether it is the window or the baseline.
type fakeQueueSource struct {
	window Window
	// values gives each subscription's samples for a metric name fragment.
	values func(metric, sub string, window bool) []float64
	subs   []string
	fail   string
	exprs  []string
}

func (f *fakeQueueSource) Range(_ context.Context, expr string, start, _ time.Time) ([]metrics.Series, error) {
	f.exprs = append(f.exprs, expr)
	if f.fail != "" && strings.Contains(expr, f.fail) {
		return nil, errors.New("boom")
	}
	inWindow := start.Equal(f.window.Start)
	metric := expr[strings.Index(expr, pubsub)+len(pubsub):]
	metric = metric[:strings.IndexAny(metric, "{")]
	if strings.Contains(expr, `status!="ok"`) {
		metric += ":suspended"
	}
	// Totals aggregate everything into one series.
	if !strings.Contains(expr, "by (subscription_id") && !strings.Contains(expr, "by (topic_id") {
		return []metrics.Series{{Labels: map[string]string{}, Samples: samples(start, f.values(metric, "", inWindow))}}, nil
	}
	label := "subscription_id"
	if strings.Contains(expr, "by (topic_id") {
		label = "topic_id"
	}
	var out []metrics.Series
	for _, s := range f.subs {
		v := f.values(metric, s, inWindow)
		if v == nil {
			continue
		}
		out = append(out, metrics.Series{Labels: map[string]string{label: s}, Samples: samples(start, v)})
	}
	return out, nil
}

func samples(start time.Time, vals []float64) []metrics.Sample {
	out := make([]metrics.Sample, len(vals))
	for i, v := range vals {
		out[i] = metrics.Sample{Time: start.Add(time.Duration(i) * time.Minute), Value: v}
	}
	return out
}

func (f *fakeQueueSource) MetricNames(context.Context, string, time.Time, time.Time) ([]string, error) {
	return nil, nil
}
func (f *fakeQueueSource) Services(context.Context) ([]metrics.Service, error) { return nil, nil }
func (f *fakeQueueSource) Check(context.Context) error                         { return nil }

func TestRenderQueueConsumer(t *testing.T) {
	end := time.Date(2026, 10, 22, 11, 30, 0, 0, time.UTC)
	w := Window{Start: end.Add(-30 * time.Minute), End: end}
	var subs []string
	for i := range 10 {
		subs = append(subs, fmt.Sprintf("checker-r%d", i))
	}
	src := &fakeQueueSource{window: w, subs: subs, values: func(metric, sub string, inWindow bool) []float64 {
		switch {
		case strings.HasPrefix(metric, "subscription_nack_requests"):
			// 8 of 10 subscriptions nack in the window, none before.
			if !inWindow || sub == "checker-r8" || sub == "checker-r9" {
				return nil
			}
			n := float64(100 * (len(sub) + int(sub[len(sub)-1]-'0')))
			return []float64{n / 2, n, 0}
		case metric == "subscription_open_streaming_pulls:suspended":
			if sub == "checker-r2" && inWindow {
				return []float64{0, 1, 0}
			}
			return nil
		case strings.HasPrefix(metric, "subscription_open_streaming_pulls"):
			if strings.Contains(sub, "r9") && inWindow {
				return []float64{0, 0, 0} // stopped consumer
			}
			if strings.Contains(sub, "r3") {
				return []float64{1, 0, 1} // a reconnect: not counted
			}
			return []float64{2, 2, 2}
		case strings.HasPrefix(metric, "subscription_num_undelivered_messages"):
			if sub == "checker-r9" && inWindow {
				return []float64{400, 900, 1500}
			}
			return []float64{50, 60, 70}
		case strings.HasPrefix(metric, "subscription_sent_message_count"):
			return []float64{1, 1.05, 1}
		case strings.HasPrefix(metric, "topic_message_sizes_count"):
			return []float64{90, 92, 91}
		case strings.HasPrefix(metric, "subscription_ack_message_count"):
			return []float64{80, 75, 70}
		case strings.HasPrefix(metric, "subscription_expired_ack_deadlines_count"):
			if sub == "checker-r1" {
				return []float64{0, 0.2, 0} // also in the baseline: noise
			}
		case strings.HasPrefix(metric, "subscription_oldest_unacked_message_age"):
			return []float64{90, 100, 95}
		case strings.HasPrefix(metric, "subscription_ack_latencies_bucket"):
			return []float64{3, 3, 3}
		}
		return nil
	}}
	q := &stromboliv1.QueueConfig{
		Name: "checks", Source: "gcp-metrics", Role: "consumer",
		Subscriptions: []string{`subscription_id=~"checker-.*"`, `subscription_id!="checker-dead-letter"`},
		Topics:        []string{`topic_id=~"checker-.*"`},
	}
	out := renderQueue(context.Background(), src, q, w)

	for _, want := range []string{
		"### checks (consumer, gcp-metrics)",
		"10 subscriptions.",
		"- published: avg 91 msg/s",
		"- acknowledged: avg 75 msg/s",
		"- nacks (messages rejected by consumers): 8 of 10 (baseline 0): checker-r7 1700, checker-r6 1600, checker-r5 1500, checker-r4 1400, checker-r3 1300, … 3 more",
		"- no consumer connected (no open streaming pull in the whole window): 1 of 10 (baseline 0): checker-r9\n",
		"- backlog above twice its baseline: 1 of 10: checker-r9 933.33",
		"- streams suspended (flow control or quota): 1 of 10 (baseline 0): checker-r2 1\n",
		"- expired ack deadlines: 1 of 10 (baseline 1): checker-r1 0.2 msg/s",
		"- redeliveries (deliveries per ack above 1.1 on average): 0 of 10 (baseline 0)\n",
		"- oldest unacked message above twice its baseline: 0 of 10\n",
		"- dead-lettered messages: 0 of 10 (baseline 0)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, e := range src.exprs {
		if strings.Contains(e, "topic_") && !strings.Contains(e, `topic_id=~"checker-.*"`) ||
			strings.Contains(e, "subscription_") && !strings.Contains(e, `subscription_id!="checker-dead-letter"`) {
			t.Errorf("query on the wrong selector: %s", e)
		}
	}
}

func TestRenderQueueProducerAndFailures(t *testing.T) {
	end := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	w := Window{Start: end.Add(-30 * time.Minute), End: end}
	src := &fakeQueueSource{window: w, subs: []string{"checker-ams", "checker-fra"}, fail: "send_request_latencies",
		values: func(metric, topic string, inWindow bool) []float64 {
			if strings.HasPrefix(metric, "topic_message_sizes_count") {
				if topic == "checker-fra" && inWindow {
					return nil // stopped publishing
				}
				return []float64{5, 5}
			}
			return nil
		}}
	q := &stromboliv1.QueueConfig{Name: "checks", Source: "gcp-metrics", Role: "producer", Topics: []string{`topic_id=~"checker-.*"`}}
	out := renderQueue(context.Background(), src, q, w)
	for _, want := range []string{
		"### checks (producer, gcp-metrics)",
		"2 topics.",
		"- stopped publishing (published in the baseline, not in the window): 1 of 2: checker-fra\n",
		"- failed publish requests: 0 of 2 (baseline 0)\n",
		"- failed publish requests: none\n",
		"- publish latency p99: _query failed: boom_",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "nacks") || strings.Contains(out, "acknowledged") {
		t.Errorf("a producer only reads its topics:\n%s", out)
	}
}
