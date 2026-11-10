package deploys

import (
	"context"
	"testing"
	"time"

	"github.com/openstatushq/stromboli/internal/connector/mcpconn/mcptest"
)

func TestGitHubMCP(t *testing.T) {
	srv := mcptest.New(t, map[string]mcptest.Handler{
		"list_commits": func(args map[string]any) (any, error) {
			return []map[string]any{{
				"sha": "abc", "html_url": "https://github.com/o/r/commit/abc",
				"commit": map[string]any{"message": "fix: timeout\n\nlong body", "committer": map[string]any{"date": "2026-10-31T20:10:57Z"}},
				"author": map[string]any{"login": "alice"},
			}}, nil
		},
		"get_commit": func(args map[string]any) (any, error) {
			return map[string]any{
				"sha": "abc", "commit": map[string]any{"message": "fix", "author": map[string]any{"name": "Alice"}},
				"files": []map[string]any{{"filename": "a.go", "status": "modified", "additions": 1, "deletions": 2, "patch": "@@ -1 +1 @@"}},
			}, nil
		},
		"actions_list": func(args map[string]any) (any, error) {
			// The wrapped shape GitHub sends to newer clients.
			return map[string]any{"method": "list_workflow_runs", "workflow_runs": map[string]any{"workflow_runs": []map[string]any{
				{"head_sha": "new", "display_title": "deploy", "status": "completed", "conclusion": "failure", "created_at": "2026-11-03T09:00:00Z", "updated_at": "2026-11-03T09:05:00Z", "actor": map[string]any{"login": "bob"}},
				{"head_sha": "old", "display_title": "deploy", "status": "completed", "conclusion": "success", "created_at": "2026-10-01T09:00:00Z", "updated_at": "2026-10-01T09:05:00Z"},
			}}}, nil
		},
	}, true)
	g := NewGitHub(GitHubConfig{MCP: srv.Client(t, GitHubTools...)})
	ctx := context.Background()
	since := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)

	commits, err := g.Recent(ctx, Target{Repo: "o/r", Branch: "main"}, since)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 1 || commits[0].Title != "fix: timeout" || commits[0].Author != "alice" {
		t.Errorf("commits = %+v", commits)
	}

	runs, err := g.Recent(ctx, Target{Repo: "o/r", Workflow: "deploy.yml", Branch: "main"}, since)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].SHA != "new" || runs[0].Status != "failure" {
		t.Errorf("runs = %+v, want only the run after since", runs)
	}

	c, err := g.Commit(ctx, "o/r", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if c.Author != "Alice" || len(c.Files) != 1 || c.Files[0].Patch == "" {
		t.Errorf("commit = %+v", c)
	}

	calls := srv.Calls()
	if a := calls[0].Args; a["owner"] != "o" || a["repo"] != "r" || a["sha"] != "main" || a["since"] != "2026-10-30T00:00:00Z" {
		t.Errorf("list_commits args = %v", a)
	}
	if a := calls[1].Args; a["method"] != "list_workflow_runs" || a["resource_id"] != "deploy.yml" {
		t.Errorf("actions_list args = %v", a)
	}
	if a := calls[2].Args; a["detail"] != "full_patch" {
		t.Errorf("get_commit args = %v", a)
	}

	if _, err := g.Recent(ctx, Target{Repo: "nope", Workflow: "x"}, since); err == nil {
		t.Error("a repo without owner should fail")
	}
}
