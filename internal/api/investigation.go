package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
	"github.com/openstatushq/stromboli/internal/agent"
	"github.com/openstatushq/stromboli/internal/mcpserver"
	"github.com/openstatushq/stromboli/internal/store"
	"github.com/openstatushq/stromboli/internal/store/db"
)

type InvestigationService struct {
	store  *store.Store
	engine *agent.Engine
}

func NewInvestigationService(s *store.Store, e *agent.Engine) *InvestigationService {
	return &InvestigationService{store: s, engine: e}
}

func (i *InvestigationService) StartInvestigation(ctx context.Context, req *connect.Request[stromboliv1.StartInvestigationRequest]) (*connect.Response[stromboliv1.StartInvestigationResponse], error) {
	m := req.Msg
	if m.Service == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("service is required"))
	}
	title := m.Title
	if title == "" {
		title = "Manual investigation of " + m.Service
	}
	start := agent.StartRequest{Title: title, Source: "manual", Services: []string{m.Service}, Description: m.Description}
	// The MCP server marks the investigations it starts, and by which client.
	if req.Header().Get(mcpserver.SourceHeader) == "mcp" {
		start.Source = "mcp"
		if c := req.Header().Get(mcpserver.ClientHeader); c != "" {
			start.Alert = map[string]any{"mcp_client": truncate(c, 100)}
		}
	}
	inv, _, err := i.engine.Start(ctx, start)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&stromboliv1.StartInvestigationResponse{Investigation: toInvestigation(inv)}), nil
}

func (i *InvestigationService) GetInvestigation(ctx context.Context, req *connect.Request[stromboliv1.GetInvestigationRequest]) (*connect.Response[stromboliv1.GetInvestigationResponse], error) {
	inv, err := i.get(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	rows, err := i.store.ListSteps(ctx, db.ListStepsParams{InvestigationID: inv.ID})
	if err != nil {
		return nil, internal(err)
	}
	resp := &stromboliv1.GetInvestigationResponse{Investigation: toInvestigation(inv)}
	for _, r := range rows {
		resp.Steps = append(resp.Steps, toStep(r))
	}
	return connect.NewResponse(resp), nil
}

func (i *InvestigationService) ListInvestigations(ctx context.Context, req *connect.Request[stromboliv1.ListInvestigationsRequest]) (*connect.Response[stromboliv1.ListInvestigationsResponse], error) {
	limit := req.Msg.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := i.store.ListInvestigations(ctx, limit)
	if err != nil {
		return nil, internal(err)
	}
	resp := &stromboliv1.ListInvestigationsResponse{}
	for _, r := range rows {
		resp.Investigations = append(resp.Investigations, toInvestigation(r))
	}
	return connect.NewResponse(resp), nil
}

// watchPoll is how often Watch checks for new steps. Polling the database
// keeps Watch correct across server restarts and multiple replicas.
const watchPoll = time.Second

func (i *InvestigationService) WatchInvestigation(ctx context.Context, req *connect.Request[stromboliv1.WatchInvestigationRequest], stream *connect.ServerStream[stromboliv1.WatchInvestigationResponse]) error {
	inv, err := i.get(ctx, req.Msg.Id)
	if err != nil {
		return err
	}
	after := req.Msg.AfterSeq
	ticker := time.NewTicker(watchPoll)
	defer ticker.Stop()
	for {
		rows, err := i.store.ListSteps(ctx, db.ListStepsParams{InvestigationID: inv.ID, Seq: after})
		if err != nil {
			return internal(err)
		}
		for _, r := range rows {
			if err := stream.Send(&stromboliv1.WatchInvestigationResponse{Event: &stromboliv1.WatchInvestigationResponse_Step{Step: toStep(r)}}); err != nil {
				return err
			}
			after = r.Seq
		}
		if inv.Status != agent.StatusRunning {
			return stream.Send(&stromboliv1.WatchInvestigationResponse{Event: &stromboliv1.WatchInvestigationResponse_Finished{Finished: toInvestigation(inv)}})
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		if inv, err = i.store.Queries.GetInvestigation(ctx, inv.ID); err != nil {
			return internal(err)
		}
	}
}

func (i *InvestigationService) GetBrief(ctx context.Context, req *connect.Request[stromboliv1.GetBriefRequest]) (*connect.Response[stromboliv1.GetBriefResponse], error) {
	since := time.Hour
	if req.Msg.Since != "" {
		d, err := time.ParseDuration(req.Msg.Since)
		if err != nil || d <= 0 || d > 7*24*time.Hour {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid since %q: use a duration like 30m or 2h (max 168h)", req.Msg.Since))
		}
		since = d
	}
	now := time.Now()
	brief, err := i.engine.Brief(ctx, req.Msg.Service, agent.Window{Start: now.Add(-since), End: now})
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&stromboliv1.GetBriefResponse{Brief: brief}), nil
}

func (i *InvestigationService) get(ctx context.Context, id string) (db.Investigation, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return db.Investigation{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid id %q", id))
	}
	inv, err := i.store.Queries.GetInvestigation(ctx, uid)
	if err != nil {
		if store.IsNotFound(err) {
			return inv, connect.NewError(connect.CodeNotFound, fmt.Errorf("investigation %s not found", id))
		}
		return inv, internal(err)
	}
	return inv, nil
}

var statuses = map[string]stromboliv1.InvestigationStatus{
	agent.StatusRunning:   stromboliv1.InvestigationStatus_INVESTIGATION_STATUS_RUNNING,
	agent.StatusCompleted: stromboliv1.InvestigationStatus_INVESTIGATION_STATUS_COMPLETED,
	agent.StatusFailed:    stromboliv1.InvestigationStatus_INVESTIGATION_STATUS_FAILED,
}

func toInvestigation(r db.Investigation) *stromboliv1.Investigation {
	out := &stromboliv1.Investigation{
		Id:           r.ID.String(),
		Title:        r.Title,
		Source:       r.Source,
		Services:     r.Services,
		Status:       statuses[r.Status],
		Error:        r.Error.String,
		CreatedAt:    timestamppb.New(r.CreatedAt.Time),
		InputTokens:  r.InputTokens,
		OutputTokens: r.OutputTokens,
	}
	if r.FinishedAt.Valid {
		out.FinishedAt = timestamppb.New(r.FinishedAt.Time)
	}
	if len(r.Alert) > 0 {
		out.Alert = toStruct(r.Alert)
	}
	if len(r.Mitigation) > 0 {
		m := &stromboliv1.Mitigation{}
		if err := protojson.Unmarshal(r.Mitigation, m); err == nil {
			out.Mitigation = m
		}
	}
	if r.MitigatedAt.Valid {
		out.MitigatedAt = timestamppb.New(r.MitigatedAt.Time)
	}
	if len(r.Report) > 0 {
		rep := &stromboliv1.Report{}
		if err := protojson.Unmarshal(r.Report, rep); err == nil {
			out.Report = rep
		}
	}
	return out
}

func toStep(r db.Step) *stromboliv1.Step {
	return &stromboliv1.Step{
		Seq:        r.Seq,
		Kind:       r.Kind,
		Name:       r.Name,
		Input:      toStruct(r.Input),
		Output:     r.Output,
		IsError:    r.IsError,
		DurationMs: r.DurationMs,
		CreatedAt:  timestamppb.New(r.CreatedAt.Time),
	}
}

func toStruct(raw json.RawMessage) *structpb.Struct {
	m := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	s, err := structpb.NewStruct(m)
	if err != nil {
		return nil
	}
	return s
}

// truncate cuts s to at most n runes.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
