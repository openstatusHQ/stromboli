// Package mcptest serves fake MCP tools over Streamable HTTP for connector
// tests.
package mcptest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openstatushq/stromboli/internal/connector/mcpconn"
)

// Handler answers a tool call with a JSON-able result, or an error the
// client sees as a tool error.
type Handler func(args map[string]any) (any, error)

// Text is a result sent as is, as text content: servers that answer in
// Markdown.
type Text string

// Server records the calls it receives.
type Server struct {
	URL string

	mu    sync.Mutex
	calls []Call
}

type Call struct {
	Tool string
	Args map[string]any
}

// Calls returns the calls received so far.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// New serves the tools, all annotated read-only unless listed in writable.
// Results are sent as structured content unless textOnly, like servers that
// only return JSON as text.
func New(t *testing.T, tools map[string]Handler, textOnly bool, writable ...string) *Server {
	t.Helper()
	srv := &Server{}
	s := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	for name, h := range tools {
		ro := true
		for _, w := range writable {
			if w == name {
				ro = false
			}
		}
		tool := &mcp.Tool{
			Name:        name,
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: ro},
		}
		s.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := map[string]any{}
			_ = json.Unmarshal(req.Params.Arguments, &args)
			srv.mu.Lock()
			srv.calls = append(srv.calls, Call{Tool: name, Args: args})
			srv.mu.Unlock()
			out, err := h(args)
			if err != nil {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
			}
			if t, ok := out.(Text); ok {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(t)}}}, nil
			}
			b, _ := json.Marshal(out)
			res := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
			if !textOnly {
				res.StructuredContent = json.RawMessage(b)
			}
			return res, nil
		})
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	hs := httptest.NewServer(h)
	t.Cleanup(hs.Close)
	srv.URL = hs.URL
	return srv
}

// Client returns a client of the server allowed to call tools.
func (s *Server) Client(t *testing.T, tools ...string) *mcpconn.Client {
	t.Helper()
	c, err := mcpconn.New(mcpconn.Config{Name: "fake", Endpoint: s.URL, Allow: tools, RequireReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
