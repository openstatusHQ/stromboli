// Package action runs approved mitigations. Investigations propose them; a
// human approves or rejects each one; only then does opensre call the
// provider. Every action is recorded with who decided, when, and what it did.
//
// Guardrails, all required for an action to run:
//   - OPENSRE_ACTIONS_ENABLED=true (which itself requires OPENSRE_API_TOKEN),
//   - the service's catalog entry names a fly_machines connector (fly.actions),
//   - the action is in the service's allow-list (fly.allow),
//   - every machine belongs to the service's Fly app, checked when proposed
//     and again when approved,
//   - a named human approves it before it expires (OPENSRE_ACTION_TTL).
package action

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/encoding/protojson"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/internal/catalog"
	"github.com/openstatushq/opensre/internal/config"
	"github.com/openstatushq/opensre/internal/connector"
	"github.com/openstatushq/opensre/internal/connector/fly"
	"github.com/openstatushq/opensre/internal/store"
	"github.com/openstatushq/opensre/internal/store/db"
)

const (
	StatusProposed   = "proposed"
	StatusRunning    = "running"
	StatusSucceeded  = "succeeded"
	StatusFailed     = "failed"
	StatusRejected   = "rejected"
	StatusExpired    = "expired"
	StatusSuperseded = "superseded"

	// KindFlyMachine is a Fly.io Machines API call.
	KindFlyMachine = "fly_machine"
)

// MaxMachines bounds how many machines one action touches.
const MaxMachines = 10

// waitStarted is how long a restarted, started or cloned machine gets to
// come up before the action stops (the Machines API caps waits at 60s).
const waitStarted = 60 * time.Second

// execTimeout bounds a whole action.
const execTimeout = 10 * time.Minute

var (
	// ErrNotFound is returned for an unknown action ID.
	ErrNotFound = errors.New("action not found")
	// ErrNotPending is returned when an action was already decided or expired.
	ErrNotPending = errors.New("action is not waiting for approval")
)

// Notifier publishes action outcomes, e.g. in the investigation's Slack thread.
type Notifier interface {
	ActionFinished(ctx context.Context, inv db.Investigation, a db.Action, m *opensrev1.MachineAction) error
}

// Service proposes, approves and runs actions.
type Service struct {
	cfg      config.Config
	store    *store.Store
	reg      *connector.Registry
	notifier Notifier
}

// New returns an action service. notifier may be nil.
func New(cfg config.Config, s *store.Store, reg *connector.Registry, n Notifier) *Service {
	return &Service{cfg: cfg, store: s, reg: reg, notifier: n}
}

// Executable reports which machine actions opensre may run for svc, and the
// connector that runs them. The error says why none can run.
func (s *Service) Executable(svc *opensrev1.Service) (conn string, allowed []string, err error) {
	switch {
	case !s.cfg.ActionsEnabled:
		return "", nil, errors.New("machine actions are disabled on this opensre instance (OPENSRE_ACTIONS_ENABLED)")
	case !catalog.IsFly(svc):
		return "", nil, fmt.Errorf("service %q does not run on Fly.io", svc.Name)
	case svc.Fly == nil || svc.Fly.Actions == "":
		return "", nil, fmt.Errorf("service %q has no fly.actions connector in the catalog", svc.Name)
	}
	return svc.Fly.Actions, catalog.AllowedMachineActions(svc), nil
}

// Propose validates a machine action against the catalog and the app's
// machines, and stores it for approval. It fills in m.Service and m.App.
func (s *Service) Propose(ctx context.Context, inv db.Investigation, graph *catalog.Graph, m *opensrev1.MachineAction) (db.Action, error) {
	if m.Service == "" {
		if len(inv.Services) != 1 {
			return db.Action{}, errors.New("service is required when the investigation covers several services")
		}
		m.Service = inv.Services[0]
	}
	svc, ok := graph.Service(m.Service)
	if !ok {
		return db.Action{}, fmt.Errorf("service %q is not in the catalog", m.Service)
	}
	conn, _, err := s.validate(ctx, svc, m)
	if err != nil {
		return db.Action{}, err
	}
	spec, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	if err != nil {
		return db.Action{}, err
	}
	a, err := s.store.CreateAction(ctx, db.CreateActionParams{
		ID:              uuid.New(),
		InvestigationID: inv.ID,
		Service:         svc.Name,
		Kind:            KindFlyMachine,
		Spec:            spec,
		Connector:       conn,
		ExpiresAt:       pgtype.Timestamptz{Time: time.Now().Add(s.cfg.ActionTTL), Valid: true},
	})
	if err != nil {
		return a, err
	}
	slog.Info("action proposed", "action", a.ID, "investigation", inv.ID, "service", svc.Name,
		"app", m.App, "do", m.Action, "machines", m.MachineIds)
	return a, nil
}

// Supersede marks the investigation's pending actions as replaced, when its
// mitigation is revised.
func (s *Service) Supersede(ctx context.Context, investigation uuid.UUID) error {
	_, err := s.store.SupersedeActions(ctx, investigation)
	return err
}

// validate checks m against the current catalog entry and the app's
// machines, normalises it, and returns the connector and a client.
func (s *Service) validate(ctx context.Context, svc *opensrev1.Service, m *opensrev1.MachineAction) (string, *fly.Machines, error) {
	conn, allowed, err := s.Executable(svc)
	if err != nil {
		return "", nil, err
	}
	if !slices.Contains(catalog.MachineActions, m.Action) {
		return "", nil, fmt.Errorf("unknown machine action %q (use %s)", m.Action, strings.Join(catalog.MachineActions, ", "))
	}
	if !slices.Contains(allowed, m.Action) {
		return "", nil, fmt.Errorf("%q is not allowed for %s (fly.allow: %s)", m.Action, svc.Name, strings.Join(allowed, ", "))
	}
	ids := make([]string, 0, len(m.MachineIds))
	for _, id := range m.MachineIds {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	switch {
	case len(ids) == 0:
		return "", nil, errors.New("machine_ids is required: pick the machines from the brief's Fly.io section")
	case len(ids) > MaxMachines:
		return "", nil, fmt.Errorf("at most %d machines per action", MaxMachines)
	}
	m.MachineIds = ids
	if m.Action != "clone" {
		m.Region = ""
	}
	app := catalog.FlyApp(svc)
	if m.App != "" && m.App != app {
		return "", nil, fmt.Errorf("the action targets app %q but %s is now app %q in the catalog", m.App, svc.Name, app)
	}
	m.App = app

	client, kind, err := s.reg.FlyMachines(ctx, conn)
	if err != nil {
		return "", nil, err
	}
	if kind != "fly_machines" {
		return "", nil, fmt.Errorf("connector %q is a %s connector: fly.actions needs a fly_machines connector", conn, kind)
	}
	machines, err := client.List(ctx, app)
	if err != nil {
		return "", nil, fmt.Errorf("list machines of %s: %w", app, err)
	}
	for _, id := range ids {
		i := slices.IndexFunc(machines, func(mc fly.Machine) bool { return mc.ID == id })
		if i < 0 {
			return "", nil, fmt.Errorf("machine %q is not a machine of app %s", id, app)
		}
		if m.Action == "clone" && machines[i].HasVolumes() {
			return "", nil, fmt.Errorf("machine %s: %w", id, fly.ErrHasVolumes)
		}
	}
	return conn, client, nil
}

// Get returns an action, marking it expired first if it is past its TTL.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (db.Action, error) {
	if _, err := s.store.ExpireActions(ctx); err != nil {
		return db.Action{}, err
	}
	a, err := s.store.GetAction(ctx, id)
	if store.IsNotFound(err) {
		return a, ErrNotFound
	}
	return a, err
}

// List returns actions, newest first.
func (s *Service) List(ctx context.Context, p db.ListActionsParams) ([]db.Action, error) {
	if _, err := s.store.ExpireActions(ctx); err != nil {
		return nil, err
	}
	return s.store.ListActions(ctx, p)
}

// Spec decodes an action's machine action.
func Spec(a db.Action) (*opensrev1.MachineAction, error) {
	m := &opensrev1.MachineAction{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(a.Spec, m); err != nil {
		return nil, fmt.Errorf("action %s: invalid spec: %w", a.ID, err)
	}
	return m, nil
}

// Approve records approver's decision and runs the action. It returns the
// finished action; a failed run is reported in its status and error, not as
// an error of Approve.
func (s *Service) Approve(ctx context.Context, id uuid.UUID, approver, note string) (db.Action, error) {
	approver = strings.TrimSpace(approver)
	if approver == "" {
		return db.Action{}, errors.New("approver is required: say who approves the action")
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return a, err
	}
	if a.Status != StatusProposed {
		return a, fmt.Errorf("%w: it is %s", ErrNotPending, a.Status)
	}
	m, err := Spec(a)
	if err != nil {
		return a, err
	}
	// Re-check against the catalog and the machines as they are now.
	_, graph, err := catalog.Load(ctx, s.store.Queries)
	if err != nil {
		return a, err
	}
	svc, ok := graph.Service(a.Service)
	if !ok {
		return a, fmt.Errorf("service %q is no longer in the catalog", a.Service)
	}
	conn, client, err := s.validate(ctx, svc, m)
	if err != nil {
		return a, fmt.Errorf("the action can no longer run: %w", err)
	}
	if conn != a.Connector {
		return a, fmt.Errorf("the action can no longer run: %s now uses connector %q, not %q", a.Service, conn, a.Connector)
	}

	a, err = s.store.ClaimAction(ctx, db.ClaimActionParams{
		ID: id, DecidedBy: text(approver), Note: text(note),
	})
	if store.IsNotFound(err) {
		return a, fmt.Errorf("%w: someone else decided it, or it expired", ErrNotPending)
	}
	if err != nil {
		return a, err
	}
	log := slog.With("action", a.ID, "investigation", a.InvestigationID, "app", m.App, "do", m.Action, "machines", m.MachineIds, "approved_by", approver)
	log.Info("action approved, running")

	// The action must not stop halfway because the caller went away.
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), execTimeout)
	defer cancel()
	result, runErr := execute(runCtx, client, m)
	status, errText := StatusSucceeded, ""
	if runErr != nil {
		status, errText = StatusFailed, runErr.Error()
		log.Error("action failed", "err", runErr, "result", result)
	} else {
		log.Info("action succeeded", "result", result)
	}
	a, err = s.store.FinishAction(runCtx, db.FinishActionParams{
		ID: a.ID, Status: status, Result: text(result), Error: text(errText),
	})
	if err != nil {
		return a, fmt.Errorf("the action ran (%s) but its outcome could not be saved: %w", status, err)
	}
	s.notify(runCtx, a, m)
	return a, nil
}

// Reject records that a human declined the action.
func (s *Service) Reject(ctx context.Context, id uuid.UUID, approver, reason string) (db.Action, error) {
	approver = strings.TrimSpace(approver)
	if approver == "" {
		return db.Action{}, errors.New("approver is required: say who rejects the action")
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return a, err
	}
	if a.Status != StatusProposed {
		return a, fmt.Errorf("%w: it is %s", ErrNotPending, a.Status)
	}
	a, err = s.store.RejectAction(ctx, db.RejectActionParams{ID: id, DecidedBy: text(approver), Note: text(reason)})
	if store.IsNotFound(err) {
		return a, fmt.Errorf("%w: someone else decided it", ErrNotPending)
	}
	if err != nil {
		return a, err
	}
	slog.Info("action rejected", "action", a.ID, "investigation", a.InvestigationID, "rejected_by", approver)
	if m, err := Spec(a); err == nil {
		s.notify(ctx, a, m)
	}
	return a, nil
}

func (s *Service) notify(ctx context.Context, a db.Action, m *opensrev1.MachineAction) {
	if s.notifier == nil {
		return
	}
	inv, err := s.store.GetInvestigation(ctx, a.InvestigationID)
	if err != nil {
		slog.Warn("notify action", "action", a.ID, "err", err)
		return
	}
	if err := s.notifier.ActionFinished(ctx, inv, a, m); err != nil {
		slog.Warn("notify action", "action", a.ID, "err", err)
	}
}

// execute runs m machine by machine and stops at the first failure, so a
// rolling restart never takes down more than one machine that did not come
// back. The result lists what was done and how to undo it.
func execute(ctx context.Context, c *fly.Machines, m *opensrev1.MachineAction) (string, error) {
	var lines []string
	app := m.App
	waitFor := func(id string) error {
		if err := c.WaitStarted(ctx, app, id, waitStarted); err != nil {
			return fmt.Errorf("machine %s did not reach started within %s: %w", id, waitStarted, err)
		}
		return nil
	}
	for i, id := range m.MachineIds {
		var err error
		switch m.Action {
		case "restart":
			if err = c.Restart(ctx, app, id); err == nil {
				if err = waitFor(id); err == nil {
					lines = append(lines, fmt.Sprintf("restarted %s (started)", id))
				}
			}
		case "start":
			if err = c.Start(ctx, app, id); err == nil {
				if err = waitFor(id); err == nil {
					lines = append(lines, fmt.Sprintf("started %s · undo: fly machine stop %s -a %s", id, id, app))
				}
			}
		case "stop":
			if err = c.Stop(ctx, app, id); err == nil {
				lines = append(lines, fmt.Sprintf("stopped %s · undo: fly machine start %s -a %s", id, id, app))
			}
		case "cordon":
			if err = c.Cordon(ctx, app, id); err == nil {
				lines = append(lines, fmt.Sprintf("cordoned %s (no traffic, still running) · undo: fly machine uncordon %s -a %s", id, id, app))
			}
		case "uncordon":
			if err = c.Uncordon(ctx, app, id); err == nil {
				lines = append(lines, fmt.Sprintf("uncordoned %s · undo: fly machine cordon %s -a %s", id, id, app))
			}
		case "clone":
			var nm fly.Machine
			if nm, err = c.Clone(ctx, app, id, m.Region); err == nil {
				line := fmt.Sprintf("cloned %s → %s in %s · undo: fly machine destroy %s -a %s --force", id, nm.ID, nm.Region, nm.ID, app)
				// The clone exists even if it does not come up: report it either way.
				if err = waitFor(nm.ID); err == nil {
					line = strings.Replace(line, " · undo", " (started) · undo", 1)
				}
				lines = append(lines, line)
			}
		default:
			err = fmt.Errorf("unknown machine action %q", m.Action)
		}
		if err != nil {
			if rest := len(m.MachineIds) - i - 1; rest > 0 {
				err = fmt.Errorf("%w (stopped: %d machine(s) left untouched)", err, rest)
			}
			return strings.Join(lines, "\n"), fmt.Errorf("%s %s: %w", m.Action, id, err)
		}
	}
	return strings.Join(lines, "\n"), nil
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
