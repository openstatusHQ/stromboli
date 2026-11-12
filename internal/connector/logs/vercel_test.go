package logs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openstatushq/stromboli/internal/connector/mcpconn/mcptest"
)

// What get_runtime_logs returns, as Markdown text.
const vercelLogsMD = `## Runtime Logs

**Project:** prj_1
**Time range:** 2026-11-05T20:49:42.117Z → 2026-11-12T20:49:42.117Z

### 2026-11-05T22:45:56.957Z POST / [warn/function]
dep=dpl_1 branch=main request=abc-1 domain=www.example.com environment=production region=cdg1 runtime=nodejs24.x stream=stderr
    Error: Failed to find Server Action "x". This request might be from an older or newer deployment.
    Read more: https://nextjs.org/docs/messages/failed-to-find-server-action
        at ignore-listed frames

### 2026-11-05T22:40:00.000Z [info/daemon]
dep=dpl_1 environment=production
    cache warmed

`

const vercelCountsMD = `## Runtime Log Counts

**Project:** prj_1
**Grouped by:** level
**Showing:** Top 25 groups by log count; this may omit other groups.

| level | count |
|---|---|
| warn | 3 |
| error | 2 |
`

func TestParseVercelLogs(t *testing.T) {
	ls := parseVercelLogs(vercelLogsMD)
	if len(ls) != 2 {
		t.Fatalf("lines = %+v", ls)
	}
	l := ls[0]
	if !l.Time.Equal(time.Date(2026, 11, 5, 22, 45, 56, 957e6, time.UTC)) || !strings.HasPrefix(l.Text, `level=warn Error: Failed to find Server Action "x".`) ||
		!strings.Contains(l.Text, "    at ignore-listed frames") {
		t.Errorf("line = %+v", l)
	}
	if l.Labels["request"] != "POST /" || l.Labels["deployment"] != "dpl_1" || l.Labels["region"] != "cdg1" || l.Labels["source"] != "function" {
		t.Errorf("labels = %v", l.Labels)
	}
	if ls[1].Text != "level=info cache warmed" || ls[1].Labels["request"] != "" {
		t.Errorf("line = %+v", ls[1])
	}
	if n := len(parseVercelLogs("## Runtime Logs\n\nNo logs found for the specified criteria.\n")); n != 0 {
		t.Errorf("no logs: %d lines", n)
	}
}

func TestParseVercelCounts(t *testing.T) {
	if n, err := parseVercelCounts(vercelCountsMD); err != nil || n != 5 {
		t.Fatalf("count = %d, %v", n, err)
	}
	empty := "## Runtime Log Counts\n\n| level | count |\n|---|---|\n"
	if n, err := parseVercelCounts(empty); err != nil || n != 0 {
		t.Fatalf("empty: %d, %v", n, err)
	}
	if _, err := parseVercelCounts("something else"); err == nil {
		t.Fatal("want an error for an unknown format")
	}
}

func TestVercelSource(t *testing.T) {
	srv := mcptest.New(t, map[string]mcptest.Handler{
		"get_runtime_logs": func(args map[string]any) (any, error) {
			if args["group_by"] == "level" {
				return mcptest.Text(vercelCountsMD), nil
			}
			return mcptest.Text(vercelLogsMD), nil
		},
		"list_projects": func(args map[string]any) (any, error) {
			return map[string]any{"result": map[string]any{"projects": []map[string]any{{"id": "prj_2", "name": "web"}, {"id": "prj_1", "name": "api"}}}}, nil
		},
	}, true)
	v := &Vercel{TeamID: "team_1", MCP: srv.Client(t, "get_runtime_logs", "list_projects")}
	ctx := context.Background()
	end := time.Date(2026, 11, 12, 21, 0, 0, 0, time.UTC)
	q := Query{Selector: "web environment=production", Start: end.Add(-time.Hour), End: end}

	if n, err := v.Count(ctx, q); err != nil || n != 5 {
		t.Fatalf("count = %d, %v", n, err)
	}
	q.Filter = ErrorFilter
	ls, err := v.Query(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 1 || !strings.Contains(ls[0].Text, "Server Action") {
		t.Fatalf("filtered lines = %+v", ls)
	}
	if n, err := v.Count(ctx, q); err != nil || n != 1 {
		t.Fatalf("filtered count = %d, %v", n, err)
	}
	args := srv.Calls()[0].Args
	if args["projectId"] != "web" || args["teamId"] != "team_1" || args["environment"] != "production" || args["since"] != "2026-11-12T20:00:00Z" || args["until"] != "2026-11-12T21:00:00Z" {
		t.Errorf("args = %v", args)
	}

	svcs, err := v.Services(ctx)
	if err != nil || len(svcs) != 2 || svcs[0] != (Service{Name: "api", Selector: "api"}) {
		t.Fatalf("services = %+v, %v", svcs, err)
	}
	if _, err := v.Query(ctx, Query{Selector: "web env=prod"}); err == nil {
		t.Error("want an error for an unknown selector option")
	}
}
