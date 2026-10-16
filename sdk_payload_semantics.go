package main

import (
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// The native 2.0.29 Kt, He, FR and VE adapters serialize these generation
// controls. Restore them after the pinned SDK converter, which maps a subset.
// An absent destination mapping is a local error, never permission to drop it.
func sdkPreserveGeneration(src, out map[string]any, source, target translator.Format) error {
	keys := map[string]string{"temperature": "temperature", "top_p": "top_p", "top_k": "top_k", "presence_penalty": "presence_penalty", "frequency_penalty": "frequency_penalty", "seed": "seed", "stop": "stop", "stop_sequences": "stop", "max_tokens": "max_tokens", "max_completion_tokens": "max_tokens", "max_output_tokens": "max_tokens"}
	// These controls have no audited generic cross-protocol lowering. Reject
	// explicit requests instead of inheriting converter-specific omissions.
	for _, key := range []string{"n", "logit_bias", "logprobs", "min_p", "typical_p", "repetition_penalty"} {
		if value, present := src[key]; present && value != nil {
			return fail(400, "unsupported cross-protocol sampling field: "+key)
		}
	}
	generation := src
	if source == translator.FormatGemini {
		generation = object(src["generationConfig"])
		keys = map[string]string{"temperature": "temperature", "topP": "top_p", "topK": "top_k", "presencePenalty": "presence_penalty", "frequencyPenalty": "frequency_penalty", "seed": "seed", "stopSequences": "stop", "maxOutputTokens": "max_tokens"}
	}
	dest := out
	fields := map[string]string{"temperature": "temperature", "top_p": "top_p", "presence_penalty": "presence_penalty", "frequency_penalty": "frequency_penalty", "seed": "seed", "stop": "stop", "max_tokens": "max_tokens"}
	switch target {
	case translator.FormatClaude:
		fields = map[string]string{"temperature": "temperature", "top_p": "top_p", "top_k": "top_k", "stop": "stop_sequences", "max_tokens": "max_tokens"}
	case translator.FormatGemini:
		dest = object(out["generationConfig"])
		if dest == nil {
			dest = map[string]any{}
		}
		fields = map[string]string{"temperature": "temperature", "top_p": "topP", "top_k": "topK", "presence_penalty": "presencePenalty", "frequency_penalty": "frequencyPenalty", "seed": "seed", "stop": "stopSequences", "max_tokens": "maxOutputTokens"}
	case translator.FormatOpenAIResponse:
		fields = map[string]string{"temperature": "temperature", "top_p": "top_p", "presence_penalty": "presence_penalty", "frequency_penalty": "frequency_penalty", "max_tokens": "max_output_tokens"}
	}
	if value, present := src["top_logprobs"]; present && value != nil {
		if target != translator.FormatOpenAI && target != translator.FormatOpenAIResponse {
			return fail(400, "unsupported cross-protocol sampling field: top_logprobs")
		}
		out["top_logprobs"] = resolverClone(value)
	}
	for from, canonical := range keys {
		value, exists := generation[from]
		if !exists || value == nil {
			continue
		}
		to, supported := fields[canonical]
		if !supported {
			return fail(400, "target protocol does not support requested sampling field: "+from)
		}
		if canonical == "stop" {
			if text, ok := value.(string); ok && target != translator.FormatOpenAI {
				value = []any{text}
			}
		}
		dest[to] = resolverClone(value)
	}
	// Chat's modern limit takes precedence, independent of map iteration order.
	if value, exists := generation["max_completion_tokens"]; exists {
		dest[fields["max_tokens"]] = resolverClone(value)
	}
	if target == translator.FormatGemini && len(dest) > 0 {
		out["generationConfig"] = dest
	}
	return nil
}

// SDK converters may report success when a mixed text/media turn loses a part.
// Compare only semantic content, never tool schemas or arbitrary tool arguments.
// Signatures and encrypted state are opaque: require the exact originating bytes.
func sdkValidateContentPreserved(src, out map[string]any) error {
	before, err := sdkContentInventory(src)
	if err != nil {
		return err
	}
	after, err := sdkContentInventory(out)
	if err != nil {
		return err
	}
	for key, count := range before {
		if after[key] < count {
			return fail(400, "protocol conversion cannot preserve requested attachment or reasoning history")
		}
	}
	return nil
}

func sdkContentInventory(body map[string]any) (map[string]int, error) {
	inventory := map[string]int{}
	add := func(kind string, value any) {
		if text, ok := value.(string); ok && text != "" {
			inventory[kind+":"+text]++
		}
	}
	media := func(value any) {
		text, ok := value.(string)
		if !ok || text == "" {
			return
		}
		// Base64 data URLs and native inline media carry the same original bytes.
		if strings.HasPrefix(text, "data:") {
			if i := strings.Index(text, ";base64,"); i >= 0 {
				add("media-type", strings.ToLower(text[5:i]))
				text = text[i+8:]
			}
		}
		add("media", text)
	}
	var walk func(any) error
	walk = func(value any) error {
		if values, ok := value.([]any); ok {
			for _, v := range values {
				if err := walk(v); err != nil {
					return err
				}
			}
			return nil
		}
		node, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		// Multiple equivalent Chat aliases describe one reasoning segment.
		reasoning := ""
		for _, key := range []string{"reasoning_content", "reasoning_text", "reasoning"} {
			if text, ok := node[key].(string); ok && text != "" {
				if reasoning != "" && reasoning != text {
					return fail(400, "conflicting assistant reasoning aliases")
				}
				reasoning = text
			}
		}
		add("reasoning", reasoning)
		for _, key := range []string{"signature", "thoughtSignature", "thought_signature", "encrypted_content"} {
			add("signature", node[key])
		}
		switch node["type"] {
		case nil, "message", "text", "input_text", "output_text", "summary_text", "function", "function_call", "function_call_output", "tool_use", "tool_result", "refusal":
		case "thinking":
			add("reasoning", node["thinking"])
		case "redacted_thinking":
			add("signature", node["data"])
		case "reasoning":
			for _, summary := range list(node["summary"]) {
				add("reasoning", object(summary)["text"])
			}
		case "image_url":
			media(object(node["image_url"])["url"])
		case "input_image":
			media(node["image_url"])
			media(node["file_id"])
		case "input_audio":
			audio := object(node["input_audio"])
			media(audio["data"])
			format, _ := audio["format"].(string)
			mime := map[string]string{"": "audio/wav", "wav": "audio/wav", "mp3": "audio/mpeg", "pcm16": "audio/pcm", "g711_ulaw": "audio/basic", "g711_alaw": "audio/basic"}[format]
			if mime == "" {
				mime = "audio/" + format
			}
			add("media-type", mime)
		case "file":
			file := object(node["file"])
			media(file["file_data"])
			media(file["file_id"])
		case "input_file":
			media(node["file_data"])
			media(node["file_url"])
			media(node["file_id"])
		case "image", "document":
			source := object(node["source"])
			add("media-type", source["media_type"])
			media(source["data"])
			media(source["url"])
			media(source["file_id"])
		default:
			return fail(400, "unsupported content type in protocol conversion")
		}
		if node["thought"] == true {
			add("reasoning", node["text"])
		}
		for _, key := range []string{"inlineData", "inline_data", "fileData", "file_data"} {
			data := object(node[key])
			if mime, ok := data["mimeType"]; ok {
				add("media-type", mime)
			} else {
				add("media-type", data["mime_type"])
			}
			media(data["data"])
			media(data["fileUri"])
			media(data["file_uri"])
		}
		// Opaque Chat reasoning details cannot be inferred or safely rewritten.
		if details, exists := node["reasoning_details"]; exists && details != nil {
			encoded, _ := json.Marshal(details)
			add("opaque", string(encoded))
		}
		for _, key := range []string{"messages", "contents", "input", "system", "systemInstruction", "content", "parts", "tool_calls", "extra_content", "google", "function"} {
			if err := walk(node[key]); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(body); err != nil {
		return nil, err
	}
	return inventory, nil
}

// Numeric thinking budgets are not equivalent to a guessed effort tier.
// Preserve an exact representation or refuse the cross-protocol request.
func sdkValidateReasoningControls(src, out map[string]any) error {
	controls := func(body map[string]any) map[string]any {
		result := map[string]any{}
		if value := body["reasoning_effort"]; value != nil {
			result["effort"] = value
		}
		reasoning := object(body["reasoning"])
		for _, key := range []string{"effort", "summary"} {
			if value := reasoning[key]; value != nil {
				result[key] = value
			}
		}
		thinking := object(body["thinking"])
		for _, key := range []string{"budget_tokens", "budgetTokens"} {
			if value := thinking[key]; value != nil {
				result["budget"] = value
			}
		}
		if value := object(body["output_config"])["effort"]; value != nil {
			result["effort"] = value
		}
		for _, key := range []string{"display", "block_binding"} {
			if value := thinking[key]; value != nil {
				result[key] = value
			}
		}
		generation := object(body["generationConfig"])
		config := object(generation["thinkingConfig"])
		for _, entry := range []struct {
			aliases   []string
			canonical string
		}{
			{[]string{"thinkingBudget", "thinking_budget"}, "budget"},
			{[]string{"thinkingLevel", "thinking_level"}, "effort"},
			{[]string{"includeThoughts", "include_thoughts"}, "includeThoughts"},
		} {
			for _, alias := range entry.aliases {
				if value := config[alias]; value != nil {
					result[entry.canonical] = value
				}
			}
		}
		// The pinned Responses converter also recognizes generation-level levels.
		for _, alias := range []string{"thinkingLevel", "thinking_level"} {
			if value := generation[alias]; value != nil {
				result["effort"] = value
			}
		}
		// Mode-only controls have no generic lossless cross-protocol equivalent.
		if mode := thinking["type"]; mode != nil && result["budget"] == nil {
			result["mode"] = mode
		}
		return result
	}
	// Do not let precedence between aliases hide a conflicting caller request.
	generation := object(src["generationConfig"])
	config := object(generation["thinkingConfig"])
	for _, aliases := range [][]string{{"thinkingBudget", "thinking_budget"}, {"thinkingLevel", "thinking_level"}, {"includeThoughts", "include_thoughts"}} {
		for _, section := range []map[string]any{config, generation} {
			if first, second := section[aliases[0]], section[aliases[1]]; first != nil && second != nil {
				a, _ := json.Marshal(first)
				b, _ := json.Marshal(second)
				if string(a) != string(b) {
					return fail(400, "conflicting reasoning control aliases")
				}
			}
		}
	}
	before, after := controls(src), controls(out)
	for key, value := range before {
		if key == "summary" && (value == "auto" || value == "none" || value == nil) {
			continue
		}
		a, _ := json.Marshal(value)
		b, _ := json.Marshal(after[key])
		if string(a) != string(b) {
			return fail(400, "protocol conversion cannot preserve requested reasoning control: "+key)
		}
	}
	return nil
}
