package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slack-go/slack"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
	"github.com/openstatushq/stromboli/internal/store/db"
)

// Button action IDs. The button's value is the stromboli action ID.
const (
	approveActionID = "stromboli_approve"
	rejectActionID  = "stromboli_reject"
	actionsBlockID  = "stromboli_action"
)

// Decider approves and rejects actions; *action.Service implements it.
type Decider interface {
	Get(ctx context.Context, id uuid.UUID) (db.Action, error)
	Approve(ctx context.Context, id uuid.UUID, approver, note string) (db.Action, error)
	Reject(ctx context.Context, id uuid.UUID, approver, reason string) (db.Action, error)
}

// Investigations looks up an investigation, to redraw its message.
type Investigations func(ctx context.Context, id uuid.UUID) (db.Investigation, error)

type buttons struct {
	decider        Decider
	investigations Investigations
	signingSecret  string
	// approvers, when set, are the Slack user IDs allowed to click.
	approvers []string
	// postResponse answers through a response_url; replaced in tests.
	postResponse func(ctx context.Context, url string, msg *slack.WebhookMessage) error
}

// EnableButtons adds Approve and Reject buttons to proposed actions. Clicks
// arrive at InteractionsHandler, signed with signingSecret. With approvers
// set, only those Slack user IDs may decide; otherwise anyone in the channel.
func (s *Slack) EnableButtons(d Decider, invs Investigations, signingSecret string, approvers []string) {
	s.buttons = &buttons{
		decider: d, investigations: invs, signingSecret: signingSecret, approvers: approvers,
		postResponse: slack.PostWebhookContext,
	}
}

// actionBlocks renders a proposed action's buttons, or its outcome once decided.
func (b *buttons) actionBlocks(ctx context.Context, m *stromboliv1.Mitigation) []slack.Block {
	id, err := uuid.Parse(m.ActionId)
	if err != nil {
		return nil
	}
	what := MachineActionText(m.MachineAction)
	a, err := b.decider.Get(ctx, id)
	if err != nil {
		slog.Warn("slack: load action", "action", id, "err", err)
		return []slack.Block{slack.NewContextBlock("", md(fmt.Sprintf("stromboli can %s once approved: `%s`", what, ApproveCommand(m.ActionId))))}
	}
	if a.Status != "proposed" {
		return []slack.Block{slack.NewContextBlock("", md(actionOutcome(a, what)))}
	}
	approve := slack.NewButtonBlockElement(approveActionID, m.ActionId, plain("Apply: "+m.MachineAction.GetAction())).
		WithStyle(slack.StylePrimary).
		// Slack shows the confirmation dialog as plain text: no markdown.
		WithConfirm(slack.NewConfirmationBlockObject(
			plain("Apply this mitigation?"),
			plain(shorten(fmt.Sprintf("stromboli will %s now.\n\nRisk: %s\nUndo: %s", what, dash(m.Risk), machineUndo(m.MachineAction, m.Undo)), 300)),
			plain("Apply"), plain("Cancel")))
	reject := slack.NewButtonBlockElement(rejectActionID, m.ActionId, plain("Reject")).WithStyle(slack.StyleDanger)
	expires := ""
	if a.ExpiresAt.Valid {
		expires = fmt.Sprintf(" · expires <!date^%d^{time}|%s>", a.ExpiresAt.Time.Unix(), a.ExpiresAt.Time.UTC().Format(time.Kitchen+" UTC"))
	}
	return []slack.Block{
		slack.NewContextBlock("", md(fmt.Sprintf("*stromboli can do it once approved:* %s%s", what, expires))),
		slack.NewActionBlock(actionsBlockID, approve, reject),
	}
}

// machineUndo is how to revert m, with its real machine IDs: the model's
// undo is often a template like `fly machine stop <id>`. fallback is used
// when the undo depends on what the action creates.
func machineUndo(m *stromboliv1.MachineAction, fallback string) string {
	inverse := map[string]string{"start": "stop", "stop": "start", "cordon": "uncordon", "uncordon": "cordon"}
	switch verb := m.GetAction(); {
	case verb == "restart":
		return "none needed (same machines, image and config)"
	case inverse[verb] != "" && len(m.GetMachineIds()) > 0:
		cmds := make([]string, 0, len(m.MachineIds))
		for _, id := range m.MachineIds {
			cmds = append(cmds, fmt.Sprintf("fly machine %s %s -a %s", inverse[verb], id, m.App))
		}
		return strings.Join(cmds, "\n")
	case verb == "clone":
		return fmt.Sprintf("destroy the new machine: fly machine destroy <new-id> -a %s --force (its ID is posted in the thread)", m.App)
	}
	return dash(fallback)
}

func actionOutcome(a db.Action, what string) string {
	switch a.Status {
	case "running":
		return fmt.Sprintf("⏳ %s: approved by %s, running…", what, slackMention(a.DecidedBy.String))
	case "succeeded":
		return fmt.Sprintf("✅ %s: done, approved by %s", what, slackMention(a.DecidedBy.String))
	case "failed":
		return fmt.Sprintf("❌ %s: approved by %s, failed (see thread)", what, slackMention(a.DecidedBy.String))
	case "rejected":
		return fmt.Sprintf("🚫 %s: rejected by %s", what, slackMention(a.DecidedBy.String))
	case "expired":
		return fmt.Sprintf("⌛ %s: expired without a decision", what)
	case "superseded":
		return fmt.Sprintf("↪️ %s: replaced by a revised mitigation", what)
	}
	return fmt.Sprintf("%s: %s", what, a.Status)
}

// InteractionsHandler serves Slack's interactivity Request URL
// (POST /slack/interactions). It answers within Slack's 3 second limit and
// runs the decision in the background: the outcome is posted in the thread.
func (s *Slack) InteractionsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := s.buttons
		if b == nil {
			http.Error(w, "Slack buttons are not enabled", http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		sv, err := slack.NewSecretsVerifier(r.Header, b.signingSecret)
		if err == nil {
			_, _ = sv.Write(body)
			err = sv.Ensure()
		}
		if err != nil {
			slog.Warn("slack: rejected interaction", "err", err)
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var cb slack.InteractionCallback
		if err := json.Unmarshal([]byte(form.Get("payload")), &cb); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		if cb.Type != slack.InteractionTypeBlockActions {
			return
		}
		for _, ba := range cb.ActionCallback.BlockActions {
			if ba.ActionID != approveActionID && ba.ActionID != rejectActionID {
				continue
			}
			// The click must not be cancelled with the request.
			go b.decide(context.WithoutCancel(r.Context()), s, cb, ba)
		}
	})
}

func (b *buttons) decide(ctx context.Context, s *Slack, cb slack.InteractionCallback, ba *slack.BlockAction) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	who := slackUser(cb.User)
	log := slog.With("slack_user", cb.User.ID, "button", ba.ActionID, "action", ba.Value)
	tell := func(text string) {
		if cb.ResponseURL == "" {
			return
		}
		if err := b.postResponse(ctx, cb.ResponseURL, &slack.WebhookMessage{ResponseType: slack.ResponseTypeEphemeral, Text: text}); err != nil {
			log.Warn("slack: respond", "err", err)
		}
	}
	if len(b.approvers) > 0 && !slices.Contains(b.approvers, cb.User.ID) {
		log.Warn("slack: click from a user who is not in SLACK_APPROVERS")
		tell("🚫 You are not allowed to decide stromboli actions (SLACK_APPROVERS).")
		return
	}
	id, err := uuid.Parse(ba.Value)
	if err != nil {
		tell("❌ Invalid action.")
		return
	}
	a, err := b.decider.Get(ctx, id)
	if err != nil {
		tell("❌ " + err.Error())
		return
	}
	if ba.ActionID == rejectActionID {
		if _, err := b.decider.Reject(ctx, id, who, "rejected from Slack"); err != nil {
			tell("❌ Could not reject: " + err.Error())
			b.redraw(ctx, s, a.InvestigationID)
		}
		// Reject notifies the thread and redraws the message.
		return
	}
	log.Info("slack: action approved")
	// No ephemeral note: Approve redraws the message with the action running
	// in place of its buttons, then posts the outcome in the thread.
	if _, err := b.decider.Approve(ctx, id, who, "approved from Slack"); err != nil {
		// Not run (no longer pending, or no longer valid): Approve did not notify.
		log.Warn("slack: approve", "err", err)
		tell("❌ Not applied: " + err.Error())
		b.redraw(ctx, s, a.InvestigationID)
	}
}

func (b *buttons) redraw(ctx context.Context, s *Slack, investigation uuid.UUID) {
	inv, err := b.investigations(ctx, investigation)
	if err == nil {
		err = s.refresh(ctx, inv)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("slack: redraw investigation message", "investigation", investigation, "err", err)
	}
}

// slackUser is how a Slack user is recorded as the decider of an action.
func slackUser(u slack.User) string {
	name := u.Name
	if name == "" {
		name = u.ID
	}
	return fmt.Sprintf("@%s (Slack %s)", name, u.ID)
}

func plain(s string) *slack.TextBlockObject {
	return slack.NewTextBlockObject(slack.PlainTextType, s, false, false)
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
