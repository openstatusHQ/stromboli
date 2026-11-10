package mcpserver

import (
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
	"github.com/openstatushq/stromboli/internal/report"
)

func servicesMarkdown(services []*stromboliv1.Service) string {
	if len(services) == 0 {
		return "The catalog is empty. Services are added with `stromboli catalog import`."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d services:\n", len(services))
	for _, s := range services {
		fmt.Fprintf(&b, "- %s", s.Name)
		var meta []string
		if s.Tier > 0 {
			meta = append(meta, fmt.Sprintf("tier %d", s.Tier))
		}
		if len(s.Owners) > 0 {
			meta = append(meta, "owners "+strings.Join(s.Owners, ", "))
		}
		if len(s.DependsOn) > 0 {
			meta = append(meta, "depends on "+strings.Join(s.DependsOn, ", "))
		}
		if len(meta) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(meta, "; "))
		}
		if s.Description != "" {
			fmt.Fprintf(&b, ": %s", s.Description)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func serviceMarkdown(r *stromboliv1.GetServiceResponse) string {
	s := r.Service
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", s.Name)
	if s.Description != "" {
		b.WriteString(s.Description + "\n")
	}
	b.WriteString("\n")
	field := func(name, value string) {
		if value != "" {
			fmt.Fprintf(&b, "- %s: %s\n", name, value)
		}
	}
	if s.Tier > 0 {
		field("Tier", fmt.Sprint(s.Tier))
	}
	field("Owners", strings.Join(s.Owners, ", "))
	field("Runtime", s.Runtime)
	field("Repo", s.Repo)
	field("Depends on", strings.Join(s.DependsOn, ", "))
	field("Upstream (everything it depends on)", strings.Join(r.Upstream, ", "))
	field("Downstream (affected when it fails)", strings.Join(r.Downstream, ", "))
	if l := s.Logs; l != nil && l.Source != "" {
		field("Logs", fmt.Sprintf("%s `%s`", l.Source, l.Selector))
	}
	if m := s.Metrics; m != nil && m.Source != "" {
		field("Metrics", fmt.Sprintf("%s `%s`", m.Source, strings.Join(m.Selectors, ", ")))
	}
	if d := s.Deploys; d != nil && d.Source != "" {
		v := d.Source + " " + d.Repo
		switch {
		case d.Workflow != "":
			v += " (workflow " + d.Workflow + ")"
		case d.Environment != "":
			v += " (environment " + d.Environment + ")"
		default:
			v += " (commits)"
		}
		field("Deploys", v)
	}
	if f := s.Fly; f != nil {
		field("Fly app", f.App)
	}
	field("Runbooks", strings.Join(s.Runbooks, ", "))
	field("Monitors", strings.Join(s.Monitors, ", "))
	for _, slo := range s.Slos {
		field("SLO "+slo.Name, fmt.Sprintf("%g", slo.Target))
	}
	return strings.TrimRight(b.String(), "\n")
}

func investigationsMarkdown(invs []*stromboliv1.Investigation) string {
	if len(invs) == 0 {
		return "No investigations match."
	}
	var b strings.Builder
	for _, inv := range invs {
		fmt.Fprintf(&b, "- %s [%s] %s: %s (%s, %s)", inv.Id, statusName(inv.Status), strings.Join(inv.Services, ", "), inv.Title, inv.Source, when(inv.CreatedAt))
		if inv.Mitigation != nil {
			fmt.Fprintf(&b, "; mitigation: %s %s", inv.Mitigation.Kind, inv.Mitigation.Target)
		}
		if inv.Report != nil && inv.Report.LikelyCause != "" {
			fmt.Fprintf(&b, "; likely cause: %s", clip(inv.Report.LikelyCause, 200))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func investigationMarkdown(inv *stromboliv1.Investigation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", inv.Title)
	fmt.Fprintf(&b, "- ID: %s\n- Status: %s\n- Services: %s\n- Source: %s\n- Started: %s\n",
		inv.Id, statusName(inv.Status), strings.Join(inv.Services, ", "), inv.Source, when(inv.CreatedAt))
	if inv.MitigatedAt != nil {
		fmt.Fprintf(&b, "- Mitigation published: %s\n", when(inv.MitigatedAt))
	}
	if inv.FinishedAt != nil {
		fmt.Fprintf(&b, "- Finished: %s\n", when(inv.FinishedAt))
	}
	if inv.Mitigation != nil {
		b.WriteString("\n## Mitigation\n\n" + report.MitigationMarkdown(inv.Mitigation) + "\n")
		if inv.Mitigation.ActionId != "" {
			b.WriteString("\nThe machine action needs a named human to approve it, with the stromboli CLI or in Slack. This MCP server cannot approve it.\n")
		}
	} else if inv.Status == stromboliv1.InvestigationStatus_INVESTIGATION_STATUS_RUNNING {
		b.WriteString("\nNo mitigation yet: the triage phase is still gathering evidence.\n")
	}
	if inv.Report != nil {
		b.WriteString("\n## Report\n\n" + report.Markdown(inv.Report) + "\n")
	}
	if inv.Error != "" {
		b.WriteString("\n## Error\n\n" + inv.Error + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func stepsMarkdown(steps []*stromboliv1.Step, more int64) string {
	if len(steps) == 0 {
		return "## Steps\n\nNo steps."
	}
	var b strings.Builder
	b.WriteString("## Steps\n")
	for _, s := range steps {
		fmt.Fprintf(&b, "\n### %s\n", stepLine(s))
		if s.Output != "" {
			b.WriteString(clip(s.Output, maxStepOutput) + "\n")
		}
	}
	if more > 0 {
		fmt.Fprintf(&b, "\nMore steps: call get_investigation with include_steps and after_seq=%d.\n", more)
	}
	return strings.TrimRight(b.String(), "\n")
}

func stepLine(s *stromboliv1.Step) string {
	line := fmt.Sprintf("#%d %s", s.Seq, s.Kind)
	if s.Name != "" {
		line += " " + s.Name
	}
	if s.DurationMs > 0 {
		line += fmt.Sprintf(" (%dms)", s.DurationMs)
	}
	if s.IsError {
		line += " [error]"
	}
	return line
}

func actionsMarkdown(actions []*stromboliv1.Action) string {
	if len(actions) == 0 {
		return "No actions match."
	}
	var b strings.Builder
	for _, a := range actions {
		fmt.Fprintf(&b, "- %s [%s] %s: %s (investigation %s, %s)\n", a.Id, actionStatus(a.Status), a.Service,
			report.MachineActionText(a.MachineAction), a.InvestigationId, when(a.CreatedAt))
	}
	return strings.TrimRight(b.String(), "\n")
}

func actionMarkdown(a *stromboliv1.Action) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Action %s\n\n", a.Id)
	fmt.Fprintf(&b, "- Status: %s\n- Service: %s\n- Does: %s\n- Investigation: %s\n- Proposed: %s\n",
		actionStatus(a.Status), a.Service, report.MachineActionText(a.MachineAction), a.InvestigationId, when(a.CreatedAt))
	if a.Status == stromboliv1.ActionStatus_ACTION_STATUS_PROPOSED {
		fmt.Fprintf(&b, "- Expires: %s\n\nA named human must approve it: `%s`, or the Apply button in Slack. This MCP server cannot approve it.\n",
			when(a.ExpiresAt), report.ApproveCommand(a.Id))
	}
	if a.DecidedBy != "" {
		fmt.Fprintf(&b, "- Decided by: %s, %s\n", a.DecidedBy, when(a.DecidedAt))
	}
	if a.Note != "" {
		fmt.Fprintf(&b, "- Note: %s\n", a.Note)
	}
	if a.Result != "" {
		b.WriteString("\n## Result\n\n" + a.Result + "\n")
	}
	if a.Error != "" {
		b.WriteString("\n## Error\n\n" + a.Error + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func statusName(s stromboliv1.InvestigationStatus) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "INVESTIGATION_STATUS_"))
}

func actionStatus(s stromboliv1.ActionStatus) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "ACTION_STATUS_"))
}

func when(t *timestamppb.Timestamp) string {
	if t == nil {
		return "unknown"
	}
	return t.AsTime().UTC().Format(time.RFC3339)
}

// clip cuts s to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const marker = "\n…(truncated)"
	cut := max(n-len(marker), 0)
	for cut > 0 && cut < len(s) && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + marker
}
