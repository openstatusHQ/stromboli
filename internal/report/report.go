// Package report formats investigation results and publishes them to Slack.
package report

import (
	"context"
	"fmt"
	"strings"
	"time"

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
}

func NewSlack(token, channel string) *Slack {
	return &Slack{client: slack.New(token), channel: channel}
}

func (s *Slack) Started(ctx context.Context, inv db.Investigation) (string, string, error) {
	text := fmt.Sprintf("🔎 Investigating *%s* (%s) — services: %s", inv.Title, inv.Source, strings.Join(inv.Services, ", "))
	ch, ts, err := s.client.PostMessageContext(ctx, s.channel, slack.MsgOptionText(text, false))
	return ch, ts, err
}

// Mitigated edits the message to lead with the mitigation while the root-cause
// investigation continues, and pings the thread so on-call is notified.
func (s *Slack) Mitigated(ctx context.Context, inv db.Investigation, m *opensrev1.Mitigation) error {
	if !inv.SlackChannel.Valid {
		return nil
	}
	out := []slack.Block{slack.NewSectionBlock(md(header(inv)), nil, nil)}
	out = append(out, mitigationBlocks(inv, m)...)
	out = append(out, slack.NewContextBlock("", md(fmt.Sprintf("🔎 Investigating the root cause… · opensre investigation `%s` · suggestions only, nothing was changed", inv.ID))))
	text := fmt.Sprintf("🩹 %s — mitigate first: %s %s", inv.Title, m.Kind, m.Target)
	if _, _, _, err := s.client.UpdateMessageContext(ctx, inv.SlackChannel.String, inv.SlackTs.String,
		slack.MsgOptionText(text, false), slack.MsgOptionBlocks(out...)); err != nil {
		return err
	}
	// Edits do not notify anyone; a thread reply does.
	_, _, err := s.client.PostMessageContext(ctx, inv.SlackChannel.String,
		slack.MsgOptionTS(inv.SlackTs.String), slack.MsgOptionText(text+"\n"+clip(m.Action), false))
	return err
}

func (s *Slack) Finished(ctx context.Context, inv db.Investigation, r *opensrev1.Report) error {
	if !inv.SlackChannel.Valid {
		return nil
	}
	_, _, _, err := s.client.UpdateMessageContext(ctx, inv.SlackChannel.String, inv.SlackTs.String,
		slack.MsgOptionText(fallbackText(inv, r), false), slack.MsgOptionBlocks(blocks(inv, r)...))
	return err
}

func fallbackText(inv db.Investigation, r *opensrev1.Report) string {
	if r == nil {
		return fmt.Sprintf("❌ Investigation of %s failed", inv.Title)
	}
	return fmt.Sprintf("🔎 %s — likely cause: %s", inv.Title, r.LikelyCause)
}

func md(s string) *slack.TextBlockObject {
	return slack.NewTextBlockObject(slack.MarkdownType, s, false, false)
}

func header(inv db.Investigation) string {
	return fmt.Sprintf("🔎 *%s* — %s", inv.Title, strings.Join(inv.Services, ", "))
}

// mitigationBlocks renders the mitigation, with how long after the start of
// the investigation it was proposed (a proxy for time to mitigation).
func mitigationBlocks(inv db.Investigation, m *opensrev1.Mitigation) []slack.Block {
	if m == nil {
		return nil
	}
	when := ""
	if inv.MitigatedAt.Valid && inv.CreatedAt.Valid {
		when = fmt.Sprintf(", proposed %s after the alert", inv.MitigatedAt.Time.Sub(inv.CreatedAt.Time).Round(time.Second))
	}
	lines := []string{fmt.Sprintf("🩹 *Mitigate first: %s* %s\n_risk: %s · confidence: %s%s_", m.Kind, m.Target, m.Risk, m.Confidence, when)}
	for _, f := range [][2]string{{"Impact", m.Impact}, {"Why", m.Rationale}, {"Run", m.Action}, {"Verify", m.Verify}, {"Undo", m.Undo}} {
		if f[1] != "" {
			lines = append(lines, fmt.Sprintf("*%s:* %s", f[0], f[1]))
		}
	}
	return []slack.Block{slack.NewSectionBlock(md(clip(strings.Join(lines, "\n"))), nil, nil)}
}

func blocks(inv db.Investigation, r *opensrev1.Report) []slack.Block {
	out := []slack.Block{slack.NewSectionBlock(md(header(inv)), nil, nil)}
	out = append(out, mitigationBlocks(inv, Mitigation(inv))...)

	if r == nil {
		return append(out, slack.NewSectionBlock(md(fmt.Sprintf("❌ Investigation failed: %s", inv.Error.String)), nil, nil))
	}

	out = append(out, slack.NewSectionBlock(md(fmt.Sprintf("*Likely cause* (confidence: %s): %s", r.Confidence, r.LikelyCause)), nil, nil))
	if r.Summary != "" {
		out = append(out, slack.NewSectionBlock(md(r.Summary), nil, nil))
	}
	bullets := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		out = append(out, slack.NewSectionBlock(md(clip(fmt.Sprintf("*%s*\n• %s", title, strings.Join(items, "\n• ")))), nil, nil))
	}
	bullets("Evidence", r.Evidence)
	bullets("Ruled out", r.RuledOut)
	if r.SuggestedMitigation != "" && r.SuggestedMitigation != "none" {
		out = append(out, slack.NewSectionBlock(md(clip("*Mitigation, given the root cause*\n"+r.SuggestedMitigation)), nil, nil))
	}
	if r.Fix != "" && r.Fix != "unknown" {
		out = append(out, slack.NewSectionBlock(md(clip("*Fix*\n"+r.Fix)), nil, nil))
	}
	bullets("Next steps", r.NextSteps)
	out = append(out, slack.NewContextBlock("", md(fmt.Sprintf("opensre investigation `%s` · suggestions only, nothing was changed", inv.ID))))
	return out
}

// clip keeps a section under Slack's 3000 character limit.
func clip(s string) string {
	if len(s) > 2900 {
		return s[:2900] + "…"
	}
	return s
}
