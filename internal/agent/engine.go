// Package agent runs investigations: a deterministic brief, then an LLM
// tool-use loop over read-only tools in two phases. The triage phase proposes
// a generic mitigation and publishes it right away; the investigation phase
// then looks for the root cause and ends with a report.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/encoding/protojson"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/internal/action"
	"github.com/openstatushq/opensre/internal/catalog"
	"github.com/openstatushq/opensre/internal/config"
	"github.com/openstatushq/opensre/internal/connector"
	"github.com/openstatushq/opensre/internal/llm"
	"github.com/openstatushq/opensre/internal/store"
	"github.com/openstatushq/opensre/internal/store/db"
)

const (
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// Models are the models of each LLM task. They may be the same model.
type Models struct {
	// Triage runs until a mitigation is published.
	Triage llm.Model
	// Investigate takes over the same conversation to find the root cause.
	Investigate llm.Model
}

func (m Models) ok() bool { return m.Triage != nil && m.Investigate != nil }

// Notifier publishes investigation progress, e.g. to Slack.
type Notifier interface {
	Started(ctx context.Context, inv db.Investigation) (channel, ts string, err error)
	// Mitigated is called as soon as a mitigation is proposed, before the
	// root cause is known. It may be called again if the mitigation is revised.
	Mitigated(ctx context.Context, inv db.Investigation, m *opensrev1.Mitigation) error
	Finished(ctx context.Context, inv db.Investigation, report *opensrev1.Report) error
}

type Engine struct {
	cfg        config.Config
	store      *store.Store
	connectors *connector.Registry
	models     Models
	notifier   Notifier
	actions    *action.Service
	wg         sync.WaitGroup
	baseCtx    context.Context
}

// New returns an engine. acts may be nil, in which case mitigations are
// suggestions only.
func New(baseCtx context.Context, cfg config.Config, s *store.Store, reg *connector.Registry, models Models, n Notifier, acts *action.Service) *Engine {
	return &Engine{cfg: cfg, store: s, connectors: reg, models: models, notifier: n, actions: acts, baseCtx: baseCtx}
}

// Wait blocks until running investigations finish (used on shutdown).
func (e *Engine) Wait() { e.wg.Wait() }

// StartRequest describes a new investigation.
type StartRequest struct {
	Title       string
	Source      string
	Services    []string
	Description string
	Alert       map[string]any
	// Fingerprint dedupes repeated alerts; empty disables dedupe.
	Fingerprint string
	// DedupeWindow overrides OPENSRE_DEDUPE_WINDOW for this fingerprint.
	DedupeWindow time.Duration
	// At is when the problem was observed; defaults to now.
	At time.Time
}

// Start creates an investigation and runs it in the background. When an
// investigation with the same fingerprint is running or finished recently,
// it is returned instead and reused is true.
func (e *Engine) Start(ctx context.Context, req StartRequest) (inv db.Investigation, reused bool, err error) {
	if !e.models.ok() {
		return inv, false, errors.New("no LLM is configured (OPENSRE_MODEL and the provider's API key)")
	}
	if len(req.Services) == 0 {
		return inv, false, errors.New("no service to investigate")
	}
	_, graph, err := catalog.Load(ctx, e.store.Queries)
	if err != nil {
		return inv, false, err
	}
	for _, s := range req.Services {
		if _, ok := graph.Service(s); !ok {
			return inv, false, fmt.Errorf("service %q is not in the catalog", s)
		}
	}

	if req.Fingerprint != "" {
		window := e.cfg.DedupeWindow
		if req.DedupeWindow > 0 {
			window = req.DedupeWindow
		}
		existing, err := e.store.FindOpenInvestigation(ctx, db.FindOpenInvestigationParams{
			Fingerprint:   pgtype.Text{String: req.Fingerprint, Valid: true},
			WindowMinutes: int32(window.Minutes()),
		})
		if err == nil {
			return existing, true, nil
		}
		if !store.IsNotFound(err) {
			return inv, false, err
		}
	}

	if req.At.IsZero() {
		req.At = time.Now()
	}
	if req.Alert == nil {
		req.Alert = map[string]any{}
	}
	if req.Description != "" {
		req.Alert["description"] = req.Description
	}
	req.Alert["observed_at"] = req.At.UTC().Format(time.RFC3339)
	alert, err := json.Marshal(req.Alert)
	if err != nil {
		return inv, false, err
	}
	inv, err = e.store.CreateInvestigation(ctx, db.CreateInvestigationParams{
		ID:          uuid.New(),
		Title:       req.Title,
		Source:      req.Source,
		Fingerprint: pgtype.Text{String: req.Fingerprint, Valid: req.Fingerprint != ""},
		Services:    req.Services,
		Status:      StatusRunning,
		Alert:       alert,
	})
	if err != nil {
		return inv, false, err
	}

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.run(inv, req)
	}()
	return inv, false, nil
}

func (e *Engine) run(inv db.Investigation, req StartRequest) {
	ctx, cancel := context.WithTimeout(e.baseCtx, e.cfg.InvestigationTimeout)
	defer cancel()
	log := slog.With("investigation", inv.ID, "services", inv.Services)
	log.Info("investigation started", "title", inv.Title, "source", inv.Source)

	if e.notifier != nil {
		if ch, ts, err := e.notifier.Started(ctx, inv); err != nil {
			log.Warn("notify start", "err", err)
		} else if ch != "" {
			inv.SlackChannel = pgtype.Text{String: ch, Valid: true}
			inv.SlackTs = pgtype.Text{String: ts, Valid: true}
			_ = e.store.SetInvestigationSlack(ctx, db.SetInvestigationSlackParams{ID: inv.ID, SlackChannel: inv.SlackChannel, SlackTs: inv.SlackTs})
		}
	}

	rec := &recorder{store: e.store, id: inv.ID}
	// investigate updates inv (its mitigation), which Finished then renders.
	report, err := e.investigate(ctx, rec, &inv, req)

	status, errText := StatusCompleted, ""
	var reportJSON []byte
	if err != nil {
		status, errText = StatusFailed, err.Error()
		rec.record(ctx, step{Kind: "error", Output: errText, IsError: true})
		log.Error("investigation failed", "err", err)
	} else {
		reportJSON, _ = protojson.MarshalOptions{UseProtoNames: true}.Marshal(report)
		rec.record(ctx, step{Kind: "report", Output: string(reportJSON)})
		log.Info("investigation completed", "confidence", report.Confidence)
	}
	finishCtx := context.WithoutCancel(ctx)
	if err := e.store.FinishInvestigation(finishCtx, db.FinishInvestigationParams{
		ID: inv.ID, Status: status, Report: reportJSON, Error: pgtype.Text{String: errText, Valid: errText != ""},
	}); err != nil {
		log.Error("save investigation result", "err", err)
	}
	if e.notifier != nil {
		inv.Status, inv.Error = status, pgtype.Text{String: errText, Valid: errText != ""}
		if err := e.notifier.Finished(finishCtx, inv, report); err != nil {
			log.Warn("notify finish", "err", err)
		}
	}
}

func (e *Engine) investigate(ctx context.Context, rec *recorder, inv *db.Investigation, req StartRequest) (*opensrev1.Report, error) {
	_, graph, err := catalog.Load(ctx, e.store.Queries)
	if err != nil {
		return nil, err
	}
	tc := &toolContext{engine: e, graph: graph, now: time.Now()}
	log := slog.With("investigation", inv.ID)

	// Orient: deterministic brief for every affected service. The window
	// starts 30 minutes before the problem was observed, bounded to the last
	// day (an alert that has fired for days, or a timestamp in the future,
	// must not produce a huge or inverted window).
	at := req.At
	if at.After(tc.now) {
		at = tc.now
	}
	start := at.Add(-30 * time.Minute)
	if earliest := tc.now.Add(-24 * time.Hour); start.Before(earliest) {
		start = earliest
	}
	w := Window{Start: start, End: tc.now}
	var briefs []string
	for _, svc := range inv.Services {
		start := time.Now()
		b, err := e.Brief(ctx, svc, w)
		if err != nil {
			b = fmt.Sprintf("Brief for %s unavailable: %v", svc, err)
		}
		rec.record(ctx, step{Kind: "brief", Name: svc, Output: b, IsError: err != nil, Duration: time.Since(start)})
		briefs = append(briefs, b)
	}

	alert, _ := json.MarshalIndent(json.RawMessage(inv.Alert), "", "  ")
	first := fmt.Sprintf("Investigation: %s\nSource: %s\nAffected services: %s\nCurrent time: %s\n\nAlert payload:\n```json\n%s\n```\n\n%s",
		inv.Title, inv.Source, strings.Join(inv.Services, ", "), tc.now.UTC().Format(time.RFC3339), alert, strings.Join(briefs, "\n\n---\n\n"))

	params := llm.Request{
		System:    systemPrompt,
		Tools:     toolDefs(),
		MaxTokens: 16000,
		Messages:  []llm.Message{llm.UserText(first)},
	}

	toolCalls, nudged, mitigated := 0, false, false
	for turn := 1; ; turn++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("investigation timed out after %s: %w", e.cfg.InvestigationTimeout, err)
		}
		// Triage until a mitigation is out, then the investigate model takes
		// over the same conversation.
		task, model, effort := "investigate", e.models.Investigate, e.cfg.Investigate.Effort
		if !mitigated {
			task, model, effort = "triage", e.models.Triage, e.cfg.Triage.Effort
		}
		params.Effort = effort
		start := time.Now()
		resp, err := model.Generate(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", model.ID(), err)
		}
		_ = e.store.AddInvestigationTokens(ctx, db.AddInvestigationTokensParams{
			ID: inv.ID, InputTokens: resp.Usage.TotalInput(), OutputTokens: resp.Usage.OutputTokens,
		})
		rec.record(ctx, step{
			Kind: "llm", Name: resp.Model,
			Input: map[string]any{
				"turn": turn, "task": task, "model": model.ID(), "stop_reason": resp.RawStop,
				"input_tokens": resp.Usage.InputTokens, "cache_read_tokens": resp.Usage.CacheReadTokens,
				"cache_write_tokens": resp.Usage.CacheWriteTokens, "output_tokens": resp.Usage.OutputTokens,
			},
			Output:   resp.Text(),
			Duration: time.Since(start),
		})

		switch resp.Stop {
		case llm.StopRefusal:
			return nil, errors.New("the model declined to continue this investigation")
		case llm.StopMaxTokens:
			return nil, errors.New("the model hit the output token limit")
		}
		params.Messages = append(params.Messages, resp.Message)
		uses := resp.ToolCalls()

		// Mitigations are handled first so a report in the same turn sees them.
		results := make([]llm.Part, len(uses))
		var lookups []llm.ToolCall
		var lookupIdx []int
		reportIdx := -1
		for i, u := range uses {
			switch u.Name {
			case submitMitigationTool:
				out, isErr := e.mitigate(ctx, rec, tc, inv, u)
				if !isErr {
					mitigated = true
					log.Info("mitigation proposed", "after", time.Since(tc.now).Round(time.Second))
				}
				results[i] = llm.ToolResultPart(u.ID, out, isErr)
			case submitReportTool:
				if reportIdx >= 0 {
					// Every tool call needs a result, or the next request is invalid.
					results[reportIdx] = llm.ToolResultPart(uses[reportIdx].ID, "Ignored: submit_report was called more than once in this turn.", true)
				}
				reportIdx = i
			default:
				lookups = append(lookups, u)
				lookupIdx = append(lookupIdx, i)
			}
		}
		if reportIdx >= 0 {
			u := uses[reportIdx]
			if mitigated {
				report := &opensrev1.Report{}
				if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(u.Input, report); err != nil {
					return nil, fmt.Errorf("invalid report: %w", err)
				}
				return report, nil
			}
			// Mitigate first: send the model back to phase 1.
			results[reportIdx] = llm.ToolResultPart(u.ID,
				"Rejected: call submit_mitigation first. The on-call engineer needs a way to stop user impact before the root cause; then submit the report.", true)
		}

		if len(uses) == 0 {
			if nudged {
				// The model answered in prose twice; keep its text as the summary.
				return &opensrev1.Report{Summary: resp.Text(), LikelyCause: "unknown", Confidence: "low"}, nil
			}
			nudged = true
			nudge := "Call submit_report now with your findings."
			if !mitigated {
				nudge = "Call submit_mitigation now, then submit_report."
			}
			params.Messages = append(params.Messages, llm.UserText(nudge))
			continue
		}

		if toolCalls >= e.cfg.MaxToolCalls+10 {
			return nil, fmt.Errorf("gave up after %d tool calls without a report", toolCalls)
		}
		if len(lookups) > 0 {
			for j, r := range e.runTools(ctx, rec, tc, lookups) {
				results[lookupIdx[j]] = r
			}
			toolCalls += len(lookups)
		}
		switch {
		case toolCalls >= e.cfg.MaxToolCalls:
			next := "submit_report"
			if !mitigated {
				next = "submit_mitigation and then submit_report"
			}
			results = append(results, llm.TextPart(fmt.Sprintf(
				"Tool budget reached (%d calls). Do not call any more investigation tools; call %s now with what you have.", toolCalls, next)))
		case !mitigated && toolCalls >= e.cfg.MitigationToolCalls:
			results = append(results, llm.TextPart(fmt.Sprintf(
				"%d tool calls and no mitigation yet. Users may still be affected: call submit_mitigation now with the best generic mitigation the evidence supports (or kind \"none\"), then continue the investigation.", toolCalls)))
		}
		params.Messages = append(params.Messages, llm.UserMessage(results...))
	}
}

// mitigate validates, stores and publishes a proposed mitigation. It returns
// the tool result for the model.
func (e *Engine) mitigate(ctx context.Context, rec *recorder, tc *toolContext, inv *db.Investigation, u llm.ToolCall) (string, bool) {
	m := &opensrev1.Mitigation{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(u.Input, m); err != nil {
		return "invalid mitigation: " + err.Error(), true
	}
	if !slices.Contains(mitigationKinds, m.Kind) {
		return fmt.Sprintf("invalid kind %q: use one of %s", m.Kind, strings.Join(mitigationKinds, ", ")), true
	}
	m.ActionId = "" // set by opensre only
	actionNote := e.proposeAction(ctx, tc, inv, m)
	raw, _ := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	revised := len(inv.Mitigation) > 0
	rec.record(ctx, step{Kind: "mitigation", Name: m.Kind, Output: string(raw)})
	if err := e.store.SetInvestigationMitigation(ctx, db.SetInvestigationMitigationParams{ID: inv.ID, Mitigation: raw}); err != nil {
		return "could not save the mitigation: " + err.Error(), true
	}
	inv.Mitigation = raw
	if !inv.MitigatedAt.Valid {
		inv.MitigatedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	}
	if e.notifier != nil {
		if err := e.notifier.Mitigated(context.WithoutCancel(ctx), *inv, m); err != nil {
			slog.Warn("notify mitigation", "investigation", inv.ID, "err", err)
		}
	}
	verb := "published"
	if revised {
		verb = "revised and republished"
	}
	return fmt.Sprintf("Mitigation %s to the on-call engineer.%s Now find the root cause (phase 2), then call submit_report. "+
		"If the evidence shows this mitigation will not help, call submit_mitigation again with a better one.", verb, actionNote), false
}

// proposeAction turns the mitigation's machine_action into an action waiting
// for approval. A revised mitigation supersedes the previous pending action.
// When the action is not accepted it is dropped and the mitigation stays a
// suggestion; the returned note tells the model why.
func (e *Engine) proposeAction(ctx context.Context, tc *toolContext, inv *db.Investigation, m *opensrev1.Mitigation) string {
	if e.actions != nil {
		if err := e.actions.Supersede(ctx, inv.ID); err != nil {
			slog.Warn("supersede actions", "investigation", inv.ID, "err", err)
		}
	}
	ma := m.MachineAction
	derived := false
	if (ma == nil || ma.Action == "") && m.Kind != "none" && e.actions != nil {
		// The model sometimes writes the fly machine command but forgets
		// machine_action: offer it anyway when the command is one opensre can run.
		if ma = machineActionFromCommands(m.Action); ma != nil {
			derived = true
			m.MachineAction = ma
		}
	}
	if ma == nil || ma.Action == "" {
		m.MachineAction = nil
		return ""
	}
	if m.Kind == "none" {
		m.MachineAction = nil
		return " machine_action was ignored: the mitigation kind is none."
	}
	if e.actions == nil {
		m.MachineAction = nil
		return " machine_action was ignored: this opensre instance cannot run actions, the mitigation is a suggestion."
	}
	a, err := e.actions.Propose(ctx, *inv, tc.graph, ma)
	if err != nil {
		m.MachineAction = nil
		if derived {
			// Not the model's choice: nothing to correct, the command stays a suggestion.
			slog.Info("machine action from the mitigation's command not offered", "investigation", inv.ID, "err", err)
			return ""
		}
		return fmt.Sprintf(" machine_action was not accepted (%v): the mitigation is published as a suggestion only.", err)
	}
	m.ActionId = a.ID.String()
	from := ""
	if derived {
		from = " (machine_action was filled in from your command)"
	}
	return fmt.Sprintf(" opensre will %s %s of app %s once a human approves action %s%s; you cannot run it yourself.",
		ma.Action, strings.Join(ma.MachineIds, ", "), ma.App, a.ID, from)
}

// maxToolResult bounds what a single tool call adds to the context.
const maxToolResult = 24 << 10

// runTools executes tool calls in parallel and returns their results in order.
func (e *Engine) runTools(ctx context.Context, rec *recorder, tc *toolContext, uses []llm.ToolCall) []llm.Part {
	results := make([]llm.Part, len(uses))
	var wg sync.WaitGroup
	for i, u := range uses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			out, err := e.callTool(ctx, tc, u)
			isErr := err != nil
			if isErr {
				out = "error: " + err.Error()
			}
			if len(out) > maxToolResult {
				out = cut(out, maxToolResult) + "\n… (truncated: narrow the query)"
			}
			var input any
			_ = json.Unmarshal(u.Input, &input)
			rec.record(ctx, step{Kind: "tool_call", Name: u.Name, Input: input, Output: out, IsError: isErr, Duration: time.Since(start)})
			results[i] = llm.ToolResultPart(u.ID, out, isErr)
		}()
	}
	wg.Wait()
	return results
}

func (e *Engine) callTool(ctx context.Context, tc *toolContext, u llm.ToolCall) (string, error) {
	t, ok := findTool(u.Name)
	if !ok || t.run == nil {
		return "", fmt.Errorf("unknown tool %q", u.Name)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := t.run(ctx, tc, u.Input)
	if err != nil {
		return "", err
	}
	if s, ok := out.(string); ok {
		return s, nil
	}
	b, err := json.Marshal(out)
	return string(b), err
}
