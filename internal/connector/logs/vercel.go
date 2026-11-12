package logs

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openstatushq/stromboli/internal/connector/mcpconn"
	"github.com/openstatushq/stromboli/internal/connector/vercel"
)

// Vercel reads a project's runtime logs through Vercel's MCP server. A
// selector is a project name or ID, optionally followed by
// environment=production or environment=preview.
type Vercel struct {
	// TeamID is the team (ID or slug) owning the projects.
	TeamID string
	MCP    *mcpconn.Client
}

const (
	// vercelPageSize is get_runtime_logs' maximum.
	vercelPageSize = 100
	// vercelMaxPages bounds a query: filtered counts stop at 500 lines.
	vercelMaxPages = 5
)

type vercelSelector struct {
	project, environment string
}

func parseVercelSelector(sel string) (vercelSelector, error) {
	f := strings.Fields(sel)
	if len(f) == 0 {
		return vercelSelector{}, fmt.Errorf("vercel: the selector needs a project")
	}
	s := vercelSelector{project: f[0]}
	for _, kv := range f[1:] {
		k, v, _ := strings.Cut(kv, "=")
		if k != "environment" || (v != "production" && v != "preview") {
			return s, fmt.Errorf("vercel: unknown selector option %q (use environment=production or environment=preview)", kv)
		}
		s.environment = v
	}
	return s, nil
}

func (v *Vercel) args(s vercelSelector, start, end time.Time) map[string]any {
	args := map[string]any{"projectId": s.project, "teamId": v.TeamID}
	if !start.IsZero() {
		args["since"] = start.UTC().Format(time.RFC3339Nano)
	}
	if !end.IsZero() {
		args["until"] = end.UTC().Format(time.RFC3339Nano)
	}
	if s.environment != "" {
		args["environment"] = s.environment
	}
	return args
}

// lines fetches up to max lines matching q, newest first, paging back in
// time from q.End.
func (v *Vercel) lines(ctx context.Context, q Query, max int) ([]Line, error) {
	s, err := parseVercelSelector(q.Selector)
	if err != nil {
		return nil, err
	}
	var re *regexp.Regexp
	if q.Filter != "" {
		if re, err = regexp.Compile(q.Filter); err != nil {
			return nil, fmt.Errorf("vercel: invalid filter: %w", err)
		}
	}
	size := vercelPageSize
	if re == nil {
		size = min(max, vercelPageSize)
	}
	var out []Line
	seen := map[string]bool{}
	end := q.End
	for range vercelMaxPages {
		args := v.args(s, q.Start, end)
		args["limit"] = size
		text, err := vercel.Text(ctx, v.MCP, "get_runtime_logs", args)
		if err != nil {
			return nil, err
		}
		page := parseVercelLogs(text)
		for _, l := range page {
			// Pages overlap at their boundary millisecond.
			key := l.Time.String() + l.Labels["request"] + l.Text
			if seen[key] || re != nil && !re.MatchString(l.Text) {
				continue
			}
			seen[key] = true
			out = append(out, l)
		}
		if len(out) >= max || len(page) < size {
			break
		}
		end = page[len(page)-1].Time
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out[:min(len(out), max)], nil
}

func (v *Vercel) Query(ctx context.Context, q Query) ([]Line, error) {
	limit := q.Limit
	if limit <= 0 || limit > maxLines {
		limit = maxLines
	}
	return v.lines(ctx, q, limit)
}

// Count counts lines exactly without a filter (grouped by level on Vercel's
// side); with one, it counts matching lines among at most 500.
func (v *Vercel) Count(ctx context.Context, q Query) (int64, error) {
	if q.Filter != "" {
		ls, err := v.lines(ctx, q, vercelPageSize*vercelMaxPages)
		return int64(len(ls)), err
	}
	s, err := parseVercelSelector(q.Selector)
	if err != nil {
		return 0, err
	}
	args := v.args(s, q.Start, q.End)
	args["group_by"] = "level"
	text, err := vercel.Text(ctx, v.MCP, "get_runtime_logs", args)
	if err != nil {
		return 0, err
	}
	return parseVercelCounts(text)
}

// Services proposes one service per project of the team.
func (v *Vercel) Services(ctx context.Context) ([]Service, error) {
	ps, err := vercel.Projects(ctx, v.MCP, v.TeamID, 100)
	if err != nil {
		return nil, err
	}
	out := make([]Service, 0, len(ps))
	for _, p := range ps {
		out = append(out, Service{Name: p.Name, Selector: p.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (v *Vercel) Check(ctx context.Context) error { return vercel.Check(ctx, v.MCP, v.TeamID) }

// vercelHeader is a log entry's heading, e.g.
//
//	### 2026-11-05T22:45:56.957Z POST / [warn/function]
var vercelHeader = regexp.MustCompile(`^### (\S+)\s*(.*?)\s*\[([^/\]]+)/([^\]]+)\]\s*$`)

// vercelLabels are copied from an entry's key=value line into Line.Labels.
var vercelLabels = map[string]string{"dep": "deployment", "branch": "branch", "environment": "environment", "region": "region", "domain": "domain"}

// parseVercelLogs reads get_runtime_logs' Markdown: per entry, a heading, a
// line of key=value fields, then the message indented by four spaces.
func parseVercelLogs(text string) []Line {
	var (
		out []Line
		cur *Line
		msg []string
	)
	flush := func() {
		if cur == nil {
			return
		}
		cur.Text = strings.TrimSpace(cur.Text + " " + strings.Join(msg, "\n"))
		out = append(out, *cur)
		cur, msg = nil, nil
	}
	for _, line := range strings.Split(text, "\n") {
		if m := vercelHeader.FindStringSubmatch(line); m != nil {
			flush()
			t, err := time.Parse(time.RFC3339Nano, m[1])
			if err != nil {
				continue
			}
			cur = &Line{Time: t.UTC(), Text: "level=" + m[3], Labels: map[string]string{"source": m[4]}}
			if m[2] != "" {
				cur.Labels["request"] = m[2]
			}
			continue
		}
		if cur == nil {
			continue
		}
		switch {
		case strings.HasPrefix(line, "    "):
			msg = append(msg, strings.TrimPrefix(line, "    "))
		case len(msg) == 0 && strings.Contains(line, "="):
			for _, kv := range strings.Fields(line) {
				if k, v, ok := strings.Cut(kv, "="); ok && vercelLabels[k] != "" {
					cur.Labels[vercelLabels[k]] = v
				}
			}
		}
	}
	flush()
	return out
}

// parseVercelCounts sums the counts of get_runtime_logs' group_by table:
//
//	| level | count |
//	|---|---|
//	| warn | 1 |
func parseVercelCounts(text string) (int64, error) {
	var n int64
	for _, line := range strings.Split(text, "\n") {
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) != 2 {
			continue
		}
		c, err := strconv.ParseInt(strings.TrimSpace(cells[1]), 10, 64)
		if err != nil {
			continue // header or separator
		}
		n += c
	}
	if n == 0 && !strings.Contains(text, "| count |") {
		return 0, fmt.Errorf("vercel: unexpected get_runtime_logs counts: %.200s", text)
	}
	return n, nil
}
