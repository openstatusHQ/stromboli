package agent

import (
	"strings"
	"testing"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
	"github.com/openstatushq/stromboli/internal/connector/metrics"
)

func TestPresetNames(t *testing.T) {
	got := presetNames(metrics.ApplicablePresets("gcp_monitoring", `topic_id="checker-ams"`))
	if want := "pubsub_published_per_second, pubsub_publish_errors_per_second, pubsub_publish_latency_p99_seconds"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := presetNames(metrics.PresetsFor("prometheus")); got != "none" {
		t.Errorf("got %q, want none", got)
	}
}

func TestToolTarget(t *testing.T) {
	svc := &stromboliv1.Service{
		Name:    "checker",
		Metrics: &stromboliv1.MetricsConfig{Source: "fly", Selectors: []string{`app="checker"`}},
		Queues: []*stromboliv1.QueueConfig{
			{Name: "checks", Source: "gcp", Role: "consumer", Subscriptions: []string{`subscription_id=~"checker-.*"`}, Topics: []string{`topic_id=~"checker-.*"`}},
			{Name: "results", Source: "gcp", Role: "producer", Topics: []string{`topic_id="results"`}},
		},
	}
	own, err := toolTarget(svc, "")
	if err != nil || own.source != "fly" || own.matchers != `app="checker"` {
		t.Fatalf("own metrics: %+v, %v", own, err)
	}

	checks, err := toolTarget(svc, "checks")
	if err != nil || checks.source != "gcp" {
		t.Fatalf("checks: %+v, %v", checks, err)
	}
	nacks, _ := metrics.FindPreset("gcp_monitoring", "pubsub_nacks")
	published, _ := metrics.FindPreset("gcp_monitoring", "pubsub_published_per_second")
	if got := checks.selectorFor(nacks); got != `subscription_id=~"checker-.*"` {
		t.Errorf("subscription preset reads %s", got)
	}
	if got := checks.selectorFor(published); got != `topic_id=~"checker-.*"` {
		t.Errorf("topic preset reads %s", got)
	}
	names := presetNames(checks.presets("gcp_monitoring"))
	if !strings.Contains(names, "pubsub_nacks") || !strings.Contains(names, "pubsub_published_per_second") || strings.Contains(names, "tasks_") {
		t.Errorf("consumer presets: %s", names)
	}
	q, err := checks.expand(`sum(rate(a{$selector}[5m])) / sum(rate(b{$topic_selector}[5m]))`)
	if err != nil || q != `sum(rate(a{subscription_id=~"checker-.*"}[5m])) / sum(rate(b{topic_id=~"checker-.*"}[5m]))` {
		t.Errorf("expand = %q, %v", q, err)
	}

	results, _ := toolTarget(svc, "results")
	if results.matchers != `topic_id="results"` {
		t.Errorf("a producer's $selector is its topics, got %q", results.matchers)
	}
	if names := presetNames(results.presets("gcp_monitoring")); strings.Contains(names, "pubsub_nacks") {
		t.Errorf("producer presets: %s", names)
	}
	if _, err := own.expand("x{$topic_selector}"); err == nil {
		t.Error("$topic_selector without topics should fail")
	}
	if _, err := toolTarget(svc, "nope"); err == nil || !strings.Contains(err.Error(), "queues: checks, results") {
		t.Errorf("unknown queue: %v", err)
	}
}
