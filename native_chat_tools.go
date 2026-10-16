package main

import (
	"regexp"
	"strings"
)

// Xe and Qe from the verified 2.0.29 native Chat adapter. Canonical model
// identity wins over package-construction settings, just as in ModelResolver.
func nativeChatSupportsStrict(r route) bool {
	if flag, ok := r.Compatibility["supportsStrictMode"].(bool); ok {
		return flag
	}
	provider, base := strings.ToLower(nativeModelProvider(r)), strings.ToLower(r.BaseURL)
	return !nativeHasWord("moonshotai moonshotai-cn togetherai together cloudflare-ai-gateway nvidia", provider) && !strings.Contains(base, "api.moonshot.") && !strings.Contains(base, "api.together.") && !strings.Contains(base, "gateway.ai.cloudflare.com") && !strings.Contains(base, "integrate.api.nvidia.com")
}
func nativeChatZaiToolStream(r route) bool {
	if flag, ok := r.Compatibility["zaiToolStream"].(bool); ok {
		return flag
	}
	provider, base := strings.ToLower(nativeModelProvider(r)), strings.ToLower(r.BaseURL)
	if !nativeHasWord("zai zai-coding-plan zhipuai zhipuai-coding-plan", provider) && !strings.Contains(base, "api.z.ai") && !strings.Contains(base, "open.bigmodel.cn") {
		return false
	}
	return !nativeHasWord("glm-4.5 glm-4.5-air glm-4.5-flash glm-4.5v", strings.ToLower(r.ModelID))
}

var nativeMistralToolID = regexp.MustCompile(`[^a-zA-Z0-9]`)
var nativeClaudeToolID = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

const readGateStubDescription = "INTERNAL STUB - DO NOT CALL. This tool is disabled, non-functional, and deprecated. Under no circumstances should you invoke this function."

func ensureGatewayReadTool(body map[string]any, r route) {
	cfg := loadedConfig()
	if cfg.TestGuard != nil && cfg.TestGuard.Enabled && cfg.TestGuard.StrictTextOnly {
		return
	}
	base := strings.ToLower(r.BaseURL)
	provider := strings.ToLower(r.ProviderID)
	canonical := strings.ToLower(r.Canonical)
	if !strings.Contains(base, "opencode.ai") &&
		!strings.Contains(provider, "opencode") &&
		!strings.Contains(canonical, "opencode") {
		return
	}
	rawTools, hasTools := body["tools"]
	tools := list(rawTools)
	isResponses := r.Protocol == "openai-response"

	if !hasTools || len(tools) == 0 {
		var readTool map[string]any
		if isResponses {
			readTool = map[string]any{
				"type":        "function",
				"name":        "read",
				"description": readGateStubDescription,
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			}
		} else {
			readTool = map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "read",
					"description": readGateStubDescription,
					"parameters": map[string]any{
						"type":       "object",
						"properties": map[string]any{},
					},
				},
			}
			body["tool_choice"] = "none"
		}
		body["tools"] = []any{readTool}
		return
	}

	for _, t := range tools {
		toolObj := object(t)
		if toolObj["type"] == "function" {
			if isResponses {
				if name, ok := toolObj["name"].(string); ok && strings.ToLower(strings.TrimSpace(name)) == "read" {
					return
				}
			}
			fn := object(toolObj["function"])
			if fn != nil {
				if name, ok := fn["name"].(string); ok && strings.ToLower(strings.TrimSpace(name)) == "read" {
					return
				}
			}
		}
	}

	var readStub map[string]any
	if isResponses {
		readStub = map[string]any{
			"type":        "function",
			"name":        "read",
			"description": readGateStubDescription,
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		}
	} else {
		readStub = map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "read",
				"description": readGateStubDescription,
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
		}
	}
	body["tools"] = append(tools, readStub)
}

func nativeChatTools(body map[string]any, r route) error {
	ensureGatewayReadTool(body, r)
	tools := list(body["tools"])
	for _, value := range tools {
		tool := object(value)
		if tool["type"] != "function" {
			continue
		}
		function := object(tool["function"])
		if function == nil {
			return fail(400, "Chat function tool requires its definition")
		}
		strict, supplied := function["strict"]
		if supplied && !nativeKind(strict, "bool") {
			return fail(400, "tool strict mode must be boolean")
		}
		if !nativeChatSupportsStrict(r) {
			if supplied {
				return fail(400, "explicit tool strict mode conflicts with unsupported strict-mode compatibility")
			}
		} else if !supplied {
			function["strict"] = false
		}
	}
	if len(tools) > 0 && nativeChatZaiToolStream(r) {
		if _, supplied := body["tool_stream"]; !supplied {
			body["tool_stream"] = true
		}
	}
	model := strings.ToLower(r.ModelID)
	mistral := false
	for _, name := range []string{"mistral", "devstral", "codestral", "pixtral", "mixtral"} {
		mistral = mistral || strings.Contains(model, name)
	}
	afterTool := mistral
	if flag, ok := r.Compatibility["requireAssistantAfterTool"].(bool); ok {
		afterTool = flag
	}
	messages := list(body["messages"])
	normalized := make([]any, 0, len(messages)+2)
	lastWasTool := false
	seenToolCallIDs := map[string]string{}
	for index, raw := range messages {
		message := object(raw)
		role := message["role"]
		if role == "tool" {
			id, ok := message["tool_call_id"].(string)
			if !ok || id == "" {
				return fail(400, "tool message requires tool_call_id")
			}
			if afterTool {
				clean := nativeMistralToolID.ReplaceAllString(id, "")
				if len(clean) > 9 {
					clean = clean[:9]
				}
				if original, exists := seenToolCallIDs[clean]; exists && original != id {
					return fail(400, "Mistral tool call ID collision")
				}
				seenToolCallIDs[clean] = id
				message["tool_call_id"] = clean
			}
			lastWasTool = true
			normalized = append(normalized, message)
			continue
		}
		if role == "assistant" {
			toolCalls := list(message["tool_calls"])
			for _, value := range toolCalls {
				call := object(value)
				id, ok := call["id"].(string)
				if !ok || id == "" {
					return fail(400, "assistant tool call requires an id")
				}
				if afterTool {
					clean := nativeMistralToolID.ReplaceAllString(id, "")
					if len(clean) > 9 {
						clean = clean[:9]
					}
					if original, exists := seenToolCallIDs[clean]; exists && original != id {
						return fail(400, "Mistral tool call ID collision")
					}
					seenToolCallIDs[clean] = id
					call["id"] = clean
				}
			}
		}
		if lastWasTool && afterTool && role != "tool" {
			normalized = append(normalized, map[string]any{"role": "assistant", "content": "Done."})
			lastWasTool = false
		}
		normalized = append(normalized, message)
		if index == len(messages)-1 && role == "tool" && afterTool {
			normalized = append(normalized, map[string]any{"role": "assistant", "content": "Done."})
		}
	}
	body["messages"] = normalized
	return nil
}
