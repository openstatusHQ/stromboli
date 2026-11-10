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
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/encoding/protojson"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/internal/catalog"
	"github.com/openstatushq/opensre/internal/config"
	"github.com/openstatushq/opensre/internal/connector"
	"github.com/openstatushq/opensre/internal/llm"
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

// models routes every task through the Anthropic adapter backed by s, with
// a different model per task.
func (s *scripted) models() Models {
	triage, _ := llm.Anthropic(s)("claude-haiku-5-5")
	investigate, _ := llm.Anthropic(s)("claude-opus-5-5")
	return Models{Triage: triage, Investigate: investigate}
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

// errorLines are the incident: 5 identical errors in the last 5 minutes.
func errorLines(start time.Time) []string {
	if start.Before(time.Now().Add(-45 * time.Minute)) {
		return nil // baseline window: no errors
	}
	var out []string
	for i := range 5 {
		out = append(out, fmt.Sprintf(`level=error msg="query failed" err="too many clients" id=%d`, i))
	}
	return out
}

// fakeAxiom serves the same incident as fakeLoki through the APL API.
func fakeAxiom(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ APL, StartTime string }
		if r.URL.Path != "/v1/datasets/_apl" || json.NewDecoder(r.Body).Decode(&body) != nil {
			http.NotFound(w, r)
			return
		}
		start, _ := time.Parse(time.RFC3339Nano, body.StartTime)
		now := time.Now().UTC()
		table := func(fields string, columns ...any) {
			b, _ := json.Marshal(columns)
			fmt.Fprintf(w, `{"tables":[{"fields":%s,"columns":%s}]}`, fields, b)
		}
		switch {
		case strings.HasSuffix(body.APL, "| take 1"):
			table(`[{"name":"_time"},{"name":"message"}]`, []string{now.Format(time.RFC3339)}, []string{"x"})
		case strings.Contains(body.APL, "by t = bin(_time"):
			table(`[{"name":"t"},{"name":"n"}]`, []string{now.Add(-2 * time.Minute).Format(time.RFC3339)}, []int{5})
		case strings.Contains(body.APL, "summarize n = count()"):
			table(`[{"name":"n"}]`, []int{len(errorLines(start))})
		default:
			lines := errorLines(start)
			times := make([]string, len(lines))
			for i := range lines {
				times[i] = now.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339Nano)
			}
			table(`[{"name":"_time"},{"name":"message"}]`, times, lines)
		}
	}))
}

// fakeCloudWatch serves the same incident through Logs Insights.
func fakeCloudWatch(t *testing.T) *httptest.Server {
	var mu sync.Mutex
	var queries []map[string]any
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		mu.Lock()
		defer mu.Unlock()
		field := func(k, v string) string { return fmt.Sprintf(`{"field":%q,"value":%q}`, k, v) }
		switch r.Header.Get("X-Amz-Target") {
		case "Logs_20140328.StartQuery":
			if fmt.Sprint(in["logGroupIdentifiers"]) != "[/ecs/api]" {
				t.Errorf("unexpected log groups %v", in["logGroupIdentifiers"])
			}
			queries = append(queries, in)
			fmt.Fprintf(w, `{"queryId":"%d"}`, len(queries)-1)
		case "Logs_20140328.GetQueryResults":
			idx, _ := strconv.Atoi(in["queryId"].(string))
			q := queries[idx]["queryString"].(string)
			start := time.Unix(int64(queries[idx]["startTime"].(float64)), 0)
			now := time.Now().UTC()
			var rows []string
			switch {
			case strings.Contains(q, "stats count(*) as n by bin("):
				bin := q[strings.Index(q, "bin("):]
				rows = append(rows, "["+field(bin, now.Add(-2*time.Minute).Format("2006-01-02 15:04:05.000"))+","+field("n", "5")+"]")
			case strings.Contains(q, "stats count(*)"):
				rows = append(rows, "["+field("n", strconv.Itoa(len(errorLines(start))))+"]")
			default:
				for i, l := range errorLines(start) {
					ts := now.Add(-time.Duration(i) * time.Minute).Format("2006-01-02 15:04:05.000")
					rows = append(rows, "["+field("@timestamp", ts)+","+field("@message", l)+"]")
				}
			}
			fmt.Fprintf(w, `{"status":"Complete","results":[%s]}`, strings.Join(rows, ","))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
}

// fakeFlyMetrics serves Fly's Prometheus API: one api machine whose memory
// climbs to 97% in the window, from 40% in the baseline.
func fakeFlyMetrics(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "FlyV1 fm2_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		q := r.URL.Query()
		switch r.URL.Path {
		case "/prometheus/acme/api/v1/query_range":
			expr := q.Get("query")
			if !strings.Contains(expr, `app="api"`) {
				t.Errorf("metrics query not scoped to the service: %s", expr)
			}
			start, _ := strconv.ParseInt(q.Get("start"), 10, 64)
			end, _ := strconv.ParseInt(q.Get("end"), 10, 64)
			v := "40"
			if time.Unix(end, 0).After(time.Now().Add(-5 * time.Minute)) {
				v = "97"
			}
			if !strings.Contains(expr, "fly_instance_memory") {
				fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
				return
			}
			fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"instance":"148e","region":"ams"},"values":[[%d,%q],[%d,%q]]}]}}`, start, v, end, v)
		case "/prometheus/acme/api/v1/label/__name__/values":
			fmt.Fprint(w, `{"status":"success","data":["fly_instance_memory_mem_total"]}`)
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

	loki := fakeLoki(t)
	defer loki.Close()
	axiom := fakeAxiom(t)
	defer axiom.Close()
	cloudwatch := fakeCloudWatch(t)
	defer cloudwatch.Close()
	fly := fakeFlyMetrics(t)
	defer fly.Close()
	box, _ := secrets.New("test")
	public, sealed, _ := connector.Split(box, "fly", map[string]string{"org": "acme", "token": "fm2_test", "url": fly.URL + "/prometheus"})
	if err := st.ReplaceConnector(ctx, db.ReplaceConnectorParams{Name: "test-fly", Kind: "fly", Config: public, Secrets: sealed}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Queries.DeleteConnector(context.Background(), "test-fly") })
	t.Setenv("AWS_CONFIG_FILE", os.DevNull)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", os.DevNull)

	// The same investigation must work whatever the service's log backend is.
	for _, b := range []logBackend{
		{"loki", map[string]string{"url": loki.URL}, `{service_name="api"}`},
		{"axiom", map[string]string{"url": axiom.URL, "token": "xaat-test"}, `['logs'] | where ['service.name'] == "api"`},
		{"cloudwatch", map[string]string{"endpoint": cloudwatch.URL, "region": "eu-west-1", "access_key_id": "AKIATEST", "secret_access_key": "secret"}, "/ecs/api"},
	} {
		t.Run(b.kind, func(t *testing.T) { runInvestigation(t, st, b) })
	}
}

type logBackend struct {
	kind     string
	config   map[string]string
	selector string
}

func runInvestigation(t *testing.T, st *store.Store, b logBackend) {
	ctx := context.Background()
	box, _ := secrets.New("test")
	connName := "test-" + b.kind
	public, sealed, _ := connector.Split(box, b.kind, b.config)
	if err := st.ReplaceConnector(ctx, db.ReplaceConnectorParams{Name: connName, Kind: b.kind, Config: public, Secrets: sealed}); err != nil {
		t.Fatal(err)
	}
	svcName := fmt.Sprintf("api-test-%d", time.Now().UnixNano())
	spec, _ := catalog.MarshalSpec(&opensrev1.Service{
		Name: svcName, Logs: &opensrev1.LogsConfig{Source: connName, Selector: b.selector},
		Metrics: &opensrev1.MetricsConfig{Source: "test-fly", Selectors: []string{`app="api"`}},
	})
	if err := st.UpsertService(ctx, db.UpsertServiceParams{Name: svcName, Spec: spec}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Queries.DeleteService(context.Background(), svcName)
		st.Queries.DeleteConnector(context.Background(), connName)
	})

	model := &scripted{responses: []string{
		message("tool_use",
			`{"type":"text","text":"Checking the error patterns."}`,
			toolUse("t1", "log_patterns", fmt.Sprintf(`{"service":%q}`, svcName)),
			toolUse("t2", "get_service", `{"service":"does-not-exist"}`)),
		// Reporting before mitigating is sent back to phase 1.
		message("tool_use", toolUse("t3", "submit_report", reportJSON)),
		message("tool_use", toolUse("t4", "submit_mitigation", mitigationJSON)),
		message("tool_use",
			toolUse("t5", "log_timeline", fmt.Sprintf(`{"service":%q}`, svcName)),
			toolUse("t5b", "get_metrics", fmt.Sprintf(`{"service":%q,"metric":"memory_used_percent","since_minutes":30}`, svcName))),
		message("tool_use", toolUse("t6", "submit_report", reportJSON)),
	}}

	cfg := config.Config{MaxToolCalls: 10, MitigationToolCalls: 2, InvestigationTimeout: time.Minute, DedupeWindow: time.Minute,
		Triage: config.Task{Effort: "low"}, Investigate: config.Task{Effort: "max"}}
	notifier := &recordingNotifier{}
	engine := New(ctx, cfg, st, connector.NewRegistry(st, box), model.models(), notifier, nil)

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

	// Triage runs on its model until the mitigation is out (turn 3), then the
	// investigate model continues the same conversation.
	for i, want := range []string{"claude-haiku-5-5/low", "claude-haiku-5-5/low", "claude-haiku-5-5/low", "claude-opus-5-5/max", "claude-opus-5-5/max"} {
		r := model.requests[i]
		if got := string(r.Model) + "/" + string(r.OutputConfig.Effort); got != want {
			t.Errorf("request %d ran on %s, want %s", i, got, want)
		}
	}
	if n := len(model.requests[3].Messages); n != 7 {
		t.Errorf("the investigate model should see the whole triage conversation, got %d messages", n)
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
	var patternsOut, badToolOut, timelineOut, metricsOut string
	for _, s := range steps {
		kinds[s.Kind]++
		switch s.Name {
		case "log_timeline":
			timelineOut = s.Output
			if s.IsError {
				t.Errorf("log_timeline failed: %s", s.Output)
			}
		case "get_metrics":
			metricsOut = s.Output
			if s.IsError {
				t.Errorf("get_metrics failed: %s", s.Output)
			}
		case "log_patterns":
			patternsOut = s.Output
			if s.IsError {
				t.Errorf("log_patterns failed: %s", s.Output)
			}
		case "get_service":
			badToolOut = s.Output
			if !s.IsError {
				t.Error("unknown service lookup should be recorded as an error")
			}
		}
	}
	if kinds["brief"] != 1 || kinds["llm"] != 5 || kinds["tool_call"] != 4 || kinds["mitigation"] != 1 || kinds["report"] != 1 {
		t.Errorf("unexpected steps: %v", kinds)
	}
	if !strings.Contains(patternsOut, "too many clients") || !strings.Contains(patternsOut, `"new":true`) {
		t.Errorf("log_patterns should surface the new error pattern, got: %s", patternsOut)
	}
	if !strings.Contains(timelineOut, `"count":5`) && !strings.Contains(timelineOut, `"count":42`) {
		t.Errorf("log_timeline should report the error burst, got: %s", timelineOut)
	}
	if !strings.Contains(metricsOut, `"max":97`) || !strings.Contains(metricsOut, `"baseline_avg":40`) || !strings.Contains(metricsOut, `"instance":"148e"`) {
		t.Errorf("get_metrics should report memory per instance against the baseline, got: %s", metricsOut)
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
	if !strings.Contains(first, "**memory_used_percent**\n  - instance=148e region=ams: avg 97.0%, peak 97.0%, last 97.0% (baseline 40.0%)") ||
		!strings.Contains(first, "**cpu_percent**: no data") {
		t.Errorf("brief should show machine metrics against the baseline, got: %s", first[strings.Index(first, "## Metrics"):])
	}

	// A per-request dedupe window outlasts OPENSRE_DEDUPE_WINDOW (1m here):
	// an investigation finished 10 minutes ago is reused, without a new run.
	if _, err := st.Pool.Exec(ctx, `UPDATE investigations SET fingerprint = 'healthcheck:x', finished_at = now() - interval '10 minutes' WHERE id = $1`, inv.ID); err != nil {
		t.Fatal(err)
	}
	again, reused, err := engine.Start(ctx, StartRequest{Title: "again", Source: "healthcheck", Services: []string{svcName},
		Fingerprint: "healthcheck:x", DedupeWindow: time.Hour})
	if err != nil || !reused || again.ID != inv.ID {
		t.Errorf("want the finished investigation reused, got %v reused=%v err=%v", again.ID, reused, err)
	}
	if len(model.requests) != 5 {
		t.Errorf("a deduplicated start must not call the model, got %d requests", len(model.requests))
	}

	// Once one of its actions succeeded, or the alert resolved, the alert firing
	// again is a new incident: the investigation is no longer reused.
	findOpen := func() error {
		_, err := st.Queries.FindOpenInvestigation(ctx, db.FindOpenInvestigationParams{
			Fingerprint: pgtype.Text{String: "healthcheck:x", Valid: true}, WindowMinutes: 60})
		return err
	}
	if _, err := st.Pool.Exec(ctx, `INSERT INTO actions (id, investigation_id, service, kind, spec, connector, status, expires_at)
		VALUES (gen_random_uuid(), $1, $2, 'fly_machine', '{}', 'fly', 'succeeded', now())`, inv.ID, svcName); err != nil {
		t.Fatal(err)
	}
	if err := findOpen(); !store.IsNotFound(err) {
		t.Errorf("an investigation whose action succeeded must not be reused, got err=%v", err)
	}
	if _, err := st.Pool.Exec(ctx, `DELETE FROM actions WHERE investigation_id = $1`, inv.ID); err != nil {
		t.Fatal(err)
	}
	if err := findOpen(); err != nil {
		t.Fatalf("without the action it is reused again, got err=%v", err)
	}
	if n, err := st.Queries.ResolveInvestigations(ctx, pgtype.Text{String: "healthcheck:x", Valid: true}); err != nil || n != 1 {
		t.Fatalf("resolve: n=%d err=%v", n, err)
	}
	if err := findOpen(); !store.IsNotFound(err) {
		t.Errorf("a resolved investigation must not be reused, got err=%v", err)
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
