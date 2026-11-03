package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/gen/opensre/v1/opensrev1connect"
	"github.com/openstatushq/opensre/internal/api"
	"github.com/openstatushq/opensre/internal/catalog"
	"github.com/openstatushq/opensre/internal/report"
)

type clients struct {
	catalog       opensrev1connect.CatalogServiceClient
	connectors    opensrev1connect.ConnectorServiceClient
	investigation opensrev1connect.InvestigationServiceClient
	actions       opensrev1connect.ActionServiceClient
}

func newClients() clients {
	url := os.Getenv("OPENSRE_URL")
	if url == "" {
		url = "http://localhost:8080"
	}
	opts := connect.WithInterceptors(api.BearerToken(os.Getenv("OPENSRE_API_TOKEN")))
	hc := &http.Client{}
	return clients{
		catalog:       opensrev1connect.NewCatalogServiceClient(hc, url, opts),
		connectors:    opensrev1connect.NewConnectorServiceClient(hc, url, opts),
		investigation: opensrev1connect.NewInvestigationServiceClient(hc, url, opts),
		actions:       opensrev1connect.NewActionServiceClient(hc, url, opts),
	}
}

func runClient(ctx context.Context, cmd string, args []string) error {
	c := newClients()
	switch cmd {
	case "catalog":
		return catalogCmd(ctx, c, args)
	case "connector":
		return connectorCmd(ctx, c, args)
	case "brief":
		return briefCmd(ctx, c, args)
	case "investigate":
		return investigateCmd(ctx, c, args)
	case "investigations":
		return listCmd(ctx, c, args)
	case "show":
		return showCmd(ctx, c, args)
	case "actions":
		return actionsCmd(ctx, c, args)
	}
	return fmt.Errorf("unknown command %q (run opensre help)", cmd)
}

func catalogCmd(ctx context.Context, c clients, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: opensre catalog import|export|validate|discover")
	}
	fs := flag.NewFlagSet("catalog", flag.ContinueOnError)
	runbooks := fs.String("runbooks", "", "directory of runbook markdown files")
	prune := fs.Bool("prune", false, "delete services and runbooks not in the file")
	dryRun := fs.Bool("dry-run", false, "validate only")
	checkConnectors := fs.Bool("check-connectors", false, "run a test query per log and metrics selector")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}

	switch args[0] {
	case "import":
		if len(pos) != 1 {
			return errors.New("usage: opensre catalog import <services.yaml> [--runbooks dir] [--prune] [--dry-run]")
		}
		data, err := os.ReadFile(pos[0])
		if err != nil {
			return err
		}
		req := &opensrev1.ImportCatalogRequest{ServicesYaml: data, Prune: *prune, DryRun: *dryRun}
		if *runbooks != "" {
			if req.Runbooks, err = readRunbooks(*runbooks); err != nil {
				return err
			}
		}
		resp, err := c.catalog.ImportCatalog(ctx, connect.NewRequest(req))
		if err != nil {
			return err
		}
		printIssues(resp.Msg.Issues)
		if *dryRun {
			fmt.Println("dry run: catalog is valid")
			return nil
		}
		fmt.Printf("imported %d services, %d runbooks; deleted %d services\n",
			resp.Msg.ServicesUpserted, resp.Msg.RunbooksUpserted, resp.Msg.ServicesDeleted)
		return nil

	case "export":
		resp, err := c.catalog.ExportCatalog(ctx, connect.NewRequest(&opensrev1.ExportCatalogRequest{}))
		if err != nil {
			return err
		}
		os.Stdout.Write(resp.Msg.ServicesYaml)
		if *runbooks != "" {
			for _, rb := range resp.Msg.Runbooks {
				path := filepath.Join(*runbooks, filepath.FromSlash(strings.TrimPrefix(rb.Path, "runbooks/")))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(path, []byte(rb.Content), 0o644); err != nil {
					return err
				}
			}
		}
		return nil

	case "validate":
		resp, err := c.catalog.ValidateCatalog(ctx, connect.NewRequest(&opensrev1.ValidateCatalogRequest{CheckConnectors: *checkConnectors}))
		if err != nil {
			return err
		}
		printIssues(resp.Msg.Issues)
		for _, i := range resp.Msg.Issues {
			if !i.Warning {
				return errors.New("catalog has errors")
			}
		}
		fmt.Println("catalog is valid")
		return nil

	case "discover":
		if len(pos) != 1 {
			return errors.New("usage: opensre catalog discover <log-or-metrics-connector>")
		}
		resp, err := c.catalog.DiscoverServices(ctx, connect.NewRequest(&opensrev1.DiscoverServicesRequest{Connector: pos[0]}))
		if err != nil {
			return err
		}
		if len(resp.Msg.Candidates) == 0 {
			fmt.Fprintln(os.Stderr, "no new services found")
			return nil
		}
		fmt.Fprintln(os.Stderr, "# Candidate services: add owners and depends_on, then `opensre catalog import`.")
		out, err := catalog.MarshalYAML(resp.Msg.Candidates)
		if err != nil {
			return err
		}
		os.Stdout.Write(out)
		return nil
	}
	return fmt.Errorf("unknown catalog command %q", args[0])
}

// readRunbooks loads *.md files; their path is "runbooks/<relative path>".
func readRunbooks(dir string) ([]*opensrev1.Runbook, error) {
	var out []*opensrev1.Runbook
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out = append(out, &opensrev1.Runbook{Path: "runbooks/" + filepath.ToSlash(rel), Content: string(content)})
		return nil
	})
	return out, err
}

func connectorCmd(ctx context.Context, c clients, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: opensre connector add|list|test|rm")
	}
	fs := flag.NewFlagSet("connector", flag.ContinueOnError)
	replace := fs.Bool("replace", false, "overwrite an existing connector")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	switch args[0] {
	case "add":
		if len(pos) < 2 {
			return errors.New("usage: opensre connector add <name> <kind> key=value... (kinds: loki, axiom, cloudwatch, prometheus, fly, fly_machines, github)")
		}
		cfg := map[string]string{}
		for _, kv := range pos[2:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return fmt.Errorf("expected key=value, got %q", kv)
			}
			cfg[k] = v
		}
		if _, err := c.connectors.CreateConnector(ctx, connect.NewRequest(&opensrev1.CreateConnectorRequest{
			Name: pos[0], Kind: pos[1], Config: cfg, Replace: *replace,
		})); err != nil {
			return err
		}
		fmt.Printf("connector %q saved\n", pos[0])
		return testConnector(ctx, c, pos[0])
	case "list":
		resp, err := c.connectors.ListConnectors(ctx, connect.NewRequest(&opensrev1.ListConnectorsRequest{}))
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tKIND\tCONFIG")
		for _, cn := range resp.Msg.Connectors {
			var kv []string
			for k, v := range cn.Config {
				if v == "" {
					v = "••••"
				}
				kv = append(kv, k+"="+v)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", cn.Name, cn.Kind, strings.Join(kv, " "))
		}
		return tw.Flush()
	case "test":
		if len(pos) != 1 {
			return errors.New("usage: opensre connector test <name>")
		}
		return testConnector(ctx, c, pos[0])
	case "rm":
		if len(pos) != 1 {
			return errors.New("usage: opensre connector rm <name>")
		}
		_, err := c.connectors.DeleteConnector(ctx, connect.NewRequest(&opensrev1.DeleteConnectorRequest{Name: pos[0]}))
		return err
	}
	return fmt.Errorf("unknown connector command %q", args[0])
}

func testConnector(ctx context.Context, c clients, name string) error {
	resp, err := c.connectors.TestConnector(ctx, connect.NewRequest(&opensrev1.TestConnectorRequest{Name: name}))
	if err != nil {
		return err
	}
	if !resp.Msg.Ok {
		return fmt.Errorf("connector %q test failed: %s", name, resp.Msg.Message)
	}
	fmt.Printf("connector %q is reachable\n", name)
	return nil
}

func briefCmd(ctx context.Context, c clients, args []string) error {
	fs := flag.NewFlagSet("brief", flag.ContinueOnError)
	since := fs.String("since", "1h", "look-back window")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: opensre brief <service> [--since 1h]")
	}
	resp, err := c.investigation.GetBrief(ctx, connect.NewRequest(&opensrev1.GetBriefRequest{Service: pos[0], Since: *since}))
	if err != nil {
		return err
	}
	fmt.Print(resp.Msg.Brief)
	return nil
}

func investigateCmd(ctx context.Context, c clients, args []string) error {
	fs := flag.NewFlagSet("investigate", flag.ContinueOnError)
	title := fs.String("title", "", "investigation title")
	desc := fs.String("description", "", "what you observed")
	noWatch := fs.Bool("no-watch", false, "return immediately")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: opensre investigate <service> [--title t] [--description d]")
	}
	resp, err := c.investigation.StartInvestigation(ctx, connect.NewRequest(&opensrev1.StartInvestigationRequest{
		Service: pos[0], Title: *title, Description: *desc,
	}))
	if err != nil {
		return err
	}
	id := resp.Msg.Investigation.Id
	fmt.Printf("investigation %s started\n", id)
	if *noWatch {
		return nil
	}
	return watch(ctx, c, id)
}

func watch(ctx context.Context, c clients, id string) error {
	stream, err := c.investigation.WatchInvestigation(ctx, connect.NewRequest(&opensrev1.WatchInvestigationRequest{Id: id}))
	if err != nil {
		return err
	}
	defer stream.Close()
	for stream.Receive() {
		switch ev := stream.Msg().Event.(type) {
		case *opensrev1.WatchInvestigationResponse_Step:
			printStep(ev.Step)
		case *opensrev1.WatchInvestigationResponse_Finished:
			printResult(ev.Finished)
		}
	}
	return stream.Err()
}

func printStep(s *opensrev1.Step) {
	d := time.Duration(s.DurationMs) * time.Millisecond
	switch s.Kind {
	case "brief":
		fmt.Printf("· built brief for %s (%s)\n", s.Name, d.Round(time.Millisecond))
	case "tool_call":
		mark := "→"
		if s.IsError {
			mark = "✗"
		}
		args, _ := s.Input.MarshalJSON()
		fmt.Printf("  %s %s %s (%s)\n", mark, s.Name, args, d.Round(time.Millisecond))
	case "llm":
		if text := strings.TrimSpace(s.Output); text != "" {
			fmt.Printf("· %s\n", indent(text))
		}
	case "mitigation":
		m := &opensrev1.Mitigation{}
		if err := protojson.Unmarshal([]byte(s.Output), m); err == nil {
			fmt.Printf("\n%s\n\n· investigating the root cause…\n", report.MitigationMarkdown(m))
		}
	case "error":
		fmt.Printf("✗ %s\n", s.Output)
	}
}

func printResult(inv *opensrev1.Investigation) {
	fmt.Println()
	if inv.Mitigation != nil {
		fmt.Println(report.MitigationMarkdown(inv.Mitigation))
		if inv.MitigatedAt != nil {
			fmt.Printf("  (proposed %s after the alert)\n", inv.MitigatedAt.AsTime().Sub(inv.CreatedAt.AsTime()).Round(time.Second))
		}
		fmt.Println()
	}
	switch {
	case inv.Report != nil:
		fmt.Println(report.Markdown(inv.Report))
	case inv.Error != "":
		fmt.Println("investigation failed:", inv.Error)
	}
	fmt.Printf("\n(%d input tokens, %d output tokens)\n", inv.InputTokens, inv.OutputTokens)
}

func listCmd(ctx context.Context, c clients, args []string) error {
	fs := flag.NewFlagSet("investigations", flag.ContinueOnError)
	limit := fs.Int("limit", 20, "max investigations")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	resp, err := c.investigation.ListInvestigations(ctx, connect.NewRequest(&opensrev1.ListInvestigationsRequest{Limit: int32(*limit)}))
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tCREATED\tSTATUS\tSERVICES\tTITLE")
	for _, inv := range resp.Msg.Investigations {
		status := strings.ToLower(strings.TrimPrefix(inv.Status.String(), "INVESTIGATION_STATUS_"))
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", inv.Id, inv.CreatedAt.AsTime().Local().Format("2006-01-02 15:04"),
			status, strings.Join(inv.Services, ","), inv.Title)
	}
	return tw.Flush()
}

func showCmd(ctx context.Context, c clients, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: opensre show <investigation-id>")
	}
	resp, err := c.investigation.GetInvestigation(ctx, connect.NewRequest(&opensrev1.GetInvestigationRequest{Id: args[0]}))
	if err != nil {
		return err
	}
	inv := resp.Msg.Investigation
	fmt.Printf("%s\n%s · %s · %s\n\n", inv.Title, inv.Source, strings.Join(inv.Services, ","), inv.CreatedAt.AsTime().Local().Format(time.RFC1123))
	for _, s := range resp.Msg.Steps {
		printStep(s)
	}
	printResult(inv)
	return nil
}

func actionsCmd(ctx context.Context, c clients, args []string) error {
	const usage = "usage: opensre actions list [--investigation id] [--status proposed] | show <id> | approve <id> --as <name> [--note n] | reject <id> --as <name> [--note reason]"
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("actions", flag.ContinueOnError)
	investigation := fs.String("investigation", "", "only the actions of this investigation")
	status := fs.String("status", "", "only actions with this status (proposed, running, succeeded, failed, rejected, expired, superseded)")
	limit := fs.Int("limit", 20, "max actions")
	as := fs.String("as", os.Getenv("OPENSRE_USER"), "your name, recorded as the approver (default $OPENSRE_USER)")
	note := fs.String("note", "", "note or reason recorded with the decision")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	switch sub {
	case "list":
		req := &opensrev1.ListActionsRequest{InvestigationId: *investigation, Limit: int32(*limit)}
		if *status != "" {
			v, ok := opensrev1.ActionStatus_value["ACTION_STATUS_"+strings.ToUpper(*status)]
			if !ok {
				return fmt.Errorf("unknown status %q", *status)
			}
			req.Status = opensrev1.ActionStatus(v)
		}
		resp, err := c.actions.ListActions(ctx, connect.NewRequest(req))
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tCREATED\tSTATUS\tSERVICE\tACTION\tBY")
		for _, a := range resp.Msg.Actions {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", a.Id, a.CreatedAt.AsTime().Local().Format("2006-01-02 15:04"),
				actionStatus(a.Status), a.Service, report.MachineActionText(a.MachineAction), a.DecidedBy)
		}
		return tw.Flush()
	case "show", "approve", "reject":
		if len(pos) != 1 {
			return errors.New(usage)
		}
	default:
		return errors.New(usage)
	}

	var a *opensrev1.Action
	switch sub {
	case "show":
		resp, err := c.actions.GetAction(ctx, connect.NewRequest(&opensrev1.GetActionRequest{Id: pos[0]}))
		if err != nil {
			return err
		}
		a = resp.Msg.Action
	case "approve":
		if *as == "" {
			return errors.New("--as <your name> is required: approvals are recorded")
		}
		got, err := c.actions.GetAction(ctx, connect.NewRequest(&opensrev1.GetActionRequest{Id: pos[0]}))
		if err != nil {
			return err
		}
		if got.Msg.Action.Status != opensrev1.ActionStatus_ACTION_STATUS_PROPOSED {
			printAction(got.Msg.Action)
			return fmt.Errorf("action is %s, not waiting for approval", actionStatus(got.Msg.Action.Status))
		}
		fmt.Printf("running: %s (approved by %s)…\n", report.MachineActionText(got.Msg.Action.MachineAction), *as)
		resp, err := c.actions.ApproveAction(ctx, connect.NewRequest(&opensrev1.ApproveActionRequest{Id: pos[0], Approver: *as, Note: *note}))
		if err != nil {
			return err
		}
		a = resp.Msg.Action
	case "reject":
		if *as == "" {
			return errors.New("--as <your name> is required: decisions are recorded")
		}
		resp, err := c.actions.RejectAction(ctx, connect.NewRequest(&opensrev1.RejectActionRequest{Id: pos[0], Approver: *as, Reason: *note}))
		if err != nil {
			return err
		}
		a = resp.Msg.Action
	}
	printAction(a)
	if a.Status == opensrev1.ActionStatus_ACTION_STATUS_FAILED {
		return errors.New("action failed")
	}
	return nil
}

func actionStatus(s opensrev1.ActionStatus) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "ACTION_STATUS_"))
}

func printAction(a *opensrev1.Action) {
	fmt.Printf("action %s: %s\n", a.Id, actionStatus(a.Status))
	fmt.Printf("  %s (service %s, investigation %s)\n", report.MachineActionText(a.MachineAction), a.Service, a.InvestigationId)
	switch {
	case a.Status == opensrev1.ActionStatus_ACTION_STATUS_PROPOSED:
		fmt.Printf("  expires %s · %s\n", a.ExpiresAt.AsTime().Local().Format(time.Kitchen), report.ApproveCommand(a.Id))
	case a.DecidedBy != "":
		fmt.Printf("  decided by %s at %s\n", a.DecidedBy, a.DecidedAt.AsTime().Local().Format(time.RFC1123))
	}
	if a.Note != "" {
		fmt.Printf("  note: %s\n", a.Note)
	}
	if a.Result != "" {
		fmt.Printf("  result:\n    %s\n", strings.ReplaceAll(a.Result, "\n", "\n    "))
	}
	if a.Error != "" {
		fmt.Printf("  error: %s\n", a.Error)
	}
}

func printIssues(issues []*opensrev1.ValidationIssue) {
	for _, i := range issues {
		level := "error"
		if i.Warning {
			level = "warning"
		}
		fmt.Fprintf(os.Stderr, "%s: %s.%s: %s\n", level, i.Service, i.Field, i.Message)
	}
}

// parse allows flags before and after positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func indent(s string) string { return strings.ReplaceAll(s, "\n", "\n  ") }
