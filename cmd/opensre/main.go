// Command opensre is the opensre server and its CLI.
//
// The CLI follows the guidelines at https://clig.dev: help and examples on
// every command, data on stdout and messages on stderr, --json for scripts,
// confirmation before destructive actions, and secrets never required on the
// command line.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const docsURL = "https://github.com/openstatusHQ/opensre"

// Exit codes.
const (
	exitOK          = 0
	exitError       = 1
	exitUsage       = 2
	exitInterrupted = 130
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		// After the first Ctrl-C, restore default handling so a second one
		// exits immediately.
		<-ctx.Done()
		stop()
	}()

	a := newApp(os.Stdin, os.Stdout, os.Stderr)
	os.Exit(a.run(ctx, os.Args[1:]))
}

// run executes the CLI and returns the process exit code.
func (a *app) run(ctx context.Context, args []string) int {
	root := a.rootCmd()
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return exitOK
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		fmt.Fprintln(a.stderr, "interrupted")
		return exitInterrupted
	}
	fmt.Fprintln(a.stderr, "error:", a.explain(err))
	var u usageError
	if errors.As(err, &u) {
		if u.cmd != "" {
			fmt.Fprintf(a.stderr, "Run '%s --help' for usage.\n", u.cmd)
		}
		return exitUsage
	}
	return exitError
}

func (a *app) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "opensre",
		Short: "An AI SRE agent that investigates incidents and proposes mitigations",
		Long: `opensre is an AI SRE agent. When an alert fires it builds a brief from your
logs, deploys and service catalog, proposes a mitigation, then investigates
the root cause.

The same binary runs the server (opensre serve) and the client commands below,
which talk to a running server.`,
		Example: `  # Point the CLI at your server
  export OPENSRE_URL=http://localhost:8080 OPENSRE_API_TOKEN=...

  # Set up a log source and the service catalog
  opensre connector add loki loki url=http://loki:3100
  opensre catalog discover loki > services.yaml
  opensre catalog import services.yaml --runbooks runbooks/

  # Investigate
  opensre brief api --since 1h
  opensre investigate api --description "5xx on /v1/monitors"`,
		Version:       buildVersion(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		RunE:          helpOrSuggest,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			level := slog.LevelInfo
			if a.debug {
				level = slog.LevelDebug
			}
			slog.SetDefault(slog.New(slog.NewTextHandler(a.stderr, &slog.HandlerOptions{Level: level})))
			return nil
		},
	}
	root.SetIn(a.stdin)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return usageError{cmd: cmd.CommandPath(), err: err}
	})
	root.SetVersionTemplate("opensre {{.Version}}\n")

	pf := root.PersistentFlags()
	pf.StringVar(&a.url, "url", envOr("OPENSRE_URL", "http://localhost:8080"), "opensre server URL (env OPENSRE_URL)")
	pf.StringVar(&a.tokenFile, "token-file", "", "read the API token from this file instead of OPENSRE_API_TOKEN")
	pf.BoolVar(&a.json, "json", false, "print machine-readable JSON on stdout")
	pf.BoolVarP(&a.quiet, "quiet", "q", false, "only print errors, warnings and requested data")
	pf.BoolVar(&a.noInput, "no-input", false, "never prompt; fail instead of asking for confirmation or secrets")
	pf.BoolVar(&a.debug, "debug", envBool("OPENSRE_DEBUG"), "print debug logs on stderr (env OPENSRE_DEBUG)")

	root.AddGroup(
		&cobra.Group{ID: "setup", Title: "Setup:"},
		&cobra.Group{ID: "investigate", Title: "Investigations:"},
		&cobra.Group{ID: "server", Title: "Server:"},
		&cobra.Group{ID: "integrations", Title: "Integrations:"},
	)
	for _, c := range []*cobra.Command{a.connectorCmd(), a.catalogCmd()} {
		c.GroupID = "setup"
		root.AddCommand(c)
	}
	for _, c := range []*cobra.Command{a.briefCmd(), a.investigateCmd(), a.investigationsCmd(), a.showCmd(), a.actionsCmd()} {
		c.GroupID = "investigate"
		root.AddCommand(c)
	}
	for _, c := range []*cobra.Command{serveCmd(), a.migrateCmd()} {
		c.GroupID = "server"
		root.AddCommand(c)
	}
	mcpCmd := a.mcpCmd()
	mcpCmd.GroupID = "integrations"
	root.AddCommand(mcpCmd)

	root.SetHelpTemplate(root.HelpTemplate() + `
Environment:
  OPENSRE_URL         server URL (default http://localhost:8080)
  OPENSRE_API_TOKEN   API token sent to the server
  OPENSRE_USER        your name, recorded when you approve or reject an action
  OPENSRE_DEBUG       set to 1 to enable debug logs

Docs: ` + docsURL + "\n")
	return root
}

func buildVersion() string {
	if version != "dev" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 12 {
			return "dev (" + s.Value[:12] + ")"
		}
	}
	return version
}

// envBool reports whether a flag-like environment variable is turned on:
// any non-empty value except 0, false, no and off.
func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// app holds the global flags and I/O streams shared by every command.
type app struct {
	stdin          io.Reader
	stdout, stderr io.Writer

	url, tokenFile              string
	json, quiet, noInput, debug bool

	// interactive reports whether we may prompt; overridable in tests.
	interactive func() bool
	// readSecret reads a line without echo; overridable in tests.
	readSecret func(context.Context) (string, error)

	clients *clients
}

func newApp(stdin io.Reader, stdout, stderr io.Writer) *app {
	a := &app{stdin: stdin, stdout: stdout, stderr: stderr}
	a.interactive = func() bool { return isTerminal(stdin) && isTerminal(stderr) }
	a.readSecret = func(ctx context.Context) (string, error) { return readPassword(ctx, stdin) }
	return a
}
