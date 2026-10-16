package protocol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
)

func nativeTestBody(t *testing.T, text string) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func nativeTestRoute(protocol string) catalog.Route {
	return catalog.Route{Protocol: protocol, ProviderID: "custom", ModelID: "custom-model", Settings: map[string]any{}, Compatibility: map[string]any{}}
}

func TestNativeThinkingBudgetExactHD(t *testing.T) {
	cases := []struct {
		name                           string
		budget, maximum, minimum, want float64
		hasMaximum                     bool
	}{
		{"absent max", 9000, 0, 1024, 9000, false},
		{"below threshold unchanged", 200, 4000, 1024, 200, true},
		{"equal threshold unchanged", 2000, 4000, 1024, 2000, true},
		{"over threshold halved", 4000, 5001, 1024, 2500, true},
		{"minimum only on reduction", 1000, 1500, 1024, 1024, true},
		{"negative auto budget", -1, 5000, 512, -1, true},
		{"zero disable budget", 0, 5000, 512, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nativeThinkingBudget(tc.budget, tc.maximum, tc.minimum, tc.hasMaximum); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestNativeAnthropicThinkingAndBinding(t *testing.T) {
	r := nativeTestRoute("claude")
	r.Settings["thinking"] = map[string]any{"type": "enabled", "budgetTokens": 30000.0, "display": "summarized"}
	body := nativeTestBody(t, `{"max_tokens":10000,"messages":[{"role":"user","content":[{"type":"text","text":"Keep me"}]}]}`)
	if err := normalizeNativeBody(body, r, Options{}); err != nil {
		t.Fatal(err)
	}
	thinking := body["thinking"].(map[string]any)
	if thinking["budget_tokens"] != 5000.0 || thinking["display"] != "summarized" {
		t.Fatalf("unexpected thinking: %#v", thinking)
	}
	if _, exists := thinking["budgetTokens"]; exists {
		t.Fatal("camelCase budget leaked")
	}
	if _, exists := r.Settings["thinking"].(map[string]any)["budget_tokens"]; exists {
		t.Fatal("mutated route settings")
	}
	r = nativeTestRoute("claude")
	r.ModelID = "claude-sonnet-5-1"
	body = nativeTestBody(t, `{"messages":[]}`)
	if err := normalizeNativeBody(body, r, Options{}); err != nil {
		t.Fatal(err)
	}
	thinking = body["thinking"].(map[string]any)
	if thinking["type"] != "adaptive" || thinking["block_binding"].(map[string]any)["prefix_mismatch_behavior"] != "drop_block" {
		t.Fatalf("binding default missing: %#v", thinking)
	}
	r.Settings["thinking"] = map[string]any{"type": "disabled"}
	if err := normalizeNativeBody(body, r, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, exists := body["thinking"].(map[string]any)["block_binding"]; exists {
		t.Fatal("binding enabled for disabled thinking")
	}
}

func TestNativeAnthropicDefaultBudgetAndExplicitLowBudget(t *testing.T) {
	r := nativeTestRoute("claude")
	for _, tc := range []struct{ budget, want float64 }{{30000, 16000}, {100, 100}} {
		body := map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": tc.budget}}
		if err := normalizeNativeBody(body, r, Options{}); err != nil {
			t.Fatal(err)
		}
		if got := body["thinking"].(map[string]any)["budget_tokens"]; got != tc.want {
			t.Fatalf("got %v want %v", got, tc.want)
		}
	}
}

func TestNativeAnthropicHistoryPreservationAndCacheOrder(t *testing.T) {
	r := nativeTestRoute("claude")
	body := nativeTestBody(t, `{
  "tools":[{"name":"tool","input_schema":{"type":"object","properties":{"cache_control":{"type":"string"}}},"cache_control":{"type":"ephemeral"}}],
  "system":[{"type":"text","text":"Caller system","cache_control":{"type":"ephemeral"}}],
  "messages":[
   {"role":"user","content":[{"type":"text","text":" \n "},{"type":"text","text":"Caller prompt","cache_control":{"type":"ephemeral"}}]},
   {"role":"assistant","content":[{"type":"thinking","thinking":"Original reasoning","signature":"original-signed-by-provider","cache_control":{"type":"ephemeral"}},{"type":"redacted_thinking","data":"encrypted-original"},{"type":"tool_use","id":"call_1","name":"tool","input":{"cache_control":{"retain":"tool argument"}}}]},
   {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"result","cache_control":{"type":"ephemeral"}}]},
   {"role":"user","content":" \t "}
  ]}`)
	if err := normalizeNativeBody(body, r, Options{}); err != nil {
		t.Fatal(err)
	}
	messages := body["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("empty message retained: %#v", messages)
	}
	userBlocks := messages[0].(map[string]any)["content"].([]any)
	if len(userBlocks) != 1 || userBlocks[0].(map[string]any)["text"] != "Caller prompt" {
		t.Fatal("incorrect text omission")
	}
	assistant := messages[1].(map[string]any)["content"].([]any)
	if assistant[0].(map[string]any)["signature"] != "original-signed-by-provider" || assistant[1].(map[string]any)["data"] != "encrypted-original" {
		t.Fatal("reasoning data changed")
	}
	toolInput := assistant[2].(map[string]any)["input"].(map[string]any)
	if toolInput["cache_control"] == nil {
		t.Fatal("tool argument was mistaken for a cache hint")
	}
	last := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if _, exists := last["cache_control"]; exists {
		t.Fatal("fifth breakpoint retained")
	}
	if assistant[0].(map[string]any)["cache_control"] == nil {
		t.Fatal("earlier fourth breakpoint dropped")
	}
}

func TestNativeAnthropicSignatureRules(t *testing.T) {
	for _, tc := range []struct {
		name, provider, model, base string
		override                    any
		permitted                   bool
	}{
		{name: "default required"},
		{name: "explicit required wins", provider: "moonshotai", override: true},
		{name: "explicit off", override: false, permitted: true},
		{name: "moonshot", provider: "moonshotai", permitted: true},
		{name: "kimi model", model: "kimi-test", permitted: true},
		{name: "mimo model", model: "mimo-v2", permitted: true},
		{name: "kimi URL", base: "https://api.kimi.com/coding/v1", permitted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := nativeTestRoute("claude")
			r.ProviderID, r.ModelID, r.BaseURL = tc.provider, tc.model, tc.base
			if tc.override != nil {
				r.Compatibility["requireSignature"] = tc.override
			}
			body := nativeTestBody(t, `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"retain"}]}]}`)
			err := normalizeNativeBody(body, r, Options{})
			if (err == nil) != tc.permitted {
				t.Fatalf("err=%v permitted=%v", err, tc.permitted)
			}
			block := body["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
			if _, exists := block["signature"]; exists {
				t.Fatal("synthesized a signature")
			}
		})
	}
}

func TestNativeAnthropicProviderOptionsAndBetas(t *testing.T) {
	r := nativeTestRoute("claude")
	r.Settings = nativeTestBody(t, `{"serviceTier":"auto","service_tier":"standard_only","inferenceGeo":"us","cacheControl":{"type":"ephemeral"},"outputConfig":{"effort":"low","format":{"type":"json_schema","schema":{"type":"object"}}},"effort":"high","metadata":{"user_id":"caller"},"contextManagement":{"edits":[{"type":"compact_20260112","trigger":{"type":"input_tokens","value":50000},"pauseAfterCompaction":true,"instructions":"Caller compaction instruction"}]}}`)
	body := nativeTestBody(t, `{"messages":[{"role":"system","content":[],"output_config":{"effort":"medium"}}],"thinking":{"type":"adaptive","block_binding":{"prefix_mismatch_behavior":"drop_block"}}}`)
	if err := normalizeNativeBody(body, r, Options{}); err != nil {
		t.Fatal(err)
	}
	if body["service_tier"] != "standard_only" || body["output_config"].(map[string]any)["effort"] != "high" || body["inference_geo"] != "us" {
		t.Fatalf("wrong options: %#v", body)
	}
	edit := body["context_management"].(map[string]any)["edits"].([]any)[0].(map[string]any)
	if edit["pause_after_compaction"] != true || edit["instructions"] != "Caller compaction instruction" {
		t.Fatalf("wrong compaction: %#v", edit)
	}
	header := nativeHeaders(r, body).Get("anthropic-beta")
	want := "interleaved-thinking-2025-05-14,compact-2026-01-12,mid-conversation-output-config-2026-07-01,thinking-binding-controls-2026-08-01"
	if header != want {
		t.Fatalf("got %q want %q", header, want)
	}
	if got := nativeHeaders(r, map[string]any{}).Get("anthropic-beta"); got != "interleaved-thinking-2025-05-14" {
		t.Fatalf("base beta %q", got)
	}
	if len(nativeHeaders(nativeTestRoute("openai"), body)) != 0 {
		t.Fatal("Anthropic betas leaked to OpenAI")
	}
}

func TestNativeGeminiOptionsAndBudget(t *testing.T) {
	r := nativeTestRoute("gemini")
	r.Settings = nativeTestBody(t, `{"thinkingConfig":{"thinkingBudget":6000},"cachedContent":"cachedContents/caller","safetySettings":[{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"BLOCK_ONLY_HIGH"}],"serviceTier":"priority"}`)
	body := nativeTestBody(t, `{"generationConfig":{"maxOutputTokens":5001,"temperature":0.2},"contents":[{"role":"model","parts":[{"functionCall":{"id":"call1","name":"tool","args":{}},"thoughtSignature":"real-sig"}]}]}`)
	before, _ := json.Marshal(body["contents"])
	if err := normalizeNativeBody(body, r, Options{}); err != nil {
		t.Fatal(err)
	}
	thinking := body["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)
	if thinking["thinkingBudget"] != 2500.0 || thinking["includeThoughts"] != true {
		t.Fatalf("wrong Gemini thinking: %#v", thinking)
	}
	after, _ := json.Marshal(body["contents"])
	if string(before) != string(after) {
		t.Fatal("Gemini content/ID/signature changed")
	}
	if body["cachedContent"] != "cachedContents/caller" || body["serviceTier"] != "priority" {
		t.Fatal("missing Gemini provider options")
	}
	r.Settings["thinkingConfig"] = map[string]any{"thinkingBudget": -1.0, "includeThoughts": false}
	if err := normalizeNativeBody(body, r, Options{}); err != nil {
		t.Fatal(err)
	}
	thinking = body["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)
	if thinking["thinkingBudget"] != -1.0 || thinking["includeThoughts"] != false {
		t.Fatal("explicit auto budget/false changed")
	}
}

func TestNativeResponsesProviderOptionMapping(t *testing.T) {
	r := nativeTestRoute("openai-response")
	r.Settings = nativeTestBody(t, `{"store":false,"metadata":{"caller":"value"},"safetyIdentifier":"safe-id","streamOptions":{"includeObfuscation":false},"topLogprobs":7,"reasoningEffort":"high","reasoningSummary":"detailed","include":["message.output_text.logprobs"],"textVerbosity":"low","serviceTier":"priority","truncation":"disabled","maxToolCalls":3,"parallelToolCalls":false,"allowedTools":{"toolNames":["lookup"]},"contextManagement":[{"type":"compaction","compactThreshold":90000}]}`)
	body := nativeTestBody(t, `{"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"input":[{"type":"reasoning","id":"rs_1","encrypted_content":"original-encrypted","summary":[]}],"text":{"format":{"type":"json_object"}},"stream_options":{"existing":true},"prompt_cache_key":"caller-key"}`)
	history := catalog.Clone(body["input"])
	if err := normalizeNativeBody(body, r, Options{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(history, body["input"]) {
		t.Fatal("Responses encrypted history changed")
	}
	for key, want := range map[string]any{"store": false, "safety_identifier": "safe-id", "top_logprobs": 7.0, "service_tier": "priority", "truncation": "disabled", "max_tool_calls": 3.0, "parallel_tool_calls": false, "prompt_cache_key": "caller-key"} {
		if body[key] != want {
			t.Fatalf("%s got %#v want %#v", key, body[key], want)
		}
	}
	reasoning := body["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" || reasoning["summary"] != "detailed" {
		t.Fatal("reasoning mapping missing")
	}
	text := body["text"].(map[string]any)
	if text["verbosity"] != "low" || text["format"] == nil {
		t.Fatal("text format overwritten")
	}
	stream := body["stream_options"].(map[string]any)
	if stream["include_obfuscation"] != false || stream["existing"] != true {
		t.Fatal("stream settings overwritten")
	}
	choice := body["tool_choice"].(map[string]any)
	if choice["type"] != "allowed_tools" || choice["mode"] != "auto" {
		t.Fatal("allowed tools mapping missing")
	}
	if body["context_management"].([]any)[0].(map[string]any)["compact_threshold"] != 90000.0 {
		t.Fatal("compaction threshold mapping missing")
	}
}

func TestNativeChatCallerPayloadAndEmptyTools(t *testing.T) {
	r := nativeTestRoute("openai")
	r.Settings = map[string]any{"reasoningEffort": "high", "store": false}
	body := nativeTestBody(t, `{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"original-id","type":"function","function":{"name":"tool","arguments":"{}"}}]},{"role":"tool","tool_call_id":"original-id","content":"result"}],"parallel_tool_calls":true,"tool_choice":"required"}`)
	history := catalog.Clone(body["messages"])
	body["prompt_cache_key"] = strings.Repeat("界", 70)
	if err := normalizeNativeBody(body, r, Options{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(history, body["messages"]) || body["parallel_tool_calls"] != true {
		t.Fatal("caller data changed")
	}
	if tools, ok := body["tools"].([]any); !ok || len(tools) != 0 {
		t.Fatal("historical tool calls require empty tools")
	}
	if _, exists := body["tool_choice"]; exists {
		t.Fatal("tool_choice retained without active tools")
	}
	if len([]rune(body["prompt_cache_key"].(string))) != 64 {
		t.Fatal("cache key not Unicode-clipped")
	}
}

func TestNativeUnsupportedOrMalformedSettingsFailClosed(t *testing.T) {
	tests := []struct{ protocol, settings string }{
		{"openai", `{"parallelToolCalls":true}`}, {"openai", `{"textVerbosity":"high"}`},
		{"openai", `{"promptCacheKey":"provider-option-is-not-canonical-request"}`},
		{"openai", `{"unknownCriticalFeature":true}`},
		{"claude", `{"speed":"fast"}`}, {"claude", `{"thinking":{"type":"enabled"}}`},
		{"claude", `{"contextManagement":{"edits":[{"type":"unsupported"}]}}`},
		{"gemini", `{"thinkingConfig":{"extraCriticalFeature":true}}`},
		{"openai-response", `{"topLogprobs":21}`}, {"openai-response", `{"streamOptions":{"unknown":true}}`},
		{"openai-response", `{"allowedTools":{"toolNames":["tool"]}}`},
	}
	for _, tc := range tests {
		t.Run(tc.protocol+tc.settings, func(t *testing.T) {
			r := nativeTestRoute(tc.protocol)
			r.Settings = nativeTestBody(t, tc.settings)
			if err := normalizeNativeBody(map[string]any{}, r, Options{}); err == nil {
				t.Fatal("unsupported setting silently accepted")
			}
		})
	}
}

func TestNativeFinalOverlayCannotBypassCriticalChecks(t *testing.T) {
	r := nativeTestRoute("claude")
	unsigned := nativeTestBody(t, `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"missing signature"}]}]}`)
	if err := validateNativeBody(unsigned, r); err == nil {
		t.Fatal("unsigned final overlay allowed")
	}
	many := nativeTestBody(t, `{"messages":[{"role":"user","content":[{"type":"text","text":"1","cache_control":{}},{"type":"text","text":"2","cache_control":{}},{"type":"text","text":"3","cache_control":{}},{"type":"text","text":"4","cache_control":{}},{"type":"text","text":"5","cache_control":{}}]}]}`)
	if err := validateNativeBody(many, r); err == nil {
		t.Fatal("fifth final-overlay cache breakpoint allowed")
	}
	original := catalog.Clone(many)
	_ = validateNativeBody(many, r)
	if !reflect.DeepEqual(original, many) {
		t.Fatal("validation mutated body")
	}
}

func TestNativeAnthropicNestedOptionsFailClosed(t *testing.T) {
	for _, settings := range []string{
		`{"thinking":{"type":"disabled","block_binding":{"prefix_mismatch_behavior":"drop_block"}}}`,
		`{"metadata":{"user_id":"u","unsupported":"x"}}`,
		`{"outputConfig":{"format":{"type":"text"}}}`,
		`{"outputConfig":{"unsupported":true}}`,
		`{"container":{"unsupported":true}}`,
		`{"cacheControl":{"type":"persistent"}}`,
	} {
		r := nativeTestRoute("claude")
		r.Settings = nativeTestBody(t, settings)
		if err := normalizeNativeBody(map[string]any{}, r, Options{}); err == nil {
			t.Fatalf("accepted %s", settings)
		}
	}
}

func TestNativeOpenAIY0Defaults(t *testing.T) {
	for _, protocol := range []string{"openai", "openai-response"} {
		for _, tc := range []struct {
			model     string
			reasoning bool
		}{{"gpt-5", true}, {"gpt-5.2-codex", true}, {"gpt-5-chat-latest", false}, {"gpt-5-pro", false}, {"gpt-4.1", false}} {
			t.Run(protocol+tc.model, func(t *testing.T) {
				r := nativeTestRoute(protocol)
				r.Package = "@opencode/ai/providers/openai"
				r.ModelID = tc.model
				body := map[string]any{}
				if err := normalizeNativeBody(body, r, Options{}); err != nil {
					t.Fatal(err)
				}
				if body["store"] != false {
					t.Fatal("missing store default")
				}
				if protocol == "openai" {
					if got := body["reasoning_effort"] == "medium"; got != tc.reasoning {
						t.Fatalf("reasoning default=%v want=%v", got, tc.reasoning)
					}
				} else {
					reasoning, _ := body["reasoning"].(map[string]any)
					if got := reasoning["effort"] == "medium" && reasoning["summary"] == "auto"; got != tc.reasoning {
						t.Fatalf("reasoning defaults=%v want=%v", reasoning, tc.reasoning)
					}
				}
			})
		}
	}
	r := nativeTestRoute("openai-response")
	r.Package = "@opencode/ai/providers/openai"
	r.ModelID = "gpt-5.2"
	body := nativeTestBody(t, `{"store":true,"reasoning":{"effort":"low","summary":"detailed"},"include":["message.output_text.logprobs"]}`)
	want := catalog.Clone(body)
	if err := normalizeNativeBody(body, r, Options{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("defaults changed caller fields: %#v", body)
	}
	r.Settings = map[string]any{"reasoningEffort": "high", "store": false}
	if err := normalizeNativeBody(body, r, Options{}); err != nil {
		t.Fatal(err)
	}
	if body["store"] != false || body["reasoning"].(map[string]any)["effort"] != "high" {
		t.Fatal("explicit settings failed to override defaults/caller")
	}
	r.Package = "@opencode/ai/providers/openai-compatible/responses"
	r.Settings = map[string]any{}
	body = map[string]any{}
	if err := normalizeNativeBody(body, r, Options{}); err != nil {
		t.Fatal(err)
	}
	if body["reasoning"] != nil {
		t.Fatal("native OpenAI heuristic applied to compatible provider")
	}
}

func TestNativeNullSettingPreservesCallerValue(t *testing.T) {
	r := nativeTestRoute("openai")
	r.Settings["store"] = nil
	body := map[string]any{"store": true}
	if err := normalizeNativeBody(body, r, Options{}); err != nil {
		t.Fatal(err)
	}
	if body["store"] != true {
		t.Fatal("null provider option deleted caller field")
	}
}

func TestNativeAnthropicAutomaticCacheSettingPreserved(t *testing.T) {
	r := nativeTestRoute("claude")
	r.Settings["cacheControl"] = map[string]any{"type": "ephemeral"}
	body := nativeTestBody(t, `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"1","cache_control":{"type":"ephemeral"}},{"type":"text","text":"2","cache_control":{"type":"ephemeral"}},{"type":"text","text":"3","cache_control":{"type":"ephemeral"}}]}]}`)
	if err := normalizeNativeBody(body, r, Options{}); err != nil {
		t.Fatal(err)
	}
	if body["cache_control"] == nil || nativeCacheCount(body) != 4 {
		t.Fatal("automatic cache setting counted as explicit breakpoint")
	}
	if err := validateNativeBody(body, r); err != nil {
		t.Fatal(err)
	}
}

func TestNativeChatReasoningFieldRemapsAliases(t *testing.T) {
	for _, source := range []string{"reasoning_content", "reasoning", "reasoning_text"} {
		for _, target := range []string{"reasoning_content", "reasoning", "reasoning_text"} {
			t.Run(source+" to "+target, func(t *testing.T) {
				r := nativeTestRoute("openai")
				r.Compatibility["reasoningField"] = target
				body := nativeTestBody(t, `{"messages":[{"role":"assistant","content":"Keep answer","tool_calls":[{"id":"call-1","type":"function","function":{"name":"tool","arguments":"{}"}}],"reasoning_details":[{"type":"reasoning.encrypted","data":"original-ciphertext","signature":"original-signature"}]},{"role":"tool","tool_call_id":"call-1","content":"Keep result"}]}`)
				assistant := body["messages"].([]any)[0].(map[string]any)
				assistant[source] = "Caller reasoning"
				details := catalog.Clone(assistant["reasoning_details"])
				calls := catalog.Clone(assistant["tool_calls"])
				if err := normalizeNativeBody(body, r, Options{}); err != nil {
					t.Fatal(err)
				}
				if assistant[target] != "Caller reasoning" || assistant["content"] != "Keep answer" || !reflect.DeepEqual(calls, assistant["tool_calls"]) || !reflect.DeepEqual(details, assistant["reasoning_details"]) {
					t.Fatalf("reasoning/history changed: %#v", assistant)
				}
				for _, alias := range []string{"reasoning_content", "reasoning", "reasoning_text"} {
					if alias != target {
						if _, exists := assistant[alias]; exists {
							t.Fatalf("old alias %q survived", alias)
						}
					}
				}
			})
		}
	}
}

func TestNativeChatReasoningRequirement(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		configured            bool
		require               any
		provider, model, base string
		wantField             string
	}{
		{name: "plain untouched"},
		{name: "configured defaults required", configured: true, wantField: "reasoning_text"},
		{name: "configured explicit optional", configured: true, require: false},
		{name: "explicit required", require: true, wantField: "reasoning_content"},
		{name: "DeepSeek provider", provider: "deepseek", wantField: "reasoning_content"},
		{name: "DeepSeek model", model: "vendor/DeepSeek-V3", wantField: "reasoning_content"},
		{name: "DeepSeek URL", base: "https://api.deepseek.com/v1", wantField: "reasoning_content"},
		{name: "DeepSeek explicitly optional", provider: "deepseek", require: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := nativeTestRoute("openai")
			r.ProviderID, r.ModelID, r.BaseURL = tc.provider, tc.model, tc.base
			if tc.configured {
				r.Compatibility["reasoningField"] = "reasoning_text"
			}
			if tc.require != nil {
				r.Compatibility["requireReasoning"] = tc.require
			}
			body := nativeTestBody(t, `{"messages":[{"role":"assistant","content":"Caller answer"},{"role":"user","content":"Caller question"}]}`)
			if err := normalizeNativeBody(body, r, Options{}); err != nil {
				t.Fatal(err)
			}
			assistant := body["messages"].([]any)[0].(map[string]any)
			if tc.wantField != "" {
				if assistant[tc.wantField] != "" {
					t.Fatalf("expected empty %s", tc.wantField)
				}
			} else if len(assistant) != 2 {
				t.Fatalf("unexpected reasoning inserted: %#v", assistant)
			}
			if len(body["messages"].([]any)[1].(map[string]any)) != 2 {
				t.Fatal("reasoning added to user history")
			}
		})
	}
}

func TestNativeChatReasoningConflictFailsClosed(t *testing.T) {
	r := nativeTestRoute("openai")
	r.Compatibility["reasoningField"] = "reasoning_text"
	for _, text := range []string{
		`{"messages":[{"role":"assistant","content":"answer","reasoning_content":"one","reasoning_text":"two"}]}`,
		`{"messages":[{"role":"assistant","content":"answer","reasoning":{"unsupported":"object"}}]}`,
	} {
		if err := normalizeNativeBody(nativeTestBody(t, text), r, Options{}); err == nil {
			t.Fatal("conflicting or unsupported reasoning silently accepted")
		}
	}
	body := nativeTestBody(t, `{"messages":[{"role":"assistant","content":"answer","reasoning_content":"same","reasoning":"same"}]}`)
	if err := normalizeNativeBody(body, r, Options{}); err != nil {
		t.Fatal(err)
	}
	assistant := body["messages"].([]any)[0].(map[string]any)
	if assistant["reasoning_text"] != "same" || len(assistant) != 3 {
		t.Fatal("identical reasoning aliases not normalized")
	}
}

func TestNativeReasoningCompatibilityRejectsUnknownValues(t *testing.T) {
	for _, compatibility := range []map[string]any{
		{"reasoningField": "content"}, {"reasoningField": "custom_reasoning"}, {"reasoningField": ""}, {"reasoningField": false},
		{"requireReasoning": "true"},
	} {
		r := nativeTestRoute("openai")
		r.Compatibility = compatibility
		if err := normalizeNativeBody(map[string]any{}, r, Options{}); err == nil {
			t.Fatalf("accepted compatibility %#v", compatibility)
		}
	}
}

func TestNativeCompatibilityFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		protocol, key string
		value         any
	}{
		{"openai", "supportsStreamOptions", false}, {"openai", "unknownFutureFlag", true},
		{"openai", "supportsUsageInStreaming", "false"}, {"openai", "supportsStore", nil},
		{"openai", "supportsPromptCacheKey", 1.0}, {"openai", "maxTokensField", "max_output_tokens"},
		{"openai", "sanitizer", "moonshot"}, {"openai", "cacheFormat", "anthropic"},
		{"openai", "supportsStrictMode", "invalid"}, {"openai", "requireAssistantAfterTool", "invalid"},
		{"openai", "zaiToolStream", "invalid"}, {"openai", "requireFinishReason", "invalid"},
		{"claude", "requireFinishReason", false}, {"claude", "supportsEffortUpdates", true},
		{"gemini", "requireSignature", true}, {"claude", "reasoningField", "reasoning_content"},
	} {
		t.Run(tc.protocol+tc.key, func(t *testing.T) {
			r := nativeTestRoute(tc.protocol)
			r.Compatibility[tc.key] = tc.value
			if err := normalizeNativeBody(map[string]any{}, r, Options{}); err == nil {
				t.Fatalf("silently ignored %s=%v", tc.key, tc.value)
			}
		})
	}
}

func TestNativeCompatibilitySupportedAndNoopValues(t *testing.T) {
	r := nativeTestRoute("openai")
	r.Compatibility = map[string]any{"reasoningField": "reasoning", "requireReasoning": false, "maxTokensField": "max_tokens", "supportsStore": false, "supportsUsageInStreaming": false, "supportsPromptCacheKey": true, "sanitizer": "none", "requireAssistantAfterTool": false, "supportsStrictMode": false, "zaiToolStream": false}
	if err := normalizeNativeBody(map[string]any{}, r, Options{}); err != nil {
		t.Fatal(err)
	}
	body := nativeTestBody(t, `{"tools":[{"type":"function","function":{"name":"tool","strict":true}}]}`)
	if err := normalizeNativeBody(body, r, Options{}); err == nil {
		t.Fatal("strict tool requirement silently ignored")
	}
	for _, protocol := range []string{"claude", "openai-response"} {
		r = nativeTestRoute(protocol)
		r.Compatibility["supportsEffortUpdates"] = false
		if err := normalizeNativeBody(map[string]any{}, r, Options{}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNativeChatReasoningSkipsEmptyAlias(t *testing.T) {
	r := nativeTestRoute("openai")
	r.Compatibility["reasoningField"] = "reasoning_text"
	body := nativeTestBody(t, `{"messages":[{"role":"assistant","content":"answer","reasoning_text":"","reasoning_content":"preserve actual reasoning"}]}`)
	if err := normalizeNativeBody(body, r, Options{}); err != nil {
		t.Fatal(err)
	}
	assistant := body["messages"].([]any)[0].(map[string]any)
	if assistant["reasoning_text"] != "preserve actual reasoning" {
		t.Fatal("empty alias masked real reasoning")
	}
}
