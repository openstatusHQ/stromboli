package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
)

type tools struct {
	api  API
	poll time.Duration
	log  *slog.Logger
}

func ptr[T any](v T) *T { return &v }

var (
	readOnly = &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}
	// Brief reads the service's logs, metrics and deploys from the providers.
	readOnlyOpenWorld = &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(true)}
	// Starting an investigation spends LLM tokens and posts to Slack, but
	// changes nothing in production.
	startsWork = &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(false), IdempotentHint: false, OpenWorldHint: ptr(true)}
)

const (
	maxText        = 64 << 10
	maxStepOutput  = 2000
	maxStepsPerGet = 50
	defaultWait    = 60
	maxWait        = 300
)

func (t *tools) register(s *mcp.Server) {
	add(t, s, &mcp.Tool{
		Name:        "list_services",
		Title:       "List services",
		Description: "List every service in stromboli's catalog with its tier, owners and direct dependencies.",
		Annotations: readOnly,
	}, t.listServices)
	add(t, s, &mcp.Tool{
		Name:        "get_service",
		Title:       "Get a service",
		Description: "Get a service's catalog entry: owners, runtime, where its logs, metrics and deploys live, runbooks, and the services upstream and downstream of it.",
		Annotations: readOnly,
	}, t.getService)
	add(t, s, &mcp.Tool{
		Name:        "get_brief",
		Title:       "Get a service brief",
		Description: "Build the brief an on-call engineer looks at first, without the LLM: log error patterns against the previous window, per-instance metrics, recent deploys of the service and its dependencies, runbooks and similar past investigations. Free and read-only.",
		Annotations: readOnlyOpenWorld,
	}, t.getBrief)
	add(t, s, &mcp.Tool{
		Name:        "list_investigations",
		Title:       "List investigations",
		Description: "List recent investigations, newest first, optionally only those of a service or with a status.",
		Annotations: readOnly,
	}, t.listInvestigations)
	add(t, s, &mcp.Tool{
		Name:        "get_investigation",
		Title:       "Get an investigation",
		Description: "Get an investigation: its status, the mitigation proposed first, and the root-cause report once finished. Set include_steps for its audit trail (tool calls and their output), paged with after_seq.",
		Annotations: readOnly,
	}, t.getInvestigation)
	add(t, s, &mcp.Tool{
		Name:        "wait_investigation",
		Title:       "Wait for an investigation",
		Description: "Wait until a running investigation has published its mitigation (until=mitigation, the default) or has finished (until=finished), for at most timeout_s seconds, sending progress as steps happen. Returns what get_investigation returns. Call it again if it is still running.",
		Annotations: readOnly,
	}, t.waitInvestigation)
	add(t, s, &mcp.Tool{
		Name:        "start_investigation",
		Title:       "Start an investigation",
		Description: "Start an investigation of a catalog service: stromboli proposes a mitigation within a few tool calls, then looks for the root cause. It spends LLM tokens and posts to Slack, but only reads from production. Returns the investigation ID right away; follow it with wait_investigation.",
		Annotations: startsWork,
	}, t.startInvestigation)
	add(t, s, &mcp.Tool{
		Name:        "list_actions",
		Title:       "List machine actions",
		Description: "List machine actions stromboli proposed (restart, clone, cordon... on Fly.io), with their status and who approved them. Approving one needs a named human, with the stromboli CLI or in Slack.",
		Annotations: readOnly,
	}, t.listActions)
	add(t, s, &mcp.Tool{
		Name:        "get_action",
		Title:       "Get a machine action",
		Description: "Get one machine action: what it does, its status, who approved or rejected it, and its result.",
		Annotations: readOnly,
	}, t.getAction)
}

// result is what every tool returns: markdown for the model and the API
// response as structured content.
type result struct {
	text string
	data proto.Message
}

// add registers a tool whose handler returns a result. Errors from the API
// become tool errors with the API's message, and every call is logged.
func add[In any](t *tools, s *mcp.Server, tool *mcp.Tool, h func(context.Context, *mcp.CallToolRequest, In) (result, error)) {
	mcp.AddTool(s, tool, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		start := time.Now()
		r, err := h(ctx, req, in)
		attrs := []any{"tool", tool.Name, "client", clientName(req), "duration", time.Since(start)}
		if err != nil {
			t.log.Info("mcp tool call failed", append(attrs, "err", err)...)
			return nil, nil, toolError(err)
		}
		t.log.Debug("mcp tool call", attrs...)
		res := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: clip(r.text, maxText)}}}
		if r.data == nil {
			return res, nil, nil
		}
		b, err := protojson.Marshal(r.data)
		if err != nil {
			return nil, nil, err
		}
		return res, json.RawMessage(b), nil
	})
}

// toolError keeps the API's message without Connect's code prefix.
func toolError(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return errors.New(ce.Message())
	}
	return err
}

func clientName(req *mcp.CallToolRequest) string {
	if req == nil || req.Session == nil {
		return ""
	}
	p := req.Session.InitializeParams()
	if p == nil || p.ClientInfo == nil {
		return ""
	}
	name := p.ClientInfo.Name
	if p.ClientInfo.Version != "" {
		name += " " + p.ClientInfo.Version
	}
	return clip(name, 100)
}

// --- catalog

type noInput struct{}

func (t *tools) listServices(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (result, error) {
	resp, err := t.api.Catalog.ListServices(ctx, connect.NewRequest(&stromboliv1.ListServicesRequest{}))
	if err != nil {
		return result{}, err
	}
	return result{text: servicesMarkdown(resp.Msg.Services), data: resp.Msg}, nil
}

type serviceInput struct {
	Name string `json:"name" jsonschema:"catalog service name, as listed by list_services"`
}

func (t *tools) getService(ctx context.Context, _ *mcp.CallToolRequest, in serviceInput) (result, error) {
	if in.Name == "" {
		return result{}, errors.New("name is required")
	}
	resp, err := t.api.Catalog.GetService(ctx, connect.NewRequest(&stromboliv1.GetServiceRequest{Name: in.Name}))
	if err != nil {
		return result{}, err
	}
	return result{text: serviceMarkdown(resp.Msg), data: resp.Msg}, nil
}

// --- investigations

type briefInput struct {
	Service string `json:"service" jsonschema:"catalog service name"`
	Since   string `json:"since,omitempty" jsonschema:"look-back window as a duration, e.g. 30m or 2h (default 1h, max 168h)"`
}

func (t *tools) getBrief(ctx context.Context, _ *mcp.CallToolRequest, in briefInput) (result, error) {
	if in.Service == "" {
		return result{}, errors.New("service is required")
	}
	resp, err := t.api.Investigations.GetBrief(ctx, connect.NewRequest(&stromboliv1.GetBriefRequest{Service: in.Service, Since: in.Since}))
	if err != nil {
		return result{}, err
	}
	return result{text: resp.Msg.Brief, data: resp.Msg}, nil
}

type listInvestigationsInput struct {
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum number of investigations (default 10, max 50)"`
	Service string `json:"service,omitempty" jsonschema:"only investigations of this service"`
	Status  string `json:"status,omitempty" jsonschema:"only investigations with this status: running, completed or failed"`
}

var investigationStatuses = map[string]stromboliv1.InvestigationStatus{
	"running":   stromboliv1.InvestigationStatus_INVESTIGATION_STATUS_RUNNING,
	"completed": stromboliv1.InvestigationStatus_INVESTIGATION_STATUS_COMPLETED,
	"failed":    stromboliv1.InvestigationStatus_INVESTIGATION_STATUS_FAILED,
}

func (t *tools) listInvestigations(ctx context.Context, _ *mcp.CallToolRequest, in listInvestigationsInput) (result, error) {
	limit := clampInt(in.Limit, 10, 50)
	var status stromboliv1.InvestigationStatus
	if in.Status != "" {
		s, ok := investigationStatuses[strings.ToLower(in.Status)]
		if !ok {
			return result{}, fmt.Errorf("invalid status %q: use running, completed or failed", in.Status)
		}
		status = s
	}
	fetch := int32(limit)
	if in.Service != "" || status != 0 {
		fetch = 200 // filtered here: the API has no filters
	}
	resp, err := t.api.Investigations.ListInvestigations(ctx, connect.NewRequest(&stromboliv1.ListInvestigationsRequest{Limit: fetch}))
	if err != nil {
		return result{}, err
	}
	out := &stromboliv1.ListInvestigationsResponse{}
	for _, inv := range resp.Msg.Investigations {
		if len(out.Investigations) == limit {
			break
		}
		if status != 0 && inv.Status != status {
			continue
		}
		if in.Service != "" && !contains(inv.Services, in.Service) {
			continue
		}
		out.Investigations = append(out.Investigations, inv)
	}
	return result{text: investigationsMarkdown(out.Investigations), data: out}, nil
}

type getInvestigationInput struct {
	ID           string `json:"id" jsonschema:"investigation ID"`
	IncludeSteps bool   `json:"include_steps,omitempty" jsonschema:"also return the audit trail: brief, tool calls, model turns"`
	AfterSeq     int64  `json:"after_seq,omitempty" jsonschema:"with include_steps, only steps after this sequence number"`
}

func (t *tools) getInvestigation(ctx context.Context, _ *mcp.CallToolRequest, in getInvestigationInput) (result, error) {
	if in.ID == "" {
		return result{}, errors.New("id is required")
	}
	resp, err := t.api.Investigations.GetInvestigation(ctx, connect.NewRequest(&stromboliv1.GetInvestigationRequest{Id: in.ID}))
	if err != nil {
		return result{}, err
	}
	return investigationResult(resp.Msg, in.IncludeSteps, in.AfterSeq), nil
}

// investigationResult renders an investigation, with its steps after
// afterSeq when asked, at most maxStepsPerGet of them.
func investigationResult(msg *stromboliv1.GetInvestigationResponse, includeSteps bool, afterSeq int64) result {
	out := &stromboliv1.GetInvestigationResponse{Investigation: msg.Investigation}
	var more int64
	if includeSteps {
		for _, s := range msg.Steps {
			if s.Seq <= afterSeq {
				continue
			}
			if len(out.Steps) == maxStepsPerGet {
				more = out.Steps[len(out.Steps)-1].Seq
				break
			}
			out.Steps = append(out.Steps, s)
		}
	}
	text := investigationMarkdown(out.Investigation)
	if includeSteps {
		text += "\n\n" + stepsMarkdown(out.Steps, more)
	}
	return result{text: text, data: out}
}

type waitInput struct {
	ID       string `json:"id" jsonschema:"investigation ID"`
	Until    string `json:"until,omitempty" jsonschema:"mitigation (default): return once the mitigation is published; finished: once the report is written"`
	TimeoutS int    `json:"timeout_s,omitempty" jsonschema:"maximum seconds to wait (default 60, max 300)"`
}

func (t *tools) waitInvestigation(ctx context.Context, req *mcp.CallToolRequest, in waitInput) (result, error) {
	if in.ID == "" {
		return result{}, errors.New("id is required")
	}
	untilFinished := false
	switch strings.ToLower(in.Until) {
	case "", "mitigation":
	case "finished":
		untilFinished = true
	default:
		return result{}, fmt.Errorf("invalid until %q: use mitigation or finished", in.Until)
	}
	timeout := time.Duration(clampInt(in.TimeoutS, defaultWait, maxWait)) * time.Second
	deadline := time.Now().Add(timeout)

	var token any
	if req != nil && req.Params != nil {
		token = req.Params.GetProgressToken()
	}
	var lastSeq int64
	ticker := time.NewTicker(t.poll)
	defer ticker.Stop()
	for {
		resp, err := t.api.Investigations.GetInvestigation(ctx, connect.NewRequest(&stromboliv1.GetInvestigationRequest{Id: in.ID}))
		if err != nil {
			return result{}, err
		}
		inv := resp.Msg.Investigation
		for _, s := range resp.Msg.Steps {
			if s.Seq <= lastSeq {
				continue
			}
			lastSeq = s.Seq
			if token != nil && req.Session != nil {
				_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
					ProgressToken: token,
					Progress:      float64(s.Seq),
					Message:       stepLine(s),
				})
			}
		}
		running := inv.Status == stromboliv1.InvestigationStatus_INVESTIGATION_STATUS_RUNNING
		done := !running || (!untilFinished && inv.Mitigation != nil)
		if done || !time.Now().Before(deadline) {
			r := investigationResult(resp.Msg, false, 0)
			if !done {
				r.text = fmt.Sprintf("Still running after %s: call wait_investigation again.\n\n", timeout) + r.text
			}
			return r, nil
		}
		select {
		case <-ctx.Done():
			return result{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

type startInput struct {
	Service     string `json:"service" jsonschema:"catalog service to investigate"`
	Description string `json:"description,omitempty" jsonschema:"what was observed, e.g. '5xx on /v1/monitors since 14:05'"`
	Title       string `json:"title,omitempty" jsonschema:"short title (default: Investigation of <service>)"`
}

func (t *tools) startInvestigation(ctx context.Context, req *mcp.CallToolRequest, in startInput) (result, error) {
	if in.Service == "" {
		return result{}, errors.New("service is required")
	}
	title := in.Title
	if title == "" {
		title = "Investigation of " + in.Service
	}
	r := connect.NewRequest(&stromboliv1.StartInvestigationRequest{Service: in.Service, Title: title, Description: in.Description})
	r.Header().Set(SourceHeader, "mcp")
	if c := clientName(req); c != "" {
		r.Header().Set(ClientHeader, c)
	}
	resp, err := t.api.Investigations.StartInvestigation(ctx, r)
	if err != nil {
		return result{}, err
	}
	inv := resp.Msg.Investigation
	text := fmt.Sprintf("Started investigation %s of %s.\nCall wait_investigation with id %q to get the mitigation as soon as it is published.", inv.Id, in.Service, inv.Id)
	return result{text: text, data: resp.Msg}, nil
}

// --- actions

type listActionsInput struct {
	InvestigationID string `json:"investigation_id,omitempty" jsonschema:"only the actions of this investigation"`
	Status          string `json:"status,omitempty" jsonschema:"only actions with this status: proposed, running, succeeded, failed, rejected, expired or superseded"`
	Limit           int    `json:"limit,omitempty" jsonschema:"maximum number of actions (default 20, max 50)"`
}

func (t *tools) listActions(ctx context.Context, _ *mcp.CallToolRequest, in listActionsInput) (result, error) {
	m := &stromboliv1.ListActionsRequest{InvestigationId: in.InvestigationID, Limit: int32(clampInt(in.Limit, 20, 50))}
	if in.Status != "" {
		v, ok := stromboliv1.ActionStatus_value["ACTION_STATUS_"+strings.ToUpper(in.Status)]
		if !ok || v == 0 {
			return result{}, fmt.Errorf("invalid status %q: use proposed, running, succeeded, failed, rejected, expired or superseded", in.Status)
		}
		m.Status = stromboliv1.ActionStatus(v)
	}
	resp, err := t.api.Actions.ListActions(ctx, connect.NewRequest(m))
	if err != nil {
		return result{}, err
	}
	return result{text: actionsMarkdown(resp.Msg.Actions), data: resp.Msg}, nil
}

type getActionInput struct {
	ID string `json:"id" jsonschema:"action ID"`
}

func (t *tools) getAction(ctx context.Context, _ *mcp.CallToolRequest, in getActionInput) (result, error) {
	if in.ID == "" {
		return result{}, errors.New("id is required")
	}
	resp, err := t.api.Actions.GetAction(ctx, connect.NewRequest(&stromboliv1.GetActionRequest{Id: in.ID}))
	if err != nil {
		return result{}, err
	}
	return result{text: actionMarkdown(resp.Msg.Action), data: resp.Msg}, nil
}

func clampInt(v, def, maxV int) int {
	if v <= 0 {
		return def
	}
	return min(v, maxV)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
