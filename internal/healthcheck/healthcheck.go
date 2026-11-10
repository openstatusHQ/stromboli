// Package healthcheck periodically reads each service's machine metrics and
// starts an investigation when one crosses a threshold: memory, CPU, root
// filesystem or volume close to full. It catches slow failures, like a volume
// filling up, before any alert fires.
package healthcheck

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
	"github.com/openstatushq/stromboli/internal/agent"
	"github.com/openstatushq/stromboli/internal/connector/metrics"
	"github.com/openstatushq/stromboli/internal/store/db"
)

// Source is the investigation source of health check findings.
const Source = "healthcheck"

const (
	// window is how far back each check looks.
	window = 15 * time.Minute
	// Renotify is how long a finding that persists is not investigated again.
	Renotify = 6 * time.Hour
	// firstRunDelay lets the server settle before the first check.
	firstRunDelay = time.Minute
	// serviceTimeout bounds one service's checks and investigation start, so
	// a slow service does not use up the time of the services after it.
	serviceTimeout = 2 * time.Minute
)

// Check is a threshold on a metric preset.
type Check struct {
	Preset    string
	Threshold float64
	// Avg compares the window average instead of the last value, for noisy
	// metrics such as CPU.
	Avg  bool
	Hint string
}

// Checks are the checks run on every service whose metrics connector has
// the preset (today: Fly.io).
var Checks = []Check{
	{Preset: "volume_used_percent", Threshold: 85, Hint: "a volume does not free space on restart: extend it or delete data before it is full"},
	{Preset: "disk_used_percent", Threshold: 90, Hint: "the root filesystem is reset from the image on restart"},
	{Preset: "memory_used_percent", Threshold: 90, Hint: "memory close to the limit leads to OOM kills"},
	{Preset: "cpu_percent", Threshold: 90, Avg: true, Hint: "sustained CPU saturation slows requests down"},
}

// Finding is one series over its threshold.
type Finding struct {
	Check     string            `json:"check"`
	Labels    map[string]string `json:"labels,omitempty"`
	Value     float64           `json:"value"`
	Threshold float64           `json:"threshold"`
	Unit      string            `json:"unit,omitempty"`
	Hint      string            `json:"hint,omitempty"`
}

func (f Finding) String() string {
	s := fmt.Sprintf("%s %s (threshold %s)", f.Check, metrics.FormatValue(f.Value, f.Unit), metrics.FormatValue(f.Threshold, f.Unit))
	if len(f.Labels) > 0 {
		s += " on " + metrics.FormatLabels(f.Labels)
	}
	return s
}

// Starter starts investigations; *agent.Engine implements it. A nil Starter
// only logs findings (no LLM is configured).
type Starter interface {
	Start(ctx context.Context, req agent.StartRequest) (db.Investigation, bool, error)
}

// MetricsFunc returns a metrics connector and its kind by name.
type MetricsFunc func(ctx context.Context, name string) (metrics.Source, string, error)

// CatalogFunc returns the catalog's services.
type CatalogFunc func(ctx context.Context) ([]*stromboliv1.Service, error)

// Summarizer is told when a run found every checked service healthy;
// *report.Slack implements it.
type Summarizer interface {
	Healthy(ctx context.Context, services []string) error
}

type Checker struct {
	interval time.Duration
	catalog  CatalogFunc
	metrics  MetricsFunc
	starter  Starter
	summary  Summarizer
	now      func() time.Time
}

func New(interval time.Duration, catalog CatalogFunc, m MetricsFunc, s Starter) *Checker {
	return &Checker{interval: interval, catalog: catalog, metrics: m, starter: s, now: time.Now}
}

// WithSummary posts a summary after each run where every checked service is
// healthy. Without it, a healthy run is only logged.
func (c *Checker) WithSummary(s Summarizer) *Checker {
	c.summary = s
	return c
}

// Run checks every service once per interval until ctx is cancelled.
func (c *Checker) Run(ctx context.Context) {
	slog.Info("health checks are enabled", "interval", c.interval)
	t := time.NewTimer(firstRunDelay)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c.RunOnce(ctx)
		t.Reset(c.interval)
	}
}

// Result is the outcome of one service's checks.
type Result struct {
	Service  string
	Findings []Finding
	// Errors are the checks that could not run.
	Errors []string
	// InvestigationID is set when findings started (or matched) an investigation.
	InvestigationID string
	Deduplicated    bool
}

// RunOnce checks every service and starts an investigation per unhealthy
// service.
func (c *Checker) RunOnce(ctx context.Context) []Result {
	services, err := c.catalog(ctx)
	if err != nil {
		slog.Error("health check: load catalog", "err", err)
		return nil
	}
	var results []Result
	unhealthy, failed := 0, 0
	for _, svc := range services {
		if svc.Metrics == nil || svc.Metrics.Source == "" {
			continue
		}
		svcCtx, cancel := context.WithTimeout(ctx, serviceTimeout)
		r := c.checkService(svcCtx, svc)
		if len(r.Findings) > 0 {
			unhealthy++
			c.investigate(svcCtx, svc, &r)
		} else if len(r.Errors) > 0 {
			failed++
		}
		cancel()
		results = append(results, r)
	}
	slog.Info("health check done", "services", len(results), "healthy", len(results)-unhealthy-failed, "unhealthy", unhealthy, "failed", failed)
	// Only an all-clear: a run with findings or failed checks is not one, and
	// a run that checked nothing proves nothing.
	if c.summary != nil && len(results) > 0 && unhealthy == 0 && failed == 0 {
		names := make([]string, len(results))
		for i, r := range results {
			names[i] = r.Service
		}
		if err := c.summary.Healthy(ctx, names); err != nil {
			slog.Error("health check: post summary", "err", err)
		}
	}
	return results
}

func (c *Checker) checkService(ctx context.Context, svc *stromboliv1.Service) Result {
	r := Result{Service: svc.Name}
	log := slog.With("service", svc.Name)
	matchers := metrics.Matchers(svc.Metrics.Selectors)
	if matchers == "" {
		return r
	}
	src, kind, err := c.metrics(ctx, svc.Metrics.Source)
	if err != nil {
		r.Errors = append(r.Errors, err.Error())
		log.Warn("health check: metrics source unavailable", "err", err)
		return r
	}
	end := c.now()
	for _, chk := range Checks {
		p, ok := metrics.FindPreset(kind, chk.Preset)
		if !ok || !p.Applies(matchers) {
			continue
		}
		series, err := src.Range(ctx, metrics.Expand(p.Expr, matchers), end.Add(-window), end)
		if err != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("%s: %v", chk.Preset, err))
			log.Warn("health check: query failed", "check", chk.Preset, "err", err)
			continue
		}
		for _, s := range series {
			v, ok := value(s.Samples, chk.Avg)
			if !ok || v < chk.Threshold {
				continue
			}
			r.Findings = append(r.Findings, Finding{
				Check: chk.Preset, Labels: s.Labels, Value: math.Round(v*10) / 10,
				Threshold: chk.Threshold, Unit: p.Unit, Hint: chk.Hint,
			})
		}
	}
	// Worst first within each check, checks in the order of Checks.
	order := func(name string) int {
		return slices.IndexFunc(Checks, func(c Check) bool { return c.Preset == name })
	}
	sort.SliceStable(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if a.Check != b.Check {
			return order(a.Check) < order(b.Check)
		}
		return a.Value > b.Value
	})
	return r
}

func value(samples []metrics.Sample, avg bool) (float64, bool) {
	var vals []float64
	for _, s := range samples {
		if !math.IsNaN(s.Value) && !math.IsInf(s.Value, 0) {
			vals = append(vals, s.Value)
		}
	}
	if len(vals) == 0 {
		return 0, false
	}
	if !avg {
		return vals[len(vals)-1], true
	}
	var t float64
	for _, v := range vals {
		t += v
	}
	return t / float64(len(vals)), true
}

func (c *Checker) investigate(ctx context.Context, svc *stromboliv1.Service, r *Result) {
	checks := []string{}
	lines := []string{}
	findings := make([]any, 0, len(r.Findings))
	for _, f := range r.Findings {
		if !slices.Contains(checks, f.Check) {
			checks = append(checks, f.Check)
		}
		lines = append(lines, "- "+f.String()+": "+f.Hint)
		findings = append(findings, map[string]any{
			"check": f.Check, "labels": f.Labels, "value": f.Value, "threshold": f.Threshold, "unit": f.Unit,
		})
	}
	sort.Strings(checks)

	title := fmt.Sprintf("Health check: %s", r.Findings[0])
	if n := len(r.Findings) - 1; n > 0 {
		title += fmt.Sprintf(" (+%d more)", n)
	}
	req := agent.StartRequest{
		Title:    title,
		Source:   Source,
		Services: []string{svc.Name},
		Description: "The scheduled health check found machine metrics over their thresholds. No alert has fired yet: " +
			"check whether users are affected and how soon this gets worse (how fast is it growing?).\n" + strings.Join(lines, "\n"),
		Alert:        map[string]any{"findings": findings, "window_minutes": int(window.Minutes())},
		Fingerprint:  Source + ":" + svc.Name + ":" + strings.Join(checks, ","),
		DedupeWindow: Renotify,
		At:           c.now(),
	}
	log := slog.With("service", svc.Name, "findings", len(r.Findings), "first", r.Findings[0].String())
	if c.starter == nil {
		log.Warn("health check: unhealthy (no LLM is configured, so it is not investigated)", "details", strings.Join(lines, "; "))
		return
	}
	inv, reused, err := c.starter.Start(ctx, req)
	if err != nil {
		r.Errors = append(r.Errors, "start investigation: "+err.Error())
		log.Error("health check: unhealthy, but the investigation did not start", "err", err)
		return
	}
	r.InvestigationID, r.Deduplicated = inv.ID.String(), reused
	if reused {
		log.Info("health check: still unhealthy, investigation already open or recent", "investigation", inv.ID)
	} else {
		log.Warn("health check: unhealthy, investigation started", "investigation", inv.ID)
	}
}
