// Package server wires the HTTP server: Connect API, webhooks and health.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"
	"connectrpc.com/grpcreflect"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/gen/opensre/v1/opensrev1connect"
	"github.com/openstatushq/opensre/internal/action"
	"github.com/openstatushq/opensre/internal/agent"
	"github.com/openstatushq/opensre/internal/api"
	"github.com/openstatushq/opensre/internal/catalog"
	"github.com/openstatushq/opensre/internal/config"
	"github.com/openstatushq/opensre/internal/connector"
	"github.com/openstatushq/opensre/internal/healthcheck"
	"github.com/openstatushq/opensre/internal/llm"
	"github.com/openstatushq/opensre/internal/report"
	"github.com/openstatushq/opensre/internal/secrets"
	"github.com/openstatushq/opensre/internal/store"
	"github.com/openstatushq/opensre/internal/trigger"
)

// Run starts the server and blocks until ctx is cancelled.
func Run(ctx context.Context, cfg config.Config) error {
	box, err := secrets.New(cfg.SecretKey)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.DBURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	if n, err := st.FailRunningInvestigations(ctx); err != nil {
		return err
	} else if n > 0 {
		slog.Warn("marked interrupted investigations as failed", "count", n)
	}
	if n, err := st.FailRunningActions(ctx); err != nil {
		return err
	} else if n > 0 {
		slog.Warn("marked interrupted actions as failed: check their machines", "count", n)
	}

	reg := connector.NewRegistry(st, box)
	// Ends MCP sessions and stops local MCP servers (flyctl) on shutdown.
	defer reg.Close()

	models, err := resolveModels(cfg)
	if err != nil {
		slog.Warn("LLM is not configured: briefs work, investigations are disabled", "err", err)
	}
	var notifier agent.Notifier
	var actionNotifier action.Notifier
	var slack *report.Slack
	if cfg.SlackBotToken != "" && cfg.SlackChannel != "" {
		slack = report.NewSlack(cfg.SlackBotToken, cfg.SlackChannel)
		notifier, actionNotifier = slack, slack
	} else {
		slog.Info("Slack is not configured (SLACK_BOT_TOKEN, SLACK_CHANNEL): reports are available through the API only")
	}

	// Investigations outlive the request that started them, but not the server.
	engineCtx, cancelEngine := context.WithCancel(context.Background())
	defer cancelEngine()
	actions := action.New(cfg, st, reg, actionNotifier)
	if cfg.ActionsEnabled {
		slog.Info("machine actions are enabled: investigations can propose them, a human approves each one", "ttl", cfg.ActionTTL)
	}
	slackButtons := slack != nil && cfg.ActionsEnabled && cfg.SlackSigningSecret != ""
	switch {
	case slackButtons:
		slack.EnableButtons(actions, st.GetInvestigation, cfg.SlackSigningSecret, cfg.SlackApprovers)
		if len(cfg.SlackApprovers) == 0 {
			slog.Warn("Slack approval buttons are enabled for anyone in the channel: set SLACK_APPROVERS to limit them")
		} else {
			slog.Info("Slack approval buttons are enabled", "approvers", cfg.SlackApprovers)
		}
	case cfg.SlackSigningSecret != "":
		slog.Warn("SLACK_SIGNING_SECRET is set but Slack buttons need SLACK_BOT_TOKEN, SLACK_CHANNEL and OPENSRE_ACTIONS_ENABLED=true")
	}
	engine := agent.New(engineCtx, cfg, st, reg, models, notifier, actions)
	if cfg.HealthcheckInterval > 0 {
		if models.Triage == nil || models.Investigate == nil {
			slog.Warn("health checks are enabled but no LLM is configured: findings are logged, not investigated")
		}
		services := func(ctx context.Context) ([]*opensrev1.Service, error) {
			s, _, err := catalog.Load(ctx, st.Queries)
			return s, err
		}
		go healthcheck.New(cfg.HealthcheckInterval, services, reg.Metrics, engine).Run(engineCtx)
	}

	if cfg.APIToken == "" {
		slog.Warn("OPENSRE_API_TOKEN is not set: the API is unauthenticated, do not expose this instance")
	}
	opts := connect.WithInterceptors(api.NewAuthInterceptor(cfg.APIToken))

	mux := http.NewServeMux()
	mux.Handle(opensrev1connect.NewCatalogServiceHandler(api.NewCatalogService(st, reg), opts))
	mux.Handle(opensrev1connect.NewConnectorServiceHandler(api.NewConnectorService(st, box, reg), opts))
	mux.Handle(opensrev1connect.NewInvestigationServiceHandler(api.NewInvestigationService(st, engine), opts))
	mux.Handle(opensrev1connect.NewActionServiceHandler(api.NewActionService(actions), opts))
	mux.Handle(grpchealth.NewHandler(grpchealth.NewStaticChecker(
		opensrev1connect.CatalogServiceName,
		opensrev1connect.ConnectorServiceName,
		opensrev1connect.InvestigationServiceName,
		opensrev1connect.ActionServiceName,
	)))
	reflector := grpcreflect.NewStaticReflector(
		opensrev1connect.CatalogServiceName,
		opensrev1connect.ConnectorServiceName,
		opensrev1connect.InvestigationServiceName,
		opensrev1connect.ActionServiceName,
	)
	mux.Handle(grpcreflect.NewHandlerV1(reflector))
	mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))
	trigger.New(engine, st, cfg.WebhookToken).Register(mux)
	if slackButtons {
		// Authenticated by Slack's request signature, not the API token.
		mux.Handle("POST /slack/interactions", slack.InteractionsHandler())
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := st.Pool.Ping(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true) // gRPC clients without TLS
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           logRequests(mux),
		Protocols:         protocols,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		slog.Info("opensre listening", "addr", cfg.Addr, "triage_model", cfg.Triage.Model, "investigate_model", cfg.Investigate.Model)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	cancelEngine()
	engine.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path != "/healthz" {
			slog.Debug("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
		}
	})
}

// resolveModels resolves each task's model from the environment. Any error
// leaves investigations disabled.
func resolveModels(cfg config.Config) (agent.Models, error) {
	reg := llm.FromEnv(nil)
	triage, err := reg.Model(cfg.Triage.Model)
	if err != nil {
		return agent.Models{}, fmt.Errorf("triage: %w", err)
	}
	investigate, err := reg.Model(cfg.Investigate.Model)
	if err != nil {
		return agent.Models{}, fmt.Errorf("investigate: %w", err)
	}
	return agent.Models{Triage: triage, Investigate: investigate}, nil
}
