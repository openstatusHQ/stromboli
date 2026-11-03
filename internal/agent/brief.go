package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/internal/catalog"
	"github.com/openstatushq/opensre/internal/connector/deploys"
	"github.com/openstatushq/opensre/internal/connector/logs"
	"github.com/openstatushq/opensre/internal/policy"
	"github.com/openstatushq/opensre/internal/store/db"
)

// Window is the time range under investigation. The baseline is the window
// of the same length right before it.
type Window struct {
	Start time.Time
	End   time.Time
}

func (w Window) Baseline() Window {
	d := w.End.Sub(w.Start)
	return Window{Start: w.Start.Add(-d), End: w.Start}
}

// Brief builds the deterministic "orient" context for a service: no LLM
// involved, every section degrades to a note when its source fails.
func (e *Engine) Brief(ctx context.Context, service string, w Window) (string, error) {
	_, graph, err := catalog.Load(ctx, e.store.Queries)
	if err != nil {
		return "", err
	}
	svc, ok := graph.Service(service)
	if !ok {
		return "", fmt.Errorf("service %q is not in the catalog", service)
	}

	var (
		wg                          sync.WaitGroup
		logSection, deploySection   string
		runbookSection, pastSection string
		metricSection               string
	)
	wg.Add(5)
	go func() { defer wg.Done(); logSection = e.briefLogs(ctx, svc, w) }()
	go func() { defer wg.Done(); metricSection = e.briefMetrics(ctx, svc, w) }()
	go func() { defer wg.Done(); deploySection = e.briefDeploys(ctx, graph, svc, w) }()
	go func() { defer wg.Done(); runbookSection = e.briefRunbooks(ctx, svc) }()
	go func() { defer wg.Done(); pastSection = e.briefPast(ctx, svc) }()
	wg.Wait()

	var b strings.Builder
	fmt.Fprintf(&b, "# Brief: %s\n\n", svc.Name)
	fmt.Fprintf(&b, "Window: %s → %s (%s). Baseline: the %s before.\n\n",
		w.Start.Format(time.RFC3339), w.End.Format(time.RFC3339), w.End.Sub(w.Start).Round(time.Minute), w.End.Sub(w.Start).Round(time.Minute))

	b.WriteString("## Service\n\n")
	card, _ := catalog.MarshalYAML([]*opensrev1.Service{svc})
	b.WriteString("```yaml\n" + string(card) + "```\n\n")

	b.WriteString("## Dependencies\n\n")
	fmt.Fprintf(&b, "- Depends on (downstream): %s\n", list(graph.Downstream(svc.Name)))
	fmt.Fprintf(&b, "- Called by (upstream): %s\n", list(graph.Upstream(svc.Name)))
	fmt.Fprintf(&b, "- Blast radius if this service fails: %s\n\n", list(graph.BlastRadius(svc.Name)))

	b.WriteString("## Logs\n\n" + logSection + "\n")
	b.WriteString("## Metrics\n\n" + metricSection + "\n")
	b.WriteString("## Recent changes\n\n" + deploySection + "\n")
	b.WriteString("## Runbooks\n\n" + runbookSection + "\n")
	b.WriteString("## Similar past investigations\n\n" + pastSection)
	return b.String(), nil
}

func (e *Engine) briefLogs(ctx context.Context, svc *opensrev1.Service, w Window) string {
	if svc.Logs == nil || svc.Logs.Source == "" {
		return "_No log source configured for this service._\n"
	}
	src, err := e.connectors.Logs(ctx, svc.Logs.Source)
	if err != nil {
		return fmt.Sprintf("_Log source unavailable: %v_\n", err)
	}
	a, err := analyzeLogs(ctx, src, svc.Logs.Selector, logs.ErrorFilter, w, 10)
	if err != nil {
		return fmt.Sprintf("_Log query failed: %v_\n", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "- Total lines: %d (baseline %d)\n", a.Total, a.BaselineTotal)
	fmt.Fprintf(&b, "- Error-like lines: %d (baseline %d)%s\n", a.Errors, a.BaselineErrors, ratio(a.Errors, a.BaselineErrors))
	if len(a.Patterns) == 0 {
		b.WriteString("- No error patterns in the window.\n")
		return b.String()
	}
	b.WriteString("\nTop error patterns (incident vs baseline count):\n\n")
	for _, p := range a.Patterns {
		tag := ""
		if p.New {
			tag = " **NEW**"
		}
		fmt.Fprintf(&b, "- %d vs %d%s, first seen %s: `%s`\n", p.IncidentCount, p.BaselineCount, tag,
			p.FirstSeen.Format("15:04:05"), policy.Redact(p.Sample))
	}
	return b.String()
}

// logAnalysis is the aggregated log evidence for a window.
type logAnalysis struct {
	Total          int64              `json:"total_lines"`
	BaselineTotal  int64              `json:"baseline_total_lines"`
	Errors         int64              `json:"matching_lines"`
	BaselineErrors int64              `json:"baseline_matching_lines"`
	Patterns       []logs.PatternDiff `json:"patterns"`
	SampledLines   int                `json:"sampled_lines"`
}

func analyzeLogs(ctx context.Context, src logs.Source, selector, filter string, w Window, limit int) (*logAnalysis, error) {
	base := w.Baseline()
	q := func(win Window, f string) logs.Query {
		return logs.Query{Selector: selector, Filter: f, Start: win.Start, End: win.End, Limit: 1000}
	}

	var (
		a                        logAnalysis
		incidentLines, baseLines []logs.Line
		errs                     [6]error
		wg                       sync.WaitGroup
	)
	wg.Add(6)
	go func() { defer wg.Done(); a.Total, errs[0] = src.Count(ctx, q(w, "")) }()
	go func() { defer wg.Done(); a.BaselineTotal, errs[1] = src.Count(ctx, q(base, "")) }()
	go func() { defer wg.Done(); a.Errors, errs[2] = src.Count(ctx, q(w, filter)) }()
	go func() { defer wg.Done(); a.BaselineErrors, errs[3] = src.Count(ctx, q(base, filter)) }()
	go func() { defer wg.Done(); incidentLines, errs[4] = src.Query(ctx, q(w, filter)) }()
	go func() { defer wg.Done(); baseLines, errs[5] = src.Query(ctx, q(base, filter)) }()
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	a.Patterns = logs.Diff(incidentLines, baseLines, limit)
	for i := range a.Patterns {
		a.Patterns[i].Sample = policy.Redact(a.Patterns[i].Sample)
		a.Patterns[i].Template = policy.Redact(a.Patterns[i].Template)
	}
	a.SampledLines = len(incidentLines)
	return &a, nil
}

func (e *Engine) briefDeploys(ctx context.Context, graph *catalog.Graph, svc *opensrev1.Service, w Window) string {
	// Changes to the service itself and to what it depends on, from a bit before the window.
	since := w.Start.Add(-2 * time.Hour)
	names := append([]string{svc.Name}, graph.Downstream(svc.Name)...)

	var (
		mu    sync.Mutex
		all   []deploys.Deploy
		notes []string
		wg    sync.WaitGroup
	)
	for _, name := range names {
		s, ok := graph.Service(name)
		if !ok || s.Deploys == nil || s.Deploys.Source == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ds, err := e.recentDeploys(ctx, s, since)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				notes = append(notes, fmt.Sprintf("_%s: %v_", s.Name, err))
				return
			}
			all = append(all, ds...)
		}()
	}
	wg.Wait()

	var b strings.Builder
	sort.Slice(all, func(i, j int) bool { return all[i].Time.After(all[j].Time) })
	if len(all) == 0 {
		fmt.Fprintf(&b, "No changes since %s for %s.\n", since.Format(time.RFC3339), strings.Join(names, ", "))
	}
	for i, d := range all {
		if i >= 15 {
			fmt.Fprintf(&b, "- … %d more\n", len(all)-i)
			break
		}
		fmt.Fprintf(&b, "- %s **%s** %s `%s` %q by %s %s\n", d.Time.Format(time.RFC3339), d.Service, d.Kind, short(d.SHA), d.Title, d.Author, d.Status)
	}
	for _, n := range notes {
		b.WriteString("- " + n + "\n")
	}
	return b.String()
}

func (e *Engine) recentDeploys(ctx context.Context, s *opensrev1.Service, since time.Time) ([]deploys.Deploy, error) {
	gh, err := e.connectors.GitHub(ctx, s.Deploys.Source)
	if err != nil {
		return nil, err
	}
	ds, err := gh.Recent(ctx, deploys.Target{
		Repo: s.Deploys.Repo, Workflow: s.Deploys.Workflow, Environment: s.Deploys.Environment, Branch: s.Deploys.Branch,
	}, since)
	if err != nil {
		return nil, err
	}
	for i := range ds {
		ds[i].Service = s.Name
		ds[i].Title = policy.Redact(ds[i].Title)
	}
	return ds, nil
}

// maxRunbookInBrief keeps the brief compact; the agent can read the full runbook.
const maxRunbookInBrief = 3000

func (e *Engine) briefRunbooks(ctx context.Context, svc *opensrev1.Service) string {
	if len(svc.Runbooks) == 0 {
		return "No runbooks linked.\n"
	}
	var b strings.Builder
	for _, path := range svc.Runbooks {
		rb, err := e.store.GetRunbook(ctx, path)
		if err != nil {
			fmt.Fprintf(&b, "### %s\n\n_Not found._\n\n", path)
			continue
		}
		content := rb.Content
		if len(content) > maxRunbookInBrief {
			content = content[:maxRunbookInBrief] + "\n… (truncated, use read_runbook for the rest)"
		}
		fmt.Fprintf(&b, "### %s\n\n%s\n\n", path, content)
	}
	return b.String()
}

func (e *Engine) briefPast(ctx context.Context, svc *opensrev1.Service) string {
	rows, err := e.store.SearchInvestigations(ctx, db.SearchInvestigationsParams{Limit: 3, Service: svc.Name})
	if err != nil {
		return fmt.Sprintf("_Search failed: %v_\n", err)
	}
	if len(rows) == 0 {
		return "None.\n"
	}
	var b strings.Builder
	for _, r := range rows {
		rep := &opensrev1.Report{}
		_ = protojson.Unmarshal(r.Report, rep)
		fmt.Fprintf(&b, "- %s %q: likely cause %q (confidence %s)\n",
			r.CreatedAt.Time.Format("2006-01-02"), r.Title, rep.LikelyCause, rep.Confidence)
	}
	return b.String()
}

func list(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, ", ")
}

func ratio(cur, base int64) string {
	switch {
	case base == 0 && cur > 0:
		return " (new: none in baseline)"
	case base > 0:
		return fmt.Sprintf(" (×%.1f)", float64(cur)/float64(base))
	}
	return ""
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
