package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"google.golang.org/protobuf/encoding/protojson"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/internal/catalog"
	"github.com/openstatushq/opensre/internal/config"
	"github.com/openstatushq/opensre/internal/connector"
	"github.com/openstatushq/opensre/internal/secrets"
	"github.com/openstatushq/opensre/internal/store"
	"github.com/openstatushq/opensre/internal/store/db"
)

// scripted replays canned model responses and records the requests.
type scripted struct {
	mu        sync.Mutex
	responses []string
	requests  []anthropic.BetaMessageNewParams
}

func (s *scripted) New(_ context.Context, p anthropic.BetaMessageNewParams, _ ...option.RequestOption) (*anthropic.BetaMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, p)
	if len(s.responses) == 0 {
		return nil, fmt.Errorf("no more scripted responses")
	}
	var m anthropic.BetaMessage
	if err := json.Unmarshal([]byte(s.responses[0]), &m); err != nil {
		return nil, err
	}
	s.responses = s.responses[1:]
	return &m, nil
}

func message(stop string, content ...string) string {
	return fmt.Sprintf(`{"id":"msg","type":"message","role":"assistant","model":"claude-opus-5-5","stop_reason":%q,
		"content":[%s],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`,
		stop, strings.Join(content, ","))
}

func toolUse(id, name, input string) string {
	return fmt.Sprintf(`{"type":"tool_use","id":%q,"name":%q,"input":%s}`, id, name, input)
}

// fakeLoki serves error lines for api in the last 10 minutes only.
func fakeLoki(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/loki/api/v1/labels":
			fmt.Fprint(w, `{"status":"success","data":["service_name"]}`)
		case "/loki/api/v1/query":
			fmt.Fprint(w, `{"data":{"resultType":"vector","result":[{"metric":{},"value":[0,"42"]}]}}`)
		case "/loki/api/v1/query_range":
			start, _ := strconv.ParseInt(q.Get("start"), 10, 64)
			var values []string
			if time.Unix(0, start).After(time.Now().Add(-45 * time.Minute)) {
				for i := range 5 {
					ts := time.Now().Add(-time.Duration(i) * time.Minute).UnixNano()
					values = append(values, fmt.Sprintf(`["%d","level=error msg=\"query failed\" err=\"too many clients\" id=%d"]`, ts, i))
				}
			}
			fmt.Fprintf(w, `{"data":{"resultType":"streams","result":[{"stream":{"service_name":"api"},"values":[%s]}]}}`, strings.Join(values, ","))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestInvestigationEndToEnd(t *testing.T) {
	dbURL := os.Getenv("OPENSRE_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("set OPENSRE_TEST_DB_URL to run (e.g. postgres://opensre:opensre@localhost:55432/opensre?sslmode=disable)")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close) // registered first, so it runs after the row cleanups
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	box, _ := secrets.New("test")
	loki := fakeLoki(t)
	defer loki.Close()
	public, sealed, _ := connector.Split(box, "loki", map[string]string{"url": loki.URL})
	if err := st.ReplaceConnector(ctx, db.ReplaceConnectorParams{Name: "test-loki", Kind: "loki", Config: public, Secrets: sealed}); err != nil {
		t.Fatal(err)
	}
	svcName := fmt.Sprintf("api-test-%d", time.Now().UnixNano())
	spec, _ := catalog.MarshalSpec(&opensrev1.Service{
		Name: svcName, Logs: &opensrev1.LogsConfig{Source: "test-loki", Selector: `{service_name="api"}`},
	})
	if err := st.UpsertService(ctx, db.UpsertServiceParams{Name: svcName, Spec: spec}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Queries.DeleteService(context.Background(), svcName)
		st.Queries.DeleteConnector(context.Background(), "test-loki")
	})

	model := &scripted{responses: []string{
		message("tool_use",
			`{"type":"text","text":"Checking the error patterns."}`,
			toolUse("t1", "log_patterns", fmt.Sprintf(`{"service":%q}`, svcName)),
			toolUse("t2", "get_service", `{"service":"does-not-exist"}`)),
		// Reporting before mitigating is sent back to phase 1.
		message("tool_use", toolUse("t3", "submit_report", reportJSON)),
		message("tool_use", toolUse("t4", "submit_mitigation", mitigationJSON)),
		message("tool_use", toolUse("t5", "log_timeline", fmt.Sprintf(`{"service":%q}`, svcName))),
		message("tool_use", toolUse("t6", "submit_report", reportJSON)),
	}}

	cfg := config.Config{Model: "claude-opus-5-5", Effort: "high", MaxToolCalls: 10, MitigationToolCalls: 2, InvestigationTimeout: time.Minute, DedupeWindow: time.Minute}
	notifier := &recordingNotifier{}
	engine := New(ctx, cfg, st, connector.NewRegistry(st, box), model, notifier)

	inv, _, err := engine.Start(ctx, StartRequest{Title: "api errors", Source: "manual", Services: []string{svcName}})
	if err != nil {
		t.Fatal(err)
	}
	engine.Wait()

	got, err := st.GetInvestigation(ctx, inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusCompleted {
		t.Fatalf("status = %s, error = %s", got.Status, got.Error.String)
	}
	rep := &opensrev1.Report{}
	if err := protojson.Unmarshal(got.Report, rep); err != nil {
		t.Fatal(err)
	}
	if rep.LikelyCause != "database connection limit reached" || rep.Confidence != "medium" || rep.Fix == "" {
		t.Errorf("unexpected report: %v", rep)
	}

	// Mitigate first: the mitigation is stored and published before the report.
	mit := &opensrev1.Mitigation{}
	if err := protojson.Unmarshal(got.Mitigation, mit); err != nil {
		t.Fatalf("mitigation not stored: %v", err)
	}
	if mit.Kind != "restart" || !got.MitigatedAt.Valid {
		t.Errorf("unexpected mitigation: %v (at %v)", mit, got.MitigatedAt)
	}
	if got.MitigatedAt.Time.After(got.FinishedAt.Time) {
		t.Error("mitigation should be proposed before the investigation finishes")
	}
	if len(notifier.mitigations) != 1 || notifier.mitigations[0].Kind != "restart" || notifier.finishedBeforeMitigation {
		t.Errorf("notifier should get the mitigation before the report: %+v", notifier)
	}

	steps, err := st.ListSteps(ctx, db.ListStepsParams{InvestigationID: inv.ID})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	var patternsOut, badToolOut string
	for _, s := range steps {
		kinds[s.Kind]++
		switch s.Name {
		case "log_patterns":
			patternsOut = s.Output
		case "get_service":
			badToolOut = s.Output
			if !s.IsError {
				t.Error("unknown service lookup should be recorded as an error")
			}
		}
	}
	if kinds["brief"] != 1 || kinds["llm"] != 5 || kinds["tool_call"] != 3 || kinds["mitigation"] != 1 || kinds["report"] != 1 {
		t.Errorf("unexpected steps: %v", kinds)
	}
	if !strings.Contains(patternsOut, "too many clients") || !strings.Contains(patternsOut, `"new":true`) {
		t.Errorf("log_patterns should surface the new error pattern, got: %s", patternsOut)
	}
	if !strings.Contains(badToolOut, "not in the catalog") {
		t.Errorf("unexpected get_service output: %s", badToolOut)
	}

	if len(model.requests) != 5 {
		t.Fatalf("got %d model requests", len(model.requests))
	}
	// The second request must carry both tool results in one user message,
	// plus the nudge to mitigate since the triage budget (2) is spent.
	last := lastUser(t, model.requests[1])
	if len(last.Content) != 3 || last.Content[0].OfToolResult == nil || last.Content[1].OfToolResult == nil ||
		last.Content[2].OfText == nil || !strings.Contains(last.Content[2].OfText.Text, "submit_mitigation") {
		t.Errorf("expected two tool results and a mitigation nudge, got %d blocks", len(last.Content))
	}
	// A report before any mitigation is rejected.
	if r := lastUser(t, model.requests[2]).Content[0].OfToolResult; r == nil || !r.IsError.Value {
		t.Error("submit_report before submit_mitigation should be rejected")
	}
	if r := lastUser(t, model.requests[3]).Content[0].OfToolResult; r == nil || r.IsError.Value {
		t.Error("submit_mitigation should be accepted")
	}
	first := model.requests[0].Messages[0].OfUser.Content[0].OfText.Text
	if !strings.Contains(first, "# Brief: "+svcName) {
		t.Errorf("first message should include the brief, got: %.300s", first)
	}
}

const (
	mitigationJSON = `{"kind":"restart","target":"api","action":"kubectl rollout restart deploy/api","rationale":"connection pool exhausted",
		"verify":"'too many clients' stops in log_patterns","undo":"none needed","risk":"safe","confidence":"medium","impact":"api queries failing for 5 minutes"}`
	reportJSON = `{"summary":"api is failing database queries","likely_cause":"database connection limit reached",
		"confidence":"medium","evidence":["5 'too many clients' errors, none in baseline"],
		"ruled_out":[],"suggested_mitigation":"restart api","fix":"lower the pool size","next_steps":["check db-proxy"]}`
)

func lastUser(t *testing.T, p anthropic.BetaMessageNewParams) *anthropic.BetaUserMessageParam {
	t.Helper()
	m := p.Messages[len(p.Messages)-1]
	if m.OfUser == nil {
		t.Fatal("last message should be a user message")
	}
	return m.OfUser
}

type recordingNotifier struct {
	mu                       sync.Mutex
	mitigations              []*opensrev1.Mitigation
	finished                 bool
	finishedBeforeMitigation bool
}

func (n *recordingNotifier) Started(context.Context, db.Investigation) (string, string, error) {
	return "", "", nil
}

func (n *recordingNotifier) Mitigated(_ context.Context, _ db.Investigation, m *opensrev1.Mitigation) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.mitigations = append(n.mitigations, m)
	return nil
}

func (n *recordingNotifier) Finished(context.Context, db.Investigation, *opensrev1.Report) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.finished = true
	n.finishedBeforeMitigation = len(n.mitigations) == 0
	return nil
}
