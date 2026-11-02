package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/internal/catalog"
	"github.com/openstatushq/opensre/internal/connector/logs"
	"github.com/openstatushq/opensre/internal/connector/mcp"
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
	// mcp holds the read-only tools of the configured MCP connectors, by name.
	mcp map[string]mcp.Tool
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
			}
			services, _, err := catalog.Load(ctx, tc.engine.store.Queries)
			if err != nil {
				return nil, err
			}
			out := make([]entry, 0, len(services))
			for _, s := range services {
				out = append(out, entry{s.Name, s.DependsOn, s.Tier, s.Logs != nil && s.Logs.Selector != ""})
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
			n := clamp(args.Buckets, 12, 24)
			step := w.End.Sub(w.Start) / time.Duration(n)
			type bucket struct {
				Start string `json:"start"`
				Count int64  `json:"count"`
			}
			out := make([]bucket, n)
			errs := make([]error, n)
			var wg sync.WaitGroup
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					start := w.Start.Add(time.Duration(i) * step)
					c, err := src.Count(ctx, logs.Query{Selector: sel, Filter: filter, Start: start, End: start.Add(step)})
					out[i], errs[i] = bucket{start.Format(time.RFC3339), c}, err
				}()
			}
			wg.Wait()
			if err := errors.Join(errs...); err != nil {
				return nil, err
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
			"action":     str("The exact command or steps a human runs, filled in from the alert and the brief."),
			"rationale":  str("Which symptoms point to this mitigation, in one or two sentences."),
			"verify":     str("How to tell within minutes that it worked: the metric, log pattern or check to watch."),
			"undo":       str("How to revert it if it makes things worse."),
			"risk":       map[string]any{"type": "string", "enum": []string{"safe", "reversible", "destructive"}},
			"confidence": map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
			"impact":     str("Current user impact as far as the evidence shows: who is affected, since when, ongoing or not."),
		},
		required: []string{"kind", "target", "action", "rationale", "verify", "undo", "risk", "confidence", "impact"},
	},
	{
		name: submitReportTool,
		description: "Phase 2. Submit the final root-cause report. Call it exactly once, after submit_mitigation, when you are done. " +
			"Only state what the evidence you gathered supports; say what you could not check.",
		strict: true,
		properties: map[string]any{
			"summary":              str("One or two sentences on what is happening, for someone joining the incident."),
			"likely_cause":         str("The most likely cause, or \"unknown\" if the evidence does not support one."),
			"confidence":           map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
			"evidence":             strList("Concrete observations backing the cause: counts, timestamps, log patterns, commits."),
			"ruled_out":            strList("Hypotheses checked and rejected, each with the reason."),
			"suggested_mitigation": str("The mitigation in light of the root cause: confirm the one you proposed, or say what to do instead and why, or \"none\"."),
			"fix":                  str("The long-term fix for the root cause (e.g. revert commit X and add a guard), or \"unknown\"."),
			"next_steps":           strList("What a human should check next that you could not."),
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

// toolParams converts the tool table, then the MCP tools, into API definitions.
// Built-ins come first and MCP tools are sorted, so the cached prefix only
// changes when the connected MCP servers change.
func toolParams(remote ...mcp.Tool) []anthropic.BetaToolUnionParam {
	out := make([]anthropic.BetaToolUnionParam, 0, len(tools)+len(remote))
	for _, t := range tools {
		p := anthropic.BetaToolParam{
			Name:        t.name,
			Description: anthropic.String(t.description),
			InputSchema: anthropic.BetaToolInputSchemaParam{
				Properties:  t.properties,
				Required:    t.required,
				ExtraFields: map[string]any{"additionalProperties": false},
			},
		}
		if t.strict {
			p.Strict = anthropic.Bool(true)
		}
		out = append(out, anthropic.BetaToolUnionParam{OfTool: &p})
	}
	remote = slices.Clone(remote)
	slices.SortFunc(remote, func(a, b mcp.Tool) int { return strings.Compare(a.Name, b.Name) })
	for _, t := range remote {
		out = append(out, anthropic.BetaToolUnionParam{OfTool: &anthropic.BetaToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: mcpSchema(t.InputSchema),
		}})
	}
	return out
}

// mcpSchema maps an MCP input schema (a JSON Schema object) to the API shape.
func mcpSchema(s map[string]any) anthropic.BetaToolInputSchemaParam {
	p := anthropic.BetaToolInputSchemaParam{Properties: map[string]any{}}
	extra := map[string]any{}
	for k, v := range s {
		switch k {
		case "type", "$schema":
		case "properties":
			if props, ok := v.(map[string]any); ok {
				p.Properties = props
			}
		case "required":
			if req, ok := v.([]any); ok {
				for _, r := range req {
					if name, ok := r.(string); ok {
						p.Required = append(p.Required, name)
					}
				}
			}
		default:
			extra[k] = v
		}
	}
	if len(extra) > 0 {
		p.ExtraFields = extra
	}
	return p
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
	return s[:n] + "…"
}
