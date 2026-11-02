package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/openstatushq/opensre/gen/opensre/v1/opensrev1connect"
	"github.com/openstatushq/opensre/internal/api"
)

// usageError is a mistake in how a command was invoked (exit code 2).
type usageError struct {
	cmd string // command path for the "--help" hint
	err error
}

func (u usageError) Error() string { return u.err.Error() }
func (u usageError) Unwrap() error { return u.err }

func usagef(cmd *cobra.Command, format string, args ...any) error {
	return usageError{cmd: cmd.CommandPath(), err: fmt.Errorf(format, args...)}
}

// args builds a positional-argument validator that reports usage errors.
func args(min, max int, names string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, a []string) error {
		switch {
		case len(a) < min:
			return usagef(cmd, "missing %s", names)
		case max >= 0 && len(a) > max:
			return usagef(cmd, "unexpected argument %q", a[max])
		}
		return nil
	}
}

func exactArgs(n int, names string) cobra.PositionalArgs { return args(n, n, names) }

var noArgs = args(0, 0, "")

// helpOrSuggest runs for commands that only group subcommands: it prints help,
// or suggests the closest subcommand for a typo.
func helpOrSuggest(cmd *cobra.Command, a []string) error {
	if len(a) == 0 {
		return cmd.Help()
	}
	msg := fmt.Sprintf("unknown command %q for %q", a[0], cmd.CommandPath())
	if s := cmd.SuggestionsFor(a[0]); len(s) > 0 {
		msg += "\n\nDid you mean this?\n\t" + strings.Join(s, "\n\t")
	}
	return usageError{cmd: cmd.CommandPath(), err: errors.New(msg)}
}

// group returns a command whose only job is to hold subcommands.
func group(use, short, long string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long,
		Args:  cobra.ArbitraryArgs,
		RunE:  helpOrSuggest,
		// Cobra only defaults this on the root command.
		SuggestionsMinimumDistance: 2,
	}
}

// info prints a status message on stderr unless --quiet or --json.
func (a *app) info(format string, args ...any) {
	if a.quiet || a.json {
		return
	}
	fmt.Fprintf(a.stderr, format+"\n", args...)
}

// warn prints a warning on stderr, even with --quiet.
func (a *app) warn(format string, args ...any) {
	fmt.Fprintf(a.stderr, "warning: "+format+"\n", args...)
}

// out prints a human result on stdout unless --quiet.
func (a *app) out(format string, args ...any) {
	if a.quiet {
		return
	}
	fmt.Fprintf(a.stdout, format+"\n", args...)
}

var jsonOpts = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}

// printJSON writes one message as indented JSON.
func (a *app) printJSON(m proto.Message) error { return a.writeJSON(m, "  ") }

// printJSONLine writes one message on a single line (NDJSON, for streams).
func (a *app) printJSONLine(m proto.Message) error { return a.writeJSON(m, "") }

func (a *app) writeJSON(m proto.Message, indent string) error {
	b, err := jsonOpts.Marshal(m)
	if err != nil {
		return err
	}
	// protojson output is deliberately unstable; normalize it.
	var buf bytes.Buffer
	if indent == "" {
		err = json.Compact(&buf, b)
	} else {
		err = json.Indent(&buf, b, "", indent)
	}
	if err != nil {
		return err
	}
	buf.WriteByte('\n')
	_, err = a.stdout.Write(buf.Bytes())
	return err
}

// confirm asks a yes/no question on stderr before doing action. With --yes it
// returns nil; when prompting is impossible it fails and says how to proceed.
func (a *app) confirm(yes bool, action, question string) error {
	if yes {
		return nil
	}
	if a.noInput || !a.interactive() {
		return fmt.Errorf("refusing to %s without confirmation: re-run with --yes", action)
	}
	fmt.Fprintf(a.stderr, "%s [y/N] ", question)
	line, err := bufio.NewReader(a.stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return errors.New("aborted")
}

func isTerminal(f any) bool {
	file, ok := f.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func readPassword(stdin io.Reader) (string, error) {
	file, ok := stdin.(*os.File)
	if !ok {
		return "", errors.New("stdin is not a terminal")
	}
	b, err := term.ReadPassword(int(file.Fd()))
	return string(b), err
}

// hinted adds a suggestion for what to do next to an error.
type hinted struct {
	err  error
	hint string
}

func (h hinted) Error() string { return h.err.Error() + "\n" + h.hint }
func (h hinted) Unwrap() error { return h.err }

func withHint(err error, format string, args ...any) error {
	return hinted{err: err, hint: fmt.Sprintf(format, args...)}
}

// explain turns errors into messages a human can act on.
func (a *app) explain(err error) string {
	if h, ok := err.(hinted); ok {
		return a.explain(h.err) + "\n" + h.hint
	}
	if u, ok := err.(usageError); ok {
		return a.explain(u.err)
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return err.Error()
	}
	msg := ce.Message()
	switch ce.Code() {
	case connect.CodeUnavailable:
		return fmt.Sprintf("cannot reach the opensre server at %s (%s)\nIs it running? Set OPENSRE_URL or --url to its address.", a.url, msg)
	case connect.CodeUnauthenticated:
		return fmt.Sprintf("the server rejected the API token (%s)\nSet OPENSRE_API_TOKEN, or --token-file, to the token the server was started with.", msg)
	case connect.CodeUnimplemented:
		return fmt.Sprintf("%s\nThe server at %s may be running an older version of opensre.", msg, a.url)
	case connect.CodeInternal, connect.CodeUnknown:
		return fmt.Sprintf("%s\nThis looks like a server problem: check the opensre server logs.", msg)
	}
	return msg
}

type clients struct {
	catalog       opensrev1connect.CatalogServiceClient
	connectors    opensrev1connect.ConnectorServiceClient
	investigation opensrev1connect.InvestigationServiceClient
}

// api returns the RPC clients, configured from --url and the API token.
func (a *app) api() (*clients, error) {
	if a.clients != nil {
		return a.clients, nil
	}
	token := os.Getenv("OPENSRE_API_TOKEN")
	if a.tokenFile != "" {
		b, err := os.ReadFile(a.tokenFile)
		if err != nil {
			return nil, fmt.Errorf("reading --token-file: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}
	url := strings.TrimRight(a.url, "/")
	opts := connect.WithInterceptors(api.BearerToken(token))
	hc := &http.Client{}
	a.clients = &clients{
		catalog:       opensrev1connect.NewCatalogServiceClient(hc, url, opts),
		connectors:    opensrev1connect.NewConnectorServiceClient(hc, url, opts),
		investigation: opensrev1connect.NewInvestigationServiceClient(hc, url, opts),
	}
	return a.clients, nil
}
