// Package trigger receives alerts over plain HTTP webhooks and turns them
// into investigations.
package trigger

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/openstatushq/opensre/internal/agent"
	"github.com/openstatushq/opensre/internal/catalog"
	"github.com/openstatushq/opensre/internal/store"
)

// Alert is the normalized form of any incoming alert.
type Alert struct {
	Title       string
	Source      string
	Services    []string
	Fingerprint string
	At          time.Time
	Raw         map[string]any
	// Resolved alerts are acknowledged but do not start an investigation.
	Resolved bool
}

type Handler struct {
	engine *agent.Engine
	store  *store.Store
	token  string
}

func New(engine *agent.Engine, s *store.Store, token string) *Handler {
	return &Handler{engine: engine, store: s, token: token}
}

// Register mounts the webhook routes on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("POST /webhooks/openstatus", h.handle(parseOpenstatus))
	mux.Handle("POST /webhooks/alertmanager", h.handle(parseAlertmanager))
	mux.Handle("POST /webhooks/grafana", h.handle(parseAlertmanager)) // Grafana alerting uses the Alertmanager shape
	mux.Handle("POST /webhooks/generic", h.handle(parseGeneric))
}

type parser func(body []byte, resolve resolver) ([]Alert, error)

// resolver maps alert hints (labels, monitor ids) to catalog services.
type resolver struct {
	graph *catalog.Graph
}

func (r resolver) byName(names ...string) []string {
	for _, n := range names {
		if n == "" {
			continue
		}
		if _, ok := r.graph.Service(n); ok {
			return []string{n}
		}
	}
	return nil
}

func (r resolver) byMonitor(monitor string) []string {
	if s, ok := r.graph.FindByMonitor(monitor); ok {
		return []string{s.Name}
	}
	return nil
}

func (h *Handler) handle(parse parser) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		_, graph, err := catalog.Load(r.Context(), h.store.Queries)
		if err != nil {
			http.Error(w, "load catalog", http.StatusInternalServerError)
			return
		}
		alerts, err := parse(body, resolver{graph})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		results := make([]startResult, 0, len(alerts))
		for _, a := range alerts {
			results = append(results, h.start(r.Context(), a))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"alerts": results})
	})
}

type startResult struct {
	Title           string `json:"title"`
	Status          string `json:"status"`
	InvestigationID string `json:"investigation_id,omitempty"`
}

func (h *Handler) start(ctx context.Context, a Alert) (res startResult) {
	res.Title = a.Title
	switch {
	case a.Resolved:
		res.Status = "ignored: resolved"
		return
	case len(a.Services) == 0:
		res.Status = "ignored: no matching service in the catalog"
		slog.Warn("alert without a catalog service", "source", a.Source, "title", a.Title)
		return
	}
	inv, reused, err := h.engine.Start(ctx, agent.StartRequest{
		Title: a.Title, Source: a.Source, Services: a.Services, Alert: a.Raw, Fingerprint: a.Fingerprint, At: a.At,
	})
	switch {
	case err != nil:
		res.Status = "error: " + err.Error()
		slog.Error("start investigation", "source", a.Source, "err", err)
	case reused:
		res.Status, res.InvestigationID = "deduplicated", inv.ID.String()
	default:
		res.Status, res.InvestigationID = "started", inv.ID.String()
	}
	return
}

func (h *Handler) authorized(r *http.Request) bool {
	if h.token == "" {
		return true
	}
	got := r.URL.Query().Get("token")
	if v := r.Header.Get("Authorization"); v != "" {
		got = strings.TrimPrefix(v, "Bearer ")
	}
	if v := r.Header.Get("X-Webhook-Secret"); v != "" {
		got = v
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(h.token)) == 1
}

// parseOpenstatus handles openstatus webhook notifications:
// https://www.openstatus.dev/docs/reference/notification#webhook
func parseOpenstatus(body []byte, res resolver) ([]Alert, error) {
	var p struct {
		Monitor struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"monitor"`
		CronTimestamp int64  `json:"cronTimestamp"`
		Status        string `json:"status"`
		StatusCode    int    `json:"statusCode"`
		Latency       int64  `json:"latency"`
		ErrorMessage  string `json:"errorMessage"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("invalid openstatus payload: %w", err)
	}
	if p.Monitor.ID == 0 {
		return nil, fmt.Errorf("invalid openstatus payload: missing monitor.id")
	}
	raw := map[string]any{}
	_ = json.Unmarshal(body, &raw)

	title := fmt.Sprintf("openstatus monitor %q is %s", p.Monitor.Name, p.Status)
	if p.ErrorMessage != "" {
		title += ": " + p.ErrorMessage
	}
	at := time.Now()
	if p.CronTimestamp > 0 {
		at = time.UnixMilli(p.CronTimestamp)
	}
	return []Alert{{
		Title:       title,
		Source:      "openstatus",
		Services:    res.byMonitor(fmt.Sprintf("openstatus:%d", p.Monitor.ID)),
		Fingerprint: fmt.Sprintf("openstatus:%d", p.Monitor.ID),
		At:          at,
		Raw:         raw,
		Resolved:    p.Status == "recovered",
	}}, nil
}

// parseAlertmanager handles Alertmanager (and Grafana alerting) webhooks.
func parseAlertmanager(body []byte, res resolver) ([]Alert, error) {
	var p struct {
		Alerts []struct {
			Status       string            `json:"status"`
			Labels       map[string]string `json:"labels"`
			Annotations  map[string]string `json:"annotations"`
			StartsAt     time.Time         `json:"startsAt"`
			Fingerprint  string            `json:"fingerprint"`
			GeneratorURL string            `json:"generatorURL"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("invalid alertmanager payload: %w", err)
	}
	var out []Alert
	for _, a := range p.Alerts {
		l := a.Labels
		title := l["alertname"]
		if s := a.Annotations["summary"]; s != "" {
			title += ": " + s
		}
		raw := map[string]any{"labels": l, "annotations": a.Annotations, "startsAt": a.StartsAt, "generatorURL": a.GeneratorURL}
		fp := a.Fingerprint
		if fp == "" {
			fp = l["alertname"] + "/" + l["service"]
		}
		out = append(out, Alert{
			Title:       title,
			Source:      "alertmanager",
			Services:    res.byName(l["service"], l["service_name"], l["app"], l["job"], l["container"]),
			Fingerprint: "alertmanager:" + fp,
			At:          a.StartsAt,
			Raw:         raw,
			Resolved:    a.Status == "resolved",
		})
	}
	return out, nil
}

// parseGeneric handles {"service": "...", "title": "...", "fingerprint": "...", ...}.
func parseGeneric(body []byte, res resolver) ([]Alert, error) {
	var p struct {
		Service     string         `json:"service"`
		Title       string         `json:"title"`
		Fingerprint string         `json:"fingerprint"`
		Resolved    bool           `json:"resolved"`
		At          time.Time      `json:"at"`
		Details     map[string]any `json:"details"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if p.Service == "" || p.Title == "" {
		return nil, fmt.Errorf("invalid payload: service and title are required")
	}
	fp := ""
	if p.Fingerprint != "" {
		fp = "generic:" + p.Fingerprint
	}
	return []Alert{{
		Title: p.Title, Source: "webhook", Services: res.byName(p.Service), Fingerprint: fp,
		At: p.At, Raw: map[string]any{"details": p.Details}, Resolved: p.Resolved,
	}}, nil
}
