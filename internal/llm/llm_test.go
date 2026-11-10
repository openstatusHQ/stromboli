package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

func envOf(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

func TestRegistryResolvesIDs(t *testing.T) {
	r := FromEnv(envOf(map[string]string{
		"ANTHROPIC_API_KEY":                "sk-ant",
		"OPENAI_API_KEY":                   "sk-oa",
		"STROMBOLI_PROVIDER_GROQ_BASE_URL": "https://api.groq.com/openai/v1",
	}))
	for id, want := range map[string]string{
		"claude-opus-5-5":            "anthropic:claude-opus-5-5", // no prefix: Anthropic
		"anthropic:claude-haiku-5-5": "anthropic:claude-haiku-5-5",
		"openai:gpt-5":               "openai:gpt-5",
		"ollama:qwen3:32b":           "ollama:qwen3:32b", // only the first colon splits
		"groq:llama-3.3-70b":         "groq:llama-3.3-70b",
	} {
		m, err := r.Model(id)
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		if m.ID() != want {
			t.Errorf("%s resolved to %s, want %s", id, m.ID(), want)
		}
	}
	for id, wantErr := range map[string]string{
		"nope:model":        "unknown provider",
		"openrouter:x":      "OPENROUTER_API_KEY is not set",
		"google:gemini-2.5": "GEMINI_API_KEY is not set",
		"openai:":           "missing model name",
	} {
		if _, err := r.Model(id); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("%s: got %v, want %q", id, err, wantErr)
		}
	}
	if _, err := FromEnv(envOf(nil)).Model("claude-opus-5-5"); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("anthropic without a key should fail, got %v", err)
	}
}

// fakeAnthropic answers with one tool call and records the requests.
type fakeAnthropic struct {
	requests []anthropic.BetaMessageNewParams
}

func (f *fakeAnthropic) New(_ context.Context, p anthropic.BetaMessageNewParams, _ ...option.RequestOption) (*anthropic.BetaMessage, error) {
	f.requests = append(f.requests, p)
	var m anthropic.BetaMessage
	err := json.Unmarshal([]byte(`{"id":"msg","type":"message","role":"assistant","model":"claude-haiku-5-5","stop_reason":"tool_use",
		"content":[{"type":"thinking","thinking":"","signature":"sig"},{"type":"text","text":"Checking logs."},
		{"type":"tool_use","id":"tu_1","name":"log_patterns","input":{"service":"api"}}],
		"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":90,"cache_creation_input_tokens":0}}`), &m)
	return &m, err
}

func TestConversationMovesFromAnthropicToOpenAI(t *testing.T) {
	ctx := context.Background()
	fa := &fakeAnthropic{}
	triage, _ := Anthropic(fa)("claude-haiku-5-5")

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-oa" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		fmt.Fprint(w, `{"model":"gpt-5","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,
			"tool_calls":[{"id":"call_1","type":"function","function":{"name":"submit_report","arguments":"{\"summary\":\"db\"}"}}]}}],
			"usage":{"prompt_tokens":200,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":150}}}`)
	}))
	defer srv.Close()
	investigate, _ := OpenAICompatible(OpenAIConfig{Name: "openai", BaseURL: srv.URL + "/v1/", APIKey: "sk-oa"})("gpt-5")

	req := Request{
		System:    "You are an SRE.",
		Messages:  []Message{UserText("api is failing")},
		Tools:     []ToolDef{{Name: "log_patterns", Description: "logs", Schema: map[string]any{"type": "object", "properties": map[string]any{"service": map[string]any{"type": "string"}}, "required": []string{"service"}, "additionalProperties": false}, Strict: true}},
		MaxTokens: 1000,
		Effort:    "low",
	}
	resp, err := triage.Generate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stop != StopToolUse || resp.Text() != "Checking logs." || len(resp.ToolCalls()) != 1 ||
		string(resp.ToolCalls()[0].Input) != `{"service":"api"}` || resp.Usage.TotalInput() != 100 {
		t.Fatalf("unexpected anthropic response: %+v", resp)
	}
	p := fa.requests[0]
	if p.OutputConfig.Effort != "low" || p.Tools[0].OfTool.InputSchema.Required[0] != "service" || !p.Tools[0].OfTool.Strict.Value {
		t.Errorf("anthropic request lost settings: effort=%q", p.OutputConfig.Effort)
	}
	if p.Model != "claude-haiku-5-5" || len(p.Betas) != 0 {
		t.Errorf("haiku should not get server-side fallbacks: %v", p.Betas)
	}

	req.Messages = append(req.Messages, resp.Message, UserMessage(ToolResultPart("tu_1", "5 errors", false), TextPart("Mitigation published.")))

	// Replaying to Anthropic keeps the thinking block.
	if _, err := triage.Generate(ctx, req); err != nil {
		t.Fatal(err)
	}
	if a := fa.requests[1].Messages[1].OfAssistant; a == nil || len(a.Content) != 3 || a.Content[0].OfThinking == nil {
		t.Errorf("anthropic replay should keep the native assistant message")
	}

	// The same conversation continues on OpenAI.
	req.Effort = "max"
	resp, err = investigate.Generate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stop != StopToolUse || resp.ToolCalls()[0].Name != "submit_report" || resp.Usage.CacheReadTokens != 150 || resp.Usage.InputTokens != 50 {
		t.Fatalf("unexpected openai response: %+v", resp)
	}
	msgs := got["messages"].([]any)
	roles := []string{}
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool,user" {
		t.Errorf("roles = %v", roles)
	}
	asst := msgs[2].(map[string]any)
	call := asst["tool_calls"].([]any)[0].(map[string]any)
	if asst["content"] != "Checking logs." || call["id"] != "tu_1" || call["function"].(map[string]any)["arguments"] != `{"service":"api"}` {
		t.Errorf("assistant message = %v", asst)
	}
	if tool := msgs[3].(map[string]any); tool["tool_call_id"] != "tu_1" || tool["content"] != "5 errors" {
		t.Errorf("tool message = %v", tool)
	}
	if got["reasoning_effort"] != "high" || got["max_completion_tokens"] != float64(1000) {
		t.Errorf("effort/max tokens = %v / %v", got["reasoning_effort"], got["max_completion_tokens"])
	}
	if _, ok := got["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["strict"]; ok {
		t.Error("strict should not be sent to OpenAI-compatible endpoints")
	}
}

func TestOpenAIErrors(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, `{"error":{"message":"model not found"}}`, http.StatusNotFound)
	}))
	defer srv.Close()
	m, _ := OpenAICompatible(OpenAIConfig{Name: "ollama", BaseURL: srv.URL})("nope")
	_, err := m.Generate(context.Background(), Request{Messages: []Message{UserText("hi")}, MaxTokens: 10})
	if err == nil || !strings.Contains(err.Error(), "model not found") || calls != 1 {
		t.Errorf("4xx should fail without retry: %v (calls=%d)", err, calls)
	}
}

func TestDeepSeekReasoningRoundTrip(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-ds" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		fmt.Fprint(w, `{"model":"deepseek-v4-pro","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"",
			"reasoning_content":"The pool looks exhausted.",
			"tool_calls":[{"id":"call_0","type":"function","function":{"name":"log_patterns","arguments":"{}"}}]}}],
			"usage":{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":80}}}`)
	}))
	defer srv.Close()

	r := FromEnv(envOf(map[string]string{"DEEPSEEK_API_KEY": "sk-ds", "DEEPSEEK_BASE_URL": srv.URL, "OPENAI_API_KEY": "sk-oa"}))
	ds, err := r.Model("deepseek:deepseek-v4-pro")
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Messages: []Message{UserText("api is failing")}, MaxTokens: 1000, Effort: "max"}
	resp, err := ds.Generate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stop != StopToolUse || resp.Usage.CacheReadTokens != 80 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if bodies[0]["reasoning_effort"] != "max" || bodies[0]["max_tokens"] != float64(1000) {
		t.Errorf("effort/max tokens = %v / %v", bodies[0]["reasoning_effort"], bodies[0]["max_tokens"])
	}

	// DeepSeek requires its reasoning back in later requests of a tool loop.
	req.Messages = append(req.Messages, resp.Message, UserMessage(ToolResultPart("call_0", "5 errors", false)))
	if _, err := ds.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	asst := bodies[1]["messages"].([]any)[1].(map[string]any)
	if asst["reasoning_content"] != "The pool looks exhausted." {
		t.Errorf("reasoning_content not sent back: %v", asst)
	}

	// Another provider never sees it.
	for _, m := range openaiMessages(resp.Message, "openai") {
		if m.ReasoningContent != "" {
			t.Error("reasoning_content leaked to another provider")
		}
	}
}

func TestGatewayRoutesByModel(t *testing.T) {
	// The direct Anthropic key must not leak into gateway requests.
	t.Setenv("ANTHROPIC_API_KEY", "sk-direct")
	t.Setenv("ANTHROPIC_BASE_URL", "https://api.anthropic.com")

	var paths []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gw-key" || r.Header.Get("X-Api-Key") != "" {
			http.Error(w, "bad auth: "+r.Header.Get("Authorization")+" / "+r.Header.Get("X-Api-Key"), http.StatusUnauthorized)
			return
		}
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		paths, bodies = append(paths, r.URL.Path), append(bodies, b)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/messages":
			if beta := r.Header.Get("Anthropic-Beta"); beta != "" {
				t.Errorf("no Anthropic betas through the gateway, got %q", beta)
			}
			fmt.Fprint(w, `{"id":"msg","type":"message","role":"assistant","model":"anthropic/claude-opus-5.5","stop_reason":"end_turn",
				"content":[{"type":"text","text":"restart api"}],"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":40,"cache_creation_input_tokens":0}}`)
		case "/v1/chat/completions":
			fmt.Fprint(w, `{"model":"openai/gpt-5","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,
				"reasoning":"look at logs","reasoning_details":[{"type":"reasoning.encrypted","data":"enc","format":"openai-responses-v1"}],
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"log_patterns","arguments":"{}"}}]}}],
				"usage":{"prompt_tokens":20,"completion_tokens":5}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	r := FromEnv(envOf(map[string]string{"AI_GATEWAY_API_KEY": "gw-key", "AI_GATEWAY_BASE_URL": srv.URL}))
	ctx := context.Background()

	// AI SDK style: a bare "creator/model" id goes through the gateway.
	gpt, err := r.Model("openai/gpt-5")
	if err != nil || gpt.ID() != "gateway:openai/gpt-5" {
		t.Fatalf("openai/gpt-5 = %v, %v", gpt, err)
	}
	req := Request{Messages: []Message{UserText("api is failing")}, MaxTokens: 100, Effort: "xhigh"}
	resp, err := gpt.Generate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if bodies[0]["model"] != "openai/gpt-5" || bodies[0]["reasoning_effort"] != "xhigh" {
		t.Errorf("chat request = %v", bodies[0])
	}
	req.Messages = append(req.Messages, resp.Message, UserMessage(ToolResultPart("call_1", "ok", false)))
	if _, err := gpt.Generate(ctx, req); err != nil {
		t.Fatal(err)
	}
	asst := bodies[1]["messages"].([]any)[1].(map[string]any)
	if d, ok := asst["reasoning_details"].([]any); !ok || d[0].(map[string]any)["data"] != "enc" {
		t.Errorf("reasoning_details not sent back: %v", asst)
	}

	// Claude models use the gateway's Anthropic Messages API.
	claude, err := r.Model("gateway:anthropic/claude-opus-5.5")
	if err != nil {
		t.Fatal(err)
	}
	resp, err = claude.Generate(ctx, Request{Messages: req.Messages, MaxTokens: 100, Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text() != "restart api" || resp.Usage.CacheReadTokens != 40 {
		t.Errorf("unexpected response: %+v", resp)
	}
	b := bodies[2]
	if paths[2] != "/v1/messages" || b["model"] != "anthropic/claude-opus-5.5" || b["output_config"].(map[string]any)["effort"] != "low" || b["fallbacks"] != nil {
		t.Errorf("messages request = %s %v", paths[2], b)
	}
	// The GPT turn was rebuilt for Anthropic, without its OpenAI reasoning.
	if a := b["messages"].([]any)[1].(map[string]any)["content"].([]any); len(a) != 1 || a[0].(map[string]any)["type"] != "tool_use" {
		t.Errorf("assistant turn = %v", a)
	}

	if _, err := FromEnv(envOf(nil)).Model("openai/gpt-5"); err == nil || !strings.Contains(err.Error(), "AI_GATEWAY_API_KEY") {
		t.Errorf("gateway without a key should fail, got %v", err)
	}
	if _, err := r.Model("gateway:gpt-5"); err == nil || !strings.Contains(err.Error(), "creator/model") {
		t.Errorf("gateway ids need a creator, got %v", err)
	}
}
