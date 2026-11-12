package deploys

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/openstatushq/stromboli/internal/connector/mcpconn/mcptest"
)

func TestVercelRecent(t *testing.T) {
	since := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	ms := func(d time.Duration) int64 { return since.Add(d).UnixMilli() }
	// The shape list_deployments returns: JSON text wrapped in "result".
	srv := mcptest.New(t, map[string]mcptest.Handler{
		"list_deployments": func(args map[string]any) (any, error) {
			return map[string]any{"result": map[string]any{"deployments": map[string]any{
				"pagination": map[string]any{"count": 3, "next": nil},
				"deployments": []map[string]any{
					{"id": "dpl_2", "name": "web", "url": "web-2.vercel.app", "inspectorUrl": "https://vercel.com/acme/web/2", "created": ms(time.Hour), "state": "ERROR", "target": "production",
						"creator": map[string]any{"username": "vercel-bot"},
						"meta":    map[string]any{"githubCommitSha": "abc123", "githubCommitMessage": "fix: cache\n\nbody", "githubCommitAuthorLogin": "alice", "githubCommitRef": "main", "githubPrId": 42}},
					{"id": "dpl_1", "name": "web", "url": "web-1.vercel.app", "created": ms(time.Minute), "state": "READY", "target": "production",
						"creator": map[string]any{"username": "bob"}, "meta": map[string]any{"githubCommitRef": "main"}},
					{"id": "dpl_0", "name": "web", "created": ms(-time.Minute), "state": "READY", "target": "production"},
				},
			}}}, nil
		},
	}, true)
	v := &Vercel{TeamID: "acme", MCP: srv.Client(t, "list_deployments")}

	got, err := v.Recent(context.Background(), Target{Project: "web", Branch: "main"}, since)
	if err != nil {
		t.Fatal(err)
	}
	want := []Deploy{
		{Kind: "deployment", SHA: "abc123", Time: since.Add(time.Hour), Author: "alice", Title: "fix: cache", Status: "error", URL: "https://vercel.com/acme/web/2"},
		{Kind: "deployment", Time: since.Add(time.Minute), Author: "bob", Title: "deploy web", Status: "ready", URL: "https://web-1.vercel.app"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("deploys =\n%+v\nwant\n%+v", got, want)
	}
	args := srv.Calls()[0].Args
	if args["projectId"] != "web" || args["slug"] != "acme" || args["target"] != "production" || args["branch"] != "main" || args["since"] != float64(since.UnixMilli()) {
		t.Errorf("args = %v", args)
	}
	if _, err := v.Recent(context.Background(), Target{}, since); err == nil {
		t.Error("want an error without a project")
	}
}
