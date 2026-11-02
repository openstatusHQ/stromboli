// Package config loads server settings from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
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

	AnthropicAPIKey string // ANTHROPIC_API_KEY
	Model           string // OPENSRE_MODEL
	Effort          string // OPENSRE_EFFORT
	// MaxToolCalls bounds one investigation.
	MaxToolCalls int // OPENSRE_MAX_TOOL_CALLS
	// InvestigationTimeout bounds one investigation's wall-clock time.
	InvestigationTimeout time.Duration // OPENSRE_INVESTIGATION_TIMEOUT

	SlackBotToken string // SLACK_BOT_TOKEN
	SlackChannel  string // SLACK_CHANNEL: channel ID for reports

	// DedupeWindow reuses a finished investigation for the same alert within this window.
	DedupeWindow time.Duration // OPENSRE_DEDUPE_WINDOW
}

func Load() (Config, error) {
	c := Config{
		Addr:            env("OPENSRE_ADDR", ":8080"),
		DBURL:           os.Getenv("OPENSRE_DB_URL"),
		SecretKey:       os.Getenv("OPENSRE_SECRET_KEY"),
		APIToken:        os.Getenv("OPENSRE_API_TOKEN"),
		WebhookToken:    os.Getenv("OPENSRE_WEBHOOK_TOKEN"),
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		Model:           env("OPENSRE_MODEL", "claude-opus-5-5"),
		Effort:          env("OPENSRE_EFFORT", "high"),
		SlackBotToken:   os.Getenv("SLACK_BOT_TOKEN"),
		SlackChannel:    os.Getenv("SLACK_CHANNEL"),
	}
	if c.WebhookToken == "" {
		c.WebhookToken = c.APIToken
	}

	var err error
	if c.MaxToolCalls, err = strconv.Atoi(env("OPENSRE_MAX_TOOL_CALLS", "30")); err != nil {
		return c, fmt.Errorf("OPENSRE_MAX_TOOL_CALLS: %w", err)
	}
	if c.InvestigationTimeout, err = time.ParseDuration(env("OPENSRE_INVESTIGATION_TIMEOUT", "8m")); err != nil {
		return c, fmt.Errorf("OPENSRE_INVESTIGATION_TIMEOUT: %w", err)
	}
	if c.DedupeWindow, err = time.ParseDuration(env("OPENSRE_DEDUPE_WINDOW", "30m")); err != nil {
		return c, fmt.Errorf("OPENSRE_DEDUPE_WINDOW: %w", err)
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
