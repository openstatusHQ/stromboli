// Package mcpserver exposes opensre to MCP clients (Claude Code, Cursor,
// coding agents): read the catalog, briefs, investigations and actions, and
// start an investigation (ADR 0021).
//
// It is built on the Connect API only. The same tools run inside
// `opensre serve` (given the in-process API services) and in `opensre mcp`
// over stdio (given Connect clients), so the MCP surface is always a subset
// of the API. The interfaces below list every RPC it may call: approving or
// rejecting actions and changing the catalog or connectors are not among
// them.
package mcpserver

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
)

// Catalog is the part of CatalogService the MCP server uses.
type Catalog interface {
	ListServices(context.Context, *connect.Request[opensrev1.ListServicesRequest]) (*connect.Response[opensrev1.ListServicesResponse], error)
	GetService(context.Context, *connect.Request[opensrev1.GetServiceRequest]) (*connect.Response[opensrev1.GetServiceResponse], error)
}

// Investigations is the part of InvestigationService the MCP server uses.
type Investigations interface {
	StartInvestigation(context.Context, *connect.Request[opensrev1.StartInvestigationRequest]) (*connect.Response[opensrev1.StartInvestigationResponse], error)
	GetInvestigation(context.Context, *connect.Request[opensrev1.GetInvestigationRequest]) (*connect.Response[opensrev1.GetInvestigationResponse], error)
	ListInvestigations(context.Context, *connect.Request[opensrev1.ListInvestigationsRequest]) (*connect.Response[opensrev1.ListInvestigationsResponse], error)
	GetBrief(context.Context, *connect.Request[opensrev1.GetBriefRequest]) (*connect.Response[opensrev1.GetBriefResponse], error)
}

// Actions is the read-only part of ActionService. Approving and rejecting
// stay with a named human (ADR 0013).
type Actions interface {
	ListActions(context.Context, *connect.Request[opensrev1.ListActionsRequest]) (*connect.Response[opensrev1.ListActionsResponse], error)
	GetAction(context.Context, *connect.Request[opensrev1.GetActionRequest]) (*connect.Response[opensrev1.GetActionResponse], error)
}

// API is what the tools are built on.
type API struct {
	Catalog        Catalog
	Investigations Investigations
	Actions        Actions
}

// Options tune the server; the zero value is fine.
type Options struct {
	// Version is reported to clients in the initialize response.
	Version string
	// PollInterval is how often wait_investigation checks for new steps.
	PollInterval time.Duration
	Logger       *slog.Logger
}

// Headers the server sets on StartInvestigation so the investigation is
// recorded as started from MCP, by which client.
const (
	SourceHeader = "Opensre-Source"
	ClientHeader = "Opensre-Client"
)

const instructions = `opensre is a self-hosted AI SRE agent. It investigates incidents on the services of its catalog: it proposes a mitigation first, then finds the root cause.

- Start from list_services and get_brief (free, no LLM) to see what opensre sees on a service.
- list_investigations and get_investigation show past and running investigations, their mitigation and report.
- start_investigation runs a new investigation: it costs LLM tokens and posts to Slack. Follow it with wait_investigation.
- opensre only suggests. Machine actions it proposes must be approved by a named human with the opensre CLI or in Slack; this server cannot approve them.`

// New returns the MCP server with every tool registered.
func New(a API, o Options) *mcp.Server {
	if o.Version == "" {
		o.Version = "dev"
	}
	if o.PollInterval <= 0 {
		o.PollInterval = time.Second
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "opensre", Title: "opensre", Version: o.Version}, &mcp.ServerOptions{
		Instructions: instructions,
	})
	t := &tools{api: a, poll: o.PollInterval, log: o.Logger}
	t.register(s)
	return s
}

// Handler serves the MCP server over Streamable HTTP. Every request needs
// `Authorization: Bearer <token>`; an empty token disables auth (local dev,
// as for the Connect API).
func Handler(s *mcp.Server, token string) http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{
		// Sessions only hold the client's name for the audit trail; drop idle ones.
		SessionTimeout: 30 * time.Minute,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="opensre"`)
				http.Error(w, "invalid or missing token", http.StatusUnauthorized)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}
