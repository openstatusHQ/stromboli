// Railway infrastructure as code: opensre and its Postgres database.
//
// Preview and apply from the repository root (see docs/deploy/railway.md):
//
//	railway config plan
//	railway config apply
package main

import railway "github.com/railwayapp/railway-go-sdk"

func Railway() railway.Project {
	db := railway.Postgres("postgres")

	opensre := railway.ServiceNamed("opensre", railway.ServiceConfig{
		"source": railway.Github("openstatusHQ/opensre"),
		"build": map[string]any{
			"builder":        "DOCKERFILE",
			"dockerfilePath": "deploy/Dockerfile",
			// Docs and catalog examples don't need a new image.
			"watchPatterns": []any{"cmd/**", "internal/**", "gen/**", "deploy/Dockerfile", "go.mod", "go.sum"},
		},
		// /healthz pings the database: a deploy that can't reach it never takes traffic.
		"healthcheck":        "/healthz",
		"healthcheckTimeout": 120,
		"deploy": map[string]any{
			"restartPolicyType":       "ON_FAILURE",
			"restartPolicyMaxRetries": 10,
		},
		// Every variable the server reads (internal/config, internal/llm).
		// Plain settings are literals: change them here, not in the dashboard.
		// Secrets live in Railway, never in this file: railway.Preserve() keeps
		// the stored value and leaves an unset one unset. Set them once with
		// deploy/railway/setup.sh or `railway variable set KEY --stdin --service opensre`.
		"env": map[string]any{
			// --- Required ---
			// Private network URL. Migrations run on startup.
			"OPENSRE_DB_URL": db.Env("DATABASE_URL"),
			// The port the public domain routes to.
			"PORT":         "8080",
			"OPENSRE_ADDR": ":8080",
			// Never change OPENSRE_SECRET_KEY: it decrypts the stored connector credentials.
			"OPENSRE_SECRET_KEY": railway.Preserve(),
			// Bearer token for the API and the CLI. Empty would leave a public instance open.
			"OPENSRE_API_TOKEN": railway.Preserve(),
			// Separate token for /webhooks/*; unset falls back to OPENSRE_API_TOKEN.
			"OPENSRE_WEBHOOK_TOKEN": railway.Preserve(),

			// --- Models ("provider:model") ---
			"OPENSRE_MODEL": "anthropic:claude-opus-5-5",
			// Optional, add as needed (unset = the defaults above, the provider's effort):
			// OPENSRE_EFFORT, OPENSRE_TRIAGE_MODEL, OPENSRE_TRIAGE_EFFORT,
			// OPENSRE_INVESTIGATE_MODEL, OPENSRE_INVESTIGATE_EFFORT.
			// Investigations need the key of the provider in the models above
			// (briefs work without any). Only the ones you set are used.
			"ANTHROPIC_API_KEY":  railway.Preserve(),
			"OPENAI_API_KEY":     railway.Preserve(),
			"GEMINI_API_KEY":     railway.Preserve(),
			"OPENROUTER_API_KEY": railway.Preserve(),
			"DEEPSEEK_API_KEY":   railway.Preserve(),
			"AI_GATEWAY_API_KEY": railway.Preserve(),

			// --- Investigation limits ---
			"OPENSRE_MAX_TOOL_CALLS":        "30",
			"OPENSRE_MITIGATION_TOOL_CALLS": "6",
			"OPENSRE_INVESTIGATION_TIMEOUT": "8m",
			"OPENSRE_DEDUPE_WINDOW":         "30m",

			// --- Machine actions (Fly.io): off in the server by default, on here ---
			"OPENSRE_ACTIONS_ENABLED": "true",
			"OPENSRE_ACTION_TTL":      "30m",

			// --- Slack (optional) ---
			// Reports need both SLACK_BOT_TOKEN and SLACK_CHANNEL (a channel ID).
			"SLACK_BOT_TOKEN": railway.Preserve(),
			"SLACK_CHANNEL":   railway.Preserve(),
			// Approve/Reject buttons: also needs OPENSRE_ACTIONS_ENABLED=true and the
			// app's Interactivity Request URL set to https://<domain>/slack/interactions.
			"SLACK_SIGNING_SECRET": railway.Preserve(),
			// Comma-separated Slack user IDs allowed to click; unset = anyone in the channel.
			"SLACK_APPROVERS": railway.Preserve(),
		},
	})

	return railway.ProjectNamed("opensre", []any{db, opensre})
}
