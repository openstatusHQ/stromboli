package oauth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// LoginConfig configures Login.
type LoginConfig struct {
	// ServerURL is the MCP server's endpoint.
	ServerURL string
	// ClientMetadataURL is stromboli's client ID metadata document, used when
	// the authorization server supports them. Otherwise stromboli registers
	// itself dynamically.
	ClientMetadataURL string
	// Out shows the URL to open. In, when set, accepts the address the browser
	// was redirected to, for logins from another machine.
	Out io.Writer
	In  io.Reader
	// OpenBrowser opens a URL; errors are ignored (the URL is printed too).
	OpenBrowser func(string) error
	// HTTPClient defaults to a client with a 30s timeout.
	HTTPClient *http.Client
}

// Login authorizes stromboli on the MCP server in a browser and returns the
// grant to store on the connector. It fails unless the server issues a
// refresh token, without which the connector would stop working within hours.
func Login(ctx context.Context, cfg LoginConfig) (Grant, error) {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	// An empty path is "/" (RFC 3986): servers at the root declare their
	// resource with it, and the SDK compares the resource exactly.
	server, err := url.Parse(cfg.ServerURL)
	if err != nil {
		return Grant{}, fmt.Errorf("invalid server URL: %w", err)
	}
	if server.Path == "" {
		server.Path = "/"
	}
	cfg.ServerURL = server.String()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Grant{}, fmt.Errorf("listen for the OAuth redirect: %w", err)
	}
	defer ln.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)

	results := make(chan callback, 2)
	srv := &http.Server{Handler: callbackHandler(results), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()
	if cfg.In != nil {
		go readPasted(cfg.In, results)
	}

	fetch := func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		fmt.Fprintf(cfg.Out, "Open this URL to authorize stromboli:\n\n  %s\n\n", args.URL)
		if cfg.In != nil {
			fmt.Fprintln(cfg.Out, "Waiting for the browser. If it runs on another machine, paste the address it was redirected to:")
		}
		if cfg.OpenBrowser != nil {
			_ = cfg.OpenBrowser(args.URL)
		}
		select {
		case r := <-results:
			if r.err != nil {
				return nil, r.err
			}
			return r.res, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	var (
		conf *oauth2.Config
		tok  *oauth2.Token
	)
	hcfg := &auth.AuthorizationCodeHandlerConfig{
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			ClientName:              "stromboli",
			ClientURI:               "https://github.com/openstatusHQ/stromboli",
			RedirectURIs:            []string{redirect},
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
			TokenEndpointAuthMethod: "none",
		}},
		RedirectURL:              redirect,
		AuthorizationCodeFetcher: fetch,
		RequestRefreshToken:      true,
		Client:                   hc,
		NewTokenSource: func(ctx context.Context, c *oauth2.Config, t *oauth2.Token) (oauth2.TokenSource, error) {
			conf, tok = c, t
			return oauth2.StaticTokenSource(t), nil
		},
	}
	if cfg.ClientMetadataURL != "" {
		hcfg.ClientIDMetadataDocumentConfig = &auth.ClientIDMetadataDocumentConfig{URL: cfg.ClientMetadataURL}
	}
	h, err := auth.NewAuthorizationCodeHandler(hcfg)
	if err != nil {
		return Grant{}, err
	}

	// An unauthenticated initialize gets the 401 whose challenge starts the flow.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.ServerURL, strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"stromboli","version":"0"}}}`))
	if err != nil {
		return Grant{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := hc.Do(req)
	if err != nil {
		return Grant{}, fmt.Errorf("reach %s: %w", cfg.ServerURL, err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		resp.Body.Close()
		return Grant{}, fmt.Errorf("%s answered %s, not 401: it does not ask for OAuth", cfg.ServerURL, resp.Status)
	}
	if err := h.Authorize(ctx, req, resp); err != nil {
		return Grant{}, fmt.Errorf("authorize: %w", err)
	}
	if conf == nil || tok == nil {
		return Grant{}, errors.New("authorize: no token was issued")
	}
	if tok.RefreshToken == "" {
		return Grant{}, errors.New("the server issued no refresh token: stromboli could not keep the connector working")
	}
	return Grant{
		ClientID:     conf.ClientID,
		ClientSecret: conf.ClientSecret,
		TokenURL:     conf.Endpoint.TokenURL,
		AuthStyle:    conf.Endpoint.AuthStyle,
		RefreshToken: tok.RefreshToken,
		AuthorizedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

type callback struct {
	res *auth.AuthorizationResult
	err error
}

// parseCallback reads the authorization response from the redirect's query.
func parseCallback(q url.Values) callback {
	if e := q.Get("error"); e != "" {
		if d := q.Get("error_description"); d != "" {
			e += ": " + d
		}
		return callback{err: fmt.Errorf("authorization denied: %s", e)}
	}
	if q.Get("code") == "" {
		return callback{err: errors.New("the redirect has no authorization code")}
	}
	return callback{res: &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}}
}

func callbackHandler(results chan<- callback) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		cb := parseCallback(r.URL.Query())
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if cb.err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "stromboli: %v\n", cb.err)
		} else {
			fmt.Fprintln(w, "stromboli is authorized. You can close this tab.")
		}
		select {
		case results <- cb:
		default:
		}
	})
	return mux
}

// readPasted accepts the redirect address pasted into the terminal.
func readPasted(in io.Reader, results chan<- callback) {
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		u, err := url.Parse(strings.TrimSpace(sc.Text()))
		if err != nil || u.Query().Get("code") == "" && u.Query().Get("error") == "" {
			continue
		}
		select {
		case results <- parseCallback(u.Query()):
		default:
		}
		return
	}
}
