// Package connector resolves configured connectors (stored in Postgres with
// encrypted secrets) into backend clients.
package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/openstatushq/opensre/internal/connector/deploys"
	"github.com/openstatushq/opensre/internal/connector/fly"
	"github.com/openstatushq/opensre/internal/connector/gcp"
	"github.com/openstatushq/opensre/internal/connector/logs"
	"github.com/openstatushq/opensre/internal/connector/mcpconn"
	"github.com/openstatushq/opensre/internal/connector/metrics"
	"github.com/openstatushq/opensre/internal/secrets"
	"github.com/openstatushq/opensre/internal/store"
	"github.com/openstatushq/opensre/internal/store/db"
)

type kindSpec struct {
	required []string
	optional []string
	secret   []string
}

// Kinds lists the supported connector kinds and their config keys.
var Kinds = map[string]kindSpec{
	"loki": {
		required: []string{"url"},
		optional: []string{"tenant_id", "username", "password", "bearer_token"},
		secret:   []string{"password", "bearer_token"},
	},
	"axiom": {
		required: []string{"token"},
		optional: []string{"url", "org_id", "message_field"},
		secret:   []string{"token"},
	},
	"cloudwatch": {
		optional: []string{"region", "access_key_id", "secret_access_key", "session_token", "endpoint", "log_group_prefix", "service_field"},
		secret:   []string{"secret_access_key", "session_token"},
	},
	"prometheus": {
		required: []string{"url"},
		optional: []string{"tenant_id", "username", "password", "bearer_token", "service_label"},
		secret:   []string{"password", "bearer_token"},
	},
	"fly": {
		required: []string{"org", "token"},
		optional: []string{"url"},
		secret:   []string{"token"},
	},
	// With a token, github goes through GitHub's remote MCP server (mcp_url
	// overrides it); deployments and token-less use stay on the REST API.
	"github": {
		optional: []string{"token", "base_url", "mcp_url"},
		secret:   []string{"token"},
	},
	// fly_machines holds a token that can act on machines (fly tokens create
	// deploy), kept apart from the read-only fly metrics connector.
	"fly_machines": {
		required: []string{"org", "token"},
		optional: []string{"url"},
		secret:   []string{"token"},
	},
	// Google Cloud, through Google's remote MCP servers. Without
	// credentials_json (a service account key), Application Default
	// Credentials are used.
	"gcp_logging": {
		required: []string{"project"},
		optional: []string{"credentials_json"},
		secret:   []string{"credentials_json"},
	},
	"gcp_monitoring": {
		required: []string{"project"},
		optional: []string{"credentials_json"},
		secret:   []string{"credentials_json"},
	},
}

// KindNames returns the supported kinds, sorted.
func KindNames() []string {
	names := make([]string, 0, len(Kinds))
	for k := range Kinds {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}

// Keys returns the required and optional config keys of a kind.
func Keys(kind string) (required, optional []string) {
	spec := Kinds[kind]
	return slices.Clone(spec.required), slices.Clone(spec.optional)
}

// IsSecret reports whether key holds a secret for the given kind.
func IsSecret(kind, key string) bool { return slices.Contains(Kinds[kind].secret, key) }

// Validate checks config keys for a kind.
func Validate(kind string, config map[string]string) error {
	spec, ok := Kinds[kind]
	if !ok {
		return fmt.Errorf("unknown connector kind %q (supported: %s)", kind, strings.Join(KindNames(), ", "))
	}
	for _, k := range spec.required {
		if config[k] == "" {
			return fmt.Errorf("%s connector: %q is required", kind, k)
		}
	}
	for k := range config {
		if !slices.Contains(spec.required, k) && !slices.Contains(spec.optional, k) {
			return fmt.Errorf("%s connector: unknown config key %q", kind, k)
		}
	}
	return nil
}

// Split separates public settings from secrets and encrypts the secrets.
func Split(box *secrets.Box, kind string, config map[string]string) (public json.RawMessage, sealed []byte, err error) {
	pub := map[string]string{}
	sec := map[string]string{}
	for k, v := range config {
		if IsSecret(kind, k) {
			sec[k] = v
		} else {
			pub[k] = v
		}
	}
	if public, err = json.Marshal(pub); err != nil {
		return nil, nil, err
	}
	if len(sec) > 0 {
		raw, err := json.Marshal(sec)
		if err != nil {
			return nil, nil, err
		}
		if sealed, err = box.Seal(raw); err != nil {
			return nil, nil, err
		}
	}
	return public, sealed, nil
}

// Registry builds clients for stored connectors.
type Registry struct {
	store *store.Store
	box   *secrets.Box
	// mcp keeps one MCP session per connector across requests.
	mcp *mcpconn.Pool
}

func NewRegistry(s *store.Store, box *secrets.Box) *Registry {
	return &Registry{store: s, box: box, mcp: mcpconn.NewPool()}
}

// Close ends the connectors' MCP sessions.
func (r *Registry) Close() { r.mcp.Close() }

// gcpMCP returns the pooled MCP client of a gcp_* connector.
func (r *Registry) gcpMCP(name string, cfg map[string]string, server gcp.Server, tools []string) (*mcpconn.Client, error) {
	return r.mcp.Get(name, cfg, func() (mcpconn.Config, error) {
		return gcp.Config{Project: cfg["project"], CredentialsJSON: cfg["credentials_json"]}.MCP(server, tools...)
	})
}

// Config returns a connector's full config, secrets included.
func (r *Registry) Config(ctx context.Context, name string) (db.Connector, map[string]string, error) {
	c, err := r.store.GetConnector(ctx, name)
	if err != nil {
		if store.IsNotFound(err) {
			return c, nil, fmt.Errorf("connector %q not found", name)
		}
		return c, nil, err
	}
	cfg := map[string]string{}
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return c, nil, err
	}
	if len(c.Secrets) > 0 {
		raw, err := r.box.Open(c.Secrets)
		if err != nil {
			return c, nil, fmt.Errorf("connector %q: %w", name, err)
		}
		sec := map[string]string{}
		if err := json.Unmarshal(raw, &sec); err != nil {
			return c, nil, err
		}
		for k, v := range sec {
			cfg[k] = v
		}
	}
	return c, cfg, nil
}

// Logs returns the log source named name.
func (r *Registry) Logs(ctx context.Context, name string) (logs.Source, error) {
	c, cfg, err := r.Config(ctx, name)
	if err != nil {
		return nil, err
	}
	switch c.Kind {
	case "loki":
		return logs.NewLoki(logs.LokiConfig{
			URL: cfg["url"], TenantID: cfg["tenant_id"], Username: cfg["username"],
			Password: cfg["password"], BearerToken: cfg["bearer_token"],
		})
	case "axiom":
		return logs.NewAxiom(logs.AxiomConfig{
			URL: cfg["url"], Token: cfg["token"], OrgID: cfg["org_id"], MessageField: cfg["message_field"],
		})
	case "cloudwatch":
		return logs.NewCloudWatch(ctx, logs.CloudWatchConfig{
			Region: cfg["region"], AccessKeyID: cfg["access_key_id"], SecretAccessKey: cfg["secret_access_key"],
			SessionToken: cfg["session_token"], Endpoint: cfg["endpoint"],
			LogGroupPrefix: cfg["log_group_prefix"], ServiceField: cfg["service_field"],
		})
	case "gcp_logging":
		c, err := r.gcpMCP(name, cfg, gcp.Logging, logs.GCPLoggingTools)
		if err != nil {
			return nil, err
		}
		return logs.NewGCPLogging(cfg["project"], c), nil
	default:
		return nil, fmt.Errorf("connector %q (%s) is not a log source", name, c.Kind)
	}
}

// Metrics returns the metric source named name and its kind (presets depend on it).
func (r *Registry) Metrics(ctx context.Context, name string) (metrics.Source, string, error) {
	c, cfg, err := r.Config(ctx, name)
	if err != nil {
		return nil, "", err
	}
	var p *metrics.Prometheus
	switch c.Kind {
	case "gcp_monitoring":
		m, err := r.gcpMCP(name, cfg, gcp.Monitoring, metrics.GCPMonitoringTools)
		if err != nil {
			return nil, "", err
		}
		return metrics.NewGCPMonitoring(cfg["project"], m), c.Kind, nil
	case "prometheus":
		p, err = metrics.NewPrometheus(metrics.PrometheusConfig{
			URL: cfg["url"], TenantID: cfg["tenant_id"], Username: cfg["username"],
			Password: cfg["password"], BearerToken: cfg["bearer_token"], ServiceLabel: cfg["service_label"],
		})
	case "fly":
		p, err = metrics.NewFly(cfg["org"], cfg["token"], cfg["url"])
	default:
		return nil, "", fmt.Errorf("connector %q (%s) is not a metrics source", name, c.Kind)
	}
	if err != nil {
		return nil, "", err
	}
	return p, c.Kind, nil
}

// GitHub returns the deploy source named name.
func (r *Registry) GitHub(ctx context.Context, name string) (*deploys.GitHub, error) {
	c, cfg, err := r.Config(ctx, name)
	if err != nil {
		return nil, err
	}
	if c.Kind != "github" {
		return nil, fmt.Errorf("connector %q (%s) is not a github connector", name, c.Kind)
	}
	return deploys.NewGitHub(deploys.GitHubConfig{Token: cfg["token"]}), nil
}

// FlyMachines returns a Machines API client from a fly_machines connector,
// or from a fly metrics connector's token (enough to list machines).
func (r *Registry) FlyMachines(ctx context.Context, name string) (*fly.Machines, string, error) {
	c, cfg, err := r.Config(ctx, name)
	if err != nil {
		return nil, "", err
	}
	switch c.Kind {
	case "fly_machines":
		m, err := fly.NewMachines(cfg["org"], cfg["token"], cfg["url"])
		return m, c.Kind, err
	case "fly":
		// Its url is the Prometheus endpoint, not the Machines API.
		m, err := fly.NewMachines(cfg["org"], cfg["token"], "")
		return m, c.Kind, err
	}
	return nil, "", fmt.Errorf("connector %q (%s) is not a fly or fly_machines connector", name, c.Kind)
}

// Check verifies connectivity for any connector kind.
func (r *Registry) Check(ctx context.Context, name string) error {
	c, err := r.store.GetConnector(ctx, name)
	if err != nil {
		if store.IsNotFound(err) {
			return fmt.Errorf("connector %q not found", name)
		}
		return err
	}
	switch {
	case slices.Contains(logs.Kinds, c.Kind):
		src, err := r.Logs(ctx, name)
		if err != nil {
			return err
		}
		return src.Check(ctx)
	case slices.Contains(metrics.Kinds, c.Kind):
		src, _, err := r.Metrics(ctx, name)
		if err != nil {
			return err
		}
		return src.Check(ctx)
	case c.Kind == "fly_machines":
		m, _, err := r.FlyMachines(ctx, name)
		if err != nil {
			return err
		}
		return m.Check(ctx)
	case c.Kind == "github":
		gh, err := r.GitHub(ctx, name)
		if err != nil {
			return err
		}
		return gh.Check(ctx)
	}
	return fmt.Errorf("unknown connector kind %q", c.Kind)
}
