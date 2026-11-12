package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/openstatushq/stromboli/internal/connector/deploys"
	"github.com/openstatushq/stromboli/internal/connector/mcpconn/mcptest"
	"github.com/openstatushq/stromboli/internal/connector/oauth"
	"github.com/openstatushq/stromboli/internal/secrets"
	"github.com/openstatushq/stromboli/internal/store"
	"github.com/openstatushq/stromboli/internal/store/db"
)

// An OAuth connector refreshes its access token and saves the rotated
// refresh token, but not over a newer login.
func TestOAuthConnectorRotation(t *testing.T) {
	dbURL := os.Getenv("STROMBOLI_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("set STROMBOLI_TEST_DB_URL to run")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New("test")
	if err != nil {
		t.Fatal(err)
	}

	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "rt1" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"at2","refresh_token":"rt2","token_type":"Bearer","expires_in":3600}`)
	}))
	defer tokens.Close()
	mcp := mcptest.New(t, map[string]mcptest.Handler{
		"list_deployments": func(map[string]any) (any, error) {
			return map[string]any{"result": map[string]any{"deployments": map[string]any{"deployments": []any{}}}}, nil
		},
		"list_projects": func(map[string]any) (any, error) {
			return map[string]any{"result": map[string]any{"projects": []any{}}}, nil
		},
		"get_runtime_logs": func(map[string]any) (any, error) { return mcptest.Text("## Runtime Logs\n"), nil },
	}, false)

	name := fmt.Sprintf("vercel-test-%d", time.Now().UnixNano())
	g := oauth.Grant{ClientID: "cid", TokenURL: tokens.URL, RefreshToken: "rt1", AuthorizedAt: "2026-11-12T00:00:00Z"}
	cfg := g.Config()
	cfg["team_id"] = "team_1"
	cfg["mcp_url"] = mcp.URL
	if err := Validate("vercel", cfg); err != nil {
		t.Fatal(err)
	}
	public, sealed, err := Split(box, "vercel", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateConnector(ctx, db.CreateConnectorParams{Name: name, Kind: "vercel", Config: public, Secrets: sealed}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.DeleteConnector(context.Background(), name) })

	r := NewRegistry(st, box)
	defer r.Close()
	src, err := r.Deploys(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Recent(ctx, deploys.Target{Project: "web"}, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, got, err := r.Config(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if got[oauth.KeyRefreshToken] != "rt2" {
		t.Fatalf("refresh token = %q, want the rotated rt2", got[oauth.KeyRefreshToken])
	}
	// The pooled session survives the rotation.
	again, err := r.Deploys(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if again.(*deploys.Vercel).MCP != src.(*deploys.Vercel).MCP {
		t.Error("the rotation replaced the pooled MCP session")
	}

	// A rotation from before a newer login is not saved.
	stale := g
	stale.AuthorizedAt = "2026-11-01T00:00:00Z"
	stale.RefreshToken = "old"
	if err := r.saveGrant(ctx, name, stale); err == nil {
		t.Fatal("saved a refresh token over a newer login")
	}
	if _, got, _ := r.Config(ctx, name); got[oauth.KeyRefreshToken] != "rt2" {
		t.Fatalf("refresh token = %q after a stale save", got[oauth.KeyRefreshToken])
	}
	var pub map[string]string
	json.Unmarshal(public, &pub)
	if _, leaked := pub[oauth.KeyRefreshToken]; leaked {
		t.Error("the refresh token is stored in the public config")
	}
}

func TestValidateOAuthModes(t *testing.T) {
	grant := oauth.Grant{ClientID: "cid", TokenURL: "https://auth/token", RefreshToken: "rt", AuthorizedAt: "t"}.Config()
	withGrant := func(m map[string]string) map[string]string {
		for k, v := range grant {
			m[k] = v
		}
		return m
	}
	for _, c := range []struct {
		kind string
		cfg  map[string]string
		ok   bool
	}{
		{"axiom", map[string]string{"token": "xaat"}, true},                                     // REST
		{"axiom", withGrant(map[string]string{"mcp_url": "https://mcp.eu.axiom.co/mcp"}), true}, // MCP
		{"axiom", map[string]string{}, false},                                                   // neither
		{"axiom", withGrant(map[string]string{"token": "xaat"}), false},                         // OAuth keys without OAuth
		{"vercel", withGrant(map[string]string{"team_id": "acme"}), true},
		{"vercel", map[string]string{"team_id": "acme"}, false},
	} {
		if err := Validate(c.kind, c.cfg); (err == nil) != c.ok {
			t.Errorf("Validate(%s, %v) = %v, want ok=%v", c.kind, c.cfg, err, c.ok)
		}
	}
	if !IsSecret("axiom", oauth.KeyRefreshToken) || IsSecret("axiom", oauth.KeyClientID) || !IsSecret("vercel", oauth.KeyRefreshToken) {
		t.Error("OAuth secrets are not secret")
	}
	if _, ok := OAuthServer("axiom", map[string]string{"token": "x"}); ok {
		t.Error("axiom with a token uses OAuth")
	}
}
