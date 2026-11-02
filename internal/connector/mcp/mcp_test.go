package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeServer serves a read-only tool, a mutating tool and a failing tool,
// and requires the bearer token when one is given.
func fakeServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	srv := sdk.NewServer(&sdk.Implementation{Name: "fake", Version: "v0"}, nil)
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"region": map[string]any{"type": "string"}},
		"required":   []any{"region"},
	}
	srv.AddTool(&sdk.Tool{
		Name: "rollout.status", Description: "Rollout state per region.", InputSchema: schema,
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var args struct{ Region string }
		_ = json.Unmarshal(req.Params.Arguments, &args)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{
			Text: "region=" + args.Region + " replicas=3/5 password=hunter2 owner=bob@example.com",
		}}}, nil
	})
	srv.AddTool(&sdk.Tool{
		Name: "rollout.restart", Description: "Restart a deployment.", InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		t.Error("a mutating tool was called")
		return &sdk.CallToolResult{}, nil
	})
	srv.AddTool(&sdk.Tool{
		Name: "broken", InputSchema: map[string]any{"type": "object"},
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true, Title: "Always fails"},
	}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: "upstream unavailable"}}}, nil
	})
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, &sdk.StreamableHTTPOptions{JSONResponse: true})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func names(ts []Tool) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}

func TestOpenExposesReadOnlyTools(t *testing.T) {
	ts := fakeServer(t, "s3cret")
	ctx := context.Background()

	if _, err := Open(ctx, "k8s", Config{URL: ts.URL}); err == nil {
		t.Fatal("expected an error without the bearer token")
	}

	s, err := Open(ctx, "k8s", Config{URL: ts.URL, BearerToken: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got := strings.Join(names(s.Tools()), ",")
	if got != "mcp__k8s__rollout_status,mcp__k8s__broken" && got != "mcp__k8s__broken,mcp__k8s__rollout_status" {
		t.Fatalf("exposed tools = %s", got)
	}
	if s.Hidden != 1 {
		t.Errorf("hidden = %d, want 1 (rollout.restart)", s.Hidden)
	}

	var status, broken Tool
	for _, tool := range s.Tools() {
		switch tool.Remote {
		case "rollout.status":
			status = tool
		case "broken":
			broken = tool
		}
	}
	if !strings.Contains(status.Description, `MCP connector "k8s"`) || status.InputSchema["properties"] == nil {
		t.Errorf("unexpected tool definition: %+v", status)
	}
	if !strings.Contains(broken.Description, "Always fails") {
		t.Errorf("description should fall back to the title: %q", broken.Description)
	}

	out, err := status.Call(ctx, json.RawMessage(`{"region":"eu-west"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "region=eu-west replicas=3/5") {
		t.Errorf("output = %q", out)
	}
	if strings.Contains(out, "hunter2") || strings.Contains(out, "bob@example.com") {
		t.Errorf("output not redacted: %q", out)
	}

	if _, err := broken.Call(ctx, nil); err == nil || !strings.Contains(err.Error(), "upstream unavailable") {
		t.Errorf("err = %v, want the tool's error text", err)
	}
}

func TestOpenAllowlist(t *testing.T) {
	ts := fakeServer(t, "")
	s, err := Open(context.Background(), "k8s", Config{URL: ts.URL, Tools: ParseTools(" broken , ")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := names(s.Tools()); len(got) != 1 || got[0] != "mcp__k8s__broken" {
		t.Fatalf("exposed tools = %v", got)
	}
	if s.Hidden != 2 {
		t.Errorf("hidden = %d, want 2", s.Hidden)
	}
}

func TestToolName(t *testing.T) {
	if got := ToolName("prod k8s", "pods/list"); got != "mcp__prod_k8s__pods_list" {
		t.Errorf("got %q", got)
	}
	if got := ToolName(strings.Repeat("a", 40), strings.Repeat("b", 40)); len(got) != maxToolName {
		t.Errorf("len = %d, want %d", len(got), maxToolName)
	}
}
