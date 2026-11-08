package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/internal/report"
)

func (a *app) actionsCmd() *cobra.Command {
	cmd := group("actions", "List, approve and reject machine actions the agent proposed",
		`When an investigation proposes a mitigation it can act on (restart or stop a
Fly.io machine, for instance), it records a proposed action. Nothing runs until
a human approves it; every decision is recorded with the approver's name.`)
	cmd.Aliases = []string{"action"}
	list := a.actionsListCmd()
	// "opensre actions" with only flags lists actions.
	cmd.Flags().AddFlagSet(list.Flags())
	cmd.RunE = func(c *cobra.Command, pos []string) error {
		if len(pos) == 0 {
			return list.RunE(c, pos)
		}
		return helpOrSuggest(c, pos)
	}
	cmd.AddCommand(list, a.actionsShowCmd(), a.actionsApproveCmd(), a.actionsRejectCmd())
	return cmd
}

func (a *app) actionsListCmd() *cobra.Command {
	var investigation, statusFlag string
	var limit int32
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List proposed and past actions",
		Example: `  opensre actions list --status proposed
  opensre actions list --investigation 7c9e6679-7425-40de-944b-e07fc1f90ae7`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit <= 0 {
				return usagef(cmd, "--limit must be positive")
			}
			req := &opensrev1.ListActionsRequest{InvestigationId: investigation, Limit: limit}
			if statusFlag != "" {
				v, ok := opensrev1.ActionStatus_value["ACTION_STATUS_"+strings.ToUpper(statusFlag)]
				if !ok || v == 0 {
					return usagef(cmd, "unknown status %q (valid: %s)", statusFlag, strings.Join(actionStatuses(), ", "))
				}
				req.Status = opensrev1.ActionStatus(v)
			}
			c, err := a.api()
			if err != nil {
				return err
			}
			resp, err := c.actions.ListActions(cmd.Context(), connect.NewRequest(req))
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(resp.Msg)
			}
			if len(resp.Msg.Actions) == 0 {
				a.info("No actions.")
				return nil
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tCREATED\tSTATUS\tSERVICE\tACTION\tBY")
			for _, x := range resp.Msg.Actions {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", x.Id, x.CreatedAt.AsTime().Local().Format("2006-01-02 15:04"),
					actionStatus(x.Status), x.Service, report.MachineActionText(x.MachineAction), x.DecidedBy)
			}
			return tw.Flush()
		},
	}
	f := cmd.Flags()
	f.StringVar(&investigation, "investigation", "", "only the actions of this investigation")
	f.StringVar(&statusFlag, "status", "", "only actions with this status ("+strings.Join(actionStatuses(), ", ")+")")
	f.Int32Var(&limit, "limit", 20, "maximum number of actions to list")
	return cmd
}

func (a *app) actionsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "show <action-id>",
		Short:   "Show an action and its outcome",
		Example: `  opensre actions show 3f2b8c1e-...`,
		Args:    exactArgs(1, "<action-id>"),
		RunE: func(cmd *cobra.Command, pos []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			resp, err := c.actions.GetAction(cmd.Context(), connect.NewRequest(&opensrev1.GetActionRequest{Id: pos[0]}))
			if err != nil {
				return err
			}
			return a.printActionResult(resp.Msg, resp.Msg.Action)
		},
	}
}

func (a *app) actionsApproveCmd() *cobra.Command {
	var as, note string
	var yes bool
	cmd := &cobra.Command{
		Use:   "approve <action-id>",
		Short: "Run an action the agent proposed",
		Long: `Approve a proposed action and run it now. Your name is recorded as the
approver. You are asked to confirm unless --yes is given.`,
		Example: `  opensre actions approve 3f2b8c1e-... --as alice
  OPENSRE_USER=alice opensre actions approve 3f2b8c1e-... --yes --note "error rate spiking"`,
		Args: exactArgs(1, "<action-id>"),
		RunE: func(cmd *cobra.Command, pos []string) error {
			as, err := a.askName(cmd, as, "approvals")
			if err != nil {
				return err
			}
			c, err := a.api()
			if err != nil {
				return err
			}
			got, err := c.actions.GetAction(cmd.Context(), connect.NewRequest(&opensrev1.GetActionRequest{Id: pos[0]}))
			if err != nil {
				return err
			}
			act := got.Msg.Action
			if act.Status != opensrev1.ActionStatus_ACTION_STATUS_PROPOSED {
				if !a.json {
					a.printAction(act)
				}
				return fmt.Errorf("action is %s, not waiting for approval", actionStatus(act.Status))
			}
			text := report.MachineActionText(act.MachineAction)
			if err := a.confirm(cmd.Context(), yes, "run this action", fmt.Sprintf("Run %s on service %s?", text, act.Service)); err != nil {
				return err
			}
			a.info("Running: %s (approved by %s)…", text, as)
			resp, err := c.actions.ApproveAction(cmd.Context(), connect.NewRequest(&opensrev1.ApproveActionRequest{Id: pos[0], Approver: as, Note: note}))
			if err != nil {
				return err
			}
			return a.printActionResult(resp.Msg, resp.Msg.Action)
		},
	}
	f := cmd.Flags()
	f.StringVar(&as, "as", os.Getenv("OPENSRE_USER"), "your name, recorded as the approver (env OPENSRE_USER)")
	f.StringVar(&note, "note", "", "note recorded with the approval")
	f.BoolVarP(&yes, "yes", "y", false, "run without asking for confirmation")
	return cmd
}

func (a *app) actionsRejectCmd() *cobra.Command {
	var as, note string
	cmd := &cobra.Command{
		Use:     "reject <action-id>",
		Short:   "Reject an action the agent proposed",
		Example: `  opensre actions reject 3f2b8c1e-... --as alice --note "traffic already recovering"`,
		Args:    exactArgs(1, "<action-id>"),
		RunE: func(cmd *cobra.Command, pos []string) error {
			as, err := a.askName(cmd, as, "decisions")
			if err != nil {
				return err
			}
			c, err := a.api()
			if err != nil {
				return err
			}
			resp, err := c.actions.RejectAction(cmd.Context(), connect.NewRequest(&opensrev1.RejectActionRequest{Id: pos[0], Approver: as, Reason: note}))
			if err != nil {
				return err
			}
			return a.printActionResult(resp.Msg, resp.Msg.Action)
		},
	}
	f := cmd.Flags()
	f.StringVar(&as, "as", os.Getenv("OPENSRE_USER"), "your name, recorded with the decision (env OPENSRE_USER)")
	f.StringVar(&note, "note", "", "reason recorded with the rejection")
	return cmd
}

// askName returns the name recorded with a decision: --as (or OPENSRE_USER),
// else asked interactively.
func (a *app) askName(cmd *cobra.Command, as, what string) (string, error) {
	if as != "" {
		return as, nil
	}
	if !a.canPrompt() {
		return "", usagef(cmd, "--as <your name> is required: %s are recorded (or set OPENSRE_USER)", what)
	}
	name, err := a.prompt(cmd.Context(), "Your name (recorded with the decision): ")
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", usagef(cmd, "a name is required: %s are recorded", what)
	}
	return name, nil
}

// actionStatuses lists the values --status accepts, in lifecycle order.
func actionStatuses() []string {
	var out []string
	for i := int32(1); ; i++ {
		if _, ok := opensrev1.ActionStatus_name[i]; !ok {
			return out
		}
		out = append(out, actionStatus(opensrev1.ActionStatus(i)))
	}
}

func actionStatus(s opensrev1.ActionStatus) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "ACTION_STATUS_"))
}

// printActionResult prints an RPC response holding an action, as JSON or
// text, and fails if the action failed.
func (a *app) printActionResult(msg proto.Message, act *opensrev1.Action) error {
	if a.json {
		if err := a.printJSON(msg); err != nil {
			return err
		}
	} else {
		a.printAction(act)
	}
	if act.Status == opensrev1.ActionStatus_ACTION_STATUS_FAILED {
		return errors.New("action failed")
	}
	return nil
}

func (a *app) printAction(x *opensrev1.Action) {
	w := a.stdout
	fmt.Fprintf(w, "action %s: %s\n", x.Id, actionStatus(x.Status))
	fmt.Fprintf(w, "  %s (service %s, investigation %s)\n", report.MachineActionText(x.MachineAction), x.Service, x.InvestigationId)
	switch {
	case x.Status == opensrev1.ActionStatus_ACTION_STATUS_PROPOSED:
		fmt.Fprintf(w, "  expires %s · %s\n", x.ExpiresAt.AsTime().Local().Format(time.Kitchen), report.ApproveCommand(x.Id))
	case x.DecidedBy != "":
		fmt.Fprintf(w, "  decided by %s at %s\n", x.DecidedBy, x.DecidedAt.AsTime().Local().Format(time.RFC1123))
	}
	if x.Note != "" {
		fmt.Fprintf(w, "  note: %s\n", x.Note)
	}
	if x.Result != "" {
		fmt.Fprintf(w, "  result:\n    %s\n", strings.ReplaceAll(x.Result, "\n", "\n    "))
	}
	if x.Error != "" {
		fmt.Fprintf(w, "  error: %s\n", x.Error)
	}
}
