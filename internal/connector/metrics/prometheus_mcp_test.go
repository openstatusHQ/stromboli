package metrics

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openstatushq/stromboli/internal/connector/mcpconn"
	"github.com/openstatushq/stromboli/internal/connector/mcpconn/mcptest"
)

// promMCPResult is a prometheus-mcp (v0.8.0+) structured result.
func promMCPResult(resultType string, result []any) map[string]any {
	return map[string]any{"backend": "fly", "query": "q", "data": map[string]any{"resultType": resultType, "result": result}}
}

func newPromMCP(t *testing.T, tools map[string]mcptest.Handler, textOnly bool) (*mcptest.Server, *PrometheusMCP) {
	t.Helper()
	// Like prometheus-mcp, the fake does not mark its tools read-only.
	srv := mcptest.New(t, tools, textOnly, PrometheusMCPTools...)
	c, err := mcpconn.New(PrometheusMCPConfig("fly", srv.URL, "tok"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return srv, NewPrometheusMCP(c, "fly", "tenant", "app")
}

func TestPrometheusMCPRange(t *testing.T) {
	srv, p := newPromMCP(t, map[string]mcptest.Handler{
		"prometheus_range_query": func(args map[string]any) (any, error) {
			return promMCPResult("matrix", []any{
				map[string]any{"metric": map[string]string{"instance": "m1"}, "values": []any{[]any{1793694695.0, "7"}, []any{1793694995.0, "NaN"}, []any{1793695295.0, "18"}}},
			}), nil
		},
		"prometheus_query": func(args map[string]any) (any, error) {
			return promMCPResult("vector", []any{map[string]any{"metric": map[string]string{}, "value": []any{1793694695.0, "1"}}}), nil
		},
	}, false)
	ctx := context.Background()
	end := time.Unix(1793698295, 0)

	series, err := p.Range(ctx, `fly_instance_up{app="api"}`, end.Add(-time.Hour), end)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || len(series[0].Samples) != 2 || series[0].Samples[1].Value != 18 || series[0].Labels["instance"] != "m1" {
		t.Fatalf("series = %+v", series)
	}
	args := srv.Calls()[0].Args
	if args["backend"] != "fly" || args["org_id"] != "tenant" || args["step"] != "15s" ||
		args["start"] != "2026-11-03T08:31:35Z" || args["end"] != "2026-11-03T09:31:35Z" {
		t.Errorf("args = %v", args)
	}

	if err := p.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if q := srv.Calls()[1].Args["query"]; q != "vector(1)" {
		t.Errorf("check query = %v", q)
	}
}

func TestPrometheusMCPNamesAndServices(t *testing.T) {
	srv, p := newPromMCP(t, map[string]mcptest.Handler{
		"prometheus_range_query": func(args map[string]any) (any, error) {
			q := args["query"].(string)
			var label string
			var values []string
			switch {
			case strings.HasPrefix(q, "group by (__name__)"):
				label, values = "__name__", []string{"fly_instance_up", "fly_app_concurrency"}
			case strings.HasPrefix(q, "group by (app)"):
				label, values = "app", []string{"web", "api"}
			}
			var result []any
			for _, v := range values {
				result = append(result, map[string]any{"metric": map[string]string{label: v}, "values": []any{[]any{1793694695.0, "1"}}})
			}
			return promMCPResult("matrix", result), nil
		},
		"prometheus_query": func(map[string]any) (any, error) { return nil, nil },
	}, false)
	ctx := context.Background()
	end := time.Unix(1793698295, 0)

	names, err := p.MetricNames(ctx, `app="api"`, end.Add(-time.Hour), end)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "fly_app_concurrency,fly_instance_up" {
		t.Errorf("names = %v", names)
	}
	args := srv.Calls()[0].Args
	if args["query"] != `group by (__name__) ({app="api"})` || args["step"] != "300s" {
		t.Errorf("args = %v", args)
	}

	services, err := p.Services(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 2 || services[0].Name != "api" || services[0].Selector != `app="api"` {
		t.Errorf("services = %+v", services)
	}
	if args := srv.Calls()[1].Args; args["query"] != `group by (app) ({app!=""})` || args["step"] != "1800s" {
		t.Errorf("args = %v", args)
	}
}

func TestPrometheusMCPOldServer(t *testing.T) {
	// Before v0.8.0, prometheus-mcp only answered in TOON text.
	_, p := newPromMCP(t, map[string]mcptest.Handler{
		"prometheus_range_query": func(map[string]any) (any, error) {
			return mcptest.Text("Range Query Results [fly]:\n\nResults:\n[1]{metric,values}: ..."), nil
		},
		"prometheus_query": func(map[string]any) (any, error) { return nil, nil },
	}, true)
	end := time.Now()
	_, err := p.Range(context.Background(), "up", end.Add(-time.Hour), end)
	if err == nil || !strings.Contains(err.Error(), "v0.8.0") {
		t.Fatalf("err = %v, want the version hint", err)
	}
}

func TestPrometheusMCPConfigToken(t *testing.T) {
	if h := PrometheusMCPConfig("p", "http://x/mcp", "").Header; h != nil {
		t.Errorf("header without token = %v", h)
	}
	if h := PrometheusMCPConfig("p", "http://x/mcp", "tok").Header.Get("Authorization"); h != "Bearer tok" {
		t.Errorf("authorization = %q", h)
	}
}
