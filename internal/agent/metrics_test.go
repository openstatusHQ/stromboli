package agent

import "testing"

func TestPresetNames(t *testing.T) {
	got := presetNames("gcp_monitoring", `topic_id="checker-ams"`)
	if want := "pubsub_published_per_second, pubsub_publish_errors_per_second, pubsub_publish_latency_p99_seconds"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := presetNames("prometheus", `job="api"`); got != "none" {
		t.Errorf("got %q, want none", got)
	}
}
