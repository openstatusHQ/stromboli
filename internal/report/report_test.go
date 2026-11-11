package report

import (
	"strings"
	"testing"
	"unicode/utf8"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
)

func TestMitigationVerb(t *testing.T) {
	// The machine action is what runs: a "start" filed under kind "restart"
	// must not be announced as a restart.
	m := &stromboliv1.Mitigation{Kind: "restart", MachineAction: &stromboliv1.MachineAction{Action: "start"}}
	if got := mitigationVerb(m); got != "start" {
		t.Errorf("with a machine action: got %q, want start", got)
	}
	if got := mitigationVerb(&stromboliv1.Mitigation{Kind: "rollback"}); got != "rollback" {
		t.Errorf("without one: got %q, want rollback", got)
	}
}

func TestIsNone(t *testing.T) {
	for s, want := range map[string]bool{
		"":                            true,
		"none":                        true,
		"None needed.":                true,
		"none. This is a test alert":  true,
		"nonetheless restart the api": false,
		"Roll back deploy abc123":     false,
	} {
		if got := isNone(s); got != want {
			t.Errorf("isNone(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestShortenKeepsRunes(t *testing.T) {
	s := shorten("ééééé", 6)
	if !utf8.ValidString(s) || len(s) > 6 {
		t.Fatalf("shorten = %q (%d bytes)", s, len(s))
	}
	if got := shorten("short", 10); got != "short" {
		t.Fatalf("shorten = %q", got)
	}
}

func TestFailedText(t *testing.T) {
	got := FailedText(map[string]string{"web": "no metrics", "api": "cpu_percent: timeout"})
	want := "⚠️ Health check: could not check 2 services\n• *api*: cpu_percent: timeout\n• *web*: no metrics"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := FailedText(map[string]string{"api": "x"}); !strings.HasPrefix(got, "⚠️ Health check: could not check 1 service\n") {
		t.Errorf("got %q", got)
	}
}
