package deploys

import (
	"cmp"
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openstatushq/stromboli/internal/connector/mcpconn"
	"github.com/openstatushq/stromboli/internal/connector/vercel"
)

// Vercel lists a project's deployments through Vercel's MCP server.
type Vercel struct {
	// TeamID is the team (ID or slug) owning the projects.
	TeamID string
	MCP    *mcpconn.Client
}

// vercelDeployment is a deployment as list_deployments returns it.
type vercelDeployment struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	URL          string `json:"url"`
	InspectorURL string `json:"inspectorUrl"`
	Created      int64  `json:"created"` // ms
	State        string `json:"state"`
	Target       string `json:"target"`
	Creator      struct {
		Username string `json:"username"`
	} `json:"creator"`
	// Meta holds the Git provider's metadata, e.g. githubCommitSha.
	Meta map[string]any `json:"meta"`
}

// Recent lists the project's deployments to t.Environment (default
// production) created since the given time, newest first.
func (v *Vercel) Recent(ctx context.Context, t Target, since time.Time) ([]Deploy, error) {
	if t.Project == "" {
		return nil, fmt.Errorf("vercel: project is required")
	}
	target := cmp.Or(t.Environment, "production")
	args := vercel.TeamArgs(v.TeamID)
	args["projectId"] = t.Project
	args["since"] = since.UnixMilli()
	args["limit"] = 100
	if target != "preview" {
		args["target"] = target // preview deployments have no target to filter on
	}
	if t.Branch != "" {
		args["branch"] = t.Branch
	}
	var out struct {
		Deployments struct {
			Deployments []vercelDeployment `json:"deployments"`
		} `json:"deployments"`
	}
	if err := vercel.Call(ctx, v.MCP, "list_deployments", args, &out); err != nil {
		return nil, err
	}
	var ds []Deploy
	for _, d := range out.Deployments.Deployments {
		created := time.UnixMilli(d.Created).UTC()
		if created.Before(since) || cmp.Or(d.Target, "preview") != target {
			continue
		}
		ds = append(ds, d.deploy(created))
	}
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].Time.After(ds[j].Time) })
	return ds, nil
}

// meta returns the first non-empty string among the meta keys.
func (d vercelDeployment) meta(keys ...string) string {
	for _, k := range keys {
		if s, _ := d.Meta[k].(string); s != "" {
			return s
		}
	}
	return ""
}

func (d vercelDeployment) deploy(created time.Time) Deploy {
	// Vercel names the commit fields after the Git provider.
	var sha, msg, author string
	for _, p := range []string{"github", "gitlab", "bitbucket"} {
		if s := d.meta(p + "CommitSha"); s != "" {
			sha, msg = s, d.meta(p+"CommitMessage")
			author = d.meta(p+"CommitAuthorLogin", p+"CommitAuthorName")
			break
		}
	}
	title := firstLine(msg)
	if title == "" {
		title = "deploy " + d.Name
	}
	u := d.InspectorURL
	if u == "" && d.URL != "" {
		u = "https://" + d.URL
	}
	return Deploy{
		Kind: "deployment", SHA: sha, Time: created, Author: cmp.Or(author, d.Creator.Username),
		Title: title, Status: strings.ToLower(d.State), URL: u,
	}
}

func (v *Vercel) Check(ctx context.Context) error { return vercel.Check(ctx, v.MCP, v.TeamID) }
