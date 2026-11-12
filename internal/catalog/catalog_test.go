package catalog

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
)

const sample = `
- name: api
  owners: ["@team-api"]
  depends_on: [db, cache]
  logs: {source: loki, selector: '{service_name="api"}'}
  monitors: ["openstatus:12"]
- name: web
  owners: ["@team-web"]
  depends_on: [api]
  logs: {source: loki, selector: '{service_name="web"}'}
- name: db
  owners: ["@team-infra"]
- name: cache
  owners: ["@team-infra"]
  depends_on: [api]
`

func TestParseAndGraph(t *testing.T) {
	services, err := ParseYAML([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 4 {
		t.Fatalf("got %d services", len(services))
	}
	g := NewGraph(services)

	if got := g.Downstream("api"); !slices.Equal(got, []string{"cache", "db"}) {
		t.Errorf("downstream(api) = %v", got)
	}
	if got := g.Upstream("api"); !slices.Equal(got, []string{"cache", "web"}) {
		t.Errorf("upstream(api) = %v", got)
	}
	if got := g.BlastRadius("db"); !slices.Equal(got, []string{"api", "cache", "web"}) {
		t.Errorf("blast radius(db) = %v", got)
	}
	if s, ok := g.FindByMonitor("openstatus:12"); !ok || s.Name != "api" {
		t.Errorf("FindByMonitor = %v, %v", s, ok)
	}
	if cycles := g.Cycles(); len(cycles) != 1 {
		t.Errorf("cycles = %v", cycles)
	}
}

func TestValidate(t *testing.T) {
	services, err := ParseYAML([]byte(sample + `
- name: Bad Name
  depends_on: [ghost]
`))
	if err != nil {
		t.Fatal(err)
	}
	issues := Validate(services, ValidateOptions{Connectors: map[string]string{"loki": "loki"}})
	if !HasErrors(issues) {
		t.Fatal("expected a blocking error for the invalid name")
	}
	var msgs []string
	for _, i := range issues {
		msgs = append(msgs, i.Service+": "+i.Message)
	}
	all := strings.Join(msgs, "\n")
	for _, want := range []string{"Bad Name: must be lowercase", `unknown service "ghost"`, "dependency cycle"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing issue %q in:\n%s", want, all)
		}
	}
}

func TestYAMLRoundTrip(t *testing.T) {
	services, err := ParseYAML([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	out, err := MarshalYAML(services)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "depends_on:") {
		t.Errorf("expected snake_case keys in export:\n%s", out)
	}
	again, err := ParseYAML(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(services) {
		t.Fatalf("round trip lost services")
	}
}

func TestYAMLExportKeepsFieldOrderAndTypes(t *testing.T) {
	services, err := ParseYAML([]byte(`
- name: "123"
  owners: ["@team", "true"]
  tier: 1
`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := MarshalYAML(services)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "- name: \"123\"\n") {
		t.Errorf("name should come first and stay a string:\n%s", out)
	}
	again, err := ParseYAML(out)
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, out)
	}
	if again[0].Name != "123" || again[0].Owners[1] != "true" {
		t.Errorf("round trip changed values: %v", again[0])
	}
}

func TestValidateMetrics(t *testing.T) {
	services := []*stromboliv1.Service{
		{Name: "ok", Owners: []string{"@a"}, Metrics: &stromboliv1.MetricsConfig{Source: "fly", Selectors: []string{`app="ok"`}}},
		{Name: "no-selector", Owners: []string{"@a"}, Metrics: &stromboliv1.MetricsConfig{Source: "fly"}},
		{Name: "wrong-kind", Owners: []string{"@a"}, Metrics: &stromboliv1.MetricsConfig{Source: "loki", Selectors: []string{`app="x"`}}},
	}
	issues := Validate(services, ValidateOptions{Connectors: map[string]string{"fly": "fly", "loki": "loki"}})
	errs := map[string]string{}
	for _, i := range issues {
		if !i.Warning {
			errs[i.Service] = i.Field
		}
	}
	if len(errs) != 2 || errs["no-selector"] != "metrics.selectors" || errs["wrong-kind"] != "metrics.source" {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestValidateDeploys(t *testing.T) {
	services := []*stromboliv1.Service{
		{Name: "gh", Owners: []string{"@a"}, Deploys: &stromboliv1.DeploysConfig{Source: "gh", Repo: "o/r"}},
		{Name: "gh-no-repo", Owners: []string{"@a"}, Deploys: &stromboliv1.DeploysConfig{Source: "gh"}},
		{Name: "vercel", Owners: []string{"@a"}, Deploys: &stromboliv1.DeploysConfig{Source: "vercel", Project: "web"}},
		{Name: "vercel-no-project", Owners: []string{"@a"}, Deploys: &stromboliv1.DeploysConfig{Source: "vercel", Repo: "o/r"}},
		{Name: "wrong-kind", Owners: []string{"@a"}, Deploys: &stromboliv1.DeploysConfig{Source: "loki", Repo: "o/r"}},
	}
	issues := Validate(services, ValidateOptions{Connectors: map[string]string{"gh": "github", "vercel": "vercel", "loki": "loki"}})
	errs := map[string]string{}
	for _, i := range issues {
		if !i.Warning {
			errs[i.Service] = i.Field
		}
	}
	want := map[string]string{"gh-no-repo": "deploys.repo", "vercel-no-project": "deploys.project", "wrong-kind": "deploys.source"}
	if fmt.Sprint(errs) != fmt.Sprint(want) {
		t.Errorf("errors = %v, want %v", errs, want)
	}
}
