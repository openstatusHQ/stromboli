package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
	"github.com/openstatushq/stromboli/internal/connector"
)

func (a *app) connectorCmd() *cobra.Command {
	cmd := group("connector", "Manage connectors to logs, metrics, deploys and machines",
		"Connectors give stromboli read access to your logs (Loki, Axiom, CloudWatch),\nmetrics (Prometheus, Fly.io), deploys (GitHub, Vercel) and Fly.io machines (fly_machines). Secrets are encrypted on the server and never printed back.")
	cmd.Aliases = []string{"connectors"}
	cmd.AddCommand(a.connectorAddCmd(), a.connectorLoginCmd(), a.connectorListCmd(), a.connectorTestCmd(), a.connectorRmCmd())
	return cmd
}

func kindsHelp() string {
	var b strings.Builder
	for _, kind := range connector.KindNames() {
		req, opt := connector.Keys(kind)
		var keys []string
		for _, k := range req {
			if !connector.IsOAuthKey(kind, k) {
				keys = append(keys, k+keyMark(kind, k)+" (required)")
			}
		}
		for _, k := range opt {
			if !connector.IsOAuthKey(kind, k) {
				keys = append(keys, k+keyMark(kind, k))
			}
		}
		if _, ok := connector.OAuthServer(kind, nil); ok {
			if k := connector.OAuthUnless(kind); k != "" {
				keys = append(keys, "without "+k+": logs in with OAuth in a browser")
			} else {
				keys = append(keys, "then logs in with OAuth in a browser")
			}
		}
		fmt.Fprintf(&b, "  %-11s %s\n", kind, strings.Join(keys, ", "))
	}
	return b.String()
}

func keyMark(kind, key string) string {
	if connector.IsSecret(kind, key) {
		return "*"
	}
	return ""
}

func (a *app) connectorAddCmd() *cobra.Command {
	var replace bool
	cmd := &cobra.Command{
		Use:   "add <name> <kind> [key=value]...",
		Short: "Add a connector and test it",
		Long: `Add a connector and check that the server can reach it.

Kinds and their config keys (* marks secrets):
` + kindsHelp() + `
Avoid typing secrets on the command line, where they end up in your shell
history and the process list. Instead pass:
  key           to be prompted for the value (input is hidden)
  key=@path     to read the value from a file
  key=-         to read the value from stdin`,
		Example: `  stromboli connector add loki loki url=http://loki:3100
  stromboli connector add github github token          # prompts for the token
  stromboli connector add axiom axiom token=@axiom.token
  stromboli connector add vercel vercel team_id=my-team   # logs in in a browser
  echo "$GITHUB_TOKEN" | stromboli connector add github github token=-
  stromboli connector add cw cloudwatch region=eu-west-1 --replace`,
		Args: args(2, -1, "<name> and <kind>"),
		RunE: func(cmd *cobra.Command, pos []string) error {
			name, kind := pos[0], pos[1]
			if _, ok := connector.Kinds[kind]; !ok {
				return usagef(cmd, "unknown connector kind %q (supported: %s)", kind, strings.Join(connector.KindNames(), ", "))
			}
			cfg, err := a.readConfig(cmd.Context(), kind, pos[2:])
			if err != nil {
				return usageError{cmd: cmd.CommandPath(), err: err}
			}
			if server, ok := connector.OAuthServer(kind, cfg); ok {
				if err := a.oauthLogin(cmd.Context(), kind, server, cfg); err != nil {
					return err
				}
			}
			c, err := a.api()
			if err != nil {
				return err
			}
			if _, err := c.connectors.CreateConnector(cmd.Context(), connect.NewRequest(&stromboliv1.CreateConnectorRequest{
				Name: name, Kind: kind, Config: cfg, Replace: replace,
			})); err != nil {
				if connect.CodeOf(err) == connect.CodeAlreadyExists && !replace {
					return withHint(err, "Use --replace to overwrite it.")
				}
				return err
			}
			a.info("Connector %q saved.", name)
			if err := a.testConnector(cmd, c, name); err != nil {
				return err
			}
			if !a.json {
				if slices.Contains([]string{"loki", "axiom", "cloudwatch", "gcp_logging", "vercel", "prometheus", "fly", "gcp_monitoring"}, kind) {
					a.info("Next: propose catalog entries from it with `stromboli catalog discover %s > services.yaml`", name)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&replace, "replace", false, "overwrite an existing connector with the same name")
	return cmd
}

// readConfig parses key=value arguments, resolving @file, - (stdin) and
// prompts, and warns when a secret was typed on the command line.
func (a *app) readConfig(ctx context.Context, kind string, kvs []string) (map[string]string, error) {
	cfg := map[string]string{}
	stdinUsed := false
	for _, kv := range kvs {
		k, v, hasValue := strings.Cut(kv, "=")
		if k == "" {
			return nil, fmt.Errorf("expected key=value, got %q", kv)
		}
		if _, dup := cfg[k]; dup {
			return nil, fmt.Errorf("%s is set twice", k)
		}
		secret := connector.IsSecret(kind, k)
		switch {
		case !hasValue:
			if !a.canPrompt() {
				return nil, fmt.Errorf("no value for %s: pass %s=@file or %s=- to read it from a file or stdin", k, k, k)
			}
			fmt.Fprintf(a.stderr, "%s: ", k)
			val, err := a.readSecret(ctx)
			fmt.Fprintln(a.stderr)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err != nil {
				return nil, fmt.Errorf("reading %s: %w", k, err)
			}
			v = val
		case v == "-":
			if stdinUsed {
				return nil, fmt.Errorf("only one value can be read from stdin")
			}
			stdinUsed = true
			b, err := io.ReadAll(a.stdin)
			if err != nil {
				return nil, fmt.Errorf("reading %s from stdin: %w", k, err)
			}
			v = string(b)
		case strings.HasPrefix(v, "@"):
			b, err := os.ReadFile(v[1:])
			if err != nil {
				return nil, fmt.Errorf("reading %s: %w", k, err)
			}
			v = string(b)
		case secret && v != "":
			a.warn("%s was passed on the command line and may be saved in your shell history; use %s (prompt), %s=@file or %s=- next time", k, k, k, k)
		}
		v = strings.TrimSpace(v)
		if v == "" && (secret || !hasValue) {
			return nil, fmt.Errorf("%s is empty", k)
		}
		cfg[k] = v
	}
	return cfg, nil
}

func (a *app) connectorListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List connectors",
		Long:    "List connectors with their public config. Secret values are shown as ••••.",
		Example: `  stromboli connector list
  stromboli connector list --json`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			resp, err := c.connectors.ListConnectors(cmd.Context(), connect.NewRequest(&stromboliv1.ListConnectorsRequest{}))
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(resp.Msg)
			}
			if len(resp.Msg.Connectors) == 0 {
				a.info("No connectors yet. Add one with `stromboli connector add <name> <kind> key=value...`")
				return nil
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tKIND\tCONFIG")
			for _, cn := range resp.Msg.Connectors {
				keys := make([]string, 0, len(cn.Config))
				for k := range cn.Config {
					keys = append(keys, k)
				}
				slices.Sort(keys)
				kv := make([]string, 0, len(keys))
				for _, k := range keys {
					v := cn.Config[k]
					if v == "" {
						v = "••••"
					}
					kv = append(kv, k+"="+v)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", cn.Name, cn.Kind, strings.Join(kv, " "))
			}
			return tw.Flush()
		},
	}
}

func (a *app) connectorTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "test <name>",
		Short:   "Check that the server can reach a connector",
		Example: `  stromboli connector test loki`,
		Args:    exactArgs(1, "<name>"),
		RunE: func(cmd *cobra.Command, pos []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			return a.testConnector(cmd, c, pos[0])
		},
	}
}

func (a *app) testConnector(cmd *cobra.Command, c *clients, name string) error {
	resp, err := c.connectors.TestConnector(cmd.Context(), connect.NewRequest(&stromboliv1.TestConnectorRequest{Name: name}))
	if err != nil {
		return err
	}
	if a.json {
		if err := a.printJSON(resp.Msg); err != nil {
			return err
		}
	}
	if !resp.Msg.Ok {
		return fmt.Errorf("connector %q test failed: %s\nFix the config with `stromboli connector add %s ... --replace`", name, resp.Msg.Message, name)
	}
	if !a.json {
		a.out("Connector %q is reachable.", name)
	}
	return nil
}

func (a *app) connectorRmCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove", "delete"},
		Short:   "Delete a connector",
		Long:    "Delete a connector and its stored secrets. Services whose logs or deploys use it\nlose that data source. Asks for confirmation unless --yes is given.",
		Example: `  stromboli connector rm loki
  stromboli connector rm loki --yes`,
		Args: exactArgs(1, "<name>"),
		RunE: func(cmd *cobra.Command, pos []string) error {
			name := pos[0]
			if err := a.confirm(cmd.Context(), yes, fmt.Sprintf("delete connector %q", name), fmt.Sprintf("Delete connector %q? Services using it lose that data source.", name)); err != nil {
				return err
			}
			c, err := a.api()
			if err != nil {
				return err
			}
			if _, err := c.connectors.DeleteConnector(cmd.Context(), connect.NewRequest(&stromboliv1.DeleteConnectorRequest{Name: name})); err != nil {
				return err
			}
			a.out("Connector %q deleted.", name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "delete without asking for confirmation")
	return cmd
}
