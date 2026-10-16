package protocol

import (
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
	"github.com/megumin31/cpa-opencode-zen/internal/fault"
)

// normalizeNativeBody implements the audited OpenCode 2.0.29 provider adapter
// rules (Kt/Ut, Gemini.He, OpenResponses.FR and OpenAIChat.Ye). It deliberately
// does not apply agent token budgets, prompts, tools, or session policy. Call it
// after protocol translation, before protocol defaults/compatibility filters, and
// before the final config Body overlay. It owns the settings-to-wire mapping.
func normalizeNativeBody(body map[string]any, r catalog.Route, options Options) error {
	if err := nativeValidateCompatibility(body, r); err != nil {
		return err
	}
	allowed := "baseURL provider timeout headerTimeout chunkTimeout encoding"
	for _, key := range []string{"baseURL", "provider"} {
		if value, exists := r.Settings[key]; exists && value != nil && !nativeKind(value, "string") {
			return fault.New(400, "invalid native provider setting: "+key)
		}
	}
	switch r.Protocol {
	case "openai":
		allowed += " store reasoningEffort"
	case "openai-response":
		allowed += " store metadata safetyIdentifier streamOptions topLogprobs reasoningEffort reasoningSummary include textVerbosity serviceTier truncation allowedTools maxToolCalls parallelToolCalls contextManagement"
	case "claude":
		allowed += " contextManagement thinking effort service_tier serviceTier metadata container inference_geo inferenceGeo cache_control cacheControl output_config outputConfig"
	case "gemini":
		allowed += " cachedContent safetySettings serviceTier thinkingConfig"
	default:
		return fault.New(400, "unsupported native request protocol")
	}
	for key, value := range r.Settings {
		if value != nil && !nativeHasWord(allowed, key) {
			return fault.New(400, "unsupported native provider setting: "+key)
		}
	}
	switch r.Protocol {
	case "openai", "openai-response":
		return normalizeNativeOpenAI(body, r, options)
	case "claude":
		return normalizeNativeAnthropic(body, r, options)
	case "gemini":
		return normalizeNativeGemini(body, r)
	}
	return nil
}

func nativeHasWord(words, word string) bool {
	for _, candidate := range strings.Fields(words) {
		if candidate == word {
			return true
		}
	}
	return false
}

func nativeNumber(value any) (float64, bool) {
	var n float64
	switch v := value.(type) {
	case float64:
		n = v
	case float32:
		n = float64(v)
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	case json.Number:
		var err error
		n, err = v.Float64()
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return n, !math.IsNaN(n) && !math.IsInf(n, 0)
}

func nativeKind(value any, kind string) bool {
	switch kind {
	case "string":
		_, ok := value.(string)
		return ok
	case "bool":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := nativeNumber(value)
		return ok
	case "integer":
		n, ok := nativeNumber(value)
		return ok && n == math.Trunc(n)
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "strings":
		values, ok := value.([]any)
		if !ok {
			return false
		}
		for _, v := range values {
			if _, ok := v.(string); !ok {
				return false
			}
		}
		return true
	case "stringMap":
		values, ok := value.(map[string]any)
		if !ok {
			return false
		}
		for _, v := range values {
			if _, ok := v.(string); !ok {
				return false
			}
		}
		return true
	}
	return false
}

func nativeOption(body, options map[string]any, key, field, kind string) error {
	value, exists := options[key]
	if !exists {
		return nil
	}
	if value == nil {
		return nil
	}
	if !nativeKind(value, kind) {
		return fault.New(400, "invalid native provider setting: "+key)
	}
	body[field] = catalog.Clone(value)
	return nil
}

func nativeFields(object map[string]any, allowed, context string) error {
	for key := range object {
		if !nativeHasWord(allowed, key) {
			return fault.New(400, "unsupported "+context+" field: "+key)
		}
	}
	return nil
}

func normalizeNativeOpenAI(body map[string]any, r catalog.Route, options Options) error {
	opts := r.Settings
	nativeOpenAIDefaults(body, r)
	if err := nativeOption(body, opts, "store", "store", "bool"); err != nil {
		return err
	}
	if r.Protocol == "openai" {
		if err := nativeChatTools(body, r, options.StrictTextOnly); err != nil {
			return err
		}
		if err := nativeChatReasoning(body, r); err != nil {
			return err
		}
		if err := nativeOption(body, opts, "reasoningEffort", "reasoning_effort", "string"); err != nil {
			return err
		}
		// The native adapter retains an empty tool list when replaying tool calls.
		tools, _ := body["tools"].([]any)
		if len(tools) == 0 {
			delete(body, "tool_choice")
			messages, _ := body["messages"].([]any)
			for _, value := range messages {
				message, _ := value.(map[string]any)
				calls, _ := message["tool_calls"].([]any)
				if len(calls) > 0 {
					body["tools"] = []any{}
					break
				}
			}
		}
	} else {
		ensureGatewayReadTool(body, r, options.StrictTextOnly)
		for _, mapping := range []struct{ key, field, kind string }{
			{"metadata", "metadata", "stringMap"}, {"safetyIdentifier", "safety_identifier", "string"},
			{"topLogprobs", "top_logprobs", "integer"}, {"include", "include", "strings"},
			{"serviceTier", "service_tier", "string"}, {"truncation", "truncation", "string"},
			{"maxToolCalls", "max_tool_calls", "integer"}, {"parallelToolCalls", "parallel_tool_calls", "bool"},
		} {
			if err := nativeOption(body, opts, mapping.key, mapping.field, mapping.kind); err != nil {
				return err
			}
		}
		if n, ok := nativeNumber(opts["topLogprobs"]); ok && (n < 0 || n > 20) {
			return fault.New(400, "topLogprobs must be between 0 and 20")
		}
		if value, exists := opts["truncation"]; exists && value != nil && value != "auto" && value != "disabled" {
			return fault.New(400, "unsupported truncation setting")
		}
		for _, mapping := range []struct{ key, section, field string }{
			{"reasoningEffort", "reasoning", "effort"}, {"reasoningSummary", "reasoning", "summary"}, {"textVerbosity", "text", "verbosity"},
		} {
			if value, exists := opts[mapping.key]; exists && value != nil {
				if !nativeKind(value, "string") {
					return fault.New(400, "invalid native provider setting: "+mapping.key)
				}
				if mapping.key == "reasoningSummary" && !nativeHasWord("auto concise detailed", value.(string)) {
					return fault.New(400, "invalid reasoningSummary setting")
				}
				section, _ := body[mapping.section].(map[string]any)
				body[mapping.section] = catalog.Merge(section, map[string]any{mapping.field: value})
			}
		}
		if value, ok := opts["streamOptions"]; ok && value != nil {
			object, ok := value.(map[string]any)
			if !ok {
				return fault.New(400, "streamOptions must be an object")
			}
			if err := nativeFields(object, "includeObfuscation", "streamOptions"); err != nil {
				return err
			}
			section, _ := body["stream_options"].(map[string]any)
			section = catalog.Merge(nil, section)
			if err := nativeOption(section, object, "includeObfuscation", "include_obfuscation", "bool"); err != nil {
				return err
			}
			body["stream_options"] = section
		}
		if value, ok := opts["allowedTools"]; ok && value != nil {
			object, ok := value.(map[string]any)
			if !ok || !nativeKind(object["toolNames"], "strings") {
				return fault.New(400, "allowedTools requires a toolNames array")
			}
			if err := nativeFields(object, "toolNames mode", "allowedTools"); err != nil {
				return err
			}
			mode := "auto"
			if v, exists := object["mode"]; exists {
				var valid bool
				mode, valid = v.(string)
				if !valid || !nativeHasWord("auto none required", mode) {
					return fault.New(400, "invalid allowedTools mode")
				}
			}
			names := object["toolNames"].([]any)
			if len(names) > 0 {
				if tools, _ := body["tools"].([]any); len(tools) == 0 {
					return fault.New(400, "allowedTools requires active tools")
				}
				tools := make([]any, 0, len(names))
				for _, name := range names {
					tools = append(tools, map[string]any{"type": "function", "name": name})
				}
				body["tool_choice"] = map[string]any{"type": "allowed_tools", "mode": mode, "tools": tools}
			}
		}
		if value, ok := opts["contextManagement"]; ok && value != nil {
			// Context management is native OpenAI Responses, not OpenResponses-compatible.
			if r.Package != "" && r.Package != "@opencode/ai/providers/openai" && r.Package != "@opencode/ai/providers/openai/responses" && r.Package != "@ai-sdk/openai" {
				return fault.New(400, "contextManagement is unsupported for this Responses package")
			}
			edits, ok := value.([]any)
			if !ok {
				return fault.New(400, "Responses contextManagement must be an array")
			}
			result := make([]any, 0, len(edits))
			for _, entry := range edits {
				edit, ok := entry.(map[string]any)
				if !ok || edit["type"] != "compaction" {
					return fault.New(400, "unsupported Responses contextManagement entry")
				}
				if err := nativeFields(edit, "type compactThreshold", "contextManagement"); err != nil {
					return err
				}
				next := map[string]any{"type": "compaction"}
				if v, exists := edit["compactThreshold"]; exists {
					n, valid := nativeNumber(v)
					if !valid || n <= 0 || n != math.Trunc(n) {
						return fault.New(400, "compactThreshold must be a positive integer")
					}
					next["compact_threshold"] = v
				}
				result = append(result, next)
			}
			body["context_management"] = result
		}
	}
	// UJ clips the canonical prompt cache key at 64 Unicode codepoints.
	if key, ok := body["prompt_cache_key"].(string); ok && len([]rune(key)) > 64 {
		body["prompt_cache_key"] = string([]rune(key)[:64])
	}
	return nil
}

// HD in the native shared adapter. This is not a clamp to the minimum: a
// smaller explicit budget is preserved. The minimum only applies on reduction.
func nativeThinkingBudget(budget, maximum, minimum float64, hasMaximum bool) float64 {
	if !hasMaximum || budget <= maximum/2 {
		return budget
	}
	return math.Max(minimum, math.Floor(maximum/2))
}

func normalizeNativeGemini(body map[string]any, r catalog.Route) error {
	opts := r.Settings
	for _, mapping := range []struct{ key, kind string }{{"cachedContent", "string"}, {"serviceTier", "string"}} {
		if err := nativeOption(body, opts, mapping.key, mapping.key, mapping.kind); err != nil {
			return err
		}
	}
	if value, exists := opts["safetySettings"]; exists && value != nil {
		entries, ok := value.([]any)
		if !ok {
			return fault.New(400, "safetySettings must be an array")
		}
		for _, entry := range entries {
			object, ok := entry.(map[string]any)
			if !ok || !nativeKind(object["category"], "string") || !nativeKind(object["threshold"], "string") {
				return fault.New(400, "invalid safetySettings entry")
			}
			if err := nativeFields(object, "category threshold", "safetySettings"); err != nil {
				return err
			}
		}
		body["safetySettings"] = catalog.Clone(entries)
	}
	generation, _ := body["generationConfig"].(map[string]any)
	if value, exists := opts["thinkingConfig"]; exists && value != nil {
		if !nativeKind(value, "object") {
			return fault.New(400, "thinkingConfig must be an object")
		}
		generation = catalog.Merge(generation, map[string]any{"thinkingConfig": value})
		body["generationConfig"] = generation
	}
	if value, exists := generation["thinkingConfig"]; exists && value != nil {
		thinking, ok := value.(map[string]any)
		if !ok {
			return fault.New(400, "thinkingConfig must be an object")
		}
		if err := nativeFields(thinking, "thinkingBudget includeThoughts thinkingLevel", "thinkingConfig"); err != nil {
			return err
		}
		if value, exists := thinking["includeThoughts"]; !exists || value == nil {
			thinking["includeThoughts"] = true
		} else if !nativeKind(value, "bool") {
			return fault.New(400, "includeThoughts must be a boolean")
		}
		if value, exists := thinking["thinkingLevel"]; exists && value != nil && !nativeKind(value, "string") {
			return fault.New(400, "thinkingLevel must be a string")
		}
		if value, exists := thinking["thinkingBudget"]; exists && value != nil {
			budget, ok := nativeNumber(value)
			if !ok {
				return fault.New(400, "thinkingBudget must be a number")
			}
			maximum, hasMaximum := nativeNumber(generation["maxOutputTokens"])
			thinking["thinkingBudget"] = nativeThinkingBudget(budget, maximum, 512, hasMaximum)
		}
	}
	return nil
}

func nativePrefer(options map[string]any, first, second string) (any, bool) {
	if value, exists := options[first]; exists && value != nil {
		return value, true
	}
	value, exists := options[second]
	return value, exists && value != nil
}

func normalizeNativeAnthropic(body map[string]any, r catalog.Route, options Options) error {
	opts := r.Settings
	if err := nativeValidateAnthropicSettings(opts); err != nil {
		return err
	}
	for _, mapping := range []struct{ first, second, field, kind string }{
		{"service_tier", "serviceTier", "service_tier", "string"}, {"inference_geo", "inferenceGeo", "inference_geo", "string"},
		{"cache_control", "cacheControl", "cache_control", "object"}, {"output_config", "outputConfig", "output_config", "object"},
	} {
		if value, ok := nativePrefer(opts, mapping.first, mapping.second); ok {
			if !nativeKind(value, mapping.kind) {
				return fault.New(400, "invalid native provider setting: "+mapping.first)
			}
			if mapping.field == "output_config" {
				current, _ := body[mapping.field].(map[string]any)
				body[mapping.field] = catalog.Merge(current, value.(map[string]any))
			} else {
				body[mapping.field] = catalog.Clone(value)
			}
		}
	}
	if err := nativeOption(body, opts, "metadata", "metadata", "object"); err != nil {
		return err
	}
	if value, ok := opts["container"]; ok && value != nil {
		if !nativeKind(value, "string") && !nativeKind(value, "object") {
			return fault.New(400, "container must be a string or object")
		}
		body["container"] = catalog.Clone(value)
	}
	if value, ok := opts["effort"]; ok && value != nil {
		if !nativeKind(value, "string") {
			return fault.New(400, "effort must be a string")
		}
		output, _ := body["output_config"].(map[string]any)
		body["output_config"] = catalog.Merge(output, map[string]any{"effort": value})
	}
	if value, ok := opts["contextManagement"]; ok && value != nil {
		object, ok := value.(map[string]any)
		if !ok {
			return fault.New(400, "contextManagement must be an object")
		}
		if err := nativeFields(object, "edits", "contextManagement"); err != nil {
			return err
		}
		edits, ok := object["edits"].([]any)
		if !ok {
			return fault.New(400, "contextManagement requires edits")
		}
		result := make([]any, 0, len(edits))
		for _, entry := range edits {
			edit, ok := entry.(map[string]any)
			if !ok || edit["type"] != "compact_20260112" {
				return fault.New(400, "unsupported Anthropic context management edit")
			}
			if err := nativeFields(edit, "type trigger pauseAfterCompaction instructions", "contextManagement edit"); err != nil {
				return err
			}
			next := map[string]any{"type": "compact_20260112"}
			if err := nativeOption(next, edit, "pauseAfterCompaction", "pause_after_compaction", "bool"); err != nil {
				return err
			}
			if err := nativeOption(next, edit, "instructions", "instructions", "string"); err != nil {
				return err
			}
			if value, exists := edit["trigger"]; exists {
				trigger, ok := value.(map[string]any)
				if !ok || trigger["type"] != "input_tokens" {
					return fault.New(400, "unsupported compaction trigger")
				}
				n, valid := nativeNumber(trigger["value"])
				if !valid || n < 50000 || math.Trunc(n) != n {
					return fault.New(400, "compaction trigger value must be an integer at least 50000")
				}
				if err := nativeFields(trigger, "type value", "compaction trigger"); err != nil {
					return err
				}
				next["trigger"] = catalog.Clone(trigger)
			}
			result = append(result, next)
		}
		body["context_management"] = map[string]any{"edits": result}
	}
	if value, exists := opts["thinking"]; exists && value != nil {
		body["thinking"] = catalog.Clone(value)
	}
	var thinking map[string]any
	if value, exists := body["thinking"]; exists && value != nil {
		var ok bool
		thinking, ok = value.(map[string]any)
		if !ok {
			return fault.New(400, "thinking must be an object")
		}
		if err := nativeFields(thinking, "type budgetTokens budget_tokens display block_binding", "thinking"); err != nil {
			return err
		}
		kind, ok := thinking["type"].(string)
		if !ok || !nativeHasWord("enabled adaptive disabled between_tools", kind) {
			return fault.New(400, "unsupported thinking type")
		}
		if kind == "enabled" {
			value, exists := nativePrefer(thinking, "budgetTokens", "budget_tokens")
			budget, valid := nativeNumber(value)
			if !exists || !valid {
				return fault.New(400, "enabled thinking requires a numeric budget")
			}
			maximum, hasMaximum := nativeNumber(body["max_tokens"])
			if _, exists := body["max_tokens"]; !exists {
				maximum, hasMaximum = 32000, true
			}
			thinking["budget_tokens"] = nativeThinkingBudget(budget, maximum, 1024, hasMaximum)
			delete(thinking, "budgetTokens")
		} else if kind == "disabled" || kind == "between_tools" {
			if err := nativeFields(thinking, "type", "disabled thinking"); err != nil {
				return err
			}
		} else if _, exists := thinking["budgetTokens"]; exists {
			return fault.New(400, "thinking budget requires enabled thinking")
		} else if _, exists := thinking["budget_tokens"]; exists {
			return fault.New(400, "thinking budget requires enabled thinking")
		}
		if v, exists := thinking["display"]; exists && !nativeKind(v, "string") {
			return fault.New(400, "thinking display must be a string")
		}
		if value, exists := thinking["block_binding"]; exists {
			binding, ok := value.(map[string]any)
			if !ok {
				return fault.New(400, "block_binding must be an object")
			}
			if err := nativeFields(binding, "prefix_mismatch_behavior", "block_binding"); err != nil {
				return err
			}
			if v, exists := binding["prefix_mismatch_behavior"]; exists && !nativeKind(v, "string") {
				return fault.New(400, "prefix_mismatch_behavior must be a string")
			}
		}
	}
	if nativeThinkingBinding(r) && (thinking == nil || thinking["type"] != "disabled" && thinking["type"] != "between_tools") {
		if thinking == nil {
			thinking = map[string]any{"type": "adaptive"}
			body["thinking"] = thinking
		}
		binding, _ := thinking["block_binding"].(map[string]any)
		thinking["block_binding"] = catalog.Merge(map[string]any{"prefix_mismatch_behavior": "drop_block"}, binding)
	}
	if err := nativeNormalizeAnthropicHistory(body, r); err != nil {
		return err
	}
	// Kt allocates explicit cache breakpoints in tools, then system, then history
	// order. Do not traverse arbitrary schemas or tool arguments.
	before := nativeCacheCount(body)
	remaining := 4
	for _, field := range []string{"tools", "system", "messages"} {
		nativeLimitCache(body[field], &remaining)
	}
	if dropped := before - nativeCacheCount(body); dropped > 0 && options.Logf != nil {
		options.Logf("Anthropic Messages: dropped %d cache breakpoint(s); the API allows at most 4 per request", dropped)
	}
	return nil
}

var nativeClaudeVersion = regexp.MustCompile(`(?:^|[./])claude-([a-z]+)-(\d+)(?:[.-](\d{1,2}))?(?:$|[-:@])`)

func nativeThinkingBinding(r catalog.Route) bool {
	if value, ok := r.Compatibility["supportsThinkingBlockBinding"].(bool); ok {
		return value
	}
	match := nativeClaudeVersion.FindStringSubmatch(strings.ToLower(r.ModelID))
	if match == nil {
		return false
	}
	major, _ := strconv.Atoi(match[2])
	minor, _ := strconv.Atoi(match[3])
	return major > 5 || major == 5 && minor >= 1
}
func nativeRequiresSignature(r catalog.Route) bool {
	if value, ok := r.Compatibility["requireSignature"].(bool); ok {
		return value
	}
	provider := nativeModelProvider(r)
	provider, model, base := strings.ToLower(provider), strings.ToLower(r.ModelID), strings.ToLower(r.BaseURL)
	if nativeHasWord("kimi-for-coding moonshotai moonshotai-cn", provider) || strings.HasPrefix(model, "kimi-") || strings.Contains(base, "api.kimi.com/coding") || strings.Contains(base, "api.moonshot.ai/anthropic") || strings.Contains(base, "api.moonshot.cn/anthropic") {
		return false
	}
	return !strings.Contains(provider, "xiaomi") && !strings.Contains(model, "mimo") && !strings.Contains(base, "xiaomimimo.com")
}
func nativeNormalizeAnthropicHistory(body map[string]any, r catalog.Route) error {
	messages, ok := body["messages"].([]any)
	if !ok {
		return nil
	}
	result := make([]any, 0, len(messages))
	for _, value := range messages {
		message, ok := value.(map[string]any)
		if !ok {
			return fault.New(400, "Anthropic messages must be objects")
		}
		role, _ := message["role"].(string)
		if text, ok := message["content"].(string); ok {
			if (role == "user" || role == "assistant") && strings.TrimSpace(text) == "" {
				continue
			}
			result = append(result, message)
			continue
		}
		blocks, ok := message["content"].([]any)
		if !ok {
			result = append(result, message)
			continue
		}
		kept := make([]any, 0, len(blocks))
		for _, value := range blocks {
			block, ok := value.(map[string]any)
			if !ok {
				return fault.New(400, "Anthropic content blocks must be objects")
			}
			switch block["type"] {
			case "text":
				if text, ok := block["text"].(string); ok && strings.TrimSpace(text) == "" && (role == "user" || role == "assistant") {
					continue
				}
			case "thinking":
				signature, _ := block["signature"].(string)
				if nativeRequiresSignature(r) && strings.TrimSpace(signature) == "" {
					return fault.New(400, "Anthropic thinking replay requires its original signature")
				}
			case "redacted_thinking":
				if data, ok := block["data"].(string); !ok || strings.TrimSpace(data) == "" {
					return fault.New(400, "redacted thinking replay requires its original data")
				}
			}
			kept = append(kept, block)
		}
		if len(kept) == 0 && (role == "user" || role == "assistant") {
			continue
		}
		message["content"] = kept
		result = append(result, message)
	}
	body["messages"] = result
	return nil
}
func nativeLimitCacheControl(object map[string]any, remaining *int) {
	if value, exists := object["cache_control"]; exists && value != nil {
		if *remaining == 0 {
			delete(object, "cache_control")
		} else {
			*remaining--
		}
	}
}
func nativeLimitCache(value any, remaining *int) {
	switch object := value.(type) {
	case []any:
		for _, entry := range object {
			nativeLimitCache(entry, remaining)
		}
	case map[string]any:
		nativeLimitCacheControl(object, remaining)
		if content, exists := object["content"]; exists {
			nativeLimitCache(content, remaining)
		}
	}
}

// nativeHeaders must be evaluated after the final Body overlay. The caller must
// union these beta tokens with configured anthropic-beta, rather than overwrite
// either. No OAuth, identity or session headers are synthesized.
func nativeHeaders(r catalog.Route, body map[string]any) http.Header {
	headers := http.Header{}
	if r.Protocol != "claude" {
		return headers
	}
	betas := []string{"interleaved-thinking-2025-05-14"}
	compact, midConfig := false, false
	if context, ok := body["context_management"].(map[string]any); ok {
		if edits, ok := context["edits"].([]any); ok && len(edits) > 0 {
			compact = true
		}
	}
	if messages, ok := body["messages"].([]any); ok {
		for _, value := range messages {
			message, _ := value.(map[string]any)
			if message["role"] == "system" && message["output_config"] != nil {
				midConfig = true
			}
			blocks, _ := message["content"].([]any)
			for _, value := range blocks {
				block, _ := value.(map[string]any)
				if block["type"] == "compaction" {
					compact = true
				}
			}
		}
	}
	if compact {
		betas = append(betas, "compact-2026-01-12")
	}
	if midConfig {
		betas = append(betas, "mid-conversation-output-config-2026-07-01")
	}
	if thinking, ok := body["thinking"].(map[string]any); ok && thinking["type"] != "disabled" && thinking["type"] != "between_tools" && thinking["block_binding"] != nil {
		betas = append(betas, "thinking-binding-controls-2026-08-01")
	}
	headers.Set("anthropic-beta", strings.Join(betas, ","))
	return headers
}

// Validate option objects without walking arbitrary caller JSON Schema fields.
func nativeValidateAnthropicSettings(options map[string]any) error {
	for _, key := range []string{"cache_control", "cacheControl", "output_config", "outputConfig", "metadata", "container"} {
		value, exists := options[key]
		if !exists || value == nil {
			continue
		}
		if key == "container" && nativeKind(value, "string") {
			continue
		}
		object, ok := value.(map[string]any)
		if !ok {
			return fault.New(400, "invalid native provider setting: "+key)
		}
		allowed := ""
		switch key {
		case "cache_control", "cacheControl":
			allowed = "type ttl"
			if object["type"] != "ephemeral" {
				return fault.New(400, "cacheControl type must be ephemeral")
			}
			if ttl, exists := object["ttl"]; exists && ttl != "5m" && ttl != "1h" {
				return fault.New(400, "unsupported cacheControl ttl")
			}
		case "output_config", "outputConfig":
			allowed = "effort format"
			if effort, exists := object["effort"]; exists && effort != nil && !nativeKind(effort, "string") {
				return fault.New(400, "outputConfig effort must be a string")
			}
			if value, exists := object["format"]; exists && value != nil {
				format, ok := value.(map[string]any)
				if !ok || format["type"] != "json_schema" || !nativeKind(format["schema"], "object") {
					return fault.New(400, "outputConfig format requires json_schema and an object schema")
				}
				if err := nativeFields(format, "type schema", "outputConfig format"); err != nil {
					return err
				}
			}
		case "metadata":
			allowed = "user_id"
			if v, exists := object["user_id"]; exists && v != nil && !nativeKind(v, "string") {
				return fault.New(400, "metadata user_id must be a string")
			}
		case "container":
			allowed = "id skills"
			if v, exists := object["id"]; exists && v != nil && !nativeKind(v, "string") {
				return fault.New(400, "container id must be a string")
			}
			if v, exists := object["skills"]; exists && v != nil {
				skills, ok := v.([]any)
				if !ok {
					return fault.New(400, "container skills must be an array")
				}
				for _, skill := range skills {
					if !nativeKind(skill, "object") {
						return fault.New(400, "container skills must contain objects")
					}
				}
			}
		}
		if err := nativeFields(object, allowed, key); err != nil {
			return err
		}
	}
	return nil
}

func nativeCacheCount(body map[string]any) int {
	count := 0
	var visit func(any)
	visit = func(value any) {
		switch object := value.(type) {
		case []any:
			for _, entry := range object {
				visit(entry)
			}
		case map[string]any:
			if value, exists := object["cache_control"]; exists && value != nil {
				count++
			}
			if value, exists := object["content"]; exists {
				visit(value)
			}
		}
	}
	// Top-level cache_control is the native adapter's separate automatic-cache
	// setting, not one of the explicitly allocated content/tool breakpoints.
	for _, field := range []string{"tools", "system", "messages"} {
		visit(body[field])
	}
	return count
}

// validateNativeBody checks critical invariants after the final raw Body overlay
// without rewriting it. Configured overrides must not bypass signature checks or
// reintroduce cache hints that exceed the provider limit.
func validateNativeBody(body map[string]any, r catalog.Route) error {
	if r.Protocol != "claude" {
		return nil
	}
	if nativeCacheCount(body) > 4 {
		return fault.New(400, "configured Anthropic body exceeds four cache breakpoints")
	}
	return nativeNormalizeAnthropicHistory(catalog.Merge(nil, body), r)
}

// Y0 is the native OpenAI model wrapper, shared by its Chat/Responses routes.
// Defaults never overwrite caller payload fields; explicit settings are applied
// afterward and have the same higher priority as native providerOptions.
func nativeOpenAIDefaults(body map[string]any, r catalog.Route) {
	switch r.Package {
	case "@opencode/ai/providers/openai", "@opencode/ai/providers/openai/chat", "@opencode/ai/providers/openai/responses", "@ai-sdk/openai":
	default:
		return
	}
	if _, supplied := body["store"]; !supplied {
		if _, configured := r.Settings["store"]; !configured {
			body["store"] = false
		}
	}
	model := strings.ToLower(r.ModelID)
	if !strings.Contains(model, "gpt-5") || strings.Contains(model, "gpt-5-chat") || strings.Contains(model, "gpt-5-pro") {
		return
	}
	if r.Protocol == "openai" {
		if _, supplied := body["reasoning_effort"]; !supplied {
			if _, configured := r.Settings["reasoningEffort"]; !configured {
				body["reasoning_effort"] = "medium"
			}
		}
		return
	}
	if r.Protocol != "openai-response" {
		return
	}
	reasoning, _ := body["reasoning"].(map[string]any)
	for _, item := range []struct{ option, field, value string }{{"reasoningEffort", "effort", "medium"}, {"reasoningSummary", "summary", "auto"}} {
		if _, supplied := reasoning[item.field]; supplied {
			continue
		}
		if _, configured := r.Settings[item.option]; configured {
			continue
		}
		if reasoning == nil {
			reasoning = map[string]any{}
		}
		reasoning[item.field] = item.value
	}
	if len(reasoning) > 0 {
		body["reasoning"] = reasoning
	}
	if _, supplied := body["include"]; !supplied {
		if _, configured := r.Settings["include"]; !configured {
			body["include"] = []any{"reasoning.encrypted_content"}
		}
	}
}

// je/Je choose the configured output key for canonical reasoning text; a
// required-but-absent value is an empty structural field. The SDK envelope is
// already provider JSON here, so recognize its audited string aliases while
// preserving opaque reasoning_details, caller content, and tool history.
func nativeChatReasoning(body map[string]any, r catalog.Route) error {
	target, configured := r.Compatibility["reasoningField"].(string)
	provider := nativeModelProvider(r)
	required := configured || provider == "deepseek" || strings.Contains(strings.ToLower(r.BaseURL), "deepseek.com") || strings.Contains(strings.ToLower(r.ModelID), "deepseek")
	if override, ok := r.Compatibility["requireReasoning"].(bool); ok {
		required = override
	}
	messages, _ := body["messages"].([]any)
	for _, value := range messages {
		message, ok := value.(map[string]any)
		if !ok || message["role"] != "assistant" {
			continue
		}
		text, found := "", false
		// A configured alias has the same priority as native on(). Ambiguous,
		// disagreeing aliases fail rather than silently dropping caller history.
		fields := []string{target, "reasoning_content", "reasoning", "reasoning_text"}
		seen := map[string]bool{}
		for _, key := range fields {
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			value, exists := message[key]
			if !exists || value == nil {
				continue
			}
			candidate, valid := value.(string)
			if !valid {
				return fault.New(400, "assistant reasoning alias must be a string")
			}
			if found && candidate != "" && text != "" && candidate != text {
				return fault.New(400, "conflicting assistant reasoning aliases cannot be replayed losslessly")
			}
			// Native on() skips empty fields when another alias has actual text.
			if !found || text == "" {
				text, found = candidate, true
			}
		}
		if !configured {
			if found {
				continue
			}
			if required {
				message["reasoning_content"] = ""
			}
			continue
		}
		if found || required {
			for _, alias := range []string{"reasoning_content", "reasoning", "reasoning_text"} {
				if alias != target {
					delete(message, alias)
				}
			}
			message[target] = text
		}
	}
	return nil
}

// The native schema is intentionally finite. Recognizing a setting without
// implementing its effect would silently change replay/generation semantics.
func nativeValidateCompatibility(body map[string]any, r catalog.Route) error {
	for key, value := range r.Compatibility {
		switch key {
		case "reasoningField":
			field, ok := value.(string)
			if !ok || r.Protocol != "openai" || !nativeHasWord("reasoning_content reasoning reasoning_text", field) {
				return fault.New(400, "unsupported reasoningField compatibility")
			}
		case "maxTokensField":
			if r.Protocol != "openai" || (value != "max_tokens" && value != "max_completion_tokens") {
				return fault.New(400, "unsupported maxTokensField compatibility")
			}
		case "sanitizer":
			if value != "none" {
				return fault.New(400, "configured sanitizer is unsupported")
			}
		case "cacheFormat":
			return fault.New(400, "unsupported native compatibility: "+key)
		case "requireFinishReason", "requireReasoning", "supportsStore", "supportsUsageInStreaming", "supportsPromptCacheKey", "requireSignature", "supportsThinkingBlockBinding", "requireAssistantAfterTool", "supportsStrictMode", "zaiToolStream", "supportsEffortUpdates":
			flag, ok := value.(bool)
			if !ok {
				return fault.New(400, "native compatibility must be boolean: "+key)
			}
			validProtocol := false
			switch key {
			case "requireFinishReason", "requireReasoning", "supportsStore", "supportsUsageInStreaming", "requireAssistantAfterTool", "supportsStrictMode", "zaiToolStream":
				validProtocol = r.Protocol == "openai"
			case "supportsPromptCacheKey":
				validProtocol = r.Protocol == "openai" || r.Protocol == "openai-response"
			case "requireSignature", "supportsThinkingBlockBinding":
				validProtocol = r.Protocol == "claude"
			case "supportsEffortUpdates":
				validProtocol = r.Protocol == "claude" || r.Protocol == "openai-response"
			}
			if !validProtocol {
				return fault.New(400, "native compatibility is unsupported for this protocol: "+key)
			}
			if key == "supportsEffortUpdates" && flag {
				return fault.New(400, "unsupported enabled native compatibility: "+key)
			}
			// Native strict-mode=false omits this tool schema member. Do not silently
			// erase an explicit caller requirement, even when its value is false.
			if key == "supportsStrictMode" && !flag {
				tools, _ := body["tools"].([]any)
				for _, value := range tools {
					tool, _ := value.(map[string]any)
					function, _ := tool["function"].(map[string]any)
					if _, exists := function["strict"]; exists {
						return fault.New(400, "explicit tool strict mode conflicts with unsupported strict-mode compatibility")
					}
				}
			}
		default:
			return fault.New(400, "unknown native compatibility: "+key)
		}
	}
	return nil
}
