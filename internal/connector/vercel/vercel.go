// Package vercel holds what the Vercel deploy and log sources share: the MCP
// server config, team checks and error messages.
//
// Vercel's MCP server returns its results as text: JSON wrapped in
// {"result": ...} for API tools, Markdown for get_runtime_logs.
package vercel

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openstatushq/stromboli/internal/connector/mcpconn"
)

// MCPURL is Vercel's remote MCP server. It requires OAuth.
const MCPURL = "https://mcp.vercel.com/"

// Tools are the Vercel MCP tools stromboli may call.
var Tools = []string{"list_projects", "list_deployments", "get_runtime_logs"}

// MCP returns the MCP config of a vercel connector; token returns the current
// OAuth access token.
func MCP(endpoint string, token func(context.Context) (string, error)) mcpconn.Config {
	return mcpconn.Config{
		Name:            "vercel",
		Endpoint:        cmp.Or(endpoint, MCPURL),
		Token:           token,
		Allow:           Tools,
		RequireReadOnly: true,
	}
}

// Call calls a tool and decodes the "result" of its JSON answer into out,
// with Vercel's verbose errors reduced to their message.
func Call(ctx context.Context, c *mcpconn.Client, tool string, args map[string]any, out any) error {
	var raw json.RawMessage
	if err := c.Call(ctx, tool, args, &raw); err != nil {
		return cleanError(err)
	}
	var wrapped struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil || wrapped.Result == nil {
		return fmt.Errorf("vercel: %s: unexpected result: %.200s", tool, raw)
	}
	if err := json.Unmarshal(wrapped.Result, out); err != nil {
		return fmt.Errorf("vercel: %s: decode result: %w", tool, err)
	}
	return nil
}

// Text calls a tool that answers in text (Markdown).
func Text(ctx context.Context, c *mcpconn.Client, tool string, args map[string]any) (string, error) {
	var s string
	err := c.Call(ctx, tool, args, &s)
	return s, cleanError(err)
}

func cleanError(err error) error {
	var te *mcpconn.ToolError
	if !errors.As(err, &te) {
		return err
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(te.Message), &body) == nil && body.Error.Message != "" {
		return fmt.Errorf("vercel: %s: %s", te.Tool, body.Error.Message)
	}
	return err
}

// Project is a Vercel project.
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Projects lists up to 100 of the team's projects.
func Projects(ctx context.Context, c *mcpconn.Client, team string, limit int) ([]Project, error) {
	var out struct {
		Projects []Project `json:"projects"`
	}
	args := TeamArgs(team)
	args["limit"] = fmt.Sprint(limit)
	err := Call(ctx, c, "list_projects", args, &out)
	return out.Projects, err
}

// TeamArgs scopes an API tool call to a team: API tools take an ID ("team_...")
// as teamId and a slug as slug.
func TeamArgs(team string) map[string]any {
	if strings.HasPrefix(team, "team_") {
		return map[string]any{"teamId": team}
	}
	return map[string]any{"slug": team}
}

// Check connects and verifies that the login can read the team's projects.
func Check(ctx context.Context, c *mcpconn.Client, team string) error {
	if err := c.Check(ctx); err != nil {
		return err
	}
	if _, err := Projects(ctx, c, team, 1); err != nil {
		return fmt.Errorf("%w (is team_id %q right? It is the team's slug or ID, as in vercel.com/<slug>)", err, team)
	}
	return nil
}
