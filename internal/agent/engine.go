// Package agent runs investigations: a deterministic brief, then a Claude
// tool-use loop over read-only tools, then a report.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/anthropics/anthropic-sdk-go/v2/shared/constant"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/encoding/protojson"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/internal/catalog"
	"github.com/openstatushq/opensre/internal/config"
	"github.com/openstatushq/opensre/internal/connector"
	"github.com/openstatushq/opensre/internal/store"
	"github.com/openstatushq/opensre/internal/store/db"
)

const (
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// Messages is the subset of the Anthropic client the engine uses.
type Messages interface {
	New(ctx context.Context, params anthropic.BetaMessageNewParams, opts ...option.RequestOption) (*anthropic.BetaMessage, error)
}

// Notifier publishes investigation progress, e.g. to Slack.
type Notifier interface {
	Started(ctx context.Context, inv db.Investigation) (channel, ts string, err error)
	Finished(ctx context.Context, inv db.Investigation, report *opensrev1.Report) error
}

type Engine struct {
	cfg        config.Config
	store      *store.Store
	connectors *connector.Registry
	messages   Messages
	notifier   Notifier
	wg         sync.WaitGroup
	baseCtx    context.Context
}

func New(baseCtx context.Context, cfg config.Config, s *store.Store, reg *connector.Registry, msgs Messages, n Notifier) *Engine {
	return &Engine{cfg: cfg, store: s, connectors: reg, messages: msgs, notifier: n, baseCtx: baseCtx}
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
	// At is when the problem was observed; defaults to now.
	At time.Time
}

// Start creates an investigation and runs it in the background. When an
// investigation with the same fingerprint is running or finished recently,
// it is returned instead and reused is true.
func (e *Engine) Start(ctx context.Context, req StartRequest) (inv db.Investigation, reused bool, err error) {
	if e.messages == nil {
		return inv, false, errors.New("ANTHROPIC_API_KEY is not configured")
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
		existing, err := e.store.FindOpenInvestigation(ctx, db.FindOpenInvestigationParams{
			Fingerprint:   pgtype.Text{String: req.Fingerprint, Valid: true},
			WindowMinutes: int32(e.cfg.DedupeWindow.Minutes()),
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
	report, err := e.investigate(ctx, rec, inv, req)

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

// fallbackBeta enables server-side refusal fallbacks: a declined request is
// re-served by a fallback model inside the same call.
const fallbackBeta anthropic.AnthropicBeta = "server-side-fallback-2026-07-01"

func (e *Engine) investigate(ctx context.Context, rec *recorder, inv db.Investigation, req StartRequest) (*opensrev1.Report, error) {
	_, graph, err := catalog.Load(ctx, e.store.Queries)
	if err != nil {
		return nil, err
	}
	tc := &toolContext{engine: e, graph: graph, now: time.Now()}

	// Orient: deterministic brief for every affected service.
	w := Window{Start: req.At.Add(-30 * time.Minute), End: tc.now}
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

	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(e.cfg.Model),
		MaxTokens: 16000,
		System:    []anthropic.BetaTextBlockParam{{Text: systemPrompt}},
		Tools:     toolParams(),
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(first))},
		// Automatic caching: the breakpoint moves to the last block each turn,
		// so every turn re-reads the previous turns from cache.
		CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffort(e.cfg.Effort)},
		Fallbacks:    anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
		Betas:        []anthropic.AnthropicBeta{fallbackBeta},
	}

	toolCalls, nudged := 0, false
	for turn := 1; ; turn++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("investigation timed out after %s: %w", e.cfg.InvestigationTimeout, err)
		}
		start := time.Now()
		resp, err := e.messages.New(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("claude: %w", err)
		}
		_ = e.store.AddInvestigationTokens(ctx, db.AddInvestigationTokensParams{
			ID: inv.ID, InputTokens: resp.Usage.InputTokens + resp.Usage.CacheReadInputTokens + resp.Usage.CacheCreationInputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		})
		rec.record(ctx, step{
			Kind: "llm", Name: string(resp.Model),
			Input: map[string]any{
				"turn": turn, "stop_reason": resp.StopReason,
				"input_tokens": resp.Usage.InputTokens, "cache_read_tokens": resp.Usage.CacheReadInputTokens,
				"cache_write_tokens": resp.Usage.CacheCreationInputTokens, "output_tokens": resp.Usage.OutputTokens,
			},
			Output:   textOf(resp),
			Duration: time.Since(start),
		})

		switch resp.StopReason {
		case anthropic.BetaStopReasonRefusal:
			return nil, errors.New("the model declined to continue this investigation")
		case anthropic.BetaStopReasonMaxTokens:
			return nil, errors.New("the model hit the output token limit")
		}
		params.Messages = append(params.Messages, resp.ToParam())

		var uses []anthropic.BetaToolUseBlock
		for _, block := range resp.Content {
			if u, ok := block.AsAny().(anthropic.BetaToolUseBlock); ok {
				uses = append(uses, u)
			}
		}
		for _, u := range uses {
			if u.Name == submitReportTool {
				report := &opensrev1.Report{}
				if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(u.JSON.Input.Raw()), report); err != nil {
					return nil, fmt.Errorf("invalid report: %w", err)
				}
				return report, nil
			}
		}

		if len(uses) == 0 {
			if nudged {
				// The model answered in prose twice; keep its text as the summary.
				return &opensrev1.Report{Summary: textOf(resp), LikelyCause: "unknown", Confidence: "low"}, nil
			}
			nudged = true
			params.Messages = append(params.Messages, anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(
				"Call submit_report now with your findings.")))
			continue
		}

		if toolCalls >= e.cfg.MaxToolCalls+10 {
			return nil, fmt.Errorf("gave up after %d tool calls without a report", toolCalls)
		}
		results := e.runTools(ctx, rec, tc, uses)
		toolCalls += len(uses)
		if toolCalls >= e.cfg.MaxToolCalls {
			results = append(results, anthropic.NewBetaTextBlock(fmt.Sprintf(
				"Tool budget reached (%d calls). Do not call any more investigation tools; call submit_report now with what you have.", toolCalls)))
		}
		params.Messages = append(params.Messages, anthropic.NewBetaUserMessage(results...))
	}
}

// maxToolResult bounds what a single tool call adds to the context.
const maxToolResult = 24 << 10

// runTools executes tool calls in parallel and returns their results in order.
func (e *Engine) runTools(ctx context.Context, rec *recorder, tc *toolContext, uses []anthropic.BetaToolUseBlock) []anthropic.BetaContentBlockParamUnion {
	results := make([]anthropic.BetaContentBlockParamUnion, len(uses))
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
				out = out[:maxToolResult] + "\n… (truncated: narrow the query)"
			}
			var input any
			_ = json.Unmarshal([]byte(u.JSON.Input.Raw()), &input)
			rec.record(ctx, step{Kind: "tool_call", Name: u.Name, Input: input, Output: out, IsError: isErr, Duration: time.Since(start)})
			results[i] = anthropic.NewBetaToolResultBlock(u.ID, out, isErr)
		}()
	}
	wg.Wait()
	return results
}

func (e *Engine) callTool(ctx context.Context, tc *toolContext, u anthropic.BetaToolUseBlock) (string, error) {
	t, ok := findTool(u.Name)
	if !ok || t.run == nil {
		return "", fmt.Errorf("unknown tool %q", u.Name)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := t.run(ctx, tc, json.RawMessage(u.JSON.Input.Raw()))
	if err != nil {
		return "", err
	}
	if s, ok := out.(string); ok {
		return s, nil
	}
	b, err := json.Marshal(out)
	return string(b), err
}

func textOf(m *anthropic.BetaMessage) string {
	var parts []string
	for _, block := range m.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok && t.Text != "" {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n")
}
