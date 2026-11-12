// Package oauth connects to remote MCP servers that require OAuth, following
// the MCP authorization spec: the CLI logs in once in a browser (Login), and
// the server keeps the connector working with the refresh token (Source).
//
// Discovery, client registration (client ID metadata document, else dynamic
// registration), PKCE and the code exchange are done by the MCP SDK.
package oauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// Connector config keys filled by Login. Secrets are client_secret and
// refresh_token.
const (
	KeyClientID     = "client_id"
	KeyClientSecret = "client_secret"
	KeyTokenURL     = "token_url"
	KeyAuthStyle    = "auth_style"
	KeyRefreshToken = "refresh_token"
	// KeyAuthorizedAt changes on every login: a rotated refresh token is not
	// saved over a newer login, and pooled sessions are rebuilt after one.
	KeyAuthorizedAt = "authorized_at"
)

// Keys lists the config keys Login fills.
var Keys = []string{KeyClientID, KeyClientSecret, KeyTokenURL, KeyAuthStyle, KeyRefreshToken, KeyAuthorizedAt}

// SecretKeys lists the Keys that are secrets.
var SecretKeys = []string{KeyClientSecret, KeyRefreshToken}

// ErrLoginRequired is returned when the refresh token is expired or revoked.
var ErrLoginRequired = errors.New("the OAuth login expired or was revoked")

// Grant is what a login leaves on the connector.
type Grant struct {
	ClientID     string
	ClientSecret string // empty for public clients
	TokenURL     string
	AuthStyle    oauth2.AuthStyle
	RefreshToken string
	AuthorizedAt string
}

// Config returns the grant as connector config keys.
func (g Grant) Config() map[string]string {
	cfg := map[string]string{
		KeyClientID:     g.ClientID,
		KeyTokenURL:     g.TokenURL,
		KeyRefreshToken: g.RefreshToken,
		KeyAuthorizedAt: g.AuthorizedAt,
	}
	if g.ClientSecret != "" {
		cfg[KeyClientSecret] = g.ClientSecret
	}
	if g.AuthStyle != oauth2.AuthStyleAutoDetect {
		cfg[KeyAuthStyle] = strconv.Itoa(int(g.AuthStyle))
	}
	return cfg
}

// GrantFrom reads a grant from connector config.
func GrantFrom(cfg map[string]string) (Grant, error) {
	g := Grant{
		ClientID:     cfg[KeyClientID],
		ClientSecret: cfg[KeyClientSecret],
		TokenURL:     cfg[KeyTokenURL],
		RefreshToken: cfg[KeyRefreshToken],
		AuthorizedAt: cfg[KeyAuthorizedAt],
	}
	if g.ClientID == "" || g.TokenURL == "" || g.RefreshToken == "" || g.AuthorizedAt == "" {
		return g, ErrLoginRequired
	}
	if s := cfg[KeyAuthStyle]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return g, fmt.Errorf("invalid %s %q", KeyAuthStyle, s)
		}
		g.AuthStyle = oauth2.AuthStyle(n)
	}
	return g, nil
}

// Source returns access tokens for a grant, refreshing them as needed. A
// rotated refresh token is passed to save before it is used.
type Source struct {
	mu    sync.Mutex
	grant Grant
	conf  *oauth2.Config
	tok   *oauth2.Token
	save  func(context.Context, Grant) error
}

func NewSource(g Grant, save func(context.Context, Grant) error) *Source {
	return &Source{
		grant: g,
		conf: &oauth2.Config{
			ClientID:     g.ClientID,
			ClientSecret: g.ClientSecret,
			Endpoint:     oauth2.Endpoint{TokenURL: g.TokenURL, AuthStyle: g.AuthStyle},
		},
		save: save,
	}
}

// Token returns a valid access token.
func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tok != nil && (s.tok.Expiry.IsZero() || time.Until(s.tok.Expiry) > time.Minute) {
		return s.tok.AccessToken, nil
	}
	tok, err := s.conf.TokenSource(ctx, &oauth2.Token{RefreshToken: s.grant.RefreshToken}).Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.ErrorCode == "invalid_grant" {
			return "", fmt.Errorf("%w (%s)", ErrLoginRequired, re.ErrorDescription)
		}
		return "", fmt.Errorf("refresh the OAuth token: %w", err)
	}
	if tok.RefreshToken != "" && tok.RefreshToken != s.grant.RefreshToken {
		g := s.grant
		g.RefreshToken = tok.RefreshToken
		if err := s.save(ctx, g); err != nil {
			// The new token works in memory, but a restart will need a new login.
			slog.Warn("could not save the rotated OAuth refresh token", "err", err)
		}
		s.grant = g
	}
	s.tok = tok
	return tok.AccessToken, nil
}
