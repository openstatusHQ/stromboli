package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// fakeAuth is an MCP server that requires OAuth, with its authorization server.
type fakeAuth struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	challenge string            // PKCE challenge of the pending authorization
	refresh   map[string]string // refresh token -> next refresh token
}

func newFakeAuth(t *testing.T) *fakeAuth {
	f := &fakeAuth{t: t, refresh: map[string]string{"rt1": "rt2", "rt2": "rt2"}}
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	base := f.srv.URL
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("POST /mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp"`, base))
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}})
	})
	// Also an MCP server at the root, like mcp.vercel.com.
	mux.HandleFunc("POST /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, base))
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"resource": base + "/", "authorization_servers": []string{base}})
	})
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"issuer": base, "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token",
			"registration_endpoint": base + "/register", "scopes_supported": []string{"openid", "offline_access"},
			"response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"none"},
		})
	})
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"client_id": "cid-1", "redirect_uris": m["redirect_uris"], "token_endpoint_auth_method": "none"})
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != "cid-1" || q.Get("code_challenge_method") != "S256" || !strings.Contains(q.Get("scope"), "offline_access") {
			t.Errorf("authorize query = %v", q)
		}
		f.mu.Lock()
		f.challenge = q.Get("code_challenge")
		f.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code=c1&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			f.mu.Lock()
			ok := r.Form.Get("code") == "c1" && base64.RawURLEncoding.EncodeToString(sum[:]) == f.challenge
			f.mu.Unlock()
			if !ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, map[string]string{"error": "invalid_grant"})
				return
			}
			writeJSON(w, map[string]any{"access_token": "at1", "refresh_token": "rt1", "token_type": "Bearer", "expires_in": 3600})
		case "refresh_token":
			f.mu.Lock()
			next, ok := f.refresh[r.Form.Get("refresh_token")]
			f.mu.Unlock()
			if !ok || r.Form.Get("client_id") != "cid-1" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, map[string]string{"error": "invalid_grant", "error_description": "refresh token revoked"})
				return
			}
			writeJSON(w, map[string]any{"access_token": "at-" + next, "refresh_token": next, "token_type": "Bearer", "expires_in": 3600})
		}
	})
	return f
}

// browser follows the authorization URL like a user who approves at once.
func browser(u string) error {
	resp, err := http.Get(u)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func TestLogin(t *testing.T) {
	f := newFakeAuth(t)
	var out strings.Builder
	g, err := Login(context.Background(), LoginConfig{ServerURL: f.srv.URL + "/mcp", Out: &out, OpenBrowser: browser})
	if err != nil {
		t.Fatal(err)
	}
	if g.ClientID != "cid-1" || g.TokenURL != f.srv.URL+"/token" || g.RefreshToken != "rt1" || g.AuthorizedAt == "" {
		t.Fatalf("grant = %+v", g)
	}
	if !strings.Contains(out.String(), f.srv.URL+"/authorize?") {
		t.Errorf("the authorization URL was not shown: %q", out.String())
	}
	back, err := GrantFrom(g.Config())
	if err != nil || back != g {
		t.Fatalf("GrantFrom(Config()) = %+v, %v; want %+v", back, err, g)
	}
}

// A server at the root declares its resource as "https://host/"; the URL is
// given without the slash.
func TestLoginRootServer(t *testing.T) {
	f := newFakeAuth(t)
	g, err := Login(context.Background(), LoginConfig{ServerURL: f.srv.URL, Out: &strings.Builder{}, OpenBrowser: browser})
	if err != nil {
		t.Fatal(err)
	}
	if g.RefreshToken != "rt1" {
		t.Fatalf("grant = %+v", g)
	}
}

// A user logging in from another machine pastes the redirect address.
func TestLoginPasted(t *testing.T) {
	f := newFakeAuth(t)
	paste := make(chan string, 1)
	pr, pw := io.Pipe()
	go func() { fmt.Fprintln(pw, <-paste) }()
	open := func(u string) error {
		// The redirect to 127.0.0.1 is not followed: the user copies it.
		c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := c.Get(u)
		if err != nil {
			return err
		}
		resp.Body.Close()
		paste <- resp.Header.Get("Location")
		return nil
	}
	g, err := Login(context.Background(), LoginConfig{ServerURL: f.srv.URL + "/mcp", Out: &strings.Builder{}, In: pr, OpenBrowser: open})
	if err != nil {
		t.Fatal(err)
	}
	if g.RefreshToken != "rt1" {
		t.Fatalf("grant = %+v", g)
	}
}

func TestLoginNotOAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	if _, err := Login(context.Background(), LoginConfig{ServerURL: srv.URL, Out: &strings.Builder{}}); err == nil || !strings.Contains(err.Error(), "not 401") {
		t.Fatalf("err = %v", err)
	}
}

func TestSource(t *testing.T) {
	f := newFakeAuth(t)
	var saved []string
	g := Grant{ClientID: "cid-1", TokenURL: f.srv.URL + "/token", RefreshToken: "rt1", AuthorizedAt: "t0"}
	src := NewSource(g, func(_ context.Context, g Grant) error {
		saved = append(saved, g.RefreshToken)
		return nil
	})
	ctx := context.Background()
	for range 2 {
		tok, err := src.Token(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if tok != "at-rt2" {
			t.Fatalf("token = %q", tok)
		}
	}
	// One refresh (the token is cached), whose rotated refresh token is saved.
	if fmt.Sprint(saved) != "[rt2]" {
		t.Fatalf("saved = %v", saved)
	}

	revoked := NewSource(Grant{ClientID: "cid-1", TokenURL: f.srv.URL + "/token", RefreshToken: "gone", AuthorizedAt: "t0"}, nil)
	if _, err := revoked.Token(ctx); !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("err = %v", err)
	}
	if _, err := GrantFrom(map[string]string{KeyClientID: "x"}); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("incomplete grant: %v", err)
	}
}
