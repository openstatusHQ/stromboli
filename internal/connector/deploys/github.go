// Package deploys lists recent changes to a service: deploy workflow runs,
// GitHub deployments, or commits as a fallback.
//
// With a token, it goes through GitHub's remote MCP server (read-only
// toolsets repos and actions). Deployments have no MCP tool and use the REST
// API; without a token (public repositories only) everything does, since the
// MCP server requires authentication.
package deploys

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openstatushq/stromboli/internal/connector/mcpconn"
)

// Target is the catalog's deploy config for one service.
type Target struct {
	Repo        string // owner/name
	Workflow    string
	Environment string
	Branch      string
}

type Deploy struct {
	Kind    string    `json:"kind"` // "workflow_run", "deployment" or "commit"
	SHA     string    `json:"sha"`
	Time    time.Time `json:"time"`
	Author  string    `json:"author,omitempty"`
	Title   string    `json:"title"`
	Status  string    `json:"status,omitempty"`
	URL     string    `json:"url"`
	Service string    `json:"service,omitempty"`
}

type FileChange struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch,omitempty"`
}

type Commit struct {
	SHA     string       `json:"sha"`
	Message string       `json:"message"`
	Author  string       `json:"author"`
	Time    time.Time    `json:"time"`
	URL     string       `json:"url"`
	Files   []FileChange `json:"files"`
}

type GitHubConfig struct {
	Token   string
	BaseURL string // defaults to https://api.github.com
	// MCP, when set, serves everything but deployments.
	MCP *mcpconn.Client
}

// GitHubMCPURL is GitHub's remote MCP server.
const GitHubMCPURL = "https://api.githubcopilot.com/mcp/"

// GitHubTools are the MCP tools the connector calls.
var GitHubTools = []string{"list_commits", "get_commit", "actions_list"}

// GitHubMCP is the MCP client config for a token: only the repos and actions
// toolsets, and the server's read-only mode on top of the allow-list.
func GitHubMCP(token, endpoint string) mcpconn.Config {
	if endpoint == "" {
		endpoint = GitHubMCPURL
	}
	return mcpconn.Config{
		Name:     "github",
		Endpoint: endpoint,
		Header: http.Header{
			"Authorization":  {"Bearer " + token},
			"X-MCP-Toolsets": {"repos,actions"},
			"X-MCP-Readonly": {"true"},
		},
		Allow:           GitHubTools,
		RequireReadOnly: true,
	}
}

type GitHub struct {
	cfg  GitHubConfig
	http *http.Client
}

func NewGitHub(cfg GitHubConfig) *GitHub {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.github.com"
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &GitHub{cfg: cfg, http: &http.Client{Timeout: 20 * time.Second}}
}

// splitRepo splits owner/name.
func splitRepo(repo string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return "", "", fmt.Errorf("github: repo %q is not owner/name", repo)
	}
	return owner, name, nil
}

// Recent lists changes to the target since the given time, newest first.
func (g *GitHub) Recent(ctx context.Context, t Target, since time.Time) ([]Deploy, error) {
	switch {
	case t.Workflow != "":
		return g.workflowRuns(ctx, t, since)
	case t.Environment != "":
		return g.deployments(ctx, t, since)
	default:
		return g.commits(ctx, t, since)
	}
}

func (g *GitHub) workflowRuns(ctx context.Context, t Target, since time.Time) ([]Deploy, error) {
	params := url.Values{
		"created":  {">=" + since.UTC().Format(time.RFC3339)},
		"per_page": {"30"},
	}
	if t.Branch != "" {
		params.Set("branch", t.Branch)
	}
	var resp workflowRuns
	if g.cfg.MCP != nil {
		owner, repo, err := splitRepo(t.Repo)
		if err != nil {
			return nil, err
		}
		args := map[string]any{"method": "list_workflow_runs", "owner": owner, "repo": repo, "resource_id": t.Workflow, "perPage": 30}
		if t.Branch != "" {
			args["workflow_runs_filter"] = map[string]any{"branch": t.Branch}
		}
		var raw json.RawMessage
		if err := g.cfg.MCP.Call(ctx, "actions_list", args, &raw); err != nil {
			return nil, err
		}
		if err := resp.decodeMCP(raw); err != nil {
			return nil, err
		}
	} else {
		path := fmt.Sprintf("/repos/%s/actions/workflows/%s/runs", t.Repo, url.PathEscape(t.Workflow))
		if err := g.get(ctx, path, params, &resp); err != nil {
			return nil, err
		}
	}
	out := make([]Deploy, 0, len(resp.WorkflowRuns))
	for _, r := range resp.WorkflowRuns {
		// The MCP tool has no created filter; runs come newest first.
		if r.CreatedAt.Before(since) {
			continue
		}
		status := r.Status
		if r.Conclusion != "" {
			status = r.Conclusion
		}
		out = append(out, Deploy{
			Kind: "workflow_run", SHA: r.HeadSHA, Time: r.UpdatedAt, Author: r.Actor.Login,
			Title: r.DisplayTitle, Status: status, URL: r.HTMLURL,
		})
	}
	return out, nil
}

type workflowRuns struct {
	WorkflowRuns []struct {
		HeadSHA      string    `json:"head_sha"`
		DisplayTitle string    `json:"display_title"`
		Status       string    `json:"status"`
		Conclusion   string    `json:"conclusion"`
		HTMLURL      string    `json:"html_url"`
		CreatedAt    time.Time `json:"created_at"`
		UpdatedAt    time.Time `json:"updated_at"`
		Actor        struct {
			Login string `json:"login"`
		} `json:"actor"`
	} `json:"workflow_runs"`
}

// decodeMCP reads actions_list's answer, which is the REST response or,
// depending on the negotiated protocol version, that response wrapped as
// {"method": ..., "workflow_runs": <REST response>}.
func (w *workflowRuns) decodeMCP(raw json.RawMessage) error {
	var probe struct {
		WorkflowRuns json.RawMessage `json:"workflow_runs"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("github: actions_list: %w", err)
	}
	if t := strings.TrimSpace(string(probe.WorkflowRuns)); strings.HasPrefix(t, "{") {
		raw = probe.WorkflowRuns
	}
	if err := json.Unmarshal(raw, w); err != nil {
		return fmt.Errorf("github: actions_list: %w", err)
	}
	return nil
}

func (g *GitHub) deployments(ctx context.Context, t Target, since time.Time) ([]Deploy, error) {
	params := url.Values{"environment": {t.Environment}, "per_page": {"30"}}
	var resp []struct {
		SHA         string    `json:"sha"`
		Ref         string    `json:"ref"`
		Description string    `json:"description"`
		CreatedAt   time.Time `json:"created_at"`
		URL         string    `json:"url"`
		Creator     struct {
			Login string `json:"login"`
		} `json:"creator"`
	}
	if err := g.get(ctx, fmt.Sprintf("/repos/%s/deployments", t.Repo), params, &resp); err != nil {
		return nil, err
	}
	var out []Deploy
	for _, d := range resp {
		if d.CreatedAt.Before(since) {
			continue
		}
		title := d.Description
		if title == "" {
			title = "deploy " + d.Ref
		}
		out = append(out, Deploy{
			Kind: "deployment", SHA: d.SHA, Time: d.CreatedAt, Author: d.Creator.Login,
			Title: title, URL: fmt.Sprintf("https://github.com/%s/commit/%s", t.Repo, d.SHA),
		})
	}
	return out, nil
}

func (g *GitHub) commits(ctx context.Context, t Target, since time.Time) ([]Deploy, error) {
	params := url.Values{"since": {since.UTC().Format(time.RFC3339)}, "per_page": {"30"}}
	if t.Branch != "" {
		params.Set("sha", t.Branch)
	}
	var resp []rawCommit
	if g.cfg.MCP != nil {
		owner, repo, err := splitRepo(t.Repo)
		if err != nil {
			return nil, err
		}
		args := map[string]any{"owner": owner, "repo": repo, "since": since.UTC().Format(time.RFC3339), "perPage": 30}
		if t.Branch != "" {
			args["sha"] = t.Branch
		}
		if err := g.cfg.MCP.Call(ctx, "list_commits", args, &resp); err != nil {
			return nil, err
		}
	} else if err := g.get(ctx, fmt.Sprintf("/repos/%s/commits", t.Repo), params, &resp); err != nil {
		return nil, err
	}
	out := make([]Deploy, 0, len(resp))
	for _, c := range resp {
		out = append(out, Deploy{
			Kind: "commit", SHA: c.SHA, Time: c.Commit.Committer.Date, Author: c.author(),
			Title: firstLine(c.Commit.Message), URL: c.HTMLURL,
		})
	}
	return out, nil
}

// maxPatch bounds the diff returned per file.
const maxPatch = 1500

// Commit returns a commit with its changed files and truncated patches.
func (g *GitHub) Commit(ctx context.Context, repo, sha string) (*Commit, error) {
	var c rawCommit
	if g.cfg.MCP != nil {
		owner, name, err := splitRepo(repo)
		if err != nil {
			return nil, err
		}
		args := map[string]any{"owner": owner, "repo": name, "sha": sha, "detail": "full_patch", "perPage": 40}
		if err := g.cfg.MCP.Call(ctx, "get_commit", args, &c); err != nil {
			return nil, err
		}
	} else if err := g.get(ctx, fmt.Sprintf("/repos/%s/commits/%s", repo, url.PathEscape(sha)), nil, &c); err != nil {
		return nil, err
	}
	out := &Commit{SHA: c.SHA, Message: c.Commit.Message, Author: c.author(), Time: c.Commit.Committer.Date, URL: c.HTMLURL}
	for i, f := range c.Files {
		if i >= 40 {
			break
		}
		patch := f.Patch
		if len(patch) > maxPatch {
			patch = patch[:maxPatch] + "\n… (truncated)"
		}
		out.Files = append(out.Files, FileChange{Filename: f.Filename, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions, Patch: patch})
	}
	return out, nil
}

func (g *GitHub) Check(ctx context.Context) error {
	if g.cfg.MCP != nil {
		return g.cfg.MCP.Check(ctx)
	}
	var resp map[string]any
	return g.get(ctx, "/rate_limit", nil, &resp)
}

type rawCommit struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Commit  struct {
		Message   string `json:"message"`
		Committer struct {
			Date time.Time `json:"date"`
		} `json:"committer"`
		Author struct {
			Name string `json:"name"`
		} `json:"author"`
	} `json:"commit"`
	Author *struct {
		Login string `json:"login"`
	} `json:"author"`
	Files []struct {
		Filename  string `json:"filename"`
		Status    string `json:"status"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
		Patch     string `json:"patch"`
	} `json:"files"`
}

func (c rawCommit) author() string {
	if c.Author != nil && c.Author.Login != "" {
		return c.Author.Login
	}
	return c.Commit.Author.Name
}

func (g *GitHub) get(ctx context.Context, path string, params url.Values, out any) error {
	u := g.cfg.BaseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.cfg.Token)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return fmt.Errorf("github: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("github: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github: %s %s: %s", resp.Status, path, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
