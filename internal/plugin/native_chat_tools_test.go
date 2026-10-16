package plugin

import (
	"testing"

	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
)

func TestPreparedNativeChatToolDefaults(t *testing.T) {
	engine := newTestEngine(t)

	engine.testConfig(t, "http://127.0.0.1:18080")
	raw := `{"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{}}}}]}`
	for _, tc := range []struct {
		provider, model string
		strict, stream  bool
	}{{"openai", "generic", true, false}, {"moonshotai", "generic", false, false}, {"nvidia", "generic", false, false}, {"togetherai", "generic", false, false}, {"cloudflare-ai-gateway", "generic", false, false}, {"zai", "glm-5", true, true}, {"zhipuai", "glm-4.5", true, false}, {"zai", "glm-4.5-air", true, false}, {"zai", "glm-4.5-flash", true, false}, {"zai", "glm-4.5v", true, false}} {
		body, err := engine.prepareSemanticFixture(t, "openai", "openai", raw, func(r *catalog.Route) { r.Canonical = tc.provider; r.ModelID = tc.model })
		if err != nil {
			t.Fatal(tc, err)
		}
		function := object(object(list(body["tools"])[0])["function"])
		value, exists := function["strict"]
		if exists != tc.strict || exists && value != false {
			t.Fatal("strict default", tc, body)
		}
		if (body["tool_stream"] == true) != tc.stream {
			t.Fatal("tool stream default", tc, body)
		}
	}
	// Explicit caller strictness survives supported serialization.
	strict := `{"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"f","strict":true,"parameters":{"type":"object"}}}]}`
	body, err := engine.prepareSemanticFixture(t, "openai", "openai", strict, nil)
	if err != nil || object(object(list(body["tools"])[0])["function"])["strict"] != true {
		t.Fatal(body, err)
	}
	if _, err := engine.prepareSemanticFixture(t, "openai", "openai", strict, func(r *catalog.Route) { r.Canonical = "nvidia" }); err == nil {
		t.Fatal("explicit strict silently lost")
	}
	body, err = engine.prepareSemanticFixture(t, "openai", "openai", raw, func(r *catalog.Route) {
		r.Canonical = "nvidia"
		r.Compatibility = map[string]any{"supportsStrictMode": true, "zaiToolStream": true}
	})
	if err != nil || body["tool_stream"] != true || object(object(list(body["tools"])[0])["function"])["strict"] != false {
		t.Fatal(body, err)
	}
	body, err = engine.prepareSemanticFixture(t, "openai", "openai", raw, func(r *catalog.Route) {
		r.Canonical = "zai"
		r.Compatibility = map[string]any{"supportsStrictMode": false, "zaiToolStream": false}
	})
	if err != nil || body["tool_stream"] != nil || object(object(list(body["tools"])[0])["function"])["strict"] != nil {
		t.Fatal(body, err)
	}
}

func TestPreparedNativeChatToolHistoryNormalization(t *testing.T) {
	engine := newTestEngine(t)

	engine.testConfig(t, "http://127.0.0.1:18080")
	raw := `{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1234567890","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1234567890","content":"OK"},{"role":"user","content":"continue"}]}`
	for _, model := range []string{"mistral-fixture", "devstral-fixture", "codestral-fixture", "pixtral-fixture", "mixtral-fixture"} {
		body, err := engine.prepareSemanticFixture(t, "openai", "openai", raw, func(r *catalog.Route) { r.ModelID = model })
		if err != nil {
			t.Fatal(err)
		}
		messages := list(body["messages"])
		id := object(list(object(messages[0])["tool_calls"])[0])["id"]
		if id != "call12345" || object(messages[1])["tool_call_id"] != id || len(messages) != 4 || object(messages[2])["content"] != "Done." {
			t.Fatal(body)
		}
	}
	body, err := engine.prepareSemanticFixture(t, "openai", "openai", raw, func(r *catalog.Route) {
		r.ModelID = "mistral-fixture"
		r.Compatibility = map[string]any{"requireAssistantAfterTool": false}
	})
	if err != nil || len(list(body["messages"])) != 3 {
		t.Fatal(body, err)
	}
	body, err = engine.prepareSemanticFixture(t, "openai", "openai", raw, func(r *catalog.Route) { r.Compatibility = map[string]any{"requireAssistantAfterTool": true} })
	if err != nil || len(list(body["messages"])) != 4 {
		t.Fatal(body, err)
	}
	collision := `{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1234567890","type":"function","function":{"name":"f","arguments":"{}"}},{"id":"call_1234567891","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"user","content":"continue"}]}`
	if _, err := engine.prepareSemanticFixture(t, "openai", "openai", collision, func(r *catalog.Route) { r.ModelID = "mistral-fixture" }); err == nil {
		t.Fatal("collision accepted")
	}
	// The authorized final raw body remains the last semantic barrier.
	body, err = engine.prepareSemanticFixture(t, "openai", "openai", raw, func(r *catalog.Route) {
		r.ModelID = "mistral-fixture"
		r.Body = map[string]any{"messages": []any{map[string]any{"role": "user", "content": "final overlay"}}, "tool_stream": false}
	})
	if err != nil || len(list(body["messages"])) != 1 || body["tool_stream"] != false {
		t.Fatal(body, err)
	}
}

func TestPreparedReasoningBudgetNeverBecomesGuessedEffort(t *testing.T) {
	engine := newTestEngine(t)

	engine.testConfig(t, "http://127.0.0.1:18080")
	raw := `{"messages":[{"role":"user","content":"hello"}],"max_tokens":10000,"thinking":{"type":"enabled","budget_tokens":2345}}`
	if _, err := engine.prepareSemanticFixture(t, "claude", "openai-response", raw, nil); err == nil {
		t.Fatal("numeric budget approximated as effort")
	}
	if _, err := engine.prepareSemanticFixture(t, "openai", "claude", `{"messages":[{"role":"user","content":"hello"}],"reasoning_effort":"high"}`, nil); err == nil {
		t.Fatal("effort approximated as numeric budget")
	}
	body, err := engine.prepareSemanticFixture(t, "openai", "openai-response", `{"messages":[{"role":"user","content":"hello"}],"reasoning_effort":"high"}`, nil)
	if err != nil || object(body["reasoning"])["effort"] != "high" {
		t.Fatal(body, err)
	}
}
