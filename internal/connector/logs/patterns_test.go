package logs

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTemplateGroupsVariableParts(t *testing.T) {
	a := Template(`2026-11-02T10:00:01Z error: query 8f14e45f-ceea-467a-9575-5a1b2c3d4e5f took 1532ms from 10.0.0.12:5432`)
	b := Template(`2026-11-02T10:05:44Z error: query 1c383cd3-0b7c-48a2-b3a2-23d9d2e4a111 took 87ms from 10.0.3.9:5432`)
	if a != b {
		t.Fatalf("templates differ:\n%s\n%s", a, b)
	}
}

func TestTemplateTruncatesOnRuneBoundary(t *testing.T) {
	// 401 bytes: the "é" straddles Template's 400-byte limit.
	got := Template(strings.Repeat("a", 399) + "é")
	if !utf8.ValidString(got) {
		t.Fatalf("Template is not valid UTF-8: %q", got)
	}
	if len(got) > 400 {
		t.Fatalf("Template = %d bytes, want at most 400", len(got))
	}
}

func TestTruncateKeepsRunes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"abc", 10, "abc"},
		{"abcdef", 3, "abc…"},
		// The 3-byte cut lands in the middle of the second "é".
		{"ééé", 3, "é…"},
	} {
		if got := truncate(tc.in, tc.n); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
		if got := truncate(tc.in, tc.n); !utf8.ValidString(got) {
			t.Errorf("truncate(%q, %d) = %q, not valid UTF-8", tc.in, tc.n, got)
		}
	}
}

func TestDiffRanksNewPatternsFirst(t *testing.T) {
	now := time.Now()
	line := func(s string) Line { return Line{Time: now, Text: s} }

	baseline := []Line{
		line("warn: cache miss for key 1"),
		line("warn: cache miss for key 2"),
		line("warn: cache miss for key 3"),
	}
	incident := []Line{
		line("warn: cache miss for key 4"),
		line("warn: cache miss for key 5"),
		line("warn: cache miss for key 6"),
		line("error: too many connections (max 100)"),
		line("error: too many connections (max 100)"),
	}
	diff := Diff(incident, baseline, 10)
	if len(diff) != 2 {
		t.Fatalf("got %d patterns", len(diff))
	}
	if !diff[0].New || diff[0].IncidentCount != 2 {
		t.Errorf("expected the new connection error first, got %+v", diff[0])
	}
	if diff[1].BaselineCount != 3 {
		t.Errorf("expected baseline count 3 for cache misses, got %+v", diff[1])
	}
}
