package mcpconn

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// A failed call must only drop the session it used: concurrent calls may
// already be on a newer one.
func TestResetIfOnlyClosesItsSession(t *testing.T) {
	srv, _ := testServer(t)
	c, err := New(Config{Name: "test", Endpoint: srv.URL, Allow: []string{"echo"}, RequireReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	s1, err := c.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c.resetIf(s1)
	s2, err := c.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s2 == s1 {
		t.Fatal("resetIf on the current session should drop it")
	}
	c.resetIf(s1) // a late failure on the old session
	c.resetIf(nil)
	if s3, _ := c.connect(ctx); s3 != s2 {
		t.Error("resetIf on a stale session closed the current one")
	}
	var out echoOut
	if err := c.Call(ctx, "echo", echoIn{Word: "hi"}, &out); err != nil || out.Echo != "hi" {
		t.Errorf("call after resets: %v, %q", err, out.Echo)
	}
}

// An error the server answered with means the session works: it is kept.
func TestServerErrorKeepsSession(t *testing.T) {
	srv, _ := testServer(t)
	c, err := New(Config{Name: "test", Endpoint: srv.URL, Allow: []string{"echo"}, RequireReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	s1, err := c.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Bypass the allow-list to get a JSON-RPC error from the server.
	_, _, err = c.call(ctx, "no-such-tool", map[string]any{})
	if err == nil || !isServerError(err) {
		t.Fatalf("unknown tool: err = %v, want a JSON-RPC error from the server", err)
	}
	if s2, _ := c.connect(ctx); s2 != s1 {
		t.Error("the session was replaced")
	}

	if !isServerError(fmt.Errorf("wrapped: %w", &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams})) {
		t.Error("a wrapped JSON-RPC error is a server error")
	}
	if isServerError(errors.New("connection reset by peer")) {
		t.Error("a transport error is not a server error")
	}
}
