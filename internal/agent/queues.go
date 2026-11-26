package agent

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
	"github.com/openstatushq/stromboli/internal/catalog"
	"github.com/openstatushq/stromboli/internal/connector/metrics"
	"github.com/openstatushq/stromboli/internal/policy"
)

// briefQueues is the brief section for a service's queues (ADR 0024).
func (e *Engine) briefQueues(ctx context.Context, svc *stromboliv1.Service, w Window) string {
	parts := make([]string, len(svc.Queues))
	var wg sync.WaitGroup
	for i, q := range svc.Queues {
		wg.Add(1)
		go func() {
			defer wg.Done()
			src, kind, err := e.connectors.Metrics(ctx, q.Source)
			switch {
			case err != nil:
				parts[i] = queueHeading(q) + fmt.Sprintf("_Metrics source unavailable: %v_\n", err)
			case kind != "gcp_monitoring":
				parts[i] = queueHeading(q) + fmt.Sprintf("_Connector %q is a %s connector; queues need gcp_monitoring._\n", q.Source, kind)
			default:
				parts[i] = renderQueue(ctx, src, q, w)
			}
		}()
	}
	wg.Wait()
	return strings.Join(parts, "\n") + "Pub/Sub metrics arrive 2 to 6 minutes late: the last minutes of the window may be missing. More with get_metrics and its queue argument.\n"
}

func queueHeading(q *stromboliv1.QueueConfig) string {
	return fmt.Sprintf("### %s (%s, %s)\n\n", q.Name, q.Role, q.Source)
}

// queueTotal is one aggregate over all the queue's subscriptions or topics.
type queueTotal struct {
	name, unit, expr string
	topics           bool // reads the topics, not the subscriptions
	// noneIfEmpty reads no series as none (a count of failures).
	noneIfEmpty bool
}

// queueSignal is a failure signal counted per subscription (or topic).
type queueSignal struct {
	name  string
	label string // subscription_id or topic_id
	unit  string
	// expr uses $selector and aggregates by label.
	expr string
	// hit reports whether one subscription shows the signal. ok is false
	// when it had no series in that window.
	hit func(cur, base subStats) bool
	// worst orders the affected ones; by peak by default.
	worst func(s subStats) float64
	// relative signals compare the window with the baseline, so they have
	// no baseline count of their own.
	relative bool
	// namesOnly lists the affected ones without a value.
	namesOnly bool
}

// subStats summarises one series over a window.
type subStats struct {
	ok             bool
	avg, max, last float64
}

func statsOf(samples []metrics.Sample) subStats {
	var s subStats
	var total float64
	n := 0
	for _, p := range samples {
		if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			continue
		}
		if n == 0 || p.Value > s.max {
			s.max = p.Value
		}
		total += p.Value
		s.last = p.Value
		n++
	}
	if n > 0 {
		s.ok, s.avg = true, total/float64(n)
	}
	return s
}

const pubsub = "pubsub_googleapis_com:"

func consumerTotals() []queueTotal {
	return []queueTotal{
		{name: "published", unit: "msg/s", topics: true, expr: `sum(rate(` + pubsub + `topic_message_sizes_count{$selector}[5m]))`},
		{name: "acknowledged", unit: "msg/s", expr: `sum(rate(` + pubsub + `subscription_ack_message_count{$selector}[5m]))`},
		{name: "backlog", unit: "", expr: `sum(` + pubsub + `subscription_num_undelivered_messages{$selector})`},
		{name: "ack latency p50", unit: "s", expr: `histogram_quantile(0.5, sum by (le) (rate(` + pubsub + `subscription_ack_latencies_bucket{$selector}[5m]))) / 1000`},
		{name: "ack latency p99", unit: "s", expr: `histogram_quantile(0.99, sum by (le) (rate(` + pubsub + `subscription_ack_latencies_bucket{$selector}[5m]))) / 1000`},
	}
}

func producerTotals() []queueTotal {
	return []queueTotal{
		{name: "published", unit: "msg/s", topics: true, expr: `sum(rate(` + pubsub + `topic_message_sizes_count{$selector}[5m]))`},
		{name: "failed publish requests", unit: "req/s", topics: true, noneIfEmpty: true, expr: `sum(rate(` + pubsub + `topic_send_request_count{$selector, response_class!="success"}[5m]))`},
		{name: "publish latency p99", unit: "s", topics: true, expr: `histogram_quantile(0.99, sum by (le) (rate(` + pubsub + `topic_send_request_latencies_bucket{$selector}[5m]))) / 1e6`},
	}
}

// consumerSignals are checked on every subscription. Thresholds come from a
// production setup: a 1-minute drop to 0 open pulls and short redelivery
// peaks are normal, so they are not counted.
func consumerSignals() []queueSignal {
	positive := func(c, _ subStats) bool { return c.ok && c.max > 0 }
	return []queueSignal{
		{name: "nacks (messages rejected by consumers)", label: "subscription_id",
			expr: `max by (subscription_id) (` + pubsub + `subscription_nack_requests{$selector})`, hit: positive},
		{name: "redeliveries (deliveries per ack above 1.1 on average)", label: "subscription_id",
			expr: `sum by (subscription_id) (rate(` + pubsub + `subscription_sent_message_count{$selector}[5m])) / sum by (subscription_id) (rate(` + pubsub + `subscription_ack_message_count{$selector}[5m]))`,
			hit:  func(c, _ subStats) bool { return c.ok && c.avg > 1.1 }, worst: func(s subStats) float64 { return s.avg }},
		{name: "dead-lettered messages", label: "subscription_id", unit: "msg/s",
			expr: `sum by (subscription_id) (rate(` + pubsub + `subscription_dead_letter_message_count{$selector}[5m]))`, hit: positive},
		{name: "expired ack deadlines", label: "subscription_id", unit: "msg/s",
			expr: `sum by (subscription_id) (rate(` + pubsub + `subscription_expired_ack_deadlines_count{$selector}[5m]))`, hit: positive},
		{name: "no consumer connected (no open streaming pull in the whole window)", label: "subscription_id",
			expr: `sum by (subscription_id) (` + pubsub + `subscription_open_streaming_pulls{$selector})`,
			hit:  func(c, _ subStats) bool { return c.ok && c.max == 0 }, namesOnly: true},
		{name: "streams suspended (flow control or quota)", label: "subscription_id",
			expr: `sum by (subscription_id) (` + pubsub + `subscription_open_streaming_pulls{$selector, status!="ok"})`, hit: positive},
		{name: "backlog above twice its baseline", label: "subscription_id",
			expr: `sum by (subscription_id) (` + pubsub + `subscription_num_undelivered_messages{$selector})`,
			hit:  func(c, b subStats) bool { return c.ok && b.ok && c.avg > 100 && c.avg > 2*b.avg }, worst: func(s subStats) float64 { return s.avg }, relative: true},
		{name: "oldest unacked message above twice its baseline", label: "subscription_id", unit: "s",
			expr: `max by (subscription_id) (` + pubsub + `subscription_oldest_unacked_message_age{$selector})`,
			hit:  func(c, b subStats) bool { return c.ok && b.ok && c.max > 120 && c.max > 2*b.max }, relative: true},
	}
}

func producerSignals() []queueSignal {
	return []queueSignal{
		{name: "failed publish requests", label: "topic_id", unit: "req/s",
			expr: `sum by (topic_id) (rate(` + pubsub + `topic_send_request_count{$selector, response_class!="success"}[5m]))`,
			hit:  func(c, _ subStats) bool { return c.ok && c.max > 0 }},
		{name: "stopped publishing (published in the baseline, not in the window)", label: "topic_id", unit: "msg/s",
			expr: `sum by (topic_id) (rate(` + pubsub + `topic_message_sizes_count{$selector}[5m]))`,
			hit:  func(c, b subStats) bool { return b.ok && b.max > 0 && (!c.ok || c.max == 0) }, namesOnly: true, relative: true},
	}
}

// renderQueue runs the queue's totals and signals over the window and its
// baseline.
func renderQueue(ctx context.Context, src metrics.Source, q *stromboliv1.QueueConfig, w Window) string {
	subs, topics := metrics.Matchers(q.Subscriptions), metrics.Matchers(q.Topics)
	var totals []queueTotal
	var signals []queueSignal
	switch q.Role {
	case catalog.QueueProducer:
		totals, signals = producerTotals(), producerSignals()
	default:
		for _, t := range consumerTotals() {
			if !t.topics || topics != "" {
				totals = append(totals, t)
			}
		}
		signals = consumerSignals()
	}
	matchersFor := func(label string, topicSide bool) string {
		if topicSide || label == "topic_id" {
			return topics
		}
		return subs
	}

	totalOut := make([]string, len(totals))
	signalOut := make([]string, len(signals))
	// The resources seen by each signal, to say "n of N".
	seen := make([]map[string]bool, len(signals))
	var wg sync.WaitGroup
	for i, t := range totals {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := analyzeMetric(ctx, src, metrics.Expand(t.expr, matchersFor("", t.topics)), w, 1, 0)
			totalOut[i] = formatQueueTotal(t, a, err)
		}()
	}
	for i, s := range signals {
		wg.Add(1)
		go func() {
			defer wg.Done()
			signalOut[i], seen[i] = runQueueSignal(ctx, src, s, metrics.Expand(s.expr, matchersFor(s.label, false)), w)
		}()
	}
	wg.Wait()

	all := map[string]bool{}
	for _, m := range seen {
		for k := range m {
			all[k] = true
		}
	}
	noun := "subscriptions"
	if q.Role == catalog.QueueProducer {
		noun = "topics"
	}
	var b strings.Builder
	b.WriteString(queueHeading(q))
	fmt.Fprintf(&b, "%d %s. Totals: window average, peak and last value (baseline average).\n\n", len(all), noun)
	for _, t := range totalOut {
		b.WriteString(t)
	}
	fmt.Fprintf(&b, "\nFailure signals: %s affected in the window (in the baseline), worst first.\n\n", noun)
	for _, s := range signalOut {
		b.WriteString(strings.ReplaceAll(s, "{N}", fmt.Sprint(len(all))))
	}
	return b.String()
}

func formatQueueTotal(t queueTotal, a *metricAnalysis, err error) string {
	switch {
	case err != nil:
		return fmt.Sprintf("- %s: _query failed: %s_\n", t.name, truncate(policy.Redact(err.Error()), 200))
	case len(a.Series) == 0 && t.noneIfEmpty:
		return fmt.Sprintf("- %s: none\n", t.name)
	case len(a.Series) == 0:
		return fmt.Sprintf("- %s: no data\n", t.name)
	}
	s := a.Series[0]
	f := func(v float64) string { return metrics.FormatValue(v, t.unit) }
	base := "no baseline data"
	if s.Baseline != nil {
		base = "baseline " + f(*s.Baseline)
	}
	return fmt.Sprintf("- %s: avg %s, peak %s, last %s (%s)\n", t.name, f(s.Avg), f(s.Max), f(s.Last), base)
}

// maxWorst bounds the resources named per signal.
const maxWorst = 5

// runQueueSignal counts the resources showing the signal in the window and
// in the baseline. "{N}" in the line is the queue's resource count.
func runQueueSignal(ctx context.Context, src metrics.Source, s queueSignal, expr string, w Window) (string, map[string]bool) {
	var (
		cur, base     []metrics.Series
		errCur, errBs error
		wg            sync.WaitGroup
	)
	bw := w.Baseline()
	wg.Add(2)
	go func() { defer wg.Done(); cur, errCur = src.Range(ctx, expr, w.Start, w.End) }()
	go func() { defer wg.Done(); base, errBs = src.Range(ctx, expr, bw.Start, bw.End) }()
	wg.Wait()
	if errCur != nil {
		return fmt.Sprintf("- %s: _query failed: %s_\n", s.name, truncate(policy.Redact(errCur.Error()), 200)), nil
	}
	byKey := func(series []metrics.Series) map[string]subStats {
		out := map[string]subStats{}
		for _, x := range series {
			if k := x.Labels[s.label]; k != "" {
				out[k] = statsOf(x.Samples)
			}
		}
		return out
	}
	c, bs := byKey(cur), byKey(base)
	seen := map[string]bool{}
	for k := range c {
		seen[k] = true
	}
	for k := range bs {
		seen[k] = true
	}

	worst := s.worst
	if worst == nil {
		worst = func(x subStats) float64 { return x.max }
	}
	var hits []string
	baseHits := 0
	for k := range seen {
		if s.hit(c[k], bs[k]) {
			hits = append(hits, k)
		}
		if !s.relative && s.hit(bs[k], subStats{}) {
			baseHits++
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if a, b := worst(c[hits[i]]), worst(c[hits[j]]); a != b {
			return a > b
		}
		return hits[i] < hits[j]
	})

	line := fmt.Sprintf("- %s: %d of {N}", s.name, len(hits))
	switch {
	case s.relative && errBs != nil:
		line += " (no baseline: " + truncate(policy.Redact(errBs.Error()), 100) + ")"
	case s.relative:
	case errBs != nil:
		line += " (baseline unknown)"
	default:
		line += fmt.Sprintf(" (baseline %d)", baseHits)
	}
	if len(hits) > 0 {
		names := make([]string, 0, maxWorst)
		for i, k := range hits {
			if i == maxWorst {
				names = append(names, fmt.Sprintf("… %d more", len(hits)-i))
				break
			}
			if s.namesOnly {
				names = append(names, k)
				continue
			}
			names = append(names, fmt.Sprintf("%s %s", k, metrics.FormatValue(worst(c[k]), s.unit)))
		}
		line += ": " + strings.Join(names, ", ")
	}
	return line + "\n", seen
}
