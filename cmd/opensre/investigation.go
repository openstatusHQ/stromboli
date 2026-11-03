package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/internal/report"
)

func (a *app) briefCmd() *cobra.Command {
	var since string
	cmd := &cobra.Command{
		Use:   "brief <service>",
		Short: "Print the incident brief for a service (no LLM)",
		Long: `Print the brief an investigation starts from: the service card, its upstream
and downstream services, recent deploys, runbooks and log error patterns
compared with the baseline. No LLM is involved.`,
		Example: `  opensre brief api
  opensre brief api --since 30m`,
		Args: exactArgs(1, "<service>"),
		RunE: func(cmd *cobra.Command, pos []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			resp, err := c.investigation.GetBrief(cmd.Context(), connect.NewRequest(&opensrev1.GetBriefRequest{Service: pos[0], Since: since}))
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(resp.Msg)
			}
			_, err = fmt.Fprint(a.stdout, resp.Msg.Brief)
			return err
		},
	}
	cmd.Flags().StringVarP(&since, "since", "s", "1h", "look-back window, e.g. 30m or 2h (max 168h)")
	return cmd
}

func (a *app) investigateCmd() *cobra.Command {
	var title, desc string
	var noWatch bool
	cmd := &cobra.Command{
		Use:   "investigate <service>",
		Short: "Start an investigation and stream it live",
		Long: `Start an investigation of a service and stream its steps as they happen: the
brief, each tool call, the proposed mitigation, then the root-cause report.

Ctrl-C stops watching; the investigation keeps running on the server.
With --no-watch, only the investigation ID is printed on stdout.
With --json, every event is printed as one JSON object per line.`,
		Example: `  opensre investigate api --description "5xx on /v1/monitors"
  id=$(opensre investigate api --no-watch) && opensre show "$id" --follow
  opensre investigate api --json | jq -c 'select(.finished)'`,
		Args: exactArgs(1, "<service>"),
		RunE: func(cmd *cobra.Command, pos []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			resp, err := c.investigation.StartInvestigation(cmd.Context(), connect.NewRequest(&opensrev1.StartInvestigationRequest{
				Service: pos[0], Title: title, Description: desc,
			}))
			if err != nil {
				return err
			}
			id := resp.Msg.Investigation.Id
			if noWatch {
				if a.json {
					return a.printJSON(resp.Msg)
				}
				fmt.Fprintln(a.stdout, id)
				a.info("Follow it with: opensre show %s --follow", id)
				return nil
			}
			a.info("Investigation %s started.", id)
			return a.follow(cmd.Context(), c, id, 0)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&title, "title", "t", "", "investigation title")
	f.StringVarP(&desc, "description", "d", "", "what you observed")
	f.BoolVar(&noWatch, "no-watch", false, "print the investigation ID and return immediately")
	return cmd
}

// follow streams an investigation's steps after afterSeq until it finishes.
func (a *app) follow(ctx context.Context, c *clients, id string, afterSeq int64) error {
	stream, err := c.investigation.WatchInvestigation(ctx, connect.NewRequest(&opensrev1.WatchInvestigationRequest{Id: id, AfterSeq: afterSeq}))
	if err != nil {
		return err
	}
	defer stream.Close()
	for stream.Receive() {
		msg := stream.Msg()
		if a.json {
			if err := a.printJSONLine(msg); err != nil {
				return err
			}
			continue
		}
		switch ev := msg.Event.(type) {
		case *opensrev1.WatchInvestigationResponse_Step:
			a.printStep(ev.Step)
		case *opensrev1.WatchInvestigationResponse_Finished:
			a.printResult(ev.Finished)
		}
	}
	err = stream.Err()
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		fmt.Fprintf(a.stderr, "\nStopped watching. Investigation %s keeps running on the server.\nResume with: opensre show %s --follow\n", id, id)
		return context.Canceled
	}
	return err
}

func (a *app) printStep(s *opensrev1.Step) {
	w := a.stdout
	d := time.Duration(s.DurationMs) * time.Millisecond
	switch s.Kind {
	case "brief":
		fmt.Fprintf(w, "· built brief for %s (%s)\n", s.Name, d.Round(time.Millisecond))
	case "tool_call":
		mark := "→"
		if s.IsError {
			mark = "✗"
		}
		input, _ := s.Input.MarshalJSON()
		fmt.Fprintf(w, "  %s %s %s (%s)\n", mark, s.Name, input, d.Round(time.Millisecond))
	case "llm":
		if text := strings.TrimSpace(s.Output); text != "" {
			fmt.Fprintf(w, "· %s\n", indent(text))
		}
	case "mitigation":
		m := &opensrev1.Mitigation{}
		if err := protojson.Unmarshal([]byte(s.Output), m); err == nil {
			fmt.Fprintf(w, "\n%s\n\n· investigating the root cause…\n", report.MitigationMarkdown(m))
		}
	case "error":
		fmt.Fprintf(w, "✗ %s\n", s.Output)
	}
}

func (a *app) printResult(inv *opensrev1.Investigation) {
	w := a.stdout
	fmt.Fprintln(w)
	if inv.Mitigation != nil {
		fmt.Fprintln(w, report.MitigationMarkdown(inv.Mitigation))
		if inv.MitigatedAt != nil {
			fmt.Fprintf(w, "  (proposed %s after the alert)\n", inv.MitigatedAt.AsTime().Sub(inv.CreatedAt.AsTime()).Round(time.Second))
		}
		fmt.Fprintln(w)
	}
	switch {
	case inv.Report != nil:
		fmt.Fprintln(w, report.Markdown(inv.Report))
	case inv.Error != "":
		fmt.Fprintln(w, "Investigation failed:", inv.Error)
	}
	a.info("\n(%d input tokens, %d output tokens)", inv.InputTokens, inv.OutputTokens)
}

func (a *app) investigationsCmd() *cobra.Command {
	var limit int32
	cmd := &cobra.Command{
		Use:   "investigations",
		Short: "List recent investigations",
		Example: `  opensre investigations
  opensre investigations --limit 5 --json`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit <= 0 {
				return usagef(cmd, "--limit must be positive")
			}
			c, err := a.api()
			if err != nil {
				return err
			}
			resp, err := c.investigation.ListInvestigations(cmd.Context(), connect.NewRequest(&opensrev1.ListInvestigationsRequest{Limit: limit}))
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(resp.Msg)
			}
			if len(resp.Msg.Investigations) == 0 {
				a.info("No investigations yet. Start one with `opensre investigate <service>`.")
				return nil
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tCREATED\tSTATUS\tSERVICES\tTITLE")
			for _, inv := range resp.Msg.Investigations {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", inv.Id, inv.CreatedAt.AsTime().Local().Format("2006-01-02 15:04"),
					status(inv), strings.Join(inv.Services, ","), inv.Title)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().Int32Var(&limit, "limit", 20, "maximum number of investigations to list")
	return cmd
}

func status(inv *opensrev1.Investigation) string {
	return strings.ToLower(strings.TrimPrefix(inv.Status.String(), "INVESTIGATION_STATUS_"))
}

func (a *app) showCmd() *cobra.Command {
	var followFlag bool
	cmd := &cobra.Command{
		Use:   "show <investigation-id>",
		Short: "Show an investigation's steps and report",
		Long: `Show every step of an investigation, then its mitigation and report.
With --follow, keep streaming new steps until a running investigation finishes.`,
		Example: `  opensre show 7c9e6679-7425-40de-944b-e07fc1f90ae7
  opensre show 7c9e6679-7425-40de-944b-e07fc1f90ae7 --follow`,
		Args: exactArgs(1, "<investigation-id>"),
		RunE: func(cmd *cobra.Command, pos []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			resp, err := c.investigation.GetInvestigation(cmd.Context(), connect.NewRequest(&opensrev1.GetInvestigationRequest{Id: pos[0]}))
			if err != nil {
				return err
			}
			inv := resp.Msg.Investigation
			running := inv.Status == opensrev1.InvestigationStatus_INVESTIGATION_STATUS_RUNNING
			if a.json && !followFlag {
				return a.printJSON(resp.Msg)
			}
			var last int64
			for _, s := range resp.Msg.Steps {
				last = max(last, s.Seq)
			}
			if a.json {
				// --follow --json: replay everything as a stream of events.
				return a.follow(cmd.Context(), c, inv.Id, 0)
			}
			fmt.Fprintf(a.stdout, "%s\n%s · %s · %s · %s\n\n", inv.Title, inv.Source, strings.Join(inv.Services, ","),
				inv.CreatedAt.AsTime().Local().Format(time.RFC1123), status(inv))
			for _, s := range resp.Msg.Steps {
				a.printStep(s)
			}
			if running && followFlag {
				return a.follow(cmd.Context(), c, inv.Id, last)
			}
			if running {
				a.info("\nStill running. Use --follow to stream new steps.")
				return nil
			}
			a.printResult(inv)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&followFlag, "follow", "f", false, "stream new steps until the investigation finishes")
	return cmd
}

func indent(s string) string { return strings.ReplaceAll(s, "\n", "\n  ") }
