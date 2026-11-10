package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
	"github.com/openstatushq/stromboli/internal/connector/metrics"
	"github.com/openstatushq/stromboli/internal/policy"
)

// metricAnalysis is the summarised evidence for one metric query.
type metricAnalysis struct {
	Metric string            `json:"metric,omitempty"`
	Query  string            `json:"query"`
	Unit   string            `json:"unit,omitempty"`
	Series []metrics.Summary `json:"series"`
	// TotalSeries is the number of series before capping.
	TotalSeries int `json:"total_series"`
}

// analyzeMetric runs expr over the window and its baseline and summarises
// each series against its own baseline average.
func analyzeMetric(ctx context.Context, src metrics.Source, expr string, w Window, limit, points int) (*metricAnalysis, error) {
	var (
		cur, base     []metrics.Series
		errCur, errBs error
		wg            sync.WaitGroup
	)
	b := w.Baseline()
	wg.Add(2)
	go func() { defer wg.Done(); cur, errCur = src.Range(ctx, expr, w.Start, w.End) }()
	go func() { defer wg.Done(); base, errBs = src.Range(ctx, expr, b.Start, b.End) }()
	wg.Wait()
	if errCur != nil {
		return nil, errCur
	}
	if errBs != nil {
		base = nil // the window alone is still useful
	}
	return &metricAnalysis{
		Query:       expr,
		Series:      metrics.Summarize(cur, base, limit, points),
		TotalSeries: len(cur),
	}, nil
}

func metricsTarget(svc *stromboliv1.Service) (source, matchers string, err error) {
	m := svc.Metrics
	if m == nil || m.Source == "" {
		return "", "", fmt.Errorf("service %q has no metrics source configured", svc.Name)
	}
	matchers = metrics.Matchers(m.Selectors)
	if matchers == "" {
		return "", "", fmt.Errorf("service %q has no metrics selectors configured", svc.Name)
	}
	return m.Source, matchers, nil
}

// briefMetrics shows the brief presets of the service's metrics connector:
// per series, the window's average, peak and last value against the baseline.
func (e *Engine) briefMetrics(ctx context.Context, svc *stromboliv1.Service, w Window) string {
	if svc.Metrics == nil || svc.Metrics.Source == "" {
		return "_No metrics source configured for this service._\n"
	}
	source, matchers, err := metricsTarget(svc)
	if err != nil {
		return fmt.Sprintf("_%v_\n", err)
	}
	src, kind, err := e.connectors.Metrics(ctx, source)
	if err != nil {
		return fmt.Sprintf("_Metrics source unavailable: %v_\n", err)
	}
	var presets []metrics.Preset
	for _, p := range metrics.PresetsFor(kind) {
		if p.Brief && p.Applies(matchers) {
			presets = append(presets, p)
		}
	}
	if len(presets) == 0 {
		return fmt.Sprintf("_No metric presets for %s connectors: use list_metrics and get_metrics with a PromQL query._\n", kind)
	}

	results := make([]string, len(presets))
	var wg sync.WaitGroup
	for i, p := range presets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := analyzeMetric(ctx, src, metrics.Expand(p.Expr, matchers), w, 3, 0)
			results[i] = formatMetricBrief(p, a, err)
		}()
	}
	wg.Wait()

	var b strings.Builder
	b.WriteString("Per series: window average, peak and last value (baseline average). More with get_metrics.\n\n")
	for _, r := range results {
		b.WriteString(r)
	}
	return b.String()
}

func formatMetricBrief(p metrics.Preset, a *metricAnalysis, err error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- **%s**", p.Name)
	switch {
	case err != nil:
		fmt.Fprintf(&b, ": _query failed: %s_\n", truncate(policy.Redact(err.Error()), 200))
		return b.String()
	case len(a.Series) == 0:
		b.WriteString(": no data\n")
		return b.String()
	}
	b.WriteString("\n")
	f := func(v float64) string { return metrics.FormatValue(v, p.Unit) }
	for _, s := range a.Series {
		base := "no baseline data"
		if s.Baseline != nil {
			base = "baseline " + f(*s.Baseline)
		}
		fmt.Fprintf(&b, "  - %s: avg %s, peak %s, last %s (%s)\n", metrics.FormatLabels(s.Labels), f(s.Avg), f(s.Max), f(s.Last), base)
	}
	if n := a.TotalSeries - len(a.Series); n > 0 {
		fmt.Fprintf(&b, "  - … %d more series\n", n)
	}
	return b.String()
}

type metricArgs struct {
	Service      string `json:"service"`
	Metric       string `json:"metric"`
	Query        string `json:"query"`
	SinceMinutes int    `json:"since_minutes"`
	UntilMinutes int    `json:"until_minutes"`
}

func runGetMetrics(ctx context.Context, tc *toolContext, in json.RawMessage) (any, error) {
	var args metricArgs
	if err := json.Unmarshal(in, &args); err != nil {
		return nil, err
	}
	svc, err := tc.service(args.Service)
	if err != nil {
		return nil, err
	}
	source, matchers, err := metricsTarget(svc)
	if err != nil {
		return nil, err
	}
	src, kind, err := tc.engine.connectors.Metrics(ctx, source)
	if err != nil {
		return nil, err
	}
	var expr, unit string
	switch {
	case args.Metric != "":
		p, ok := metrics.FindPreset(kind, args.Metric)
		if !ok {
			return nil, fmt.Errorf("unknown metric %q for %s connectors (presets: %s); pass a PromQL query instead", args.Metric, kind, presetNames(kind))
		}
		expr, unit = metrics.Expand(p.Expr, matchers), p.Unit
	case args.Query != "":
		expr = metrics.Expand(args.Query, matchers)
	default:
		return nil, fmt.Errorf("pass metric (presets: %s) or query", presetNames(kind))
	}
	since := clamp(args.SinceMinutes, 60, 1440)
	until := max(0, min(args.UntilMinutes, since-1))
	w := Window{
		Start: tc.now.Add(-time.Duration(since) * time.Minute),
		End:   tc.now.Add(-time.Duration(until) * time.Minute),
	}
	a, err := analyzeMetric(ctx, src, expr, w, 10, 12)
	if err != nil {
		return nil, err
	}
	a.Metric, a.Unit = args.Metric, unit
	return map[string]any{"window": w, "baseline": w.Baseline(), "points_per_series": 12, "result": a}, nil
}

func runListMetrics(ctx context.Context, tc *toolContext, in json.RawMessage) (any, error) {
	var args struct{ Service string }
	if err := json.Unmarshal(in, &args); err != nil {
		return nil, err
	}
	svc, err := tc.service(args.Service)
	if err != nil {
		return nil, err
	}
	source, matchers, err := metricsTarget(svc)
	if err != nil {
		return nil, err
	}
	src, kind, err := tc.engine.connectors.Metrics(ctx, source)
	if err != nil {
		return nil, err
	}
	type preset struct {
		Name        string `json:"name"`
		Unit        string `json:"unit,omitempty"`
		Description string `json:"description"`
	}
	presets := []preset{}
	for _, p := range metrics.PresetsFor(kind) {
		presets = append(presets, preset{p.Name, p.Unit, p.Description})
	}
	names, err := src.MetricNames(ctx, matchers, tc.now.Add(-time.Hour), tc.now)
	if err != nil {
		return nil, err
	}
	const maxNames = 300
	total := len(names)
	if total > maxNames {
		names = names[:maxNames]
	}
	return map[string]any{
		"selector":     "{" + matchers + "}",
		"presets":      presets,
		"metric_names": names,
		"total_names":  total,
	}, nil
}

func presetNames(kind string) string {
	var names []string
	for _, p := range metrics.PresetsFor(kind) {
		names = append(names, p.Name)
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
