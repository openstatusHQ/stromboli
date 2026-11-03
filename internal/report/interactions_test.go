package report

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/slack-go/slack"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/internal/store/db"
)

const secret = "s3cret"

type fakeDecider struct {
	mu        sync.Mutex
	action    db.Action
	decisions chan string
}

func (f *fakeDecider) Get(context.Context, uuid.UUID) (db.Action, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.action, nil
}

func (f *fakeDecider) Approve(_ context.Context, _ uuid.UUID, who, _ string) (db.Action, error) {
	f.decisions <- "approve " + who
	return f.action, nil
}

func (f *fakeDecider) Reject(_ context.Context, _ uuid.UUID, who, _ string) (db.Action, error) {
	f.decisions <- "reject " + who
	return f.action, nil
}

func newTestSlack(t *testing.T, approvers []string) (*Slack, *fakeDecider, chan string) {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"channel":"C1","ts":"1.2"}`)
	}))
	t.Cleanup(api.Close)
	s := &Slack{client: slack.New("xoxb-test", slack.OptionAPIURL(api.URL+"/")), channel: "C1"}
	d := &fakeDecider{
		action:    db.Action{ID: uuid.New(), InvestigationID: uuid.New(), Status: "proposed"},
		decisions: make(chan string, 4),
	}
	s.EnableButtons(d, func(context.Context, uuid.UUID) (db.Investigation, error) {
		return db.Investigation{}, nil
	}, secret, approvers)
	responses := make(chan string, 4)
	s.buttons.postResponse = func(_ context.Context, _ string, m *slack.WebhookMessage) error {
		responses <- m.Text
		return nil
	}
	return s, d, responses
}

func click(t *testing.T, h http.Handler, actionID, value, userID string, sign func(body string, ts string) string) int {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"type":         "block_actions",
		"user":         map[string]string{"id": userID, "name": "alice"},
		"response_url": "https://hooks.slack.test/r",
		"actions":      []map[string]string{{"action_id": actionID, "block_id": actionsBlockID, "value": value}},
	})
	body := url.Values{"payload": {string(payload)}}.Encode()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, "/slack/interactions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", sign(body, ts))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func validSig(body, ts string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "v0:%s:%s", ts, body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func wait(t *testing.T, c chan string) string {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
		return ""
	}
}

func TestInteractionApprove(t *testing.T) {
	s, d, responses := newTestSlack(t, nil)
	if code := click(t, s.InteractionsHandler(), approveActionID, d.action.ID.String(), "U1", validSig); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if got := wait(t, d.decisions); got != "approve @alice (Slack U1)" {
		t.Fatalf("decision %q", got)
	}
	if got := wait(t, responses); !strings.Contains(got, "Running action") {
		t.Fatalf("response %q", got)
	}
}

func TestInteractionReject(t *testing.T) {
	s, d, _ := newTestSlack(t, nil)
	click(t, s.InteractionsHandler(), rejectActionID, d.action.ID.String(), "U1", validSig)
	if got := wait(t, d.decisions); got != "reject @alice (Slack U1)" {
		t.Fatalf("decision %q", got)
	}
}

func TestInteractionBadSignature(t *testing.T) {
	s, d, _ := newTestSlack(t, nil)
	code := click(t, s.InteractionsHandler(), approveActionID, d.action.ID.String(), "U1",
		func(string, string) string { return "v0=" + strings.Repeat("0", 64) })
	if code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", code)
	}
	select {
	case got := <-d.decisions:
		t.Fatalf("unexpected decision %q", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestInteractionApproverNotAllowed(t *testing.T) {
	s, d, responses := newTestSlack(t, []string{"U2"})
	click(t, s.InteractionsHandler(), approveActionID, d.action.ID.String(), "U1", validSig)
	if got := wait(t, responses); !strings.Contains(got, "not allowed") {
		t.Fatalf("response %q", got)
	}
	select {
	case got := <-d.decisions:
		t.Fatalf("unexpected decision %q", got)
	default:
	}
}

func TestActionBlocks(t *testing.T) {
	s, d, _ := newTestSlack(t, nil)
	m := &opensrev1.Mitigation{
		Kind: "restart", ActionId: d.action.ID.String(), Risk: "reversible",
		MachineAction: &opensrev1.MachineAction{Action: "restart", App: "api", MachineIds: []string{"148e"}},
	}
	d.action.ExpiresAt = pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}
	out, _ := json.Marshal(s.buttons.actionBlocks(context.Background(), m))
	for _, want := range []string{approveActionID, rejectActionID, `"confirm"`, "restart 148e of app api"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("proposed: missing %s in %s", want, out)
		}
	}

	d.mu.Lock()
	d.action.Status, d.action.DecidedBy = "succeeded", pgtype.Text{String: "@alice (Slack U1)", Valid: true}
	d.mu.Unlock()
	out, _ = json.Marshal(s.buttons.actionBlocks(context.Background(), m))
	if strings.Contains(string(out), approveActionID) || !strings.Contains(string(out), "done, approved by @alice") {
		t.Errorf("succeeded: %s", out)
	}
}
