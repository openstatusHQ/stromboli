// Package report formats investigation results and publishes them to Slack.
package report

import (
	"context"
	"fmt"
	"strings"

	"github.com/slack-go/slack"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/internal/store/db"
)

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

func blocks(inv db.Investigation, r *opensrev1.Report) []slack.Block {
	md := func(s string) *slack.TextBlockObject {
		return slack.NewTextBlockObject(slack.MarkdownType, s, false, false)
	}
	header := fmt.Sprintf("🔎 *%s* — %s", inv.Title, strings.Join(inv.Services, ", "))

	if r == nil {
		return []slack.Block{
			slack.NewSectionBlock(md(header), nil, nil),
			slack.NewSectionBlock(md(fmt.Sprintf("❌ Investigation failed: %s", inv.Error.String)), nil, nil),
		}
	}

	out := []slack.Block{
		slack.NewSectionBlock(md(header), nil, nil),
		slack.NewSectionBlock(md(fmt.Sprintf("*Likely cause* (confidence: %s): %s", r.Confidence, r.LikelyCause)), nil, nil),
	}
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
		out = append(out, slack.NewSectionBlock(md(clip("*Suggested mitigation*\n"+r.SuggestedMitigation)), nil, nil))
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
