package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Only the existing native transport, API-key, organization, and plugin-owned
// session headers are admitted. The transport adds its own User-Agent later.
// Unknown headers could change how the upstream interprets an otherwise safe
// body, so strict tests fail rather than silently removing them.
func strictTextHeaders(headers http.Header) bool {
	seen := map[string]bool{}
	for key, values := range headers {
		name := strings.ToLower(key)
		if seen[name] || len(values) != 1 {
			return false
		}
		seen[name] = true
		switch name {
		case "content-type":
			if values[0] != "application/json" {
				return false
			}
		case "accept":
			if values[0] != "*/*" {
				return false
			}
		case "authorization", "x-org-id", "x-opencode-org-id", "x-opencode-session", "x-session-affinity", "x-session-id":
		default:
			return false
		}
	}
	return seen["content-type"] && seen["accept"] && seen["authorization"]
}

// This deliberately narrow test profile validates, never rewrites, the final
// wire payload. It is not a provider pricing, entitlement, or billing guarantee.
func strictTextEndpoint(target string) bool {
	u, err := url.Parse(target)
	return err == nil && strings.HasSuffix(u.Path, "/chat/completions") && u.RawQuery == ""
}

func strictTextPayload(protocol, target string, payload []byte) bool {
	// Allow worst-case JSON escaping of the bounded decoded text and model ID.
	if protocol != "openai" || !strictTextEndpoint(target) || len(payload) > 65536 || !utf8.Valid(payload) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(payload))
	d.UseNumber()
	value, valid := strictJSONValue(d, 0)
	if !valid {
		return false
	}
	if _, err := d.Token(); err != io.EOF {
		return false
	}
	body, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for key := range body {
		switch key {
		case "model", "messages", "stream", "stream_options", "max_tokens", "max_completion_tokens":
		default:
			return false
		}
	}
	model, ok := body["model"].(string)
	if !ok || !guardModelValid(model) || body["stream"] != true {
		return false
	}
	if value, present := body["stream_options"]; present {
		options, ok := value.(map[string]any)
		if !ok || len(options) != 1 || options["include_usage"] != true {
			return false
		}
	}
	limits := 0
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		if value, present := body[key]; present {
			number, ok := value.(json.Number)
			if !ok {
				return false
			}
			n, err := strconv.ParseInt(string(number), 10, 64)
			if err != nil || n < 1 || n > 32 {
				return false
			}
			limits++
		}
	}
	if limits != 1 {
		return false
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) < 1 || len(messages) > 4 {
		return false
	}
	total := 0
	for index, value := range messages {
		message, ok := value.(map[string]any)
		if !ok || len(message) != 2 {
			return false
		}
		role, roleOK := message["role"].(string)
		content, contentOK := message["content"].(string)
		if !roleOK || !contentOK || (role != "system" && role != "user" && role != "assistant") || len(content) == 0 || len(content) > 4096 {
			return false
		}
		if index == len(messages)-1 && role != "user" {
			return false
		}
		total += len(content)
	}
	return total <= 8192
}

// Reject duplicate object keys at every level, including escaped equivalents.
// A depth bound also rejects deeply nested extensions before schema validation.
func strictJSONValue(d *json.Decoder, depth int) (any, bool) {
	if depth > 8 {
		return nil, false
	}
	token, err := d.Token()
	if err != nil {
		return nil, false
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return token, true
	}
	switch delim {
	case '{':
		object := map[string]any{}
		for d.More() {
			token, err := d.Token()
			key, ok := token.(string)
			if err != nil || !ok {
				return nil, false
			}
			if _, duplicate := object[key]; duplicate {
				return nil, false
			}
			value, valid := strictJSONValue(d, depth+1)
			if !valid {
				return nil, false
			}
			object[key] = value
		}
		end, err := d.Token()
		return object, err == nil && end == json.Delim('}')
	case '[':
		var array []any
		for d.More() {
			value, valid := strictJSONValue(d, depth+1)
			if !valid {
				return nil, false
			}
			array = append(array, value)
		}
		end, err := d.Token()
		return array, err == nil && end == json.Delim(']')
	default:
		return nil, false
	}
}
