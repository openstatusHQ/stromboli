package logs

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openstatushq/stromboli/internal/connector/mcpconn"
	"github.com/openstatushq/stromboli/internal/connector/mcpconn/mcptest"
)

// Answers as Axiom's MCP server sends them.
const (
	axiomMCPLines = "# Query results\n\n## results.csv\n\nEffective range (_time): 2026-11-12T20:10:09.509Z to 2026-11-12T21:10:09.509Z.\n\n" +
		"_time,body,severity_text,service.name\n" +
		"2026-11-12T21:00:00Z,\"timeout, retrying\",ERROR,api\n" +
		"2026-11-12T21:05:00Z,\"panic: \"\"boom\"\"\n  at main.go:1\",ERROR,api\n" +
		"\n\n---\n[Open in Axiom](<https://app.axiom.co/x>)\nThis link re-runs the query.\n"
	axiomMCPFields = "# Query results\n\n## results.csv\n\nEffective range (_time): x to y.\n\n_time,body,severity_text,service.name\n2026-11-12T21:00:00Z,x,INFO,api\n\n---\n"
	axiomMCPCount  = "# Query results\n\n## results.csv\n\nEffective range (_time): x to y.\n\nn\n271134\n\n\n---\n[Open in Axiom](<https://app.axiom.co/x>)\n"
	axiomMCPHisto  = "# Query results\n\n## timeseries.csv\n\nEffective range (_time): x to y.\n\nt,n\n2026-11-12T20:00:00Z,72307\n2026-11-12T20:15:00Z,68158\n\n## totals.csv\n\nEffective range (_time): x to y.\n\nn\n140465\n\n\n---\n"
	axiomMCPNoData = "# Query results\n\n## results.csv\n\nEffective range (_time): x to y.\n\nNo data: 0 of 483267 rows matched. The filter value does not occur; check the field and the value.\n\n\n---\n"
	axiomMCPSets   = "# datasets.csv\n\ndatasetName,kind,description,correlationGroup,labels\nprod,axiom:events:v1,,,\notel-demo-traces,otel:traces:v1,Demo traces,,\n"
)

func TestParseAxiomMCPResult(t *testing.T) {
	res, err := parseAxiomMCPResult(axiomMCPLines)
	if err != nil {
		t.Fatal(err)
	}
	rows := res.rows()
	if len(rows) != 2 || rows[0]["body"] != "timeout, retrying" || rows[1]["body"] != "panic: \"boom\"\n  at main.go:1" || rows[1]["service.name"] != "api" {
		t.Fatalf("rows = %q", rows)
	}
	if res, _ := parseAxiomMCPResult(axiomMCPCount); res.rows()[0]["n"] != float64(271134) {
		t.Fatalf("count rows = %v", res.rows())
	}
	// The first section: the time series, not the totals.
	if res, _ := parseAxiomMCPResult(axiomMCPHisto); len(res.rows()) != 2 || res.rows()[1]["n"] != float64(68158) {
		t.Fatalf("histogram rows = %v", res.rows())
	}
	if res, err := parseAxiomMCPResult(axiomMCPNoData); err != nil || len(res.rows()) != 0 {
		t.Fatalf("no data: %v, %v", res.rows(), err)
	}
	if res, err := parseAxiomMCPResult(axiomMCPSets); err != nil || len(res.rows()) != 2 || res.rows()[1]["kind"] != "otel:traces:v1" {
		t.Fatalf("datasets: %v", err)
	}
	if _, err := parseAxiomMCPResult("Query failed: line 1"); err == nil {
		t.Fatal("want an error for an unknown format")
	}
}

func TestAxiomOverMCP(t *testing.T) {
	fieldCache = sync.Map{}
	var mu sync.Mutex
	var calls []map[string]any
	srv := mcptest.New(t, map[string]mcptest.Handler{
		"listDatasets": func(args map[string]any) (any, error) { return mcptest.Text(axiomMCPSets), nil },
		"queryDataset": func(args map[string]any) (any, error) {
			mu.Lock()
			calls = append(calls, args)
			mu.Unlock()
			apl := args["apl"].(string)
			switch {
			case strings.HasSuffix(apl, "| take 1"):
				return mcptest.Text(axiomMCPFields), nil
			case strings.Contains(apl, "by t = bin"):
				return mcptest.Text(axiomMCPHisto), nil
			case strings.HasSuffix(apl, "summarize n = count()"):
				return mcptest.Text(axiomMCPCount), nil
			}
			return mcptest.Text(axiomMCPLines), nil
		},
	}, true, "queryDataset")
	// The connector's own config: queryDataset is used read-only.
	c, err := mcpconn.New(AxiomMCP(srv.URL, nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	ax, err := NewAxiom(AxiomConfig{MCP: c, OrgID: "acme-1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	end := time.Date(2026, 11, 12, 21, 10, 0, 0, time.UTC)
	q := Query{Selector: `['prod'] | where ['service.name'] == "api"`, Filter: "(?i)timeout|panic", Start: end.Add(-time.Hour), End: end, Limit: 10}

	lines, err := ax.Query(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || !strings.HasPrefix(lines[0].Text, "level=ERROR panic") || lines[1].Labels["service.name"] != "api" {
		t.Fatalf("lines = %+v", lines)
	}
	if n, err := ax.Count(ctx, Query{Selector: q.Selector, Start: q.Start, End: q.End}); err != nil || n != 271134 {
		t.Fatalf("count = %d, %v", n, err)
	}
	bs, err := ax.Histogram(ctx, Query{Selector: q.Selector, Start: q.Start, End: q.End}, 15*time.Minute)
	if err != nil || len(bs) != 2 || bs[1].Count != 68158 {
		t.Fatalf("histogram = %+v, %v", bs, err)
	}
	if err := ax.Check(ctx); err != nil {
		t.Fatal(err)
	}

	last := calls[len(calls)-1]
	want := `['prod'] | where _time >= datetime(2026-11-12T20:10:00Z) and _time <= datetime(2026-11-12T21:10:00Z) | where ['service.name'] == "api" | summarize n = count() by t = bin(_time, 900s)`
	if last["apl"] != want {
		t.Errorf("apl =\n%s\nwant\n%s", last["apl"], want)
	}
	if last["saveResults"] != false || last["truncate"] != false || last["orgId"] != "acme-1" || last["startTime"] != "2026-11-12T20:10:00Z" {
		t.Errorf("args = %v", last)
	}
}

func TestAPLDatasetSpan(t *testing.T) {
	for in, want := range map[string]int{
		`['logs'] | take 1`:   8,
		`  ["eu.logs"] | x`:   13,
		`['it\'s'] | take 1`:  9,
		`logs-2026 | where x`: 9,
		`logs`:                4,
		`['open | take 1`:     -1,
		`| take 1`:            -1,
	} {
		if _, got := aplDatasetSpan(in); got != want {
			t.Errorf("aplDatasetSpan(%q) = %d, want %d", in, got, want)
		}
	}
}
