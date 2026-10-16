package protocol

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestResponsesConversionPreservesExplicitSemantics(t *testing.T) {
	raw := []byte(`{"model":"test","messages":[{"role":"user","content":"hello"}],"temperature":0.2,"top_p":0.7,"max_tokens":100,"max_completion_tokens":120,"service_tier":"flex"}`)
	b, e := translateUpstreamRequest(context.Background(), translator.FormatOpenAI, translator.FormatOpenAIResponse, "m", raw)
	if e != nil {
		t.Fatal(e)
	}
	var out map[string]any
	json.Unmarshal(b, &out)
	if out["temperature"] != 0.2 || out["top_p"] != 0.7 || out["max_output_tokens"] != float64(120) || out["service_tier"] != "flex" || out["parallel_tool_calls"] != nil {
		t.Fatalf("%s", b)
	}
	if object(out["reasoning"])["effort"] != nil {
		t.Fatal("unsolicited Codex reasoning", string(b))
	}
	claude := []byte(`{"model":"m","max_tokens":120,"messages":[{"role":"user","content":"hello"}],"tool_choice":{"type":"auto","disable_parallel_tool_use":true}}`)
	b, e = translateUpstreamRequest(context.Background(), translator.FormatClaude, translator.FormatOpenAIResponse, "m", claude)
	if e != nil {
		t.Fatal(e)
	}
	json.Unmarshal(b, &out)
	if out["parallel_tool_calls"] != false {
		t.Fatal("lost explicit sequential tool calls", string(b))
	}
}
func TestResponsesRejectsUnsupportedSampling(t *testing.T) {
	for _, extra := range []string{`,"stop":["END"]`, `,"top_k":40`} {
		raw := []byte(`{"messages":[{"role":"user","content":"hello"}]` + extra + `}`)
		if _, e := translateUpstreamRequest(context.Background(), translator.FormatOpenAI, translator.FormatOpenAIResponse, "m", raw); e == nil {
			t.Fatal("silently lost", extra)
		}
	}
	native := []byte(`{"input":"hi","temperature":0.2,"max_output_tokens":100,"store":true,"instructions":"preserve me"}`)
	got, e := translateUpstreamRequest(context.Background(), translator.FormatOpenAIResponse, translator.FormatOpenAIResponse, "m", native)
	if e != nil || string(got) != string(native) {
		t.Fatal("native altered", e, string(got))
	}
}
func TestChatReasoningFieldForSDK(t *testing.T) {
	r := catalog.Route{Protocol: "openai", Compatibility: map[string]any{"reasoningField": "reasoning_text"}}
	got, e := sdkResponseBody(r, []byte(`{"choices":[{"delta":{"reasoning_text":"think","content":"hello"}}]}`))
	if e != nil || !strings.Contains(string(got), `"reasoning_content":"think"`) {
		t.Fatal(string(got), e)
	}
	if _, e = sdkResponseBody(r, []byte(`{"choices":[{"message":{"reasoning_content":"one","reasoning_text":"two"}}]}`)); e == nil {
		t.Fatal("conflicting reasoning accepted")
	}
}
