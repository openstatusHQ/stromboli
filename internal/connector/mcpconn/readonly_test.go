package mcpconn_test

import (
	"context"
	"strings"
	"testing"

	"github.com/openstatushq/stromboli/internal/connector/mcpconn"
	"github.com/openstatushq/stromboli/internal/connector/mcpconn/mcptest"
)

// A tool the server does not mark read-only is refused unless the connector
// lists it as used read-only.
func TestReadOnlyUse(t *testing.T) {
	srv := mcptest.New(t, map[string]mcptest.Handler{
		"query": func(map[string]any) (any, error) { return map[string]any{}, nil },
	}, false, "query")
	ctx := context.Background()
	strict, _ := mcpconn.New(mcpconn.Config{Name: "fake", Endpoint: srv.URL, Allow: []string{"query"}, RequireReadOnly: true})
	defer strict.Close()
	if err := strict.Check(ctx); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("err = %v", err)
	}
	lenient, _ := mcpconn.New(mcpconn.Config{Name: "fake", Endpoint: srv.URL, Allow: []string{"query"}, RequireReadOnly: true, ReadOnlyUse: []string{"query"}})
	defer lenient.Close()
	if err := lenient.Check(ctx); err != nil {
		t.Fatal(err)
	}
}
