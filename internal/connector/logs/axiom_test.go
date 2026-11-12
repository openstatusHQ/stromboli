package logs

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openstatushq/stromboli/internal/connector/mcpconn"
	"github.com/openstatushq/stromboli/internal/connector/mcpconn/mcptest"
)

// fakeAxiom answers APL queries from a function through Axiom's MCP tools and
// records them, without the time range the connector adds.
type fakeAxiom struct {
	mu      sync.Mutex
	queries []string
	answer  func(apl string) string // CSV result
}

var aplTimeRange = regexp.MustCompile(` \| where _time >= datetime\([^)]*\) and _time <= datetime\([^)]*\)`)

func (f *fakeAxiom) axiom(t *testing.T, cfg AxiomConfig) *Axiom {
	srv := mcptest.New(t, map[string]mcptest.Handler{
		"listDatasets": func(map[string]any) (any, error) {
			return mcptest.Text("# datasets.csv\n\ndatasetName,kind\nlogs,axiom:events:v1\notel-metrics,otel:metrics:v1\nedge,axiom:events:v1\n"), nil
		},
		"queryDataset": func(args map[string]any) (any, error) {
			apl := aplTimeRange.ReplaceAllString(args["apl"].(string), "")
			f.mu.Lock()
			f.queries = append(f.queries, apl)
			f.mu.Unlock()
			return mcptest.Text("# Query results\n\n## results.csv\n\nEffective range (_time): x to y.\n\n" + f.answer(apl) + "\n---\n"), nil
		},
	}, true, "queryDataset")
	c, err := mcpconn.New(AxiomMCP(srv.URL, nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	cfg.MCP = c
	ax, err := NewAxiom(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return ax
}

// table renders column-major values as CSV; nil is an empty cell.
func table(fields []string, columns ...[]any) string {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	w.Write(fields)
	n := 0
	if len(columns) > 0 {
		n = len(columns[0])
	}
	for i := range n {
		rec := make([]string, len(columns))
		for j, col := range columns {
			if col[i] != nil {
				rec[j] = fmt.Sprint(col[i])
			}
		}
		w.Write(rec)
	}
	w.Flush()
	return b.String()
}

func TestAxiomRequiresMCP(t *testing.T) {
	if _, err := NewAxiom(AxiomConfig{}); err == nil {
		t.Fatal("want an error without an MCP client")
	}
}

func TestAxiomQueryAndCount(t *testing.T) {
	fieldCache = sync.Map{}
	now := time.Now().UTC().Truncate(time.Second)
	f := &fakeAxiom{answer: func(apl string) string {
		switch {
		case strings.HasSuffix(apl, "| take 1"):
			return table([]string{"_time", "msg", "service.name"}, []any{now.Format(time.RFC3339)}, []any{"x"}, []any{"api"})
		case strings.Contains(apl, "summarize n = count()"):
			return table([]string{"n"}, []any{42})
		default:
			return table([]string{"_time", "msg", "service.name", "level"},
				[]any{now.Add(-time.Minute).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)},
				[]any{"older", "newer"},
				[]any{"api", "api"},
				[]any{"error", nil})
		}
	}}
	ax := f.axiom(t, AxiomConfig{})
	ctx := context.Background()
	sel := `['logs'] | where ['service.name'] == "api"`
	q := Query{Selector: sel, Filter: `(?i)timeout`, Start: now.Add(-time.Hour), End: now, Limit: 10}

	lines, err := ax.Query(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Text != "newer" || lines[1].Text != "older" {
		t.Fatalf("lines = %+v", lines)
	}
	if lines[1].Labels["level"] != "error" || lines[0].Labels["service.name"] != "api" {
		t.Fatalf("labels = %+v / %+v", lines[0].Labels, lines[1].Labels)
	}
	n, err := ax.Count(ctx, q)
	if err != nil || n != 42 {
		t.Fatalf("count = %d, %v", n, err)
	}

	want := sel + ` | where tostring(['msg']) matches regex "(?i)timeout" | sort by _time desc | take 10`
	found := false
	for _, apl := range f.queries {
		found = found || apl == want
	}
	if !found {
		t.Fatalf("query %q not sent; got %q", want, f.queries)
	}
	// The message field is detected once and cached.
	probes := 0
	for _, apl := range f.queries {
		if strings.HasSuffix(apl, "| take 1") {
			probes++
		}
	}
	if probes != 1 {
		t.Fatalf("probes = %d, want 1", probes)
	}
}

func TestAxiomMessageFieldOverride(t *testing.T) {
	f := &fakeAxiom{answer: func(apl string) string { return table([]string{"n"}, []any{1}) }}
	ax := f.axiom(t, AxiomConfig{MessageField: "attributes.line"})
	if _, err := ax.Count(context.Background(), Query{Selector: "['logs']", Filter: "x", Start: time.Now().Add(-time.Hour), End: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if last := f.queries[len(f.queries)-1]; !strings.Contains(last, `tostring(['attributes.line']) matches regex "x"`) {
		t.Fatalf("queries = %q", f.queries)
	}
}

func TestAxiomServices(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	f := &fakeAxiom{answer: func(apl string) string {
		switch {
		case apl == `['logs'] | take 1`:
			return table([]string{"_time", "message", "service.name"}, []any{now}, []any{"x"}, []any{"api"})
		case apl == `['edge'] | take 1`:
			return table([]string{"_time", "message"}, []any{now}, []any{"x"})
		case strings.HasPrefix(apl, `['logs'] | where isnotempty(['service.name']) | summarize count() by ['service.name']`):
			return table([]string{"service.name", "count_"}, []any{"api", "web", nil}, []any{10, 5, 1})
		}
		t.Errorf("unexpected query %q", apl)
		return table([]string{"x"})
	}}
	got, err := f.axiom(t, AxiomConfig{}).Services(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Service{
		{Name: "api", Selector: `['logs'] | where ['service.name'] == "api"`},
		{Name: "web", Selector: `['logs'] | where ['service.name'] == "web"`},
		{Name: "edge", Selector: `['edge']`},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("services = %v, want %v", got, want)
	}
}

func TestAxiomHistogram(t *testing.T) {
	start := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	f := &fakeAxiom{answer: func(apl string) string {
		return table([]string{"t", "n"}, []any{start.Add(5 * time.Minute).Format(time.RFC3339)}, []any{3})
	}}
	ax := f.axiom(t, AxiomConfig{MessageField: "message"})
	got, err := ax.Histogram(context.Background(), Query{Selector: "['logs']", Filter: "err", Start: start, End: start.Add(time.Hour)}, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Count != 3 || !got[0].Start.Equal(start.Add(5*time.Minute)) {
		t.Fatalf("buckets = %+v", got)
	}
	want := `['logs'] | where tostring(['message']) matches regex "err" | summarize n = count() by t = bin(_time, 300s)`
	if f.queries[len(f.queries)-1] != want {
		t.Fatalf("queries = %q", f.queries)
	}
}

// Fly's log shipper keeps the level in log.level: error filters must see it.
func TestAxiomFlyLevel(t *testing.T) {
	fieldCache = sync.Map{}
	now := time.Now().UTC().Format(time.RFC3339)
	f := &fakeAxiom{answer: func(apl string) string {
		if strings.HasSuffix(apl, "| take 1") {
			return table([]string{"_time", "message", "log.level", "fly.app.name"}, []any{now}, []any{"x"}, []any{"info"}, []any{"checker"})
		}
		return table([]string{"_time", "message", "log.level", "fly.app.name"}, []any{now}, []any{"query failed"}, []any{"error"}, []any{"checker"})
	}}
	ax := f.axiom(t, AxiomConfig{})
	lines, err := ax.Query(context.Background(), Query{Selector: "['fly']", Filter: ErrorFilter, Start: time.Now().Add(-time.Hour), End: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0].Text != "level=error query failed" || lines[0].Labels["fly.app.name"] != "checker" {
		t.Fatalf("lines = %+v", lines)
	}
	if last := f.queries[len(f.queries)-1]; !strings.Contains(last, `where strcat("level=", tostring(['log.level']), " ", tostring(['message'])) matches regex`) {
		t.Fatalf("query = %s", last)
	}
}
