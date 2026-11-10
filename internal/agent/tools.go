package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/internal/catalog"
	"github.com/openstatushq/opensre/internal/connector/logs"
	"github.com/openstatushq/opensre/internal/llm"
	"github.com/openstatushq/opensre/internal/policy"
	"github.com/openstatushq/opensre/internal/store/db"
)

// tool is one read-only capability exposed to the model.
type tool struct {
	name        string
	description string
	properties  map[string]any
	required    []string
	strict      bool
	run         func(ctx context.Context, tc *toolContext, input json.RawMessage) (any, error)
}

// toolContext is the per-investigation state tools work against.
type toolContext struct {
	engine *Engine
	graph  *catalog.Graph
	now    time.Time
}

const (
	submitMitigationTool = "submit_mitigation"
	submitReportTool     = "submit_report"
)

// mitigationKinds is the closed set of generic mitigations (after Google's
// "Generic Mitigations"): each stops impact without knowing the root cause.
var mitigationKinds = []string{"rollback", "restart", "scale_up", "drain", "disable_feature", "block_traffic", "failover", "degrade", "none"}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func integer(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func strList(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

var tools = []tool{
	{
		name:        "list_services",
		description: "List every service in the catalog with its direct dependencies.",
		properties:  map[string]any{},
		run: func(ctx context.Context, tc *toolContext, _ json.RawMessage) (any, error) {
			type entry struct {
				Name      string   `json:"name"`
				DependsOn []string `json:"depends_on,omitempty"`
				Tier      int32    `json:"tier,omitempty"`
				HasLogs   bool     `json:"has_logs"`
				HasMetric bool     `json:"has_metrics"`
			}
			services, _, err := catalog.Load(ctx, tc.engine.store.Queries)
			if err != nil {
				return nil, err
			}
			out := make([]entry, 0, len(services))
			for _, s := range services {
				out = append(out, entry{s.Name, s.DependsOn, s.Tier, s.Logs != nil && s.Logs.Selector != "", s.Metrics != nil && s.Metrics.Source != ""})
			}
			return out, nil
		},
	},
	{
		name:        "get_service",
		description: "Get a service's catalog entry plus who calls it (upstream), what it calls (downstream) and its blast radius.",
		properties:  map[string]any{"service": str("Service name from the catalog.")},
		required:    []string{"service"},
		run: func(ctx context.Context, tc *toolContext, in json.RawMessage) (any, error) {
			var args struct{ Service string }
			if err := json.Unmarshal(in, &args); err != nil {
				return nil, err
			}
			svc, err := tc.service(args.Service)
			if err != nil {
				return nil, err
			}
			spec, _ := catalog.MarshalSpec(svc)
			return map[string]any{
				"service":      json.RawMessage(spec),
				"upstream":     tc.graph.Upstream(svc.Name),
				"downstream":   tc.graph.Downstream(svc.Name),
				"blast_radius": tc.graph.BlastRadius(svc.Name),
			}, nil
		},
	},
	{
		name: "log_patterns",
		description: "Group a service's log lines into patterns and compare the window with the preceding window of the same length (the baseline). " +
			"Use it first: it shows which errors are new or growing. Defaults to error-like lines over the last 30 minutes.",
		properties: map[string]any{
			"service":       str("Service name."),
			"filter":        str("Optional case-insensitive RE2 regex on the line. Defaults to an error-like filter; pass \".\" for all lines."),
			"since_minutes": integer("Window start, in minutes before now. Default 30, max 1440."),
			"until_minutes": integer("Window end, in minutes before now. Default 0."),
		},
		required: []string{"service"},
		run: func(ctx context.Context, tc *toolContext, in json.RawMessage) (any, error) {
			var args logArgs
			if err := json.Unmarshal(in, &args); err != nil {
				return nil, err
			}
			src, sel, w, err := tc.logTarget(ctx, args)
			if err != nil {
				return nil, err
			}
			filter := args.Filter
			if filter == "" {
				filter = logs.ErrorFilter
			}
			a, err := analyzeLogs(ctx, src, sel, filter, w, 15)
			if err != nil {
				return nil, err
			}
			return map[string]any{"window": w, "baseline": w.Baseline(), "analysis": a}, nil
		},
	},
	{
		name:        "query_logs",
		description: "Fetch recent raw log lines for a service, newest first. Lines are redacted and truncated. Prefer log_patterns for an overview; use this to read specific lines.",
		properties: map[string]any{
			"service":       str("Service name."),
			"filter":        str("Optional case-insensitive RE2 regex on the line, e.g. \"timeout|refused\"."),
			"since_minutes": integer("Window start, in minutes before now. Default 30, max 1440."),
			"until_minutes": integer("Window end, in minutes before now. Default 0."),
			"limit":         integer("Max lines to return. Default 30, max 100."),
		},
		required: []string{"service"},
		run: func(ctx context.Context, tc *toolContext, in json.RawMessage) (any, error) {
			var args logArgs
			if err := json.Unmarshal(in, &args); err != nil {
				return nil, err
			}
			src, sel, w, err := tc.logTarget(ctx, args)
			if err != nil {
				return nil, err
			}
			limit := clamp(args.Limit, 30, 100)
			filter := args.Filter
			if filter != "" && !strings.HasPrefix(filter, "(?i)") {
				filter = "(?i)" + filter
			}
			lines, err := src.Query(ctx, logs.Query{Selector: sel, Filter: filter, Start: w.Start, End: w.End, Limit: limit})
			if err != nil {
				return nil, err
			}
			type line struct {
				Time string `json:"time"`
				Text string `json:"text"`
			}
			out := make([]line, 0, len(lines))
			for _, l := range lines {
				out = append(out, line{l.Time.Format(time.RFC3339Nano), policy.Redact(truncate(l.Text, 500))})
			}
			return map[string]any{"window": w, "lines": out}, nil
		},
	},
	{
		name:        "log_timeline",
		description: "Count a service's matching log lines per time bucket, to see when a problem started or stopped.",
		properties: map[string]any{
			"service":       str("Service name."),
			"filter":        str("Optional case-insensitive RE2 regex. Defaults to an error-like filter."),
			"since_minutes": integer("Window start, in minutes before now. Default 60, max 1440."),
			"buckets":       integer("Number of buckets. Default 12, max 24."),
		},
		required: []string{"service"},
		run: func(ctx context.Context, tc *toolContext, in json.RawMessage) (any, error) {
			var args logArgs
			if err := json.Unmarshal(in, &args); err != nil {
				return nil, err
			}
			if args.SinceMinutes == 0 {
				args.SinceMinutes = 60
			}
			args.UntilMinutes = 0
			src, sel, w, err := tc.logTarget(ctx, args)
			if err != nil {
				return nil, err
			}
			filter := args.Filter
			if filter == "" {
				filter = logs.ErrorFilter
			}
			q := logs.Query{Selector: sel, Filter: filter, Start: w.Start, End: w.End}
			buckets, step, err := logs.Timeline(ctx, src, q, clamp(args.Buckets, 12, 24))
			if err != nil {
				return nil, err
			}
			type bucket struct {
				Start string `json:"start"`
				Count int64  `json:"count"`
			}
			out := make([]bucket, len(buckets))
			for i, b := range buckets {
				out[i] = bucket{b.Start.Format(time.RFC3339), b.Count}
			}
			return map[string]any{"bucket_size": step.String(), "buckets": out}, nil
		},
	},
	{
		name:        "recent_deploys",
		description: "List recent deploys (or commits, when no deploy workflow is configured) for a service.",
		properties: map[string]any{
			"service":       str("Service name."),
			"since_minutes": integer("How far back to look. Default 240, max 10080."),
		},
		required: []string{"service"},
		run: func(ctx context.Context, tc *toolContext, in json.RawMessage) (any, error) {
			var args struct {
				Service      string `json:"service"`
				SinceMinutes int    `json:"since_minutes"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return nil, err
			}
			svc, err := tc.service(args.Service)
			if err != nil {
				return nil, err
			}
			if svc.Deploys == nil || svc.Deploys.Source == "" {
				return nil, fmt.Errorf("service %q has no deploy source configured", svc.Name)
			}
			since := tc.now.Add(-time.Duration(clamp(args.SinceMinutes, 240, 10080)) * time.Minute)
			return tc.engine.recentDeploys(ctx, svc, since)
		},
	},
	{
		name:        "get_commit",
		description: "Get a commit's message, author and changed files with truncated diffs, from the service's repository.",
		properties: map[string]any{
			"service": str("Service whose repository holds the commit."),
			"sha":     str("Commit SHA (full or abbreviated)."),
		},
		required: []string{"service", "sha"},
		run: func(ctx context.Context, tc *toolContext, in json.RawMessage) (any, error) {
			var args struct{ Service, SHA string }
			if err := json.Unmarshal(in, &args); err != nil {
				return nil, err
			}
			svc, err := tc.service(args.Service)
			if err != nil {
				return nil, err
			}
			if svc.Deploys == nil || svc.Deploys.Source == "" || svc.Deploys.Repo == "" {
				return nil, fmt.Errorf("service %q has no repository configured", svc.Name)
			}
			gh, err := tc.engine.connectors.GitHub(ctx, svc.Deploys.Source)
			if err != nil {
				return nil, err
			}
			c, err := gh.Commit(ctx, svc.Deploys.Repo, args.SHA)
			if err != nil {
				return nil, err
			}
			c.Message = policy.Redact(c.Message)
			for i := range c.Files {
				c.Files[i].Patch = policy.Redact(c.Files[i].Patch)
			}
			return c, nil
		},
	},
	{
		name: "get_metrics",
		description: "Get a service's metrics over a window, compared with the preceding window of the same length (the baseline). " +
			"Each series comes with min, max, avg, last, its baseline average and 12 evenly spaced points. " +
			"Pass a preset metric (Fly.io: memory_used_percent, memory_used_bytes, swap_used_bytes, cpu_percent, cpu_throttled_seconds_per_second, " +
			"load_average, disk_used_percent, volume_used_percent, net_recv_bytes_per_second, net_sent_bytes_per_second, http_requests_per_second, " +
			"http_5xx_per_second, http_p95_seconds, concurrency, oom_killed, instances_up; see list_metrics), or a PromQL query in which $selector " +
			"expands to the service's label matchers, e.g. max by (instance) (fly_instance_memory_mem_free{$selector}).",
		properties: map[string]any{
			"service":       str("Service name."),
			"metric":        str("Preset name. Use this or query."),
			"query":         str("PromQL expression; $selector expands to the service's label matchers. Aggregate (sum/max by ...) to keep the series count low."),
			"since_minutes": integer("Window start, in minutes before now. Default 60, max 1440."),
			"until_minutes": integer("Window end, in minutes before now. Default 0."),
		},
		required: []string{"service"},
		run:      runGetMetrics,
	},
	{
		name:        "list_metrics",
		description: "List the metric presets and the raw metric names that have series for a service in the last hour, to write get_metrics queries.",
		properties:  map[string]any{"service": str("Service name.")},
		required:    []string{"service"},
		run:         runListMetrics,
	},
	{
		name:        "read_runbook",
		description: "Read a runbook by path, as listed in a service's runbooks.",
		properties:  map[string]any{"path": str("Runbook path, e.g. runbooks/api-latency.md.")},
		required:    []string{"path"},
		run: func(ctx context.Context, tc *toolContext, in json.RawMessage) (any, error) {
			var args struct{ Path string }
			if err := json.Unmarshal(in, &args); err != nil {
				return nil, err
			}
			rb, err := tc.engine.store.GetRunbook(ctx, args.Path)
			if err != nil {
				return nil, fmt.Errorf("runbook %q not found", args.Path)
			}
			return rb.Content, nil
		},
	},
	{
		name:        "search_past_investigations",
		description: "Search finished investigations by service and/or text, newest first, with their reported cause.",
		properties: map[string]any{
			"service": str("Optional service name."),
			"query":   str("Optional text to match in the title or report."),
		},
		run: func(ctx context.Context, tc *toolContext, in json.RawMessage) (any, error) {
			var args struct{ Service, Query string }
			if err := json.Unmarshal(in, &args); err != nil {
				return nil, err
			}
			rows, err := tc.engine.store.SearchInvestigations(ctx, db.SearchInvestigationsParams{Limit: 5, Service: args.Service, Query: args.Query})
			if err != nil {
				return nil, err
			}
			type hit struct {
				Date   string          `json:"date"`
				Title  string          `json:"title"`
				Report json.RawMessage `json:"report"`
			}
			out := make([]hit, 0, len(rows))
			for _, r := range rows {
				out = append(out, hit{r.CreatedAt.Time.Format(time.RFC3339), r.Title, r.Report})
			}
			return out, nil
		},
	},
	{
		name: submitMitigationTool,
		description: "Phase 1. Propose the fastest safe way to stop user impact, before you know the root cause. " +
			"It is published to the on-call engineer immediately, then you continue to the root cause. " +
			"Pick one generic mitigation: rollback (a recent deploy), restart (stuck or leaking processes), scale_up (saturation), " +
			"drain (one region, zone or instance is bad), disable_feature (a flag-guarded change), block_traffic (abusive or bad traffic), " +
			"failover (a failing dependency has a standby), degrade (shed non-critical work), or none (no safe mitigation, or no user impact). " +
			"You may call it again to revise it if new evidence changes the best mitigation.",
		strict: true,
		properties: map[string]any{
			"kind":       map[string]any{"type": "string", "enum": mitigationKinds},
			"target":     str("What it applies to: service, deploy SHA to roll back to, region, instance, flag, or dependency."),
			"action":     str("The exact command(s) a human runs, filled in from the alert and the brief. Commands only, no prose."),
			"rationale":  str("Which symptoms point to this mitigation, in one short sentence."),
			"verify":     str("How to tell within minutes that it worked, in one short sentence: the metric, log pattern or check to watch."),
			"undo":       str("How to revert it if it makes things worse: a command or one short sentence."),
			"risk":       map[string]any{"type": "string", "enum": []string{"safe", "reversible", "destructive"}},
			"confidence": map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
			"impact":     str("Current user impact in one short sentence: who is affected, since when, ongoing or not."),
			"machine_action": map[string]any{
				"type": "object",
				"description": "Only when the brief's Fly.io section says opensre can run machine actions for the service: the same mitigation as a Fly machine action, " +
					"which a human approves before opensre runs it. Omit it otherwise.",
				"properties": map[string]any{
					"service":     str("Catalog service the machines belong to."),
					"action":      map[string]any{"type": "string", "enum": catalog.MachineActions},
					"machine_ids": strList("Machine IDs from the brief's machine table (the instance label of the Fly metrics)."),
					"region":      str("clone only: region of the new machine. Defaults to the source machine's."),
				},
				"required":             []string{"service", "action", "machine_ids"},
				"additionalProperties": false,
			},
		},
		required: []string{"kind", "target", "action", "rationale", "verify", "undo", "risk", "confidence", "impact"},
	},
	{
		name: submitReportTool,
		description: "Phase 2. Submit the final root-cause report. Call it exactly once, after submit_mitigation, when you are done. " +
			"Only state what the evidence you gathered supports; say what you could not check.",
		strict: true,
		properties: map[string]any{
			"summary":              str("One short sentence on what is happening, for someone joining the incident. Do not repeat likely_cause."),
			"likely_cause":         str("The most likely cause in one or two short sentences, or \"unknown\" if the evidence does not support one."),
			"confidence":           map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
			"evidence":             strList("At most 4 concrete observations backing the cause, one line each: counts, timestamps, log patterns, commits."),
			"ruled_out":            strList("At most 4 hypotheses checked and rejected, one line each with the reason."),
			"suggested_mitigation": str("The mitigation in light of the root cause, in one short sentence: \"confirmed\" if the one you proposed stands, what to do instead and why, or \"none\"."),
			"fix":                  str("The long-term fix for the root cause in one short sentence (e.g. revert commit X and add a guard), or \"unknown\"."),
			"next_steps":           strList("At most 3 things a human should check next that you could not, one line each."),
		},
		required: []string{"summary", "likely_cause", "confidence", "evidence", "ruled_out", "suggested_mitigation", "fix", "next_steps"},
	},
}

type logArgs struct {
	Service      string `json:"service"`
	Filter       string `json:"filter"`
	SinceMinutes int    `json:"since_minutes"`
	UntilMinutes int    `json:"until_minutes"`
	Limit        int    `json:"limit"`
	Buckets      int    `json:"buckets"`
}

func (tc *toolContext) service(name string) (*opensrev1.Service, error) {
	svc, ok := tc.graph.Service(name)
	if !ok {
		return nil, fmt.Errorf("service %q is not in the catalog; call list_services", name)
	}
	return svc, nil
}

func (tc *toolContext) logTarget(ctx context.Context, args logArgs) (logs.Source, string, Window, error) {
	svc, err := tc.service(args.Service)
	if err != nil {
		return nil, "", Window{}, err
	}
	if svc.Logs == nil || svc.Logs.Source == "" || svc.Logs.Selector == "" {
		return nil, "", Window{}, fmt.Errorf("service %q has no log source configured", svc.Name)
	}
	src, err := tc.engine.connectors.Logs(ctx, svc.Logs.Source)
	if err != nil {
		return nil, "", Window{}, err
	}
	since := clamp(args.SinceMinutes, 30, 1440)
	until := max(0, min(args.UntilMinutes, since-1))
	w := Window{
		Start: tc.now.Add(-time.Duration(since) * time.Minute),
		End:   tc.now.Add(-time.Duration(until) * time.Minute),
	}
	return src, svc.Logs.Selector, w, nil
}

// toolDefs converts the tool table into provider-neutral definitions.
func toolDefs() []llm.ToolDef {
	out := make([]llm.ToolDef, 0, len(tools))
	for _, t := range tools {
		schema := map[string]any{"type": "object", "properties": t.properties, "additionalProperties": false}
		if len(t.required) > 0 {
			schema["required"] = t.required
		}
		out = append(out, llm.ToolDef{Name: t.name, Description: t.description, Schema: schema, Strict: t.strict})
	}
	return out
}

func findTool(name string) (tool, bool) {
	for _, t := range tools {
		if t.name == name {
			return t, true
		}
	}
	return tool{}, false
}

func clamp(v, def, maxV int) int {
	if v <= 0 {
		return def
	}
	return min(v, maxV)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return cut(s, n) + "…"
}

// cut returns at most the first n bytes of s without splitting a UTF-8
// character: Postgres rejects text with invalid UTF-8.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
