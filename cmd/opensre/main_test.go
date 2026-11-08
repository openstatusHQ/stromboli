package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/gen/opensre/v1/opensrev1connect"
)

// fake implements the parts of the API the tests exercise.
type fake struct {
	opensrev1connect.UnimplementedConnectorServiceHandler
	opensrev1connect.UnimplementedCatalogServiceHandler
	opensrev1connect.UnimplementedInvestigationServiceHandler

	created  *opensrev1.CreateConnectorRequest
	deleted  []string
	imported *opensrev1.ImportCatalogRequest
	existing string // services YAML returned by ExportCatalog
}

func (f *fake) ListConnectors(context.Context, *connect.Request[opensrev1.ListConnectorsRequest]) (*connect.Response[opensrev1.ListConnectorsResponse], error) {
	return connect.NewResponse(&opensrev1.ListConnectorsResponse{Connectors: []*opensrev1.Connector{
		{Name: "loki", Kind: "loki", Config: map[string]string{"url": "http://loki:3100", "password": ""}},
	}}), nil
}

func (f *fake) CreateConnector(_ context.Context, req *connect.Request[opensrev1.CreateConnectorRequest]) (*connect.Response[opensrev1.CreateConnectorResponse], error) {
	f.created = req.Msg
	return connect.NewResponse(&opensrev1.CreateConnectorResponse{}), nil
}

func (f *fake) TestConnector(context.Context, *connect.Request[opensrev1.TestConnectorRequest]) (*connect.Response[opensrev1.TestConnectorResponse], error) {
	return connect.NewResponse(&opensrev1.TestConnectorResponse{Ok: true}), nil
}

func (f *fake) DeleteConnector(_ context.Context, req *connect.Request[opensrev1.DeleteConnectorRequest]) (*connect.Response[opensrev1.DeleteConnectorResponse], error) {
	f.deleted = append(f.deleted, req.Msg.Name)
	return connect.NewResponse(&opensrev1.DeleteConnectorResponse{}), nil
}

func (f *fake) ExportCatalog(context.Context, *connect.Request[opensrev1.ExportCatalogRequest]) (*connect.Response[opensrev1.ExportCatalogResponse], error) {
	return connect.NewResponse(&opensrev1.ExportCatalogResponse{ServicesYaml: []byte(f.existing)}), nil
}

func (f *fake) ImportCatalog(_ context.Context, req *connect.Request[opensrev1.ImportCatalogRequest]) (*connect.Response[opensrev1.ImportCatalogResponse], error) {
	f.imported = req.Msg
	return connect.NewResponse(&opensrev1.ImportCatalogResponse{ServicesUpserted: 1}), nil
}

func (f *fake) StartInvestigation(context.Context, *connect.Request[opensrev1.StartInvestigationRequest]) (*connect.Response[opensrev1.StartInvestigationResponse], error) {
	return connect.NewResponse(&opensrev1.StartInvestigationResponse{Investigation: &opensrev1.Investigation{Id: "inv-1"}}), nil
}

type result struct {
	code           int
	stdout, stderr string
}

// run executes the CLI against f with the given stdin.
func run(t *testing.T, f *fake, stdin string, interactive bool, args ...string) result {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(opensrev1connect.NewConnectorServiceHandler(f))
	mux.Handle(opensrev1connect.NewCatalogServiceHandler(f))
	mux.Handle(opensrev1connect.NewInvestigationServiceHandler(f))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var stdout, stderr bytes.Buffer
	a := newApp(strings.NewReader(stdin), &stdout, &stderr)
	a.interactive = func() bool { return interactive }
	a.readSecret = func(context.Context) (string, error) { return "prompted-secret", nil }
	code := a.run(context.Background(), append([]string{"--url", srv.URL}, args...))
	return result{code, stdout.String(), stderr.String()}
}

func TestUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"investigat", "api"}, "Did you mean this?\n\tinvestigate"},
		{[]string{"catalog", "imprt"}, "Did you mean this?\n\timport"},
		{[]string{"brief"}, "missing <service>"},
		{[]string{"show", "a", "b"}, `unexpected argument "b"`},
		{[]string{"investigations", "--limit", "x"}, `invalid argument "x"`},
		{[]string{"connector", "add", "x", "nope"}, `unknown connector kind "nope"`},
		{[]string{"actions", "list", "--status", "nope"}, "valid: proposed, running, succeeded"},
		{[]string{"actions", "approve", "x"}, "OPENSRE_USER"},
	} {
		r := run(t, &fake{}, "", false, tc.args...)
		if r.code != exitUsage || !strings.Contains(r.stderr, tc.want) || !strings.Contains(r.stderr, "--help' for usage") {
			t.Errorf("%v: exit %d, stderr %q; want exit 2 and %q", tc.args, r.code, r.stderr, tc.want)
		}
	}
}

func TestHelpHasExamples(t *testing.T) {
	r := run(t, &fake{}, "", false, "connector", "add", "--help")
	if r.code != 0 || !strings.Contains(r.stdout, "Examples:") || !strings.Contains(r.stdout, "token*") {
		t.Fatalf("exit %d, stdout %q", r.code, r.stdout)
	}
}

func TestConnectorList(t *testing.T) {
	r := run(t, &fake{}, "", false, "connector", "list")
	if r.code != 0 || !strings.Contains(r.stdout, "password=•••• url=http://loki:3100") {
		t.Fatalf("exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
	r = run(t, &fake{}, "", false, "connector", "ls", "--json")
	if r.code != 0 || !strings.Contains(r.stdout, `"connectors": [`) || r.stderr != "" {
		t.Fatalf("json: exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
}

func TestConnectorAddSecrets(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		arg, stdin  string
		interactive bool
		want        string
		warns       bool
	}{
		{arg: "token=inline", want: "inline", warns: true},
		{arg: "token=@" + file, want: "from-file"},
		{arg: "token=-", stdin: "from-stdin\n", want: "from-stdin"},
		{arg: "token", interactive: true, want: "prompted-secret"},
	} {
		f := &fake{}
		r := run(t, f, tc.stdin, tc.interactive, "connector", "add", "gh", "github", tc.arg)
		if r.code != 0 || f.created == nil || f.created.Config["token"] != tc.want {
			t.Errorf("%s: exit %d, stderr %q, created %v", tc.arg, r.code, r.stderr, f.created)
			continue
		}
		if warned := strings.Contains(r.stderr, "warning: token was passed on the command line"); warned != tc.warns {
			t.Errorf("%s: warned=%v, stderr %q", tc.arg, warned, r.stderr)
		}
	}

	// No value and no terminal: fail rather than hang.
	f := &fake{}
	r := run(t, f, "", false, "connector", "add", "gh", "github", "token")
	if r.code != exitUsage || f.created != nil || !strings.Contains(r.stderr, "token=@file") {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
}

func TestConnectorRmConfirms(t *testing.T) {
	f := &fake{}
	r := run(t, f, "", false, "connector", "rm", "loki")
	if r.code != exitError || len(f.deleted) != 0 || !strings.Contains(r.stderr, "--yes") {
		t.Fatalf("non-interactive: exit %d, deleted %v, stderr %q", r.code, f.deleted, r.stderr)
	}
	r = run(t, f, "n\n", true, "connector", "rm", "loki")
	if r.code != exitError || len(f.deleted) != 0 {
		t.Fatalf("answered no: exit %d, deleted %v", r.code, f.deleted)
	}
	r = run(t, f, "y\n", true, "connector", "rm", "loki")
	if r.code != 0 || len(f.deleted) != 1 {
		t.Fatalf("answered yes: exit %d, deleted %v, stderr %q", r.code, f.deleted, r.stderr)
	}
	r = run(t, f, "", false, "connector", "rm", "loki", "-y")
	if r.code != 0 || len(f.deleted) != 2 {
		t.Fatalf("--yes: exit %d, deleted %v", r.code, f.deleted)
	}
}

func TestCatalogImportPrune(t *testing.T) {
	yaml := "- name: api\n"

	// Nothing would be deleted: no confirmation needed, even in CI.
	f := &fake{existing: "- name: api\n"}
	r := run(t, f, yaml, false, "catalog", "import", "-", "--prune")
	if r.code != 0 || f.imported == nil || !f.imported.Prune {
		t.Fatalf("no deletions: exit %d, stderr %q", r.code, r.stderr)
	}

	// Deletions without --yes are refused and name what would go.
	f = &fake{existing: "- name: api\n- name: worker\n"}
	r = run(t, f, yaml, false, "catalog", "import", "-", "--prune")
	if r.code != exitError || f.imported != nil || !strings.Contains(r.stderr, "1 service (worker)") {
		t.Fatalf("deletions: exit %d, imported %v, stderr %q", r.code, f.imported, r.stderr)
	}
	r = run(t, f, yaml, false, "catalog", "import", "-", "--prune", "--yes")
	if r.code != 0 || f.imported == nil {
		t.Fatalf("--yes: exit %d, stderr %q", r.code, r.stderr)
	}
}

func TestInvestigateNoWatchPrintsID(t *testing.T) {
	r := run(t, &fake{}, "", false, "investigate", "api", "--no-watch")
	if r.code != 0 || r.stdout != "inv-1\n" || !strings.Contains(r.stderr, "opensre show inv-1 --follow") {
		t.Fatalf("exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
}

func TestExplain(t *testing.T) {
	a := &app{url: "http://x"}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{connect.NewError(connect.CodeUnavailable, errors.New("refused")), "cannot reach the opensre server at http://x"},
		{connect.NewError(connect.CodeUnauthenticated, errors.New("bad")), "OPENSRE_API_TOKEN"},
		{withHint(connect.NewError(connect.CodeAlreadyExists, errors.New("exists")), "Use --replace."), "exists\nUse --replace."},
	} {
		if got := a.explain(tc.err); !strings.Contains(got, tc.want) {
			t.Errorf("explain(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestInvalidURL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	a := newApp(strings.NewReader(""), &stdout, &stderr)
	code := a.run(context.Background(), []string{"--url", "localhost:8080", "connector", "list"})
	if code != exitUsage || !strings.Contains(stderr.String(), "use a full URL such as http://") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
}

func TestEnvBool(t *testing.T) {
	for v, want := range map[string]bool{"": false, "0": false, "false": false, "off": false, "1": true, "true": true, "yes": true} {
		t.Setenv("OPENSRE_TEST_BOOL", v)
		if got := envBool("OPENSRE_TEST_BOOL"); got != want {
			t.Errorf("envBool(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestPromptCanceled(t *testing.T) {
	// A reader that never returns, like a terminal waiting for Enter.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	var stderr bytes.Buffer
	a := newApp(r, &bytes.Buffer{}, &stderr)
	a.interactive = func() bool { return true }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.confirm(ctx, false, "do it", "Do it?"); !errors.Is(err, context.Canceled) {
		t.Fatalf("confirm after Ctrl-C = %v, want context.Canceled", err)
	}
}
