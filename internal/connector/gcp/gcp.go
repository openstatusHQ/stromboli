// Package gcp connects to Google Cloud's remote MCP servers (Cloud Logging,
// Cloud Monitoring, Pub/Sub, Cloud Run) with Google credentials. Every
// connector only allow-lists read-only tools, and the servers must annotate
// them so: see mcpconn.Config.RequireReadOnly.
//
// The principal needs roles/mcp.toolUser plus the viewer role of each product
// (roles/logging.viewer, roles/monitoring.viewer, roles/pubsub.viewer,
// roles/run.viewer).
package gcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/openstatushq/opensre/internal/connector/mcpconn"
)

// Scope is the OAuth scope the MCP servers accept.
const Scope = "https://www.googleapis.com/auth/cloud-platform"

// Server is a Google Cloud product with a remote MCP server.
type Server string

const (
	Logging    Server = "logging"
	Monitoring Server = "monitoring"
	PubSub     Server = "pubsub"
	Run        Server = "run"
)

// Endpoint is the server's MCP endpoint.
func (s Server) Endpoint() string { return fmt.Sprintf("https://%s.googleapis.com/mcp", s) }

// Config is a GCP connector's config.
type Config struct {
	// Project is the project ID queried, and billed for the calls.
	Project string
	// CredentialsJSON is a service account key. When empty, Application
	// Default Credentials are used (GOOGLE_APPLICATION_CREDENTIALS, workload
	// identity, gcloud auth application-default login).
	CredentialsJSON string
	// BaseURL replaces https://<server>.googleapis.com, for tests.
	BaseURL string
}

func (c Config) validate() error {
	if strings.TrimSpace(c.Project) == "" {
		return errors.New("gcp: project is required")
	}
	return nil
}

// MCP returns the MCP client config for one server of the project, allowing
// only the given (read-only) tools.
func (c Config) MCP(s Server, allow ...string) (mcpconn.Config, error) {
	if err := c.validate(); err != nil {
		return mcpconn.Config{}, err
	}
	endpoint := s.Endpoint()
	if c.BaseURL != "" {
		endpoint = strings.TrimRight(c.BaseURL, "/") + "/" + string(s) + "/mcp"
	}
	token, err := c.tokenFunc()
	if err != nil {
		return mcpconn.Config{}, err
	}
	return mcpconn.Config{
		Name:     "gcp " + string(s),
		Endpoint: endpoint,
		// Bill quota to the project rather than to the credentials' project.
		Header:          http.Header{"X-Goog-User-Project": {c.Project}},
		Token:           token,
		Allow:           allow,
		RequireReadOnly: true,
	}, nil
}

// tokenFunc resolves credentials lazily, on the first call, so building a
// connector never fails just because credentials are not there yet.
func (c Config) tokenFunc() (func(context.Context) (string, error), error) {
	if c.BaseURL != "" && c.CredentialsJSON == "" {
		return nil, nil // tests
	}
	var (
		once sync.Once
		ts   oauth2.TokenSource
		err  error
	)
	return func(context.Context) (string, error) {
		once.Do(func() {
			// Token refreshes must not be tied to one request's context.
			ctx := context.Background()
			var creds *google.Credentials
			if c.CredentialsJSON != "" {
				creds, err = google.CredentialsFromJSONWithType(ctx, []byte(c.CredentialsJSON), google.ServiceAccount, Scope)
			} else {
				creds, err = google.FindDefaultCredentials(ctx, Scope)
			}
			if err == nil {
				ts = creds.TokenSource
			}
		})
		if err != nil {
			return "", fmt.Errorf("google credentials: %w", err)
		}
		t, err := ts.Token()
		if err != nil {
			return "", err
		}
		return t.AccessToken, nil
	}, nil
}
