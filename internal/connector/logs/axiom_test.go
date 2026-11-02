package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAxiom answers APL queries from a function and records them.
type fakeAxiom struct {
	mu      sync.Mutex
	queries []string
	answer  func(apl string) string // tabular response body
}

func (f *fakeAxiom) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xaat-test" {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"forbidden"}`)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/datasets":
			fmt.Fprint(w, `[{"name":"logs","kind":"axiom:events:v1"},{"name":"otel-metrics","kind":"otel:metrics:v1"},{"name":"edge","kind":"axiom:events:v1"}]`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/datasets/_apl" && r.URL.Query().Get("format") == "tabular":
			var body struct {
				APL       string `json:"apl"`
				StartTime string `json:"startTime"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.StartTime == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.queries = append(f.queries, body.APL)
			f.mu.Unlock()
			fmt.Fprint(w, f.answer(body.APL))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func table(fields []string, columns ...[]any) string {
	fs := make([]map[string]string, len(fields))
	for i, f := range fields {
		fs[i] = map[string]string{"name": f}
	}
	b, _ := json.Marshal(map[string]any{"tables": []any{map[string]any{"fields": fs, "columns": columns}}})
	return string(b)
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
	ax, err := NewAxiom(AxiomConfig{URL: f.server(t).URL, Token: "xaat-test"})
	if err != nil {
		t.Fatal(err)
	}
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
	ax, _ := NewAxiom(AxiomConfig{URL: f.server(t).URL, Token: "xaat-test", MessageField: "attributes.line"})
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
			return table([]string{"service.name", "count_"}, []any{"api", "web", ""}, []any{10, 5, 1})
		}
		t.Errorf("unexpected query %q", apl)
		return table(nil)
	}}
	ax, _ := NewAxiom(AxiomConfig{URL: f.server(t).URL, Token: "xaat-test"})
	got, err := ax.Services(context.Background())
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

func TestAxiomError(t *testing.T) {
	f := &fakeAxiom{}
	ax, _ := NewAxiom(AxiomConfig{URL: f.server(t).URL, Token: "wrong"})
	if err := ax.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("err = %v", err)
	}
}

func TestAxiomHistogram(t *testing.T) {
	start := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	f := &fakeAxiom{answer: func(apl string) string {
		return table([]string{"t", "n"}, []any{start.Add(5 * time.Minute).Format(time.RFC3339)}, []any{3})
	}}
	ax, _ := NewAxiom(AxiomConfig{URL: f.server(t).URL, Token: "xaat-test", MessageField: "message"})
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
	ax, _ := NewAxiom(AxiomConfig{URL: f.server(t).URL, Token: "xaat-test"})
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
