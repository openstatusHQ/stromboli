package mcpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/types/known/timestamppb"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
	"github.com/openstatushq/stromboli/gen/stromboli/v1/stromboliv1connect"
	"github.com/openstatushq/stromboli/internal/api"
	"github.com/openstatushq/stromboli/internal/mcpserver"
)

var update = flag.Bool("update", false, "rewrite testdata/tools.golden.json")

// fakeAPI implements the API services in memory.
type fakeAPI struct {
	stromboliv1connect.UnimplementedCatalogServiceHandler
	stromboliv1connect.UnimplementedInvestigationServiceHandler
	stromboliv1connect.UnimplementedActionServiceHandler

	mu     sync.Mutex
	invs   []*stromboliv1.Investigation
	steps  map[string][]*stromboliv1.Step
	starts []http.Header
	// onGet is called on every GetInvestigation, to move an investigation along.
	onGet func(f *fakeAPI, calls int)
	gets  int
}

func newFake() *fakeAPI {
	return &fakeAPI{
		invs: []*stromboliv1.Investigation{
			{Id: "inv-2", Title: "5xx on api", Source: "alertmanager", Services: []string{"api"},
				Status: stromboliv1.InvestigationStatus_INVESTIGATION_STATUS_RUNNING, CreatedAt: timestamppb.New(time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC))},
			{Id: "inv-1", Title: "checker memory", Source: "healthcheck", Services: []string{"checker"},
				Status:     stromboliv1.InvestigationStatus_INVESTIGATION_STATUS_COMPLETED,
				CreatedAt:  timestamppb.New(time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)),
				Mitigation: &stromboliv1.Mitigation{Kind: "restart", Target: "checker", Risk: "safe", Confidence: "high", Action: "fly apps restart checker", ActionId: "act-1"},
				Report:     &stromboliv1.Report{LikelyCause: "memory leak in the scheduler", Confidence: "high", Evidence: []string{"memory_used_percent at 97%"}}},
		},
		steps: map[string][]*stromboliv1.Step{
			"inv-1": {
				{Seq: 1, Kind: "brief", Output: "brief output"},
				{Seq: 2, Kind: "tool_call", Name: "get_metrics", Output: "memory 97%", DurationMs: 120},
			},
		},
	}
}

func (f *fakeAPI) ListServices(context.Context, *connect.Request[stromboliv1.ListServicesRequest]) (*connect.Response[stromboliv1.ListServicesResponse], error) {
	return connect.NewResponse(&stromboliv1.ListServicesResponse{Services: []*stromboliv1.Service{
		{Name: "api", Description: "Public REST API", Tier: 1, Owners: []string{"@backend"}, DependsOn: []string{"db"}},
		{Name: "db"},
	}}), nil
}

func (f *fakeAPI) GetService(_ context.Context, req *connect.Request[stromboliv1.GetServiceRequest]) (*connect.Response[stromboliv1.GetServiceResponse], error) {
	if req.Msg.Name != "api" {
		return nil, connect.NewError(connect.CodeNotFound, errors.New(`service "`+req.Msg.Name+`" not found`))
	}
	return connect.NewResponse(&stromboliv1.GetServiceResponse{
		Service: &stromboliv1.Service{Name: "api", Tier: 1, Logs: &stromboliv1.LogsConfig{Source: "loki", Selector: `{service_name="api"}`},
			Deploys: &stromboliv1.DeploysConfig{Source: "github", Repo: "acme/api", Workflow: "deploy.yml"}},
		Upstream:   []string{"db"},
		Downstream: []string{"web"},
	}), nil
}

func (f *fakeAPI) GetBrief(_ context.Context, req *connect.Request[stromboliv1.GetBriefRequest]) (*connect.Response[stromboliv1.GetBriefResponse], error) {
	return connect.NewResponse(&stromboliv1.GetBriefResponse{Brief: "# Brief for " + req.Msg.Service + " since " + req.Msg.Since}), nil
}

func (f *fakeAPI) ListInvestigations(_ context.Context, req *connect.Request[stromboliv1.ListInvestigationsRequest]) (*connect.Response[stromboliv1.ListInvestigationsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	invs := f.invs
	if int(req.Msg.Limit) < len(invs) {
		invs = invs[:req.Msg.Limit]
	}
	return connect.NewResponse(&stromboliv1.ListInvestigationsResponse{Investigations: invs}), nil
}

func (f *fakeAPI) GetInvestigation(_ context.Context, req *connect.Request[stromboliv1.GetInvestigationRequest]) (*connect.Response[stromboliv1.GetInvestigationResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if f.onGet != nil {
		f.onGet(f, f.gets)
	}
	for _, inv := range f.invs {
		if inv.Id == req.Msg.Id {
			return connect.NewResponse(&stromboliv1.GetInvestigationResponse{Investigation: inv, Steps: f.steps[inv.Id]}), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, errors.New("investigation "+req.Msg.Id+" not found"))
}

func (f *fakeAPI) StartInvestigation(_ context.Context, req *connect.Request[stromboliv1.StartInvestigationRequest]) (*connect.Response[stromboliv1.StartInvestigationResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, req.Header().Clone())
	inv := &stromboliv1.Investigation{Id: "inv-new", Title: req.Msg.Title, Services: []string{req.Msg.Service},
		Status: stromboliv1.InvestigationStatus_INVESTIGATION_STATUS_RUNNING}
	f.invs = append([]*stromboliv1.Investigation{inv}, f.invs...)
	return connect.NewResponse(&stromboliv1.StartInvestigationResponse{Investigation: inv}), nil
}

func (f *fakeAPI) ListActions(_ context.Context, req *connect.Request[stromboliv1.ListActionsRequest]) (*connect.Response[stromboliv1.ListActionsResponse], error) {
	a := proposedAction()
	if req.Msg.Status != 0 && req.Msg.Status != a.Status {
		return connect.NewResponse(&stromboliv1.ListActionsResponse{}), nil
	}
	return connect.NewResponse(&stromboliv1.ListActionsResponse{Actions: []*stromboliv1.Action{a}}), nil
}

func (f *fakeAPI) GetAction(context.Context, *connect.Request[stromboliv1.GetActionRequest]) (*connect.Response[stromboliv1.GetActionResponse], error) {
	return connect.NewResponse(&stromboliv1.GetActionResponse{Action: proposedAction()}), nil
}

func proposedAction() *stromboliv1.Action {
	return &stromboliv1.Action{Id: "act-1", InvestigationId: "inv-1", Service: "checker", Status: stromboliv1.ActionStatus_ACTION_STATUS_PROPOSED,
		MachineAction: &stromboliv1.MachineAction{Action: "restart", MachineIds: []string{"148e"}, App: "checker"}}
}

func (f *fakeAPI) api() mcpserver.API {
	return mcpserver.API{Catalog: f, Investigations: f, Actions: f}
}

// connectClient connects an in-memory MCP client to a server on the fake.
func connectClient(t *testing.T, a mcpserver.API, copts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	s := mcpserver.New(a, mcpserver.Options{PollInterval: 5 * time.Millisecond})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.2"}, copts)
	cs, err := c.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, map[string]any) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if res.IsError {
		t.Fatalf("%s: tool error: %s", name, text)
	}
	var data map[string]any
	if res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(b, &data)
	}
	return text, data
}

func callErr(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if !res.IsError {
		t.Fatalf("%s: want a tool error, got %v", name, res.Content)
	}
	return res.Content[0].(*mcp.TextContent).Text
}

func mustContain(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}

// TestTools pins tool names, annotations and input schemas: changing them
// breaks MCP clients, and buf breaking does not see them.
func TestTools(t *testing.T) {
	cs := connectClient(t, newFake().api(), nil)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	type entry struct {
		Name        string               `json:"name"`
		Annotations *mcp.ToolAnnotations `json:"annotations"`
		InputSchema any                  `json:"inputSchema"`
	}
	var got []entry
	for _, tool := range res.Tools {
		got = append(got, entry{tool.Name, tool.Annotations, tool.InputSchema})
		if tool.Annotations == nil {
			t.Errorf("%s has no annotations", tool.Name)
			continue
		}
		if !tool.Annotations.ReadOnlyHint && tool.Name != "start_investigation" {
			t.Errorf("%s is not read-only", tool.Name)
		}
		if strings.Contains(tool.Name, "approve") || strings.Contains(tool.Name, "reject") || strings.Contains(tool.Name, "delete") {
			t.Errorf("%s must not be exposed", tool.Name)
		}
	}
	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	golden := filepath.Join("testdata", "tools.golden.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/mcpserver -update)", err)
	}
	if string(want) != string(b) {
		t.Errorf("tools changed; if intended, run go test ./internal/mcpserver -update\ngot:\n%s", b)
	}
}

func TestCatalogAndBrief(t *testing.T) {
	cs := connectClient(t, newFake().api(), nil)

	text, data := call(t, cs, "list_services", nil)
	mustContain(t, text, "2 services", "api (tier 1; owners @backend; depends on db): Public REST API")
	if svcs, _ := data["services"].([]any); len(svcs) != 2 {
		t.Errorf("structured services = %v", data)
	}

	text, _ = call(t, cs, "get_service", map[string]any{"name": "api"})
	mustContain(t, text, "# api", "Logs: loki `{service_name=\"api\"}`", "Deploys: github acme/api (workflow deploy.yml)", "Downstream (affected when it fails): web")

	msg := callErr(t, cs, "get_service", map[string]any{"name": "nope"})
	if msg != `service "nope" not found` {
		t.Errorf("error = %q, want the API message without the Connect code", msg)
	}

	text, _ = call(t, cs, "get_brief", map[string]any{"service": "api", "since": "2h"})
	mustContain(t, text, "# Brief for api since 2h")
}

func TestInvestigations(t *testing.T) {
	cs := connectClient(t, newFake().api(), nil)

	text, data := call(t, cs, "list_investigations", nil)
	mustContain(t, text, "inv-2 [running] api", "inv-1 [completed] checker", "mitigation: restart checker", "likely cause: memory leak")
	if invs, _ := data["investigations"].([]any); len(invs) != 2 {
		t.Errorf("structured investigations = %v", data)
	}

	text, _ = call(t, cs, "list_investigations", map[string]any{"service": "checker"})
	if strings.Contains(text, "inv-2") || !strings.Contains(text, "inv-1") {
		t.Errorf("service filter: %s", text)
	}
	text, _ = call(t, cs, "list_investigations", map[string]any{"status": "running"})
	if !strings.Contains(text, "inv-2") || strings.Contains(text, "inv-1") {
		t.Errorf("status filter: %s", text)
	}
	callErr(t, cs, "list_investigations", map[string]any{"status": "bogus"})

	text, data = call(t, cs, "get_investigation", map[string]any{"id": "inv-1"})
	mustContain(t, text, "# checker memory", "## Mitigation", "fly apps restart checker", "cannot approve it", "## Report", "memory leak in the scheduler")
	if strings.Contains(text, "## Steps") || data["steps"] != nil {
		t.Errorf("steps returned without include_steps")
	}

	text, data = call(t, cs, "get_investigation", map[string]any{"id": "inv-1", "include_steps": true, "after_seq": 1})
	mustContain(t, text, "## Steps", "#2 tool_call get_metrics (120ms)", "memory 97%")
	if strings.Contains(text, "#1 brief") {
		t.Errorf("after_seq ignored: %s", text)
	}
	if steps, _ := data["steps"].([]any); len(steps) != 1 {
		t.Errorf("structured steps = %v", data["steps"])
	}
}

func TestStartInvestigationMarksMCP(t *testing.T) {
	f := newFake()
	cs := connectClient(t, f.api(), nil)
	text, data := call(t, cs, "start_investigation", map[string]any{"service": "api", "description": "5xx on /v1"})
	mustContain(t, text, "Started investigation inv-new of api", "wait_investigation")
	if inv, _ := data["investigation"].(map[string]any); inv["title"] != "Investigation of api" {
		t.Errorf("investigation = %v", data)
	}
	if len(f.starts) != 1 {
		t.Fatalf("starts = %d", len(f.starts))
	}
	h := f.starts[0]
	if h.Get(mcpserver.SourceHeader) != "mcp" || h.Get(mcpserver.ClientHeader) != "test-client 1.2" {
		t.Errorf("headers = %v", h)
	}
}

func TestWaitInvestigation(t *testing.T) {
	f := newFake()
	// The mitigation of inv-2 is published on the third poll, after two steps.
	f.onGet = func(f *fakeAPI, calls int) {
		switch calls {
		case 2:
			f.steps["inv-2"] = []*stromboliv1.Step{{Seq: 1, Kind: "brief"}, {Seq: 2, Kind: "tool_call", Name: "log_patterns"}}
		case 3:
			f.steps["inv-2"] = append(f.steps["inv-2"], &stromboliv1.Step{Seq: 3, Kind: "mitigation"})
			f.invs[0].Mitigation = &stromboliv1.Mitigation{Kind: "rollback", Target: "api"}
		}
	}
	var mu sync.Mutex
	var progress []string
	cs := connectClient(t, f.api(), &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			mu.Lock()
			progress = append(progress, req.Params.Message)
			mu.Unlock()
		},
	})
	params := &mcp.CallToolParams{Name: "wait_investigation", Arguments: map[string]any{"id": "inv-2", "timeout_s": 5}}
	params.SetProgressToken("tok")
	res, err := cs.CallTool(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	mustContain(t, text, "## Mitigation", "rollback api")
	if strings.Contains(text, "Still running") {
		t.Errorf("reported a timeout: %s", text)
	}
	// Progress notifications are delivered asynchronously.
	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		n := len(progress)
		mu.Unlock()
		if n == 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(progress, "|") != "#1 brief|#2 tool_call log_patterns|#3 mitigation" {
		t.Errorf("progress = %q", progress)
	}
}

func TestWaitInvestigationTimesOut(t *testing.T) {
	f := newFake()
	cs := connectClient(t, f.api(), nil)
	start := time.Now()
	text, _ := call(t, cs, "wait_investigation", map[string]any{"id": "inv-2", "until": "finished", "timeout_s": 1})
	if time.Since(start) < time.Second {
		t.Errorf("returned before the timeout")
	}
	mustContain(t, text, "Still running after 1s", "# 5xx on api")

	// A finished investigation returns right away.
	text, _ = call(t, cs, "wait_investigation", map[string]any{"id": "inv-1", "until": "finished"})
	mustContain(t, text, "## Report")
	callErr(t, cs, "wait_investigation", map[string]any{"id": "inv-1", "until": "soon"})
}

func TestActions(t *testing.T) {
	cs := connectClient(t, newFake().api(), nil)
	text, _ := call(t, cs, "list_actions", map[string]any{"status": "proposed"})
	mustContain(t, text, "act-1 [proposed] checker: restart 148e of app checker")
	text, _ = call(t, cs, "list_actions", map[string]any{"status": "succeeded"})
	mustContain(t, text, "No actions match.")
	callErr(t, cs, "list_actions", map[string]any{"status": "unspecified"})

	text, _ = call(t, cs, "get_action", map[string]any{"id": "act-1"})
	mustContain(t, text, "# Action act-1", "stromboli actions approve act-1 --as <your name>", "cannot approve it")
}

// TestHTTP serves the tools at /mcp with a token, and calls them through
// Connect clients of a real API server, as `stromboli mcp` does.
func TestHTTP(t *testing.T) {
	f := newFake()
	apiMux := http.NewServeMux()
	apiOpts := connect.WithInterceptors(api.NewAuthInterceptor("api-secret"))
	apiMux.Handle(stromboliv1connect.NewCatalogServiceHandler(f, apiOpts))
	apiMux.Handle(stromboliv1connect.NewInvestigationServiceHandler(f, apiOpts))
	apiMux.Handle(stromboliv1connect.NewActionServiceHandler(f, apiOpts))
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)

	clientOpts := connect.WithInterceptors(api.BearerToken("api-secret"))
	clients := mcpserver.API{
		Catalog:        stromboliv1connect.NewCatalogServiceClient(apiSrv.Client(), apiSrv.URL, clientOpts),
		Investigations: stromboliv1connect.NewInvestigationServiceClient(apiSrv.Client(), apiSrv.URL, clientOpts),
		Actions:        stromboliv1connect.NewActionServiceClient(apiSrv.Client(), apiSrv.URL, clientOpts),
	}
	mcpSrv := httptest.NewServer(mcpserver.Handler(mcpserver.New(clients, mcpserver.Options{}), "mcp-secret"))
	t.Cleanup(mcpSrv.Close)

	connectHTTP := func(token string) (*mcp.ClientSession, error) {
		hc := &http.Client{Transport: bearerTransport{token: token}}
		c := mcp.NewClient(&mcp.Implementation{Name: "http-client", Version: "1"}, nil)
		return c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: mcpSrv.URL, HTTPClient: hc}, nil)
	}

	if _, err := connectHTTP("wrong"); err == nil {
		t.Fatal("connected with a wrong token")
	}
	cs, err := connectHTTP("mcp-secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	text, _ := call(t, cs, "list_services", nil)
	mustContain(t, text, "2 services")

	// The headers reach the API server through the Connect client.
	call(t, cs, "start_investigation", map[string]any{"service": "api"})
	if h := f.starts[0]; h.Get(mcpserver.SourceHeader) != "mcp" || h.Get(mcpserver.ClientHeader) != "http-client 1" {
		t.Errorf("headers = %v", h)
	}
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}
