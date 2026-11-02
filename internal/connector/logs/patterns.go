package logs

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// Pattern is a group of log lines that differ only in variable parts
// (numbers, ids, addresses...).
type Pattern struct {
	Template  string    `json:"template"`
	Count     int       `json:"count"`
	Sample    string    `json:"sample"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

var normalizers = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:?\d{2})?`), "<ts>"},
	{regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`), "<uuid>"},
	{regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}(:\d+)?\b`), "<ip>"},
	{regexp.MustCompile(`(?i)\b0x[0-9a-f]+\b`), "<hex>"},
	{regexp.MustCompile(`(?i)\b[0-9a-f]{12,}\b`), "<hex>"},
	{regexp.MustCompile(`"[^"]{24,}"`), `"<str>"`},
	{regexp.MustCompile(`\b\d+(\.\d+)?(ms|s|us|µs|ns|m|h|kb|mb|gb|b)?\b`), "<n>"},
	{regexp.MustCompile(`\s+`), " "},
}

// Template normalizes a log line so that lines from the same call site group together.
func Template(line string) string {
	t := line
	if len(t) > 400 {
		t = t[:400]
	}
	for _, n := range normalizers {
		t = n.re.ReplaceAllString(t, n.repl)
	}
	return strings.TrimSpace(t)
}

// Patterns groups lines by template, most frequent first, keeping at most limit.
func Patterns(lines []Line, limit int) []Pattern {
	byTemplate := map[string]*Pattern{}
	for _, l := range lines {
		tpl := Template(l.Text)
		p, ok := byTemplate[tpl]
		if !ok {
			p = &Pattern{Template: tpl, Sample: truncate(l.Text, 300), FirstSeen: l.Time, LastSeen: l.Time}
			byTemplate[tpl] = p
		}
		p.Count++
		if l.Time.Before(p.FirstSeen) {
			p.FirstSeen = l.Time
		}
		if l.Time.After(p.LastSeen) {
			p.LastSeen = l.Time
		}
	}
	out := make([]Pattern, 0, len(byTemplate))
	for _, p := range byTemplate {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Template < out[j].Template
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// PatternDiff compares error patterns between the incident and a baseline window.
type PatternDiff struct {
	Template      string `json:"template"`
	IncidentCount int    `json:"incident_count"`
	BaselineCount int    `json:"baseline_count"`
	// New is true when the pattern did not appear in the baseline at all.
	New       bool      `json:"new"`
	Sample    string    `json:"sample"`
	FirstSeen time.Time `json:"first_seen"`
}

// Diff returns incident patterns annotated with their baseline counts,
// new and growing patterns first.
func Diff(incident, baseline []Line, limit int) []PatternDiff {
	base := map[string]int{}
	for _, p := range Patterns(baseline, 0) {
		base[p.Template] = p.Count
	}
	var out []PatternDiff
	for _, p := range Patterns(incident, 0) {
		b := base[p.Template]
		out = append(out, PatternDiff{
			Template:      p.Template,
			IncidentCount: p.Count,
			BaselineCount: b,
			New:           b == 0,
			Sample:        p.Sample,
			FirstSeen:     p.FirstSeen,
		})
	}
	score := func(d PatternDiff) float64 {
		return float64(d.IncidentCount) / float64(d.BaselineCount+1)
	}
	sort.SliceStable(out, func(i, j int) bool { return score(out[i]) > score(out[j]) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
