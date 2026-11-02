// Package mcp connects to remote MCP servers (Streamable HTTP) and exposes
// their read-only tools to the agent, so a team can plug in the context it
// needs to choose a mitigation (rollout state, region health, feature flags,
// traffic...) without a dedicated Go connector.
//
// opensre suggests, it never executes: only tools the server marks
// readOnlyHint, or tools an operator lists explicitly, are exposed. Hints are
// declared by the server, so connect MCP servers with read-only credentials.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openstatushq/opensre/internal/policy"
)

// Config is an MCP connector's settings.
type Config struct {
	// URL is the server's Streamable HTTP endpoint.
	URL         string
	BearerToken string
	// Tools, when set, is the exact list of tools to expose and overrides the
	// readOnlyHint check. When empty, only tools marked readOnlyHint are exposed.
	Tools []string
}

// ParseTools splits a comma-separated allowlist.
func ParseTools(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// Tool is one remote tool exposed to the model.
type Tool struct {
	// Name is the name the model sees: mcp__<connector>__<tool>.
	Name        string
	Remote      string
	Description string
	InputSchema map[string]any
	session     *Session
}

// Call runs the tool and returns its redacted text output.
func (t Tool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	return t.session.call(ctx, t.Remote, args)
}

// Session is an open connection to one MCP server.
type Session struct {
	connector string
	cs        *sdk.ClientSession
	tools     []Tool
	// Hidden counts the server's tools that were not exposed.
	Hidden int
}

// Open connects to the server and lists the tools it exposes to the agent.
func Open(ctx context.Context, connector string, cfg Config) (*Session, error) {
	if cfg.URL == "" {
		return nil, errors.New("url is required")
	}
	httpClient := http.DefaultClient
	if cfg.BearerToken != "" {
		httpClient = &http.Client{Transport: bearer{token: cfg.BearerToken, next: http.DefaultTransport}}
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "opensre", Version: "v1"}, nil)
	cs, err := client.Connect(ctx, &sdk.StreamableClientTransport{
		Endpoint:   cfg.URL,
		HTTPClient: httpClient,
		// Request/response only: opensre does not need server notifications.
		DisableStandaloneSSE: true,
		MaxRetries:           2,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", cfg.URL, err)
	}
	s := &Session{connector: connector, cs: cs}
	seen := map[string]bool{}
	for t, err := range cs.Tools(ctx, nil) {
		if err != nil {
			cs.Close()
			return nil, fmt.Errorf("list tools: %w", err)
		}
		name := ToolName(connector, t.Name)
		if !exposed(t, cfg.Tools) || seen[name] {
			s.Hidden++
			continue
		}
		seen[name] = true
		schema, _ := t.InputSchema.(map[string]any)
		s.tools = append(s.tools, Tool{
			Name:        name,
			Remote:      t.Name,
			Description: description(connector, t),
			InputSchema: schema,
			session:     s,
		})
	}
	return s, nil
}

// Tools returns the exposed tools, in server order.
func (s *Session) Tools() []Tool { return s.tools }

func (s *Session) Close() error { return s.cs.Close() }

// maxRemoteOutput bounds what is read from one call before redaction; the
// agent truncates further.
const maxRemoteOutput = 256 << 10

func (s *Session) call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	var arguments map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &arguments); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
	}
	res, err := s.cs.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range res.Content {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		switch c := c.(type) {
		case *sdk.TextContent:
			b.WriteString(c.Text)
		case *sdk.EmbeddedResource:
			if c.Resource != nil && c.Resource.Text != "" {
				b.WriteString(c.Resource.Text)
			} else {
				b.WriteString("[binary resource omitted]")
			}
		default:
			fmt.Fprintf(&b, "[%T content omitted]", c)
		}
		if b.Len() > maxRemoteOutput {
			break
		}
	}
	if b.Len() == 0 && res.StructuredContent != nil {
		raw, _ := json.Marshal(res.StructuredContent)
		b.Write(raw)
	}
	out := b.String()
	if len(out) > maxRemoteOutput {
		out = out[:maxRemoteOutput]
	}
	out = policy.Redact(out)
	if res.IsError {
		return "", errors.New(out)
	}
	return out, nil
}

// exposed decides whether a remote tool may be offered to the model.
func exposed(t *sdk.Tool, allow []string) bool {
	if len(allow) > 0 {
		return slices.Contains(allow, t.Name)
	}
	return t.Annotations != nil && t.Annotations.ReadOnlyHint
}

func description(connector string, t *sdk.Tool) string {
	d := strings.TrimSpace(t.Description)
	if d == "" && t.Annotations != nil {
		d = t.Annotations.Title
	}
	return fmt.Sprintf("[MCP connector %q, read-only] %s", connector, d)
}

var invalidName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// maxToolName keeps names within the Claude API limit.
const maxToolName = 64

// Prefix starts every MCP tool name, so they never collide with built-ins.
const Prefix = "mcp__"

// ToolName is the name the model sees for a remote tool.
func ToolName(connector, tool string) string {
	n := Prefix + invalidName.ReplaceAllString(connector, "_") + "__" + invalidName.ReplaceAllString(tool, "_")
	if len(n) > maxToolName {
		n = n[:maxToolName]
	}
	return n
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}
