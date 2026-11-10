// Package config loads server settings from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr      string // STROMBOLI_ADDR
	DBURL     string // STROMBOLI_DB_URL
	SecretKey string // STROMBOLI_SECRET_KEY: encrypts connector secrets at rest
	// APIToken protects the Connect API. Empty disables auth (local dev only).
	APIToken string // STROMBOLI_API_TOKEN
	// WebhookToken protects /webhooks/*. Falls back to APIToken.
	WebhookToken string // STROMBOLI_WEBHOOK_TOKEN

	// Models are "provider:model" ids (see internal/llm), e.g.
	// "anthropic:claude-opus-5-5", "openai:gpt-5", "ollama:qwen3:32b".
	// Model and Effort are the defaults; each task can override them.
	Model  string // STROMBOLI_MODEL
	Effort string // STROMBOLI_EFFORT: empty = the provider's default
	// Triage runs until a mitigation is published: it should be fast.
	Triage Task // STROMBOLI_TRIAGE_MODEL, STROMBOLI_TRIAGE_EFFORT
	// Investigate finds the root cause and writes the report.
	Investigate Task // STROMBOLI_INVESTIGATE_MODEL, STROMBOLI_INVESTIGATE_EFFORT
	// MaxToolCalls bounds one investigation.
	MaxToolCalls int // STROMBOLI_MAX_TOOL_CALLS
	// MitigationToolCalls is how many tool calls the triage phase gets before
	// the agent is told to propose a mitigation (mitigate first, investigate second).
	MitigationToolCalls int // STROMBOLI_MITIGATION_TOOL_CALLS
	// InvestigationTimeout bounds one investigation's wall-clock time.
	InvestigationTimeout time.Duration // STROMBOLI_INVESTIGATION_TIMEOUT

	SlackBotToken string // SLACK_BOT_TOKEN
	SlackChannel  string // SLACK_CHANNEL: channel ID for reports
	// SlackSigningSecret enables Approve/Reject buttons on proposed actions,
	// received at POST /slack/interactions. Needs STROMBOLI_ACTIONS_ENABLED.
	SlackSigningSecret string // SLACK_SIGNING_SECRET
	// SlackApprovers, when set, are the only Slack user IDs that may click them.
	SlackApprovers []string // SLACK_APPROVERS: comma-separated user IDs

	// DedupeWindow reuses a finished investigation for the same alert within this window.
	DedupeWindow time.Duration // STROMBOLI_DEDUPE_WINDOW

	// ActionsEnabled lets investigations propose machine actions that a human
	// can approve and stromboli then runs. Off by default: mitigations are text only.
	ActionsEnabled bool // STROMBOLI_ACTIONS_ENABLED
	// ActionTTL is how long a proposed action can be approved.
	ActionTTL time.Duration // STROMBOLI_ACTION_TTL

	// HealthcheckInterval is how often machine metrics are checked against
	// thresholds (memory, CPU, disk, volume); a breach starts an
	// investigation. Zero (the default) disables health checks.
	HealthcheckInterval time.Duration // STROMBOLI_HEALTHCHECK_INTERVAL

	// MCPEnabled serves stromboli's MCP tools at /mcp (ADR 0021).
	MCPEnabled bool // STROMBOLI_MCP_ENABLED
	// MCPToken, when set, is the only token /mcp accepts, instead of the API
	// token: it reaches the MCP tools, not the whole API.
	MCPToken string // STROMBOLI_MCP_TOKEN
}

// Task is the model settings of one LLM task.
type Task struct {
	Model  string
	Effort string
}

func Load() (Config, error) {
	c := Config{
		Addr:          env("STROMBOLI_ADDR", ":8080"),
		DBURL:         os.Getenv("STROMBOLI_DB_URL"),
		SecretKey:     os.Getenv("STROMBOLI_SECRET_KEY"),
		APIToken:      os.Getenv("STROMBOLI_API_TOKEN"),
		WebhookToken:  os.Getenv("STROMBOLI_WEBHOOK_TOKEN"),
		Model:         env("STROMBOLI_MODEL", "anthropic:claude-opus-5-5"),
		Effort:        os.Getenv("STROMBOLI_EFFORT"),
		SlackBotToken: os.Getenv("SLACK_BOT_TOKEN"),
		SlackChannel:  os.Getenv("SLACK_CHANNEL"),

		SlackSigningSecret: os.Getenv("SLACK_SIGNING_SECRET"),
		MCPToken:           os.Getenv("STROMBOLI_MCP_TOKEN"),
	}
	for _, id := range strings.Split(os.Getenv("SLACK_APPROVERS"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			c.SlackApprovers = append(c.SlackApprovers, id)
		}
	}
	c.Triage = Task{Model: env("STROMBOLI_TRIAGE_MODEL", c.Model), Effort: env("STROMBOLI_TRIAGE_EFFORT", c.Effort)}
	c.Investigate = Task{Model: env("STROMBOLI_INVESTIGATE_MODEL", c.Model), Effort: env("STROMBOLI_INVESTIGATE_EFFORT", c.Effort)}
	if c.WebhookToken == "" {
		c.WebhookToken = c.APIToken
	}

	var err error
	if c.MaxToolCalls, err = strconv.Atoi(env("STROMBOLI_MAX_TOOL_CALLS", "30")); err != nil {
		return c, fmt.Errorf("STROMBOLI_MAX_TOOL_CALLS: %w", err)
	}
	if c.MitigationToolCalls, err = strconv.Atoi(env("STROMBOLI_MITIGATION_TOOL_CALLS", "6")); err != nil {
		return c, fmt.Errorf("STROMBOLI_MITIGATION_TOOL_CALLS: %w", err)
	}
	if c.InvestigationTimeout, err = time.ParseDuration(env("STROMBOLI_INVESTIGATION_TIMEOUT", "8m")); err != nil {
		return c, fmt.Errorf("STROMBOLI_INVESTIGATION_TIMEOUT: %w", err)
	}
	if c.DedupeWindow, err = time.ParseDuration(env("STROMBOLI_DEDUPE_WINDOW", "30m")); err != nil {
		return c, fmt.Errorf("STROMBOLI_DEDUPE_WINDOW: %w", err)
	}

	if c.ActionsEnabled, err = strconv.ParseBool(env("STROMBOLI_ACTIONS_ENABLED", "false")); err != nil {
		return c, fmt.Errorf("STROMBOLI_ACTIONS_ENABLED: %w", err)
	}
	if c.ActionTTL, err = time.ParseDuration(env("STROMBOLI_ACTION_TTL", "30m")); err != nil || c.ActionTTL <= 0 {
		return c, fmt.Errorf("STROMBOLI_ACTION_TTL: invalid duration %q", os.Getenv("STROMBOLI_ACTION_TTL"))
	}
	if v := os.Getenv("STROMBOLI_HEALTHCHECK_INTERVAL"); v != "" {
		if c.HealthcheckInterval, err = time.ParseDuration(v); err != nil || c.HealthcheckInterval < 0 {
			return c, fmt.Errorf("STROMBOLI_HEALTHCHECK_INTERVAL: invalid duration %q, e.g. 1h (0 disables)", v)
		}
		if c.HealthcheckInterval > 0 && c.HealthcheckInterval < 5*time.Minute {
			return c, fmt.Errorf("STROMBOLI_HEALTHCHECK_INTERVAL: %s is too short, use at least 5m", v)
		}
	}
	if c.MCPEnabled, err = strconv.ParseBool(env("STROMBOLI_MCP_ENABLED", "false")); err != nil {
		return c, fmt.Errorf("STROMBOLI_MCP_ENABLED: %w", err)
	}
	if c.MCPEnabled && c.APIToken == "" {
		// MCP clients are driven by models: never serve them unauthenticated.
		return c, fmt.Errorf("STROMBOLI_MCP_ENABLED requires STROMBOLI_API_TOKEN")
	}
	if c.ActionsEnabled && c.APIToken == "" {
		// Approving an action changes production: it must not be open to anyone.
		return c, fmt.Errorf("STROMBOLI_ACTIONS_ENABLED requires STROMBOLI_API_TOKEN")
	}

	if c.DBURL == "" {
		return c, fmt.Errorf("STROMBOLI_DB_URL is required")
	}
	if c.SecretKey == "" {
		return c, fmt.Errorf("STROMBOLI_SECRET_KEY is required (generate one with: openssl rand -base64 32)")
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
