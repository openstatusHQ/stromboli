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

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/internal/catalog"
)

func (a *app) catalogCmd() *cobra.Command {
	cmd := group("catalog", "Manage the service catalog",
		`The service catalog lists your services, their owners, dependencies, log and
deploy sources, and runbooks. It is stored on the server; YAML is its import and
export format, so you can keep a copy in git.`)
	cmd.AddCommand(a.catalogImportCmd(), a.catalogExportCmd(), a.catalogValidateCmd(), a.catalogDiscoverCmd())
	return cmd
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
		Example: `  opensre catalog import services.yaml --runbooks runbooks/
  opensre catalog import services.yaml --dry-run
  opensre catalog import services.yaml --runbooks runbooks/ --prune --yes   # in CI
  opensre catalog discover loki | opensre catalog import -`,
		Args: exactArgs(1, "<services.yaml> (or - for stdin)"),
		RunE: func(cmd *cobra.Command, pos []string) error {
			data, err := a.readInput(pos[0])
			if err != nil {
				return err
			}
			req := &opensrev1.ImportCatalogRequest{ServicesYaml: data, Prune: prune, DryRun: dryRun}
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
func (a *app) confirmPrune(cmd *cobra.Command, c *clients, file string, req *opensrev1.ImportCatalogRequest, yes bool) error {
	services, err := catalog.ParseYAML(req.ServicesYaml)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	cur, err := c.catalog.ExportCatalog(cmd.Context(), connect.NewRequest(&opensrev1.ExportCatalogRequest{}))
	if err != nil {
		return err
	}
	existing, err := catalog.ParseYAML(cur.Msg.ServicesYaml)
	if err != nil {
		return fmt.Errorf("reading the current catalog: %w", err)
	}
	var goneServices, goneRunbooks []string
	for _, s := range existing {
		if !slices.ContainsFunc(services, func(n *opensrev1.Service) bool { return n.Name == s.Name }) {
			goneServices = append(goneServices, s.Name)
		}
	}
	for _, rb := range cur.Msg.Runbooks {
		if !slices.ContainsFunc(req.Runbooks, func(n *opensrev1.Runbook) bool { return n.Path == rb.Path }) {
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
	return a.confirm(yes, "delete "+gone, fmt.Sprintf("--prune will delete %s. Continue?", gone))
}

func (a *app) catalogExportCmd() *cobra.Command {
	var runbooks string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Print the catalog as YAML",
		Long: `Print the catalog as YAML on stdout. With --runbooks, also write the runbooks
into that directory, in the layout catalog import expects.`,
		Example: `  opensre catalog export > services.yaml
  opensre catalog export --runbooks runbooks/ > services.yaml`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			resp, err := c.catalog.ExportCatalog(cmd.Context(), connect.NewRequest(&opensrev1.ExportCatalogRequest{}))
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
		Example: `  opensre catalog validate
  opensre catalog validate --check-connectors`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			resp, err := c.catalog.ValidateCatalog(cmd.Context(), connect.NewRequest(&opensrev1.ValidateCatalogRequest{CheckConnectors: checkConnectors}))
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
	return &cobra.Command{
		Use:   "discover <log-or-metrics-connector>",
		Short: "Propose catalog entries from the services a log or metrics connector sees",
		Long: `Ask a log or metrics connector which services it can see, and print one catalog entry per
service not in the catalog yet, with its selector filled in. Add owners and
depends_on, then import the file.`,
		Example: `  opensre catalog discover loki > services.yaml
  opensre catalog import services.yaml`,
		Args: exactArgs(1, "<log-or-metrics-connector>"),
		RunE: func(cmd *cobra.Command, pos []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			resp, err := c.catalog.DiscoverServices(cmd.Context(), connect.NewRequest(&opensrev1.DiscoverServicesRequest{Connector: pos[0]}))
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(resp.Msg)
			}
			if len(resp.Msg.Candidates) == 0 {
				a.info("No new services found.")
				return nil
			}
			out, err := catalog.MarshalYAML(resp.Msg.Candidates)
			if err != nil {
				return err
			}
			if _, err := a.stdout.Write(out); err != nil {
				return err
			}
			a.info("Found %s. Add owners and depends_on, then run `opensre catalog import <file>`.", plural(int32(len(resp.Msg.Candidates)), "service"))
			return nil
		},
	}
}

// readInput reads a file, or stdin for "-".
func (a *app) readInput(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(a.stdin)
	}
	return os.ReadFile(path)
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

func (a *app) printIssues(issues []*opensrev1.ValidationIssue) {
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
