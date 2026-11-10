package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/encoding/protojson"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/internal/action"
	"github.com/openstatushq/opensre/internal/catalog"
	"github.com/openstatushq/opensre/internal/config"
	"github.com/openstatushq/opensre/internal/connector"
	"github.com/openstatushq/opensre/internal/connector/fly/flytest"
	"github.com/openstatushq/opensre/internal/secrets"
	"github.com/openstatushq/opensre/internal/store"
	"github.com/openstatushq/opensre/internal/store/db"
)

// actionEnv is a Fly service with a fly_machines connector backed by a fake
// Machines API, and an engine that can propose actions.
type actionEnv struct {
	st      *store.Store
	fly     *flytest.Server
	svc     string
	actions *action.Service
	engine  func(model *scripted) *Engine
	cfg     config.Config
}

func newActionEnv(t *testing.T, enabled bool, allow ...string) *actionEnv {
	dbURL := os.Getenv("OPENSRE_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("set OPENSRE_TEST_DB_URL to run")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	loki := fakeLoki(t)
	t.Cleanup(loki.Close)
	srv := flytest.New("acme-api")
	t.Cleanup(srv.Close)

	suffix := time.Now().UnixNano()
	box, _ := secrets.New("test")
	logsConn, flyConn, svcName := fmt.Sprintf("test-loki-%d", suffix), fmt.Sprintf("test-flym-%d", suffix), fmt.Sprintf("api-fly-%d", suffix)
	for name, c := range map[string]struct {
		kind string
		cfg  map[string]string
	}{
		logsConn: {"loki", map[string]string{"url": loki.URL}},
		flyConn:  {"fly_machines", map[string]string{"org": "acme", "token": flytest.Token, "url": srv.URL}},
	} {
		public, sealed, _ := connector.Split(box, c.kind, c.cfg)
		if err := st.ReplaceConnector(ctx, db.ReplaceConnectorParams{Name: name, Kind: c.kind, Config: public, Secrets: sealed}); err != nil {
			t.Fatal(err)
		}
	}
	spec, _ := catalog.MarshalSpec(&opensrev1.Service{
		Name: svcName, Runtime: "fly.io",
		Logs: &opensrev1.LogsConfig{Source: logsConn, Selector: `{service_name="api"}`},
		Fly:  &opensrev1.FlyConfig{App: "acme-api", Actions: flyConn, Allow: allow},
	})
	if err := st.UpsertService(ctx, db.UpsertServiceParams{Name: svcName, Spec: spec}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Queries.DeleteService(context.Background(), svcName)
		st.Queries.DeleteConnector(context.Background(), logsConn)
		st.Queries.DeleteConnector(context.Background(), flyConn)
	})

	cfg := config.Config{Model: "claude-opus-5-5", Effort: "high", MaxToolCalls: 10, MitigationToolCalls: 4,
		InvestigationTimeout: time.Minute, DedupeWindow: time.Minute, ActionsEnabled: enabled, ActionTTL: time.Hour, APIToken: "t"}
	reg := connector.NewRegistry(st, box)
	env := &actionEnv{st: st, fly: srv, svc: svcName, cfg: cfg}
	env.actions = action.New(cfg, st, reg, nil)
	env.engine = func(model *scripted) *Engine {
		return New(ctx, cfg, st, reg, model.models(), &recordingNotifier{}, env.actions)
	}
	return env
}

// investigate runs one investigation whose mitigations are the given
// submit_mitigation inputs, in order, then a report.
func (env *actionEnv) investigate(t *testing.T, mitigations ...string) (db.Investigation, *scripted) {
	var responses []string
	for i, m := range mitigations {
		responses = append(responses, message("tool_use", toolUse(fmt.Sprintf("m%d", i), "submit_mitigation", m)))
	}
	responses = append(responses, message("tool_use", toolUse("r", "submit_report", reportJSON)))
	model := &scripted{responses: responses}
	e := env.engine(model)
	inv, _, err := e.Start(context.Background(), StartRequest{Title: "api memory", Source: "manual", Services: []string{env.svc}})
	if err != nil {
		t.Fatal(err)
	}
	e.Wait()
	got, err := env.st.GetInvestigation(context.Background(), inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusCompleted {
		t.Fatalf("investigation %s: %s", got.Status, got.Error.String)
	}
	return got, model
}

func restartMitigation(machineAction string) string {
	return `{"kind":"restart","target":"api machine 148e","action":"fly machine restart 148e -a acme-api",` +
		`"rationale":"memory climbing on one machine","verify":"memory back under 60%","undo":"none needed",` +
		`"risk":"safe","confidence":"medium","impact":"5xx on api",` + `"machine_action":` + machineAction + `}`
}

func storedMitigation(t *testing.T, inv db.Investigation) *opensrev1.Mitigation {
	m := &opensrev1.Mitigation{}
	if err := protojson.Unmarshal(inv.Mitigation, m); err != nil {
		t.Fatal(err)
	}
	return m
}

// toolResult is the text of the tool result the model got for call n (0-based request index).
func toolResult(t *testing.T, model *scripted, req int) string {
	r := lastUser(t, model.requests[req]).Content[0].OfToolResult
	if r == nil || len(r.Content) == 0 || r.Content[0].OfText == nil {
		t.Fatalf("request %d has no tool result", req)
	}
	return r.Content[0].OfText.Text
}

func TestMachineActionApproved(t *testing.T) {
	env := newActionEnv(t, true)
	ctx := context.Background()
	inv, model := env.investigate(t, restartMitigation(`{"action":"restart","machine_ids":["148e"]}`))

	// Option 2: the brief lists the machines and the Fly commands, and says
	// what opensre may run.
	first := model.requests[0].Messages[0].OfUser.Content[0].OfText.Text
	for _, want := range []string{"## Fly.io", "App `acme-api`. 3 machine(s)", "| 148e | ams | started |",
		"fly machine restart <id> -a acme-api", "after a human approves them: restart, clone, cordon, uncordon"} {
		if !strings.Contains(first, want) {
			t.Errorf("brief misses %q:\n%s", want, first[strings.Index(first, "## Recent changes"):])
		}
	}

	m := storedMitigation(t, inv)
	if m.ActionId == "" || m.MachineAction.App != "acme-api" || m.MachineAction.Service != env.svc {
		t.Fatalf("mitigation should carry the proposed action: %v", m)
	}
	if out := toolResult(t, model, 1); !strings.Contains(out, "once a human approves action "+m.ActionId) {
		t.Errorf("the model should be told the action waits for approval: %s", out)
	}
	// Nothing ran without approval.
	if calls := env.fly.Calls(); len(calls) != 0 {
		t.Fatalf("no machine should be touched before approval, got %v", calls)
	}

	id := mustUUID(t, m.ActionId)
	if _, err := env.actions.Approve(ctx, id, " ", ""); err == nil {
		t.Error("approval without an approver should fail")
	}
	a, err := env.actions.Approve(ctx, id, "alice", "approved in the incident call")
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != action.StatusSucceeded || a.DecidedBy.String != "alice" || a.Note.String != "approved in the incident call" ||
		!strings.Contains(a.Result.String, "restarted 148e (started)") || !a.DecidedAt.Valid || !a.FinishedAt.Valid {
		t.Errorf("unexpected action after approval: %+v", a)
	}
	if calls := env.fly.Calls(); len(calls) != 1 || calls[0] != "POST 148e/restart" {
		t.Errorf("calls = %v", calls)
	}
	// An action runs once.
	if _, err := env.actions.Approve(ctx, id, "bob", ""); !errors.Is(err, action.ErrNotPending) {
		t.Errorf("second approval: %v", err)
	}
	if len(env.fly.Calls()) != 1 {
		t.Error("a second approval must not run the action again")
	}
}

func TestMachineActionRejectedAndExpired(t *testing.T) {
	env := newActionEnv(t, true)
	ctx := context.Background()
	inv, _ := env.investigate(t, restartMitigation(`{"action":"cordon","machine_ids":["148e","2a7c"]}`))
	id := mustUUID(t, storedMitigation(t, inv).ActionId)
	a, err := env.actions.Reject(ctx, id, "alice", "the db is the problem")
	if err != nil || a.Status != action.StatusRejected || a.Note.String != "the db is the problem" {
		t.Fatalf("reject = %+v, %v", a, err)
	}
	if _, err := env.actions.Approve(ctx, id, "bob", ""); !errors.Is(err, action.ErrNotPending) {
		t.Errorf("approving a rejected action: %v", err)
	}

	inv, _ = env.investigate(t, restartMitigation(`{"action":"restart","machine_ids":["148e"]}`))
	id = mustUUID(t, storedMitigation(t, inv).ActionId)
	if _, err := env.st.Pool.Exec(ctx, "UPDATE actions SET expires_at = now() - interval '1 minute' WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
	if _, err := env.actions.Approve(ctx, id, "alice", ""); !errors.Is(err, action.ErrNotPending) || !strings.Contains(err.Error(), "expired") {
		t.Errorf("approving an expired action: %v", err)
	}
	if calls := env.fly.Calls(); len(calls) != 0 {
		t.Errorf("rejected and expired actions must not run, got %v", calls)
	}
}

func TestMachineActionRevisedSupersedes(t *testing.T) {
	env := newActionEnv(t, true)
	ctx := context.Background()
	inv, _ := env.investigate(t,
		restartMitigation(`{"action":"restart","machine_ids":["148e"]}`),
		restartMitigation(`{"action":"clone","machine_ids":["2a7c"],"region":"syd"}`))
	acts, err := env.actions.List(ctx, db.ListActionsParams{Limit: 10, InvestigationID: pgtype.UUID{Bytes: inv.ID, Valid: true}})
	if err != nil || len(acts) != 2 {
		t.Fatalf("actions = %v, %v", acts, err)
	}
	// Newest first: the clone is pending, the restart superseded.
	if acts[0].Status != action.StatusProposed || acts[1].Status != action.StatusSuperseded {
		t.Fatalf("statuses = %s, %s", acts[0].Status, acts[1].Status)
	}
	if m := storedMitigation(t, inv); m.ActionId != acts[0].ID.String() {
		t.Errorf("mitigation should point at the latest action")
	}
	a, err := env.actions.Approve(ctx, acts[0].ID, "alice", "")
	if err != nil || a.Status != action.StatusSucceeded || !strings.Contains(a.Result.String, "cloned 2a7c → new1 in syd (started) · undo: fly machine destroy new1 -a acme-api --force") {
		t.Fatalf("clone = %+v, %v", a, err)
	}
	if _, err := env.actions.Approve(ctx, acts[1].ID, "alice", ""); !errors.Is(err, action.ErrNotPending) {
		t.Errorf("a superseded action must not run: %v", err)
	}
}

func TestMachineActionRefused(t *testing.T) {
	env := newActionEnv(t, true, "restart")
	for _, c := range []struct{ input, why string }{
		{`{"action":"stop","machine_ids":["148e"]}`, `"stop" is not allowed`},
		{`{"action":"restart","machine_ids":["ffff"]}`, `machine "ffff" is not a machine of app acme-api`},
		{`{"action":"restart","machine_ids":[]}`, "machine_ids is required"},
		{`{"action":"restart","machine_ids":["148e"],"service":"not-a-service"}`, "not in the catalog"},
	} {
		inv, model := env.investigate(t, restartMitigation(c.input))
		m := storedMitigation(t, inv)
		if m.ActionId != "" || m.MachineAction != nil || m.Action == "" {
			t.Errorf("%s: a refused machine action must be dropped, keeping the suggestion: %v", c.input, m)
		}
		if out := toolResult(t, model, 1); !strings.Contains(out, "not accepted") || !strings.Contains(out, c.why) {
			t.Errorf("%s: the model should learn why: %s", c.input, out)
		}
	}
	// Clone is not in this service's allow-list either, and machines with a
	// volume are never cloned.
	envClone := newActionEnv(t, true)
	_, model := envClone.investigate(t, restartMitigation(`{"action":"clone","machine_ids":["d91v"]}`))
	if out := toolResult(t, model, 1); !strings.Contains(out, "mounts a volume") {
		t.Errorf("cloning a machine with a volume should be refused: %s", out)
	}
	if len(env.fly.Calls())+len(envClone.fly.Calls()) != 0 {
		t.Error("refused actions must not touch machines")
	}
}

func TestMachineActionFromCommand(t *testing.T) {
	// The model wrote `fly machine restart 148e` but no machine_action: the
	// approve button is offered anyway.
	env := newActionEnv(t, true)
	inv, model := env.investigate(t, restartMitigation(`null`))
	m := storedMitigation(t, inv)
	if m.ActionId == "" || m.MachineAction.GetAction() != "restart" || !slices.Equal(m.MachineAction.GetMachineIds(), []string{"148e"}) ||
		m.MachineAction.App != "acme-api" {
		t.Fatalf("the restart command should become an action waiting for approval: %v", m)
	}
	if out := toolResult(t, model, 1); !strings.Contains(out, "filled in from your command") {
		t.Errorf("the model should be told the action was filled in: %s", out)
	}

	// When the command is not allowed, it stays a suggestion, without
	// scolding the model for a machine_action it did not send.
	envCordon := newActionEnv(t, true, "cordon")
	inv, model = envCordon.investigate(t, restartMitigation(`null`))
	if m := storedMitigation(t, inv); m.ActionId != "" || m.MachineAction != nil {
		t.Errorf("a command outside the allow-list must not become an action: %v", m)
	}
	if out := toolResult(t, model, 1); strings.Contains(out, "machine_action") {
		t.Errorf("nothing to correct for the model: %s", out)
	}
	if len(env.fly.Calls())+len(envCordon.fly.Calls()) != 0 {
		t.Error("nothing runs without approval")
	}
}

func TestMachineActionDisabled(t *testing.T) {
	env := newActionEnv(t, false)
	inv, model := env.investigate(t, restartMitigation(`{"action":"restart","machine_ids":["148e"]}`))
	if m := storedMitigation(t, inv); m.ActionId != "" {
		t.Errorf("actions are disabled, no action should be proposed: %v", m)
	}
	first := model.requests[0].Messages[0].OfUser.Content[0].OfText.Text
	if !strings.Contains(first, "opensre cannot run machine actions for this service (machine actions are disabled") {
		t.Errorf("the brief should say actions are disabled")
	}
	if out := toolResult(t, model, 1); !strings.Contains(out, "OPENSRE_ACTIONS_ENABLED") {
		t.Errorf("the model should learn actions are disabled: %s", out)
	}
}

func TestMachineActionFailureStops(t *testing.T) {
	env := newActionEnv(t, true)
	ctx := context.Background()
	inv, _ := env.investigate(t, restartMitigation(`{"action":"restart","machine_ids":["148e","2a7c"]}`))
	env.fly.Fail = "148e/restart"
	a, err := env.actions.Approve(ctx, mustUUID(t, storedMitigation(t, inv).ActionId), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != action.StatusFailed || !strings.Contains(a.Error.String, "boom") || !strings.Contains(a.Error.String, "1 machine(s) left untouched") {
		t.Errorf("unexpected failed action: %+v", a)
	}
	// The second machine was not restarted after the first failed.
	for _, c := range env.fly.Calls() {
		if strings.Contains(c, "2a7c") {
			t.Errorf("a rolling restart must stop at the first failure, got %v", env.fly.Calls())
		}
	}
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("invalid uuid %q: %v", s, err)
	}
	return id
}
