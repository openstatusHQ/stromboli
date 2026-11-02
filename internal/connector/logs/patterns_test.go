package logs

import (
	"testing"
	"time"
)

func TestTemplateGroupsVariableParts(t *testing.T) {
	a := Template(`2026-11-02T10:00:01Z error: query 8f14e45f-ceea-467a-9575-5a1b2c3d4e5f took 1532ms from 10.0.0.12:5432`)
	b := Template(`2026-11-02T10:05:44Z error: query 1c383cd3-0b7c-48a2-b3a2-23d9d2e4a111 took 87ms from 10.0.3.9:5432`)
	if a != b {
		t.Fatalf("templates differ:\n%s\n%s", a, b)
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
