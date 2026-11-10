// Package report formats investigation results and publishes them to Slack.
package report

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/slack-go/slack"
	"google.golang.org/protobuf/encoding/protojson"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/internal/store/db"
)

// MitigationMarkdown renders a proposed mitigation for terminals.
func MitigationMarkdown(m *opensrev1.Mitigation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Mitigate first: %s %s (risk: %s, confidence: %s)\n", m.Kind, m.Target, m.Risk, m.Confidence)
	for _, f := range [][2]string{{"Impact", m.Impact}, {"Why", m.Rationale}, {"Run", m.Action}, {"Verify", m.Verify}, {"Undo", m.Undo}} {
		if f[1] != "" {
			fmt.Fprintf(&b, "  %s: %s\n", f[0], indent(f[1]))
		}
	}
	if m.ActionId != "" {
		fmt.Fprintf(&b, "  opensre can do it: %s\n    approve: %s\n", MachineActionText(m.MachineAction), ApproveCommand(m.ActionId))
	}
	return strings.TrimRight(b.String(), "\n")
}

// Mitigation decodes the mitigation stored on an investigation, or nil.
func Mitigation(inv db.Investigation) *opensrev1.Mitigation {
	if len(inv.Mitigation) == 0 {
		return nil
	}
	m := &opensrev1.Mitigation{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(inv.Mitigation, m); err != nil {
		return nil
	}
	return m
}

// MachineActionText describes a machine action, e.g. "restart 148e of app api".
func MachineActionText(m *opensrev1.MachineAction) string {
	if m == nil {
		return ""
	}
	s := fmt.Sprintf("%s %s of app %s", m.Action, strings.Join(m.MachineIds, ", "), m.App)
	if m.Region != "" {
		s += " in " + m.Region
	}
	return s
}

// ApproveCommand is the CLI command that approves and runs an action.
func ApproveCommand(id string) string {
	return fmt.Sprintf("opensre actions approve %s --as <your name>", id)
}

func indent(s string) string { return strings.ReplaceAll(s, "\n", "\n    ") }

// Markdown renders a report for terminals and the API.
func Markdown(r *opensrev1.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Likely cause (confidence: %s): %s\n\n", r.Confidence, r.LikelyCause)
	if r.Summary != "" {
		b.WriteString(r.Summary + "\n\n")
	}
	section := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		b.WriteString(title + ":\n")
		for _, i := range items {
			b.WriteString("  - " + i + "\n")
		}
		b.WriteString("\n")
	}
	section("Evidence", r.Evidence)
	section("Ruled out", r.RuledOut)
	if r.SuggestedMitigation != "" && r.SuggestedMitigation != "none" {
		fmt.Fprintf(&b, "Suggested mitigation: %s\n\n", r.SuggestedMitigation)
	}
	if r.Fix != "" && r.Fix != "unknown" {
		fmt.Fprintf(&b, "Fix: %s\n\n", r.Fix)
	}
	section("Next steps", r.NextSteps)
	return strings.TrimRight(b.String(), "\n")
}

// Slack posts one message per investigation and edits it in place.
type Slack struct {
	client  *slack.Client
	channel string
	// buttons is set by EnableButtons: proposed actions then get Approve and
	// Reject buttons instead of the CLI command.
	buttons *buttons
}

func NewSlack(token, channel string) *Slack {
	return &Slack{client: slack.New(token), channel: channel}
}

func (s *Slack) Started(ctx context.Context, inv db.Investigation) (string, string, error) {
	text := fmt.Sprintf("🔎 Investigating *%s* · %s (%s)", serviceLabel(inv), shorten(inv.Title, 300), inv.Source)
	ch, ts, err := s.client.PostMessageContext(ctx, s.channel, slack.MsgOptionText(text, false))
	return ch, ts, err
}

// Mitigated edits the message to lead with the mitigation while the root-cause
// investigation continues, and pings the thread so on-call is notified.
func (s *Slack) Mitigated(ctx context.Context, inv db.Investigation, m *opensrev1.Mitigation) error {
	if !inv.SlackChannel.Valid {
		return nil
	}
	text := mitigationText(inv, m)
	if _, _, _, err := s.client.UpdateMessageContext(ctx, inv.SlackChannel.String, inv.SlackTs.String,
		slack.MsgOptionText(text, false), slack.MsgOptionBlocks(s.mitigatedBlocks(ctx, inv, m)...)); err != nil {
		return err
	}
	// Edits do not notify anyone; a thread reply does. It also carries the
	// mitigation's details, to keep the main message short.
	opts := []slack.MsgOption{slack.MsgOptionTS(inv.SlackTs.String), slack.MsgOptionText(text, false)}
	if d := mitigationDetails(m); d != "" {
		opts = append(opts, slack.MsgOptionBlocks(slack.NewSectionBlock(md(clip(text+"\n\n"+d)), nil, nil)))
	}
	_, _, err := s.client.PostMessageContext(ctx, inv.SlackChannel.String, opts...)
	return err
}

// Finished edits the message into the report and replies in the thread with
// the supporting details, which would bury the conclusion.
func (s *Slack) Finished(ctx context.Context, inv db.Investigation, r *opensrev1.Report) error {
	if !inv.SlackChannel.Valid {
		return nil
	}
	if _, _, _, err := s.client.UpdateMessageContext(ctx, inv.SlackChannel.String, inv.SlackTs.String,
		slack.MsgOptionText(fallbackText(inv, r), false), slack.MsgOptionBlocks(s.blocks(ctx, inv, r)...)); err != nil {
		return err
	}
	details := detailBlocks(r)
	if len(details) == 0 {
		return nil
	}
	_, _, err := s.client.PostMessageContext(ctx, inv.SlackChannel.String, slack.MsgOptionTS(inv.SlackTs.String),
		slack.MsgOptionText("Investigation details", false), slack.MsgOptionBlocks(details...))
	return err
}

func (s *Slack) mitigatedBlocks(ctx context.Context, inv db.Investigation, m *opensrev1.Mitigation) []slack.Block {
	out := headerBlocks(inv, "🔎")
	out = append(out, s.mitigationBlocks(ctx, inv, m)...)
	return append(out, slack.NewContextBlock("", md(fmt.Sprintf("⏳ Looking for the root cause… · investigation `%s` · nothing runs without a human's approval", inv.ID))))
}

// refresh redraws the investigation's message from its stored state, e.g.
// once an action was decided so its buttons go away.
func (s *Slack) refresh(ctx context.Context, inv db.Investigation) error {
	if !inv.SlackChannel.Valid {
		return nil
	}
	var text string
	var out []slack.Block
	switch m := Mitigation(inv); {
	case inv.Status != "running":
		var r *opensrev1.Report
		if len(inv.Report) > 0 {
			r = &opensrev1.Report{}
			if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(inv.Report, r); err != nil {
				r = nil
			}
		}
		text, out = fallbackText(inv, r), s.blocks(ctx, inv, r)
	case m != nil:
		text = mitigationText(inv, m)
		out = s.mitigatedBlocks(ctx, inv, m)
	default:
		return nil
	}
	_, _, _, err := s.client.UpdateMessageContext(ctx, inv.SlackChannel.String, inv.SlackTs.String,
		slack.MsgOptionText(text, false), slack.MsgOptionBlocks(out...))
	return err
}

// ActionFinished replies in the investigation's thread with what an approved
// action did, or that it was rejected.
func (s *Slack) ActionFinished(ctx context.Context, inv db.Investigation, a db.Action, m *opensrev1.MachineAction) error {
	if !inv.SlackChannel.Valid {
		return nil
	}
	what := MachineActionText(m)
	var text string
	switch a.Status {
	case "succeeded":
		text = fmt.Sprintf("✅ %s, approved by %s.\n%s", what, a.DecidedBy.String, a.Result.String)
	case "failed":
		text = fmt.Sprintf("❌ %s, approved by %s, failed: %s", what, a.DecidedBy.String, a.Error.String)
		if a.Result.String != "" {
			text += "\nDone before the failure:\n" + a.Result.String
		}
	case "rejected":
		text = fmt.Sprintf("🚫 %s was rejected by %s.", what, a.DecidedBy.String)
	default:
		return nil
	}
	if a.Note.String != "" {
		text += "\n> " + a.Note.String
	}
	_, _, err := s.client.PostMessageContext(ctx, inv.SlackChannel.String,
		slack.MsgOptionTS(inv.SlackTs.String), slack.MsgOptionText(clip(text), false))
	if s.buttons != nil {
		// Replace the buttons with the outcome.
		if err := s.refresh(ctx, inv); err != nil {
			slog.Warn("slack: redraw investigation message", "investigation", inv.ID, "err", err)
		}
	}
	return err
}

// mitigationText is the one-line form of a mitigation, for notifications.
func mitigationText(inv db.Investigation, m *opensrev1.Mitigation) string {
	if isNone(m.Kind) {
		return fmt.Sprintf("🟢 *%s*: no mitigation needed; looking for the root cause.", serviceLabel(inv))
	}
	return fmt.Sprintf("🩹 *%s*: mitigate first, %s %s", serviceLabel(inv), m.Kind, m.Target)
}

func fallbackText(inv db.Investigation, r *opensrev1.Report) string {
	if r == nil {
		return fmt.Sprintf("❌ Investigation of %s failed", serviceLabel(inv))
	}
	return fmt.Sprintf("📋 %s: %s", serviceLabel(inv), shorten(r.LikelyCause, 300))
}

func md(s string) *slack.TextBlockObject {
	return slack.NewTextBlockObject(slack.MarkdownType, s, false, false)
}

// serviceLabel names the investigated services, or "unknown service".
func serviceLabel(inv db.Investigation) string {
	if len(inv.Services) == 0 {
		return "unknown service"
	}
	return strings.Join(inv.Services, ", ")
}

// headerBlocks is a short header naming the services, then the alert that
// started the investigation in small print: alert titles can be long.
func headerBlocks(inv db.Investigation, emoji string) []slack.Block {
	h := slack.NewTextBlockObject(slack.PlainTextType, shorten(emoji+" "+serviceLabel(inv), 150), true, false)
	alert := fmt.Sprintf("*Alert* · %s", shorten(inv.Title, 500))
	if inv.Source != "" {
		alert += " · via " + inv.Source
	}
	return []slack.Block{slack.NewHeaderBlock(h), slack.NewContextBlock("", md(alert))}
}

// mitigationBlocks renders the mitigation, with how long after the start of
// the investigation it was proposed (a proxy for time to mitigation).
func (s *Slack) mitigationBlocks(ctx context.Context, inv db.Investigation, m *opensrev1.Mitigation) []slack.Block {
	if m == nil {
		return nil
	}
	if isNone(m.Kind) {
		text := "🟢 *No mitigation needed*"
		if m.Impact != "" {
			text += " · " + shorten(m.Impact, 300)
		}
		return []slack.Block{slack.NewSectionBlock(md(text), nil, nil)}
	}
	when := ""
	if inv.MitigatedAt.Valid && inv.CreatedAt.Valid {
		when = fmt.Sprintf(" · proposed %s after the alert", inv.MitigatedAt.Time.Sub(inv.CreatedAt.Time).Round(time.Second))
	}
	lines := []string{
		fmt.Sprintf("🩹 *Mitigate first: %s* %s", m.Kind, m.Target),
	}
	if m.Impact != "" {
		lines = append(lines, "*Impact:* "+shorten(m.Impact, 400))
	}
	if m.Action != "" {
		lines = append(lines, fmt.Sprintf("```%s```", strings.Trim(m.Action, "`\n ")))
	}
	if m.ActionId != "" && s.buttons == nil {
		lines = append(lines, fmt.Sprintf("*opensre can do it once approved:* %s · `%s`", MachineActionText(m.MachineAction), ApproveCommand(m.ActionId)))
	}
	out := []slack.Block{
		slack.NewSectionBlock(md(clip(strings.Join(lines, "\n"))), nil, nil),
		slack.NewContextBlock("", md(fmt.Sprintf("risk: %s · confidence: %s%s · why, verify and undo in the thread", dash(m.Risk), dash(m.Confidence), when))),
	}
	if m.ActionId != "" && s.buttons != nil {
		out = append(out, s.buttons.actionBlocks(ctx, m)...)
	}
	return out
}

func (s *Slack) blocks(ctx context.Context, inv db.Investigation, r *opensrev1.Report) []slack.Block {
	emoji := "📋"
	if r == nil {
		emoji = "❌"
	}
	out := headerBlocks(inv, emoji)
	out = append(out, s.mitigationBlocks(ctx, inv, Mitigation(inv))...)
	out = append(out, slack.NewDividerBlock())

	if r == nil {
		return append(out, slack.NewSectionBlock(md(clip(fmt.Sprintf("❌ *Investigation failed*\n%s", inv.Error.String))), nil, nil))
	}

	cause := "*Likely cause*"
	if r.Confidence != "" {
		cause += fmt.Sprintf(" · _%s confidence_", r.Confidence)
	}
	cause += "\n" + shorten(r.LikelyCause, 600)
	if !isNone(r.Fix) && r.Fix != "unknown" {
		cause += "\n*Fix:* " + shorten(r.Fix, 400)
	}
	if steps := firstN(r.NextSteps, 3); len(steps) > 0 {
		cause += "\n" + bullets("Next steps", steps)
	}
	out = append(out, slack.NewSectionBlock(md(clip(cause)), nil, nil))
	foot := fmt.Sprintf("investigation `%s` · nothing runs without a human's approval", inv.ID)
	if len(detailBlocks(r)) > 0 {
		foot = "🧵 details in the thread · " + foot
	}
	return append(out, slack.NewContextBlock("", md(foot)))
}

// mitigationDetails is the reasoning behind a mitigation, posted in the thread.
func mitigationDetails(m *opensrev1.Mitigation) string {
	if isNone(m.Kind) {
		return m.Rationale
	}
	var lines []string
	for _, f := range [][2]string{{"Why", m.Rationale}, {"Verify", m.Verify}, {"Undo", m.Undo}} {
		if f[1] != "" {
			lines = append(lines, fmt.Sprintf("*%s:* %s", f[0], f[1]))
		}
	}
	return strings.Join(lines, "\n")
}

// detailBlocks are the report's supporting details, posted in the thread.
func detailBlocks(r *opensrev1.Report) []slack.Block {
	if r == nil {
		return nil
	}
	var out []slack.Block
	if r.Summary != "" {
		out = append(out, slack.NewSectionBlock(md(clip(r.Summary)), nil, nil))
	}
	for _, sec := range []struct {
		title string
		items []string
	}{{"Evidence", r.Evidence}, {"Ruled out", r.RuledOut}, {"More next steps", r.NextSteps[min(3, len(r.NextSteps)):]}} {
		if len(sec.items) > 0 {
			out = append(out, slack.NewSectionBlock(md(clip(bullets(sec.title, sec.items))), nil, nil))
		}
	}
	if !isNone(r.SuggestedMitigation) {
		out = append(out, slack.NewSectionBlock(md(clip("*Mitigation, given the root cause:* "+r.SuggestedMitigation)), nil, nil))
	}
	return out
}

func firstN(items []string, n int) []string { return items[:min(n, len(items))] }

func bullets(title string, items []string) string {
	return fmt.Sprintf("*%s*\n• %s", title, strings.Join(items, "\n• "))
}

// isNone reports whether a field says there is nothing to do, e.g. "none",
// "None needed." or "none. This is a test alert".
func isNone(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return true
	}
	rest, ok := strings.CutPrefix(s, "none")
	if !ok {
		return false
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return rest == "" || !unicode.IsLetter(r)
}

// shorten cuts s to at most n bytes on a rune boundary, adding an ellipsis.
func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// clip keeps a section under Slack's 3000 character limit.
func clip(s string) string { return shorten(s, 2900) }
