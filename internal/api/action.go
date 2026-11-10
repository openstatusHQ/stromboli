package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/types/known/timestamppb"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
	"github.com/openstatushq/stromboli/internal/action"
	"github.com/openstatushq/stromboli/internal/store/db"
)

type ActionService struct {
	actions *action.Service
}

func NewActionService(a *action.Service) *ActionService { return &ActionService{actions: a} }

func (s *ActionService) ListActions(ctx context.Context, req *connect.Request[stromboliv1.ListActionsRequest]) (*connect.Response[stromboliv1.ListActionsResponse], error) {
	m := req.Msg
	p := db.ListActionsParams{Limit: m.Limit}
	if p.Limit <= 0 || p.Limit > 200 {
		p.Limit = 50
	}
	if m.InvestigationId != "" {
		id, err := uuid.Parse(m.InvestigationId)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid investigation id %q", m.InvestigationId))
		}
		p.InvestigationID = pgtype.UUID{Bytes: id, Valid: true}
	}
	if m.Status != stromboliv1.ActionStatus_ACTION_STATUS_UNSPECIFIED {
		p.Status = statusName(m.Status)
	}
	rows, err := s.actions.List(ctx, p)
	if err != nil {
		return nil, internal(err)
	}
	resp := &stromboliv1.ListActionsResponse{}
	for _, r := range rows {
		resp.Actions = append(resp.Actions, toAction(r))
	}
	return connect.NewResponse(resp), nil
}

func (s *ActionService) GetAction(ctx context.Context, req *connect.Request[stromboliv1.GetActionRequest]) (*connect.Response[stromboliv1.GetActionResponse], error) {
	id, err := actionID(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	a, err := s.actions.Get(ctx, id)
	if err != nil {
		return nil, actionError(err)
	}
	return connect.NewResponse(&stromboliv1.GetActionResponse{Action: toAction(a)}), nil
}

func (s *ActionService) ApproveAction(ctx context.Context, req *connect.Request[stromboliv1.ApproveActionRequest]) (*connect.Response[stromboliv1.ApproveActionResponse], error) {
	id, err := actionID(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	a, err := s.actions.Approve(ctx, id, req.Msg.Approver, req.Msg.Note)
	if err != nil {
		return nil, actionError(err)
	}
	return connect.NewResponse(&stromboliv1.ApproveActionResponse{Action: toAction(a)}), nil
}

func (s *ActionService) RejectAction(ctx context.Context, req *connect.Request[stromboliv1.RejectActionRequest]) (*connect.Response[stromboliv1.RejectActionResponse], error) {
	id, err := actionID(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	a, err := s.actions.Reject(ctx, id, req.Msg.Approver, req.Msg.Reason)
	if err != nil {
		return nil, actionError(err)
	}
	return connect.NewResponse(&stromboliv1.RejectActionResponse{Action: toAction(a)}), nil
}

func actionID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return id, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid action id %q", s))
	}
	return id, nil
}

func actionError(err error) error {
	switch {
	case errors.Is(err, action.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, action.ErrNotPending):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	// Validation failures (disabled, not allowed, unknown machine...) are
	// preconditions, not server errors.
	return connect.NewError(connect.CodeFailedPrecondition, err)
}

func statusName(s stromboliv1.ActionStatus) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "ACTION_STATUS_"))
}

func toAction(a db.Action) *stromboliv1.Action {
	out := &stromboliv1.Action{
		Id:              a.ID.String(),
		InvestigationId: a.InvestigationID.String(),
		Service:         a.Service,
		Connector:       a.Connector,
		Status:          stromboliv1.ActionStatus(stromboliv1.ActionStatus_value["ACTION_STATUS_"+strings.ToUpper(a.Status)]),
		DecidedBy:       a.DecidedBy.String,
		Note:            a.Note.String,
		Result:          a.Result.String,
		Error:           a.Error.String,
		CreatedAt:       ts(a.CreatedAt),
		ExpiresAt:       ts(a.ExpiresAt),
		DecidedAt:       ts(a.DecidedAt),
		FinishedAt:      ts(a.FinishedAt),
	}
	if m, err := action.Spec(a); err == nil {
		out.MachineAction = m
	}
	return out
}

func ts(t pgtype.Timestamptz) *timestamppb.Timestamp {
	if !t.Valid {
		return nil
	}
	return timestamppb.New(t.Time)
}
