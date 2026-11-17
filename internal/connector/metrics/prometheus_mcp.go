package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/openstatushq/stromboli/internal/connector/mcpconn"
)

// PrometheusMCPTools are the tools of prometheus-mcp
// (github.com/freepik-company/prometheus-mcp) the connector calls.
var PrometheusMCPTools = []string{"prometheus_query", "prometheus_range_query"}

// PrometheusMCPConfig is the MCP config of a prometheus or fly connector with
// an mcp_url. token, when set, is sent as a bearer token (for a server behind
// JWT validation or an authenticating proxy).
func PrometheusMCPConfig(name, endpoint, token string) mcpconn.Config {
	var h http.Header
	if token != "" {
		h = http.Header{"Authorization": {"Bearer " + token}}
	}
	return mcpconn.Config{
		Name:            "prometheus-mcp " + name,
		Endpoint:        endpoint,
		Header:          h,
		Allow:           PrometheusMCPTools,
		RequireReadOnly: true,
		// prometheus-mcp does not annotate its tools, but both only evaluate
		// PromQL: they cannot change anything.
		ReadOnlyUse: PrometheusMCPTools,
	}
}

// PrometheusMCP queries a Prometheus HTTP API backend through a
// prometheus-mcp server, which holds the backend's URL and credentials.
type PrometheusMCP struct {
	mcp *mcpconn.Client
	// backend names one of the server's backends; empty when it has one.
	backend string
	// orgID is sent as org_id (X-Scope-OrgId), for multi-tenant backends.
	orgID string
	// serviceLabel is the label discovery proposes one service per value of.
	serviceLabel string
}

var _ Source = (*PrometheusMCP)(nil)

func NewPrometheusMCP(c *mcpconn.Client, backend, orgID, serviceLabel string) *PrometheusMCP {
	return &PrometheusMCP{mcp: c, backend: backend, orgID: orgID, serviceLabel: serviceLabel}
}

func (p *PrometheusMCP) args(m map[string]any) map[string]any {
	if p.backend != "" {
		m["backend"] = p.backend
	}
	if p.orgID != "" {
		m["org_id"] = p.orgID
	}
	return m
}

// errNoStructured explains a result without structuredContent: prometheus-mcp
// before v0.8.0 only answers in TOON text.
func errNoStructured(err error) error {
	var se *json.SyntaxError
	if !errors.As(err, &se) {
		return err
	}
	return fmt.Errorf("%w (stromboli needs prometheus-mcp v0.8.0 or later, which returns structured results)", err)
}

func (p *PrometheusMCP) Range(ctx context.Context, expr string, start, end time.Time) ([]Series, error) {
	return p.rangeQuery(ctx, expr, start, end, rangeStep(start, end))
}

func (p *PrometheusMCP) rangeQuery(ctx context.Context, expr string, start, end time.Time, step time.Duration) ([]Series, error) {
	var resp matrixResponse
	err := p.mcp.Call(ctx, "prometheus_range_query", p.args(map[string]any{
		"query": expr,
		"start": start.UTC().Format(time.RFC3339),
		"end":   end.UTC().Format(time.RFC3339),
		"step":  fmt.Sprintf("%ds", int64(step.Seconds())),
	}), &resp)
	if err != nil {
		return nil, errNoStructured(err)
	}
	return resp.series(), nil
}

// presenceStep samples [start, end] at most n times, at least every 5m: the
// lookback of an instant evaluation, so a series reporting throughout the
// window is seen even with the smallest step.
func presenceStep(start, end time.Time, n int) time.Duration {
	return max(end.Sub(start)/time.Duration(n), 5*time.Minute).Truncate(time.Second)
}

// labelValues returns the values of label among the series matching
// selector at some point in [start, end]. The server only offers PromQL, so
// this groups by the label instead of calling the label values API.
func (p *PrometheusMCP) labelValues(ctx context.Context, label, selector string, start, end time.Time, n int) ([]string, error) {
	var resp matrixResponse
	err := p.mcp.Call(ctx, "prometheus_range_query", p.args(map[string]any{
		"query": fmt.Sprintf("group by (%s) ({%s})", label, selector),
		"start": start.UTC().Format(time.RFC3339),
		"end":   end.UTC().Format(time.RFC3339),
		"step":  fmt.Sprintf("%ds", int64(presenceStep(start, end, n).Seconds())),
	}), &resp)
	if err != nil {
		return nil, errNoStructured(err)
	}
	seen := map[string]bool{}
	for _, r := range resp.Data.Result {
		if v := r.Metric[label]; v != "" {
			seen[v] = true
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out, nil
}

func (p *PrometheusMCP) MetricNames(ctx context.Context, matchers string, start, end time.Time) ([]string, error) {
	sel := matchers
	if sel == "" {
		sel = `__name__=~".+"`
	}
	return p.labelValues(ctx, "__name__", sel, start, end, 100)
}

// Services proposes one service per value of the service label seen in the
// last 24 hours (sampled every 30 minutes).
func (p *PrometheusMCP) Services(ctx context.Context) ([]Service, error) {
	end := time.Now()
	labels := serviceLabels
	if p.serviceLabel != "" {
		labels = []string{p.serviceLabel}
	}
	for _, name := range labels {
		values, err := p.labelValues(ctx, name, fmt.Sprintf(`%s!=""`, name), end.Add(-24*time.Hour), end, 48)
		if err != nil {
			return nil, err
		}
		if len(values) == 0 {
			continue
		}
		out := make([]Service, 0, len(values))
		for _, v := range values {
			out = append(out, Service{Name: v, Selector: fmt.Sprintf("%s=%q", name, v)})
		}
		return out, nil
	}
	return nil, nil
}

// Check connects, verifies the tools and evaluates a trivial query against
// the backend.
func (p *PrometheusMCP) Check(ctx context.Context) error {
	var resp struct {
		Data struct {
			ResultType string `json:"resultType"`
		} `json:"data"`
	}
	if err := p.mcp.Call(ctx, "prometheus_query", p.args(map[string]any{"query": "vector(1)"}), &resp); err != nil {
		return errNoStructured(err)
	}
	if resp.Data.ResultType == "" {
		return errors.New("prometheus-mcp: the result has no data (stromboli needs prometheus-mcp v0.8.0 or later, which returns structured results)")
	}
	return nil
}
