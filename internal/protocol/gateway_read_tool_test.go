package protocol

import (
	"strings"
	"testing"

	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
)

func TestEnsureGatewayReadToolBehavior(t *testing.T) {
	opencodeRoute := catalog.Route{
		ProviderID: "opencode",
		BaseURL:    "https://opencode.ai/inference/openai/v1",
		Protocol:   "openai",
	}

	// 1. Client provides no tools: read injected, tool_choice set to none
	bodyNoTools := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	ensureGatewayReadTool(bodyNoTools, opencodeRoute, false)
	tools := list(bodyNoTools["tools"])
	if len(tools) != 1 {
		t.Fatalf("expected 1 injected tool, got %d", len(tools))
	}
	fn := object(object(tools[0])["function"])
	if fn["name"] != "read" {
		t.Fatalf("expected tool name read, got %v", fn["name"])
	}
	if bodyNoTools["tool_choice"] != "none" {
		t.Fatalf("expected tool_choice none, got %v", bodyNoTools["tool_choice"])
	}

	// 2. Client provides calc tool: read appended with suppression description, tool_choice unchanged
	bodyWithCalc := map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": "calc",
				},
			},
		},
		"tool_choice": "auto",
	}
	ensureGatewayReadTool(bodyWithCalc, opencodeRoute, false)
	toolsCalc := list(bodyWithCalc["tools"])
	if len(toolsCalc) != 2 {
		t.Fatalf("expected 2 tools (calc + read), got %d", len(toolsCalc))
	}
	fnRead := object(object(toolsCalc[1])["function"])
	if fnRead["name"] != "read" || !strings.Contains(fnRead["description"].(string), "DO NOT CALL") {
		t.Fatalf("expected suppressed read tool, got %#v", fnRead)
	}
	if bodyWithCalc["tool_choice"] != "auto" {
		t.Fatalf("tool_choice altered unexpectedly: %v", bodyWithCalc["tool_choice"])
	}

	// 3. Client already provides read tool: no duplicate added
	bodyWithRead := map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": "read",
				},
			},
		},
	}
	ensureGatewayReadTool(bodyWithRead, opencodeRoute, false)
	toolsRead := list(bodyWithRead["tools"])
	if len(toolsRead) != 1 {
		t.Fatalf("expected no duplicate read tool, got %d", len(toolsRead))
	}

	// 4. Non-opencode route: no injection occurs
	nonOpencodeRoute := catalog.Route{
		ProviderID: "other",
		BaseURL:    "https://api.openai.com/v1",
		Protocol:   "openai",
	}
	bodyOther := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	ensureGatewayReadTool(bodyOther, nonOpencodeRoute, false)
	if bodyOther["tools"] != nil {
		t.Fatalf("unexpected tools injected for non-opencode route: %#v", bodyOther["tools"])
	}
}
