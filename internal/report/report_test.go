package report

import (
	"testing"
	"unicode/utf8"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
)

func TestMitigationVerb(t *testing.T) {
	// The machine action is what runs: a "start" filed under kind "restart"
	// must not be announced as a restart.
	m := &opensrev1.Mitigation{Kind: "restart", MachineAction: &opensrev1.MachineAction{Action: "start"}}
	if got := mitigationVerb(m); got != "start" {
		t.Errorf("with a machine action: got %q, want start", got)
	}
	if got := mitigationVerb(&opensrev1.Mitigation{Kind: "rollback"}); got != "rollback" {
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
