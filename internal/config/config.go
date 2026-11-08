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
	Addr      string // OPENSRE_ADDR
	DBURL     string // OPENSRE_DB_URL
	SecretKey string // OPENSRE_SECRET_KEY: encrypts connector secrets at rest
	// APIToken protects the Connect API. Empty disables auth (local dev only).
	APIToken string // OPENSRE_API_TOKEN
	// WebhookToken protects /webhooks/*. Falls back to APIToken.
	WebhookToken string // OPENSRE_WEBHOOK_TOKEN

	// Models are "provider:model" ids (see internal/llm), e.g.
	// "anthropic:claude-opus-5-5", "openai:gpt-5", "ollama:qwen3:32b".
	// Model and Effort are the defaults; each task can override them.
	Model  string // OPENSRE_MODEL
	Effort string // OPENSRE_EFFORT: empty = the provider's default
	// Triage runs until a mitigation is published: it should be fast.
	Triage Task // OPENSRE_TRIAGE_MODEL, OPENSRE_TRIAGE_EFFORT
	// Investigate finds the root cause and writes the report.
	Investigate Task // OPENSRE_INVESTIGATE_MODEL, OPENSRE_INVESTIGATE_EFFORT
	// MaxToolCalls bounds one investigation.
	MaxToolCalls int // OPENSRE_MAX_TOOL_CALLS
	// MitigationToolCalls is how many tool calls the triage phase gets before
	// the agent is told to propose a mitigation (mitigate first, investigate second).
	MitigationToolCalls int // OPENSRE_MITIGATION_TOOL_CALLS
	// InvestigationTimeout bounds one investigation's wall-clock time.
	InvestigationTimeout time.Duration // OPENSRE_INVESTIGATION_TIMEOUT

	SlackBotToken string // SLACK_BOT_TOKEN
	SlackChannel  string // SLACK_CHANNEL: channel ID for reports
	// SlackSigningSecret enables Approve/Reject buttons on proposed actions,
	// received at POST /slack/interactions. Needs OPENSRE_ACTIONS_ENABLED.
	SlackSigningSecret string // SLACK_SIGNING_SECRET
	// SlackApprovers, when set, are the only Slack user IDs that may click them.
	SlackApprovers []string // SLACK_APPROVERS: comma-separated user IDs

	// DedupeWindow reuses a finished investigation for the same alert within this window.
	DedupeWindow time.Duration // OPENSRE_DEDUPE_WINDOW

	// ActionsEnabled lets investigations propose machine actions that a human
	// can approve and opensre then runs. Off by default: mitigations are text only.
	ActionsEnabled bool // OPENSRE_ACTIONS_ENABLED
	// ActionTTL is how long a proposed action can be approved.
	ActionTTL time.Duration // OPENSRE_ACTION_TTL

	// HealthcheckInterval is how often machine metrics are checked against
	// thresholds (memory, CPU, disk, volume); a breach starts an
	// investigation. Zero (the default) disables health checks.
	HealthcheckInterval time.Duration // OPENSRE_HEALTHCHECK_INTERVAL
}

// Task is the model settings of one LLM task.
type Task struct {
	Model  string
	Effort string
}

func Load() (Config, error) {
	c := Config{
		Addr:          env("OPENSRE_ADDR", ":8080"),
		DBURL:         os.Getenv("OPENSRE_DB_URL"),
		SecretKey:     os.Getenv("OPENSRE_SECRET_KEY"),
		APIToken:      os.Getenv("OPENSRE_API_TOKEN"),
		WebhookToken:  os.Getenv("OPENSRE_WEBHOOK_TOKEN"),
		Model:         env("OPENSRE_MODEL", "anthropic:claude-opus-5-5"),
		Effort:        os.Getenv("OPENSRE_EFFORT"),
		SlackBotToken: os.Getenv("SLACK_BOT_TOKEN"),
		SlackChannel:  os.Getenv("SLACK_CHANNEL"),

		SlackSigningSecret: os.Getenv("SLACK_SIGNING_SECRET"),
	}
	for _, id := range strings.Split(os.Getenv("SLACK_APPROVERS"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			c.SlackApprovers = append(c.SlackApprovers, id)
		}
	}
	c.Triage = Task{Model: env("OPENSRE_TRIAGE_MODEL", c.Model), Effort: env("OPENSRE_TRIAGE_EFFORT", c.Effort)}
	c.Investigate = Task{Model: env("OPENSRE_INVESTIGATE_MODEL", c.Model), Effort: env("OPENSRE_INVESTIGATE_EFFORT", c.Effort)}
	if c.WebhookToken == "" {
		c.WebhookToken = c.APIToken
	}

	var err error
	if c.MaxToolCalls, err = strconv.Atoi(env("OPENSRE_MAX_TOOL_CALLS", "30")); err != nil {
		return c, fmt.Errorf("OPENSRE_MAX_TOOL_CALLS: %w", err)
	}
	if c.MitigationToolCalls, err = strconv.Atoi(env("OPENSRE_MITIGATION_TOOL_CALLS", "6")); err != nil {
		return c, fmt.Errorf("OPENSRE_MITIGATION_TOOL_CALLS: %w", err)
	}
	if c.InvestigationTimeout, err = time.ParseDuration(env("OPENSRE_INVESTIGATION_TIMEOUT", "8m")); err != nil {
		return c, fmt.Errorf("OPENSRE_INVESTIGATION_TIMEOUT: %w", err)
	}
	if c.DedupeWindow, err = time.ParseDuration(env("OPENSRE_DEDUPE_WINDOW", "30m")); err != nil {
		return c, fmt.Errorf("OPENSRE_DEDUPE_WINDOW: %w", err)
	}

	if c.ActionsEnabled, err = strconv.ParseBool(env("OPENSRE_ACTIONS_ENABLED", "false")); err != nil {
		return c, fmt.Errorf("OPENSRE_ACTIONS_ENABLED: %w", err)
	}
	if c.ActionTTL, err = time.ParseDuration(env("OPENSRE_ACTION_TTL", "30m")); err != nil || c.ActionTTL <= 0 {
		return c, fmt.Errorf("OPENSRE_ACTION_TTL: invalid duration %q", os.Getenv("OPENSRE_ACTION_TTL"))
	}
	if v := os.Getenv("OPENSRE_HEALTHCHECK_INTERVAL"); v != "" {
		if c.HealthcheckInterval, err = time.ParseDuration(v); err != nil || c.HealthcheckInterval < 0 {
			return c, fmt.Errorf("OPENSRE_HEALTHCHECK_INTERVAL: invalid duration %q, e.g. 1h (0 disables)", v)
		}
		if c.HealthcheckInterval > 0 && c.HealthcheckInterval < 5*time.Minute {
			return c, fmt.Errorf("OPENSRE_HEALTHCHECK_INTERVAL: %s is too short, use at least 5m", v)
		}
	}
	if c.ActionsEnabled && c.APIToken == "" {
		// Approving an action changes production: it must not be open to anyone.
		return c, fmt.Errorf("OPENSRE_ACTIONS_ENABLED requires OPENSRE_API_TOKEN")
	}

	if c.DBURL == "" {
		return c, fmt.Errorf("OPENSRE_DB_URL is required")
	}
	if c.SecretKey == "" {
		return c, fmt.Errorf("OPENSRE_SECRET_KEY is required (generate one with: openssl rand -base64 32)")
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
