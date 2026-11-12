// Package mcpconn is how connectors call provider MCP servers (Google Cloud,
// GitHub, Axiom, flyctl...). stromboli is the MCP client: it calls tools itself,
// from code, and the model never sees these servers directly.
//
// Guardrails:
//   - each client has an allow-list; any other tool is refused, whatever the
//     server offers;
//   - with RequireReadOnly, connecting fails unless the server annotates every
//     allowed tool as read-only (readOnlyHint), so a server that changes a
//     tool's nature is caught before it is called;
//   - tool errors and transport errors are returned as Go errors, and results
//     are decoded into typed structs, never passed through raw.
package mcpconn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Config describes one MCP server and what stromboli may call on it.
type Config struct {
	// Name identifies the server in errors, e.g. "gcp logging".
	Name string
	// Endpoint is a Streamable HTTP endpoint. Exactly one of Endpoint and
	// Command is set.
	Endpoint string
	// Header is sent with every HTTP request.
	Header http.Header
	// Token, when set, returns the bearer token sent as Authorization on
	// every HTTP request (e.g. a refreshed Google access token).
	Token func(ctx context.Context) (string, error)
	// Command starts a stdio server: the program and its arguments.
	Command []string
	// Env is added to the stdio server's environment.
	Env []string
	// Allow lists the tools stromboli may call. It is required.
	Allow []string
	// RequireReadOnly makes connecting fail unless every allowed tool is
	// annotated readOnlyHint. Leave it false only for servers that do not
	// annotate their tools, and allow-list them tool by tool.
	RequireReadOnly bool
	// ReadOnlyUse lists allowed tools the server does not mark read-only but
	// that stromboli only calls in a read-only way: the caller must keep the
	// arguments that would write unset (e.g. queryDataset's saveResults).
	ReadOnlyUse []string
	// Timeout bounds each tool call. Defaults to 60s.
	Timeout time.Duration
}

// Client calls the allowed tools of one MCP server. It connects lazily and
// reconnects once when a session breaks. It is safe for concurrent use.
type Client struct {
	cfg Config

	mu      sync.Mutex
	session *mcp.ClientSession
	tools   map[string]*mcp.Tool
}

// New validates cfg. It does not connect.
func New(cfg Config) (*Client, error) {
	switch {
	case cfg.Name == "":
		return nil, errors.New("mcp: name is required")
	case (cfg.Endpoint == "") == (len(cfg.Command) == 0):
		return nil, fmt.Errorf("mcp %s: set exactly one of endpoint and command", cfg.Name)
	case len(cfg.Allow) == 0:
		return nil, fmt.Errorf("mcp %s: the tool allow-list is empty", cfg.Name)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	return &Client{cfg: cfg}, nil
}

// ErrNotAllowed is returned when calling a tool outside the allow-list.
var ErrNotAllowed = errors.New("tool is not allowed")

// ToolError is a tool call the server answered with isError.
type ToolError struct {
	Server, Tool, Message string
}

func (e *ToolError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = "the tool reported an error without a message"
	}
	return fmt.Sprintf("%s: %s: %s", e.Server, e.Tool, msg)
}

// Call calls tool with args (marshalled to a JSON object) and decodes the
// result into out: the structured content when the server sends it, else the
// text content as JSON. out may be a *string to get the text as is, a *json.RawMessage, or nil.
func (c *Client) Call(ctx context.Context, tool string, args, out any) error {
	if !slices.Contains(c.cfg.Allow, tool) {
		return fmt.Errorf("%s: %s: %w", c.cfg.Name, tool, ErrNotAllowed)
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	var res *mcp.CallToolResult
	var err error
	if c.readOnly(ctx, tool) {
		res, err = c.call(ctx, tool, args)
		if err != nil && ctx.Err() == nil && !isToolError(err) {
			// The session may have expired or the process died. Calling a
			// read-only tool again is safe: retry once on a new session.
			c.reset()
			res, err = c.call(ctx, tool, args)
		}
	} else {
		// A tool that changes something is never sent twice: a transport
		// error after sending does not say whether it ran. Make sure the
		// session is alive first instead, and call it exactly once.
		if err := c.ensureAlive(ctx); err != nil {
			return err
		}
		res, err = c.call(ctx, tool, args)
	}
	if err != nil {
		return err
	}
	if res.IsError {
		return &ToolError{Server: c.cfg.Name, Tool: tool, Message: text(res)}
	}
	return decode(res, out)
}

// readOnly reports whether the server annotates tool as read-only.
func (c *Client) readOnly(ctx context.Context, tool string) bool {
	if slices.Contains(c.cfg.ReadOnlyUse, tool) {
		return true
	}
	t, err := c.Tool(ctx, tool)
	return err == nil && t.Annotations != nil && t.Annotations.ReadOnlyHint
}

// ensureAlive pings the session and reconnects when it is gone.
func (c *Client) ensureAlive(ctx context.Context) error {
	s, err := c.connect(ctx)
	if err != nil {
		return err
	}
	if s.Ping(ctx, nil) == nil {
		return nil
	}
	c.reset()
	_, err = c.connect(ctx)
	return err
}

func isToolError(err error) bool {
	var te *ToolError
	return errors.As(err, &te)
}

func (c *Client) call(ctx context.Context, tool string, args any) (*mcp.CallToolResult, error) {
	session, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", c.cfg.Name, tool, err)
	}
	return res, nil
}

// Check connects and verifies the allowed tools.
func (c *Client) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	_, err := c.connect(ctx)
	return err
}

// Tool returns the server's definition of an allowed tool, once connected.
func (c *Client) Tool(ctx context.Context, name string) (*mcp.Tool, error) {
	if _, err := c.connect(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.tools[name]
	if !ok {
		return nil, fmt.Errorf("%s: %s: %w", c.cfg.Name, name, ErrNotAllowed)
	}
	return t, nil
}

// Close ends the session (and stops a stdio server).
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		return nil
	}
	err := c.session.Close()
	c.session, c.tools = nil, nil
	return err
}

func (c *Client) reset() { _ = c.Close() }

func (c *Client) connect(ctx context.Context) (*mcp.ClientSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil {
		return c.session, nil
	}
	transport, err := c.transport()
	if err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "stromboli", Version: "1"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: connect: %w", c.cfg.Name, err)
	}
	tools, err := c.verify(ctx, session)
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	c.session, c.tools = session, tools
	return session, nil
}

// verify lists the server's tools and checks the allow-list against them.
func (c *Client) verify(ctx context.Context, s *mcp.ClientSession) (map[string]*mcp.Tool, error) {
	all := map[string]*mcp.Tool{}
	for t, err := range s.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("%s: list tools: %w", c.cfg.Name, err)
		}
		all[t.Name] = t
	}
	allowed := make(map[string]*mcp.Tool, len(c.cfg.Allow))
	var missing, writable []string
	for _, name := range c.cfg.Allow {
		t, ok := all[name]
		switch {
		case !ok:
			missing = append(missing, name)
		case c.cfg.RequireReadOnly && (t.Annotations == nil || !t.Annotations.ReadOnlyHint) && !slices.Contains(c.cfg.ReadOnlyUse, name):
			writable = append(writable, name)
		default:
			allowed[name] = t
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%s: the server does not offer %s (it offers %d tools: is the server or its toolset enabled?)",
			c.cfg.Name, strings.Join(missing, ", "), len(all))
	}
	if len(writable) > 0 {
		return nil, fmt.Errorf("%s: refusing to use %s: the server does not mark them read-only", c.cfg.Name, strings.Join(writable, ", "))
	}
	return allowed, nil
}

func (c *Client) transport() (mcp.Transport, error) {
	if len(c.cfg.Command) > 0 {
		cmd := exec.Command(c.cfg.Command[0], c.cfg.Command[1:]...)
		cmd.Env = append(os.Environ(), c.cfg.Env...)
		return &mcp.CommandTransport{Command: cmd}, nil
	}
	return &mcp.StreamableClientTransport{
		Endpoint: c.cfg.Endpoint,
		HTTPClient: &http.Client{
			Timeout:   c.cfg.Timeout + 10*time.Second,
			Transport: &authTransport{header: c.cfg.Header, token: c.cfg.Token, base: http.DefaultTransport},
		},
		// stromboli only makes request/response calls; no server-initiated
		// stream is needed, and some servers do not support it.
		DisableStandaloneSSE: true,
		MaxRetries:           2,
	}, nil
}

type authTransport struct {
	header http.Header
	token  func(context.Context) (string, error)
	base   http.RoundTripper
}

func (t *authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, vs := range t.header {
		r.Header[k] = slices.Clone(vs)
	}
	if t.token != nil {
		tok, err := t.token(r.Context())
		if err != nil {
			return nil, fmt.Errorf("get access token: %w", err)
		}
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	return t.base.RoundTrip(r)
}

// text joins a result's text content.
func text(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func decode(res *mcp.CallToolResult, out any) error {
	switch o := out.(type) {
	case nil:
		return nil
	case *string:
		*o = text(res)
		return nil
	case *json.RawMessage:
		if res.StructuredContent != nil {
			b, err := json.Marshal(res.StructuredContent)
			*o = b
			return err
		}
		*o = json.RawMessage(text(res))
		return nil
	}
	var raw []byte
	if res.StructuredContent != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			return err
		}
		raw = b
	} else {
		raw = []byte(text(res))
	}
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode tool result: %w", err)
	}
	return nil
}

// Pool reuses clients across requests, keyed by connector name and config,
// so a connector whose config changes gets a new session.
type Pool struct {
	mu      sync.Mutex
	clients map[string]pooled
}

type pooled struct {
	fingerprint string
	client      *Client
}

func NewPool() *Pool { return &Pool{clients: map[string]pooled{}} }

// Get returns the pooled client for name, creating it (and closing a stale
// one) when config, a map of everything the client depends on, changed.
func (p *Pool) Get(name string, config map[string]string, build func() (Config, error)) (*Client, error) {
	fp := fingerprint(config)
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[name]; ok {
		if c.fingerprint == fp {
			return c.client, nil
		}
		_ = c.client.Close()
		delete(p.clients, name)
	}
	cfg, err := build()
	if err != nil {
		return nil, err
	}
	c, err := New(cfg)
	if err != nil {
		return nil, err
	}
	p.clients[name] = pooled{fingerprint: fp, client: c}
	return c, nil
}

// Close closes every pooled client.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for name, c := range p.clients {
		_ = c.client.Close()
		delete(p.clients, name)
	}
}

func fingerprint(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%q=%q\n", k, m[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}
