package report

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/slack-go/slack"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
	"github.com/openstatushq/stromboli/internal/store/db"
)

const injection = "<!channel> see <https://evil.example|Runbook> & more"

// recordingSlack returns a Slack whose API calls are recorded: the text and
// blocks of every message, decoded from the form.
func recordingSlack(t *testing.T) (*Slack, func() string) {
	t.Helper()
	var mu sync.Mutex
	var sent strings.Builder
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		mu.Lock()
		sent.WriteString(form.Get("text") + "\n" + form.Get("blocks") + "\n")
		mu.Unlock()
		fmt.Fprint(w, `{"ok":true,"channel":"C1","ts":"1.2"}`)
	}))
	t.Cleanup(api.Close)
	s := &Slack{client: slack.New("xoxb-test", slack.OptionAPIURL(api.URL+"/")), channel: "C1"}
	return s, func() string {
		mu.Lock()
		defer mu.Unlock()
		return sent.String()
	}
}

func assertEscaped(t *testing.T, sent string) {
	t.Helper()
	for _, raw := range []string{"<!channel>", "<https://evil.example"} {
		if strings.Contains(sent, raw) {
			t.Errorf("untrusted %q reached Slack unescaped:\n%s", raw, sent)
		}
	}
	// blocks are JSON, where Go escapes & as &.
	if !strings.Contains(sent, "&lt;!channel&gt;") && !strings.Contains(sent, `&lt;!channel&gt;`) {
		t.Errorf("escaped text missing:\n%s", sent)
	}
}

func TestSlackEscapesUntrustedText(t *testing.T) {
	ctx := context.Background()
	inv := db.Investigation{
		ID: uuid.New(), Title: injection, Source: "alertmanager", Services: []string{"api"}, Status: "completed",
		SlackChannel: pgtype.Text{String: "C1", Valid: true}, SlackTs: pgtype.Text{String: "1.2", Valid: true},
	}
	m := &stromboliv1.Mitigation{Kind: "restart", Target: injection, Impact: injection, Action: injection, Rationale: injection}
	r := &stromboliv1.Report{LikelyCause: injection, Summary: injection, Evidence: []string{injection}, NextSteps: []string{injection}}

	t.Run("started", func(t *testing.T) {
		s, sent := recordingSlack(t)
		if _, _, err := s.Started(ctx, inv); err != nil {
			t.Fatal(err)
		}
		assertEscaped(t, sent())
	})
	t.Run("mitigated", func(t *testing.T) {
		s, sent := recordingSlack(t)
		if err := s.Mitigated(ctx, inv, m); err != nil {
			t.Fatal(err)
		}
		assertEscaped(t, sent())
		if m.Target != injection {
			t.Error("the caller's mitigation must not be modified")
		}
	})
	t.Run("finished", func(t *testing.T) {
		s, sent := recordingSlack(t)
		if err := s.Finished(ctx, inv, r); err != nil {
			t.Fatal(err)
		}
		assertEscaped(t, sent())
	})
	t.Run("action finished", func(t *testing.T) {
		s, sent := recordingSlack(t)
		a := db.Action{Status: "failed", DecidedBy: pgtype.Text{String: "<!here>", Valid: true},
			Error: pgtype.Text{String: injection, Valid: true}, Note: pgtype.Text{String: injection, Valid: true}}
		if err := s.ActionFinished(ctx, inv, a, &stromboliv1.MachineAction{Action: "restart", MachineIds: []string{"<!channel>"}}); err != nil {
			t.Fatal(err)
		}
		got := sent()
		assertEscaped(t, got)
		if strings.Contains(got, "<!here>") {
			t.Errorf("a non-Slack decider must be escaped:\n%s", got)
		}
	})
	t.Run("failed health check", func(t *testing.T) {
		if got := FailedText(map[string]string{"api": injection}); strings.Contains(got, "<!channel>") {
			t.Errorf("got %q", got)
		}
	})
}

func TestSlackMentionOnlyForSlackIDs(t *testing.T) {
	if got := slackMention("@alice (Slack U05AB)"); got != "<@U05AB>" {
		t.Errorf("got %q", got)
	}
	if got := slackMention("x (Slack !channel)"); strings.Contains(got, "<!") {
		t.Errorf("got %q", got)
	}
}

func TestPlainTextIsUnescaped(t *testing.T) {
	// plain_text is shown verbatim: escaped input must not show as &lt;.
	if got := plain(esc("a < b & c")).Text; got != "a < b & c" {
		t.Errorf("got %q", got)
	}
}

func TestShortenKeepsEntities(t *testing.T) {
	s := esc(strings.Repeat("x", 8) + "&&&&")
	for n := 9; n < len(s); n++ {
		got := strings.TrimSuffix(shorten(s, n), "…")
		if i := strings.LastIndexByte(got, '&'); i >= 0 && !strings.HasSuffix(got, ";") && !strings.Contains(got[i:], ";") {
			t.Errorf("shorten(%q, %d) = %q splits an entity", s, n, got)
		}
	}
}
