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

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
	"github.com/openstatushq/stromboli/internal/store/db"
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
	// No ephemeral "running" note: the message itself shows the action running.
	select {
	case got := <-responses:
		t.Fatalf("an approval that runs should not answer with an ephemeral message, got %q", got)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestConfirmDialogIsPlainText(t *testing.T) {
	s, d, _ := newTestSlack(t, nil)
	m := &stromboliv1.Mitigation{
		Kind: "restart", ActionId: d.action.ID.String(), Risk: "safe", Undo: "fly machine stop <id> -a pebble",
		MachineAction: &stromboliv1.MachineAction{Action: "start", App: "pebble", MachineIds: []string{"286d", "683e"}},
	}
	var confirm *slack.ConfirmationBlockObject
	for _, b := range s.buttons.actionBlocks(context.Background(), m) {
		if ab, ok := b.(*slack.ActionBlock); ok {
			confirm = ab.Elements.ElementSet[0].(*slack.ButtonBlockElement).Confirm
		}
	}
	if confirm == nil {
		t.Fatal("the Apply button should ask to confirm")
	}
	want := "stromboli will start 286d, 683e of app pebble now.\n\nRisk: safe\nUndo: fly machine stop 286d -a pebble\nfly machine stop 683e -a pebble"
	if confirm.Text.Type != slack.PlainTextType || confirm.Text.Text != want {
		t.Errorf("confirm text (%s) = %q, want %q", confirm.Text.Type, confirm.Text.Text, want)
	}
}

func TestActionFinishedMessage(t *testing.T) {
	m := &stromboliv1.MachineAction{Action: "start", MachineIds: []string{"683e", "286d"}, App: "pebble"}
	a := db.Action{Status: "succeeded", DecidedBy: pgtype.Text{String: "@thibault (Slack U05)", Valid: true},
		Note:   pgtype.Text{String: "approved from Slack", Valid: true},
		Result: pgtype.Text{String: "started 683e · undo: fly machine stop 683e -a pebble", Valid: true}}
	text, blocks := actionFinishedMessage(a, m)
	if text != "✅ start done · 683e, 286d · approved by <@U05>" {
		t.Errorf("text = %q", text)
	}
	if len(blocks) != 2 {
		t.Fatalf("want the outcome and the result, without the default note: %d blocks", len(blocks))
	}
	if got := blocks[1].(*slack.SectionBlock).Text.Text; got != "```started 683e · undo: fly machine stop 683e -a pebble```" {
		t.Errorf("result block = %q", got)
	}
	a.Note.String = "ok from the incident call"
	if _, blocks := actionFinishedMessage(a, m); len(blocks) != 3 {
		t.Error("a human-written note is kept")
	}
	if slackMention("alice") != "alice" {
		t.Error("a CLI approver is kept as written")
	}
}

func TestListBlocksStayShort(t *testing.T) {
	items := []string{strings.Repeat("a", 150), strings.Repeat("b", 150), strings.Repeat("c", 150), strings.Repeat("d", 500)}
	blocks := listBlocks("Evidence", items)
	if len(blocks) < 2 {
		t.Fatalf("a long list should be split, got %d block(s)", len(blocks))
	}
	var all string
	for _, b := range blocks {
		txt := b.(*slack.SectionBlock).Text.Text
		if len(txt) > listBlockSize+len("*Evidence*\n") {
			t.Errorf("block of %d characters", len(txt))
		}
		all += txt + "\n"
	}
	if !strings.HasPrefix(all, "*Evidence*\n• a") || strings.Count(all, "• ") != 4 {
		t.Errorf("every item once, after the title: %q", all)
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
	m := &stromboliv1.Mitigation{
		Kind: "restart", ActionId: d.action.ID.String(), Risk: "reversible",
		MachineAction: &stromboliv1.MachineAction{Action: "restart", App: "api", MachineIds: []string{"148e"}},
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
	// The approver is a mention, <@U1> (escaped by json.Marshal).
	if strings.Contains(string(out), approveActionID) || !strings.Contains(string(out), "done, approved by") || !strings.Contains(string(out), "@U1") {
		t.Errorf("succeeded: %s", out)
	}
}
