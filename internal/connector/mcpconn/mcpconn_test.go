package mcpconn

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoIn struct {
	Word string `json:"word"`
}

type echoOut struct {
	Echo string `json:"echo"`
}

// testServer serves read-only "echo" and "fail" tools and a writable
// "delete" tool, and records the Authorization header it saw.
func testServer(t *testing.T) (*httptest.Server, *atomic.Value) {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Annotations: ro}, func(ctx context.Context, req *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
		return nil, echoOut{Echo: in.Word}, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "fail", Annotations: ro}, func(ctx context.Context, req *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
		return nil, nil, errors.New("project not found")
	})
	mcp.AddTool(s, &mcp.Tool{Name: "delete"}, func(ctx context.Context, req *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
		t.Error("delete must never be called")
		return nil, nil, nil
	})
	var auth atomic.Value
	auth.Store("")
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &auth
}

func TestCall(t *testing.T) {
	srv, auth := testServer(t)
	c, err := New(Config{
		Name: "test", Endpoint: srv.URL, Allow: []string{"echo", "fail"}, RequireReadOnly: true,
		Token: func(context.Context) (string, error) { return "tok", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	var out echoOut
	if err := c.Call(ctx, "echo", echoIn{Word: "hi"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Echo != "hi" {
		t.Errorf("echo = %q, want hi", out.Echo)
	}
	if got := auth.Load().(string); got != "Bearer tok" {
		t.Errorf("Authorization = %q", got)
	}

	err = c.Call(ctx, "fail", echoIn{}, nil)
	var te *ToolError
	if !errors.As(err, &te) || !strings.Contains(te.Message, "project not found") {
		t.Errorf("fail: err = %v, want a ToolError with the message", err)
	}

	if err := c.Call(ctx, "delete", echoIn{}, nil); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("delete: err = %v, want ErrNotAllowed", err)
	}
}

func TestRequireReadOnly(t *testing.T) {
	srv, _ := testServer(t)
	c, _ := New(Config{Name: "test", Endpoint: srv.URL, Allow: []string{"echo", "delete"}, RequireReadOnly: true})
	defer c.Close()
	err := c.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refusing to use delete") {
		t.Fatalf("err = %v, want a refusal of delete", err)
	}
}

func TestMissingTool(t *testing.T) {
	srv, _ := testServer(t)
	c, _ := New(Config{Name: "test", Endpoint: srv.URL, Allow: []string{"echo", "list_things"}})
	defer c.Close()
	err := c.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not offer list_things") {
		t.Fatalf("err = %v, want a missing tool error", err)
	}
}

func TestPool(t *testing.T) {
	p := NewPool()
	defer p.Close()
	build := func() (Config, error) { return Config{Name: "x", Endpoint: "http://x", Allow: []string{"a"}}, nil }
	a, _ := p.Get("x", map[string]string{"token": "1"}, build)
	b, _ := p.Get("x", map[string]string{"token": "1"}, build)
	c, _ := p.Get("x", map[string]string{"token": "2"}, build)
	if a != b {
		t.Error("same config should reuse the client")
	}
	if a == c {
		t.Error("changed config should build a new client")
	}
}
