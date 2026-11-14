package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
	"github.com/openstatushq/stromboli/internal/catalog"
)

func (a *app) catalogCmd() *cobra.Command {
	cmd := group("catalog", "Manage the service catalog",
		`The service catalog lists your services, their owners, dependencies, log and
deploy sources, and runbooks. It is stored on the server; YAML is its import and
export format, so you can keep a copy in git.`)
	cmd.AddCommand(a.catalogListCmd(), a.catalogSetCmd(), a.catalogRmCmd(), a.catalogImportCmd(), a.catalogExportCmd(), a.catalogValidateCmd(), a.catalogDiscoverCmd())
	return cmd
}

func (a *app) catalogListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the services in the catalog",
		Long: `List the services in the catalog with their tier, owners, dependencies and
data sources. Use catalog export for the full entries.`,
		Example: `  stromboli catalog list
  stromboli catalog list --json`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			resp, err := c.catalog.ListServices(cmd.Context(), connect.NewRequest(&stromboliv1.ListServicesRequest{}))
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(resp.Msg)
			}
			if len(resp.Msg.Services) == 0 {
				a.info("The catalog is empty. Add services with `stromboli catalog discover <connector>`.")
				return nil
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tTIER\tOWNERS\tDEPENDS ON\tSOURCES")
			for _, s := range resp.Msg.Services {
				tier := "-"
				if s.Tier != 0 {
					tier = fmt.Sprint(s.Tier)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Name, tier, orDash(s.Owners), orDash(s.DependsOn), orDash(serviceSources(s)))
			}
			return tw.Flush()
		},
	}
}

func (a *app) catalogRmCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove", "delete"},
		Short:   "Delete a service from the catalog",
		Long: `Delete a service from the catalog. Services that depend on it then fail
catalog validate, and alerts for it no longer match. If you keep the catalog in a
file, remove the entry there too or the next import adds it back. Asks for
confirmation unless --yes is given.`,
		Example: `  stromboli catalog rm api
  stromboli catalog rm api --yes`,
		Args: exactArgs(1, "<name>"),
		RunE: func(cmd *cobra.Command, pos []string) error {
			name := pos[0]
			if err := a.confirm(cmd.Context(), yes, fmt.Sprintf("delete service %q", name), fmt.Sprintf("Delete service %q from the catalog?", name)); err != nil {
				return err
			}
			c, err := a.api()
			if err != nil {
				return err
			}
			if _, err := c.catalog.DeleteService(cmd.Context(), connect.NewRequest(&stromboliv1.DeleteServiceRequest{Name: name})); err != nil {
				return err
			}
			a.out("Service %q deleted.", name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "delete without asking for confirmation")
	return cmd
}

func (a *app) catalogSetCmd() *cobra.Command {
	var owners, addOwners, deps, addDeps []string
	var tier int32
	var description string
	cmd := &cobra.Command{
		Use:   "set <name> [flags]",
		Short: "Change the owners, dependencies, tier or description of one service",
		Long: `Change fields of one service in the catalog; the others are kept. --owner and
--depends-on replace the list (pass "" to clear it), --add-owner and
--add-depends-on add to it. If you keep the catalog in a file, make the same
change there or the next import reverts it.`,
		Example: `  stromboli catalog set web --owner @frontend
  stromboli catalog set web --add-depends-on api --add-depends-on postgres
  stromboli catalog set web --tier 1 --description "Marketing site"
  stromboli catalog set web --depends-on ""`,
		Args: exactArgs(1, "<name>"),
		RunE: func(cmd *cobra.Command, pos []string) error {
			f := cmd.Flags()
			if !slices.ContainsFunc([]string{"owner", "add-owner", "depends-on", "add-depends-on", "tier", "description"}, f.Changed) {
				return usagef(cmd, "nothing to change: pass --owner, --add-owner, --depends-on, --add-depends-on, --tier or --description")
			}
			c, err := a.api()
			if err != nil {
				return err
			}
			got, err := c.catalog.GetService(cmd.Context(), connect.NewRequest(&stromboliv1.GetServiceRequest{Name: pos[0]}))
			if err != nil {
				return err
			}
			svc := got.Msg.Service
			if f.Changed("owner") {
				svc.Owners = nonEmpty(owners)
			}
			svc.Owners = appendNew(svc.Owners, addOwners)
			if f.Changed("depends-on") {
				svc.DependsOn = nonEmpty(deps)
			}
			svc.DependsOn = appendNew(svc.DependsOn, addDeps)
			if f.Changed("tier") {
				svc.Tier = tier
			}
			if f.Changed("description") {
				svc.Description = description
			}
			resp, err := c.catalog.UpsertService(cmd.Context(), connect.NewRequest(&stromboliv1.UpsertServiceRequest{Service: svc}))
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(resp.Msg)
			}
			a.printIssues(resp.Msg.Issues)
			a.out("Service %q updated.", svc.Name)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringSliceVar(&owners, "owner", nil, "set the owners (repeatable; \"\" clears them)")
	f.StringSliceVar(&addOwners, "add-owner", nil, "add an owner (repeatable)")
	f.StringSliceVar(&deps, "depends-on", nil, "set the services it depends on (repeatable; \"\" clears them)")
	f.StringSliceVar(&addDeps, "add-depends-on", nil, "add a service it depends on (repeatable)")
	f.Int32Var(&tier, "tier", 0, "set the tier (1 is the most critical, 0 unsets it)")
	f.StringVar(&description, "description", "", "set the description")
	return cmd
}

// appendNew appends the values of add that are not in list yet.
func appendNew(list, add []string) []string {
	for _, v := range nonEmpty(add) {
		if !slices.Contains(list, v) {
			list = append(list, v)
		}
	}
	return list
}

// nonEmpty drops empty values, so --owner "" clears the list.
func nonEmpty(v []string) []string {
	return slices.DeleteFunc(slices.Clone(v), func(s string) bool { return s == "" })
}

// serviceSources names the connectors a service reads from, e.g. "logs=loki".
func serviceSources(s *stromboliv1.Service) []string {
	var out []string
	if src := s.GetLogs().GetSource(); src != "" {
		out = append(out, "logs="+src)
	}
	if src := s.GetMetrics().GetSource(); src != "" {
		out = append(out, "metrics="+src)
	}
	if src := s.GetDeploys().GetSource(); src != "" {
		out = append(out, "deploys="+src)
	}
	return out
}

func orDash(v []string) string {
	if len(v) == 0 {
		return "-"
	}
	return strings.Join(v, ",")
}

func (a *app) catalogImportCmd() *cobra.Command {
	var runbooks string
	var prune, dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "import <services.yaml>",
		Short: "Create or update services from a YAML file",
		Long: `Create or update the services in a YAML file, and upload the runbooks in
--runbooks (every *.md file, referenced as runbooks/<path>). Use - to read the
YAML from stdin.

With --prune the file becomes the source of truth: services and runbooks that
are not in it are deleted. This asks for confirmation unless --yes is given.`,
		Example: `  stromboli catalog import services.yaml --runbooks runbooks/
  stromboli catalog import services.yaml --dry-run
  stromboli catalog import services.yaml --runbooks runbooks/ --prune --yes   # in CI
  stromboli catalog discover loki | stromboli catalog import -`,
		Args: exactArgs(1, "<services.yaml> (or - for stdin)"),
		RunE: func(cmd *cobra.Command, pos []string) error {
			data, err := a.readInput(pos[0])
			if err != nil {
				return err
			}
			req := &stromboliv1.ImportCatalogRequest{ServicesYaml: data, Prune: prune, DryRun: dryRun}
			if runbooks != "" {
				if req.Runbooks, err = readRunbooks(runbooks); err != nil {
					return err
				}
			}
			c, err := a.api()
			if err != nil {
				return err
			}
			if prune && !dryRun {
				if err := a.confirmPrune(cmd, c, pos[0], req, yes); err != nil {
					return err
				}
			}
			resp, err := c.catalog.ImportCatalog(cmd.Context(), connect.NewRequest(req))
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(resp.Msg)
			}
			a.printIssues(resp.Msg.Issues)
			if dryRun {
				a.out("Dry run: the catalog is valid, nothing was changed.")
				return nil
			}
			a.out("Imported %s and %s; deleted %s.",
				plural(resp.Msg.ServicesUpserted, "service"), plural(resp.Msg.RunbooksUpserted, "runbook"), plural(resp.Msg.ServicesDeleted, "service"))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&runbooks, "runbooks", "r", "", "directory of runbook markdown files to upload")
	f.BoolVar(&prune, "prune", false, "delete services and runbooks that are not in the file")
	f.BoolVarP(&dryRun, "dry-run", "n", false, "validate only, change nothing")
	f.BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation before --prune deletes anything")
	return cmd
}

// confirmPrune lists what --prune would delete and asks before going ahead.
func (a *app) confirmPrune(cmd *cobra.Command, c *clients, file string, req *stromboliv1.ImportCatalogRequest, yes bool) error {
	services, err := catalog.ParseYAML(req.ServicesYaml)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	cur, err := c.catalog.ExportCatalog(cmd.Context(), connect.NewRequest(&stromboliv1.ExportCatalogRequest{}))
	if err != nil {
		return err
	}
	existing, err := catalog.ParseYAML(cur.Msg.ServicesYaml)
	if err != nil {
		return fmt.Errorf("reading the current catalog: %w", err)
	}
	var goneServices, goneRunbooks []string
	for _, s := range existing {
		if !slices.ContainsFunc(services, func(n *stromboliv1.Service) bool { return n.Name == s.Name }) {
			goneServices = append(goneServices, s.Name)
		}
	}
	for _, rb := range cur.Msg.Runbooks {
		if !slices.ContainsFunc(req.Runbooks, func(n *stromboliv1.Runbook) bool { return n.Path == rb.Path }) {
			goneRunbooks = append(goneRunbooks, rb.Path)
		}
	}
	if len(goneServices) == 0 && len(goneRunbooks) == 0 {
		return nil
	}
	var parts []string
	if len(goneServices) > 0 {
		parts = append(parts, fmt.Sprintf("%s (%s)", plural(int32(len(goneServices)), "service"), strings.Join(goneServices, ", ")))
	}
	if len(goneRunbooks) > 0 {
		parts = append(parts, fmt.Sprintf("%s (%s)", plural(int32(len(goneRunbooks)), "runbook"), strings.Join(goneRunbooks, ", ")))
	}
	gone := strings.Join(parts, " and ")
	return a.confirm(cmd.Context(), yes, "delete "+gone, fmt.Sprintf("--prune will delete %s. Continue?", gone))
}

func (a *app) catalogExportCmd() *cobra.Command {
	var runbooks string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Print the catalog as YAML",
		Long: `Print the catalog as YAML on stdout. With --runbooks, also write the runbooks
into that directory, in the layout catalog import expects.`,
		Example: `  stromboli catalog export > services.yaml
  stromboli catalog export --runbooks runbooks/ > services.yaml`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			resp, err := c.catalog.ExportCatalog(cmd.Context(), connect.NewRequest(&stromboliv1.ExportCatalogRequest{}))
			if err != nil {
				return err
			}
			if runbooks != "" {
				for _, rb := range resp.Msg.Runbooks {
					path := filepath.Join(runbooks, filepath.FromSlash(strings.TrimPrefix(rb.Path, "runbooks/")))
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						return err
					}
					if err := os.WriteFile(path, []byte(rb.Content), 0o644); err != nil {
						return err
					}
				}
				a.info("Wrote %s to %s.", plural(int32(len(resp.Msg.Runbooks)), "runbook"), runbooks)
			}
			if a.json {
				return a.printJSON(resp.Msg)
			}
			_, err = a.stdout.Write(resp.Msg.ServicesYaml)
			return err
		},
	}
	cmd.Flags().StringVarP(&runbooks, "runbooks", "r", "", "also write runbooks into this directory")
	return cmd
}

func (a *app) catalogValidateCmd() *cobra.Command {
	var checkConnectors bool
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Check the stored catalog for errors",
		Long: `Check the catalog stored on the server: schema, unknown dependencies, cycles
and missing runbooks. Exits with status 1 if there are errors (warnings alone
do not fail).`,
		Example: `  stromboli catalog validate
  stromboli catalog validate --check-connectors`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			resp, err := c.catalog.ValidateCatalog(cmd.Context(), connect.NewRequest(&stromboliv1.ValidateCatalogRequest{CheckConnectors: checkConnectors}))
			if err != nil {
				return err
			}
			if a.json {
				if err := a.printJSON(resp.Msg); err != nil {
					return err
				}
			} else {
				a.printIssues(resp.Msg.Issues)
			}
			if catalog.HasErrors(resp.Msg.Issues) {
				return errors.New("the catalog has errors")
			}
			if !a.json {
				a.out("The catalog is valid.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&checkConnectors, "check-connectors", false, "also run a test query for each service's log and metrics selector")
	return cmd
}

func (a *app) catalogDiscoverCmd() *cobra.Command {
	var doImport bool
	var owners []string
	cmd := &cobra.Command{
		Use:   "discover <log-or-metrics-connector>",
		Short: "Add the services a log or metrics connector sees to the catalog",
		Long: `Ask a log or metrics connector which services it can see, and propose one
catalog entry per service not in the catalog yet, with its selector filled in.

In a terminal, it lists them and asks whether to add them. When stdout is
redirected, it prints them as YAML instead, to review or keep in git before
catalog import. --import adds them without asking, e.g. in a script.

Owners and depends_on are optional; set them now with --owner, or later.`,
		Example: `  stromboli catalog discover loki
  stromboli catalog discover loki --import --owner @platform
  stromboli catalog discover loki > services.yaml`,
		Args: exactArgs(1, "<log-or-metrics-connector>"),
		RunE: func(cmd *cobra.Command, pos []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			resp, err := c.catalog.DiscoverServices(cmd.Context(), connect.NewRequest(&stromboliv1.DiscoverServicesRequest{Connector: pos[0]}))
			if err != nil {
				return err
			}
			candidates := resp.Msg.Candidates
			ask := !doImport && !a.json && a.canPrompt() && a.stdoutTerminal()
			if !doImport && !ask {
				if a.json {
					return a.printJSON(resp.Msg)
				}
				if len(candidates) == 0 {
					a.info("No new services found.")
					return nil
				}
				setOwners(candidates, owners)
				out, err := catalog.MarshalYAML(candidates)
				if err != nil {
					return err
				}
				if _, err := a.stdout.Write(out); err != nil {
					return err
				}
				a.info("Found %s. Import them with `stromboli catalog import <file>`; owners and depends_on are optional.", plural(int32(len(candidates)), "service"))
				return nil
			}
			if len(candidates) == 0 {
				a.info("No new services found.")
				return nil
			}
			a.info("Found %s: %s.", plural(int32(len(candidates)), "service"), strings.Join(serviceNames(candidates), ", "))
			if ask {
				line, err := a.prompt(cmd.Context(), "Add them to the catalog? [Y/n] ")
				if err != nil {
					return err
				}
				if l := strings.ToLower(line); l != "" && l != "y" && l != "yes" {
					return errors.New("aborted")
				}
			}
			if len(owners) == 0 && a.canPrompt() {
				line, err := a.prompt(cmd.Context(), "Owner for these services, e.g. @team (Enter to skip): ")
				if err != nil {
					return err
				}
				owners = strings.Fields(strings.ReplaceAll(line, ",", " "))
			}
			setOwners(candidates, owners)
			return a.importDiscovered(cmd, c, candidates)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&doImport, "import", false, "add the services to the catalog without asking")
	f.StringSliceVar(&owners, "owner", nil, "owner to set on every discovered service (repeatable)")
	return cmd
}

// importDiscovered adds discovered services to the catalog and points at what
// is left to fill in.
func (a *app) importDiscovered(cmd *cobra.Command, c *clients, services []*stromboliv1.Service) error {
	data, err := catalog.MarshalYAML(services)
	if err != nil {
		return err
	}
	resp, err := c.catalog.ImportCatalog(cmd.Context(), connect.NewRequest(&stromboliv1.ImportCatalogRequest{ServicesYaml: data}))
	if err != nil {
		return err
	}
	if a.json {
		return a.printJSON(resp.Msg)
	}
	// The response validates the whole catalog: only show issues about the new
	// services, and fold the expected "no owners" into one hint.
	names := serviceNames(services)
	var issues []*stromboliv1.ValidationIssue
	var ownerless []string
	for _, i := range resp.Msg.Issues {
		switch {
		case !slices.Contains(names, i.Service):
		case i.Field == "owners":
			ownerless = append(ownerless, i.Service)
		default:
			issues = append(issues, i)
		}
	}
	a.printIssues(issues)
	a.out("Added %s to the catalog.", plural(resp.Msg.ServicesUpserted, "service"))
	if len(ownerless) > 0 {
		a.info("%s no owner: %s. Set one with `stromboli catalog set <name> --owner <owner>`.",
			pluralVerb(len(ownerless), "has", "have"), strings.Join(ownerless, ", "))
	}
	return nil
}

func setOwners(services []*stromboliv1.Service, owners []string) {
	if len(owners) == 0 {
		return
	}
	for _, s := range services {
		if len(s.Owners) == 0 {
			s.Owners = owners
		}
	}
}

func serviceNames(services []*stromboliv1.Service) []string {
	names := make([]string, len(services))
	for i, s := range services {
		names[i] = s.Name
	}
	return names
}

// pluralVerb is "1 service has" or "3 services have".
func pluralVerb(n int, one, many string) string {
	if n == 1 {
		return "1 service " + one
	}
	return fmt.Sprintf("%d services %s", n, many)
}

// readInput reads a file, or stdin for "-".
func (a *app) readInput(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(a.stdin)
	}
	return os.ReadFile(path)
}

// readRunbooks loads *.md files; their path is "runbooks/<relative path>".
func readRunbooks(dir string) ([]*stromboliv1.Runbook, error) {
	var out []*stromboliv1.Runbook
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
		out = append(out, &stromboliv1.Runbook{Path: "runbooks/" + filepath.ToSlash(rel), Content: string(content)})
		return nil
	})
	return out, err
}

func (a *app) printIssues(issues []*stromboliv1.ValidationIssue) {
	for _, i := range issues {
		level := "error"
		if i.Warning {
			level = "warning"
		}
		fmt.Fprintf(a.stderr, "%s: %s.%s: %s\n", level, i.Service, i.Field, i.Message)
	}
}

func plural(n int32, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
