package protocol

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
	"github.com/megumin31/cpa-opencode-zen/internal/fault"
)

// The pinned SDK drops function.strict and lowers developer to user. Carry
// these two fields through its private conversion using collision-free markers,
// rather than inferring message indices or tool names after history merging and
// namespace lowering. Markers must survive exactly once and are removed before
// native options, validation, logging, or transport can see the converted body.
type sdkResponsesChatState struct {
	marker string
	roles  map[string]int
	tools  map[string]sdkResponsesChatTool
}
type sdkResponsesChatTool struct {
	strict        bool
	hadStrict     bool
	parameters    any
	hadParameters bool
}

func sdkProtectResponsesChat(raw []byte) ([]byte, *sdkResponsesChatState, error) {
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil || body == nil {
		return nil, nil, fault.New(400, "invalid Responses request")
	}
	canonical, _ := json.Marshal(body)
	// A fixed-size digest avoids an attacker-controlled underscore/suffix scan.
	marker := fmt.Sprintf("__cpa_responses_chat_preserve_%x", sha256.Sum256(canonical))
	if strings.Contains(string(canonical), marker) {
		return nil, nil, fault.New(400, "cannot allocate private protocol conversion marker")
	}
	state := &sdkResponsesChatState{marker: marker, roles: map[string]int{}, tools: map[string]sdkResponsesChatTool{}}
	for _, value := range list(body["input"]) {
		item := object(value)
		if item["role"] != "developer" || (item["type"] != nil && item["type"] != "" && item["type"] != "message") {
			continue
		}
		token := fmt.Sprintf("%s_role_%d", marker, len(state.roles))
		state.roles[token] = 0
		item["role"] = token
	}
	hasStrict := false
	var scanStrict func([]any)
	scanStrict = func(tools []any) {
		for _, value := range tools {
			tool := object(value)
			typ, validType := tool["type"].(string)
			if !validType && tool["type"] != nil {
				continue
			}
			typ = strings.TrimSpace(typ)
			if typ == "namespace" {
				scanStrict(list(tool["tools"]))
				continue
			}
			if typ != "" && typ != "function" {
				continue
			}
			_, flat := tool["strict"]
			_, nested := object(tool["function"])["strict"]
			hasStrict = hasStrict || flat || nested
		}
	}
	scanStrict(list(body["tools"]))
	for _, value := range list(body["input"]) {
		item := object(value)
		if item["type"] == "additional_tools" {
			scanStrict(list(item["tools"]))
		}
	}
	var protectTools func([]any) error
	protectTools = func(tools []any) error {
		for _, value := range tools {
			tool := object(value)
			if tool == nil {
				return fault.New(400, "Responses tools must be objects")
			}
			typ, validType := tool["type"].(string)
			if !validType && tool["type"] != nil {
				continue
			}
			typ = strings.TrimSpace(typ)
			if typ == "namespace" {
				if err := protectTools(list(tool["tools"])); err != nil {
					return err
				}
				continue
			}
			if typ != "" && typ != "function" {
				continue
			}
			// The SDK accepts both flat Responses definitions and nested Chat aliases.
			owner := tool
			strict, supplied := tool["strict"]
			if !supplied {
				owner = object(tool["function"])
				strict, supplied = owner["strict"]
			}
			if !hasStrict {
				continue
			}
			flag, ok := strict.(bool)
			if supplied && !ok {
				return fault.New(400, "tool strict mode must be boolean")
			}
			var parameters any
			present := false
			for _, candidate := range []struct {
				node map[string]any
				key  string
			}{
				{tool, "parameters"}, {tool, "parametersJsonSchema"}, {tool, "input_schema"},
				{object(tool["function"]), "parameters"}, {object(tool["function"]), "parametersJsonSchema"},
			} {
				if parameters, present = candidate.node[candidate.key]; present {
					break
				}
			}
			if present && object(parameters) == nil {
				return fault.New(400, "explicit strict tool parameters must be an object")
			}
			token := fmt.Sprintf("%s_tool_%d", marker, len(state.tools))
			state.tools[token] = sdkResponsesChatTool{strict: flag, hadStrict: supplied, parameters: catalog.Clone(parameters), hadParameters: present}
			schema := object(catalog.Clone(parameters))
			if schema == nil {
				schema = map[string]any{}
			}
			schema[marker] = token
			tool["parameters"] = schema
			// Remove only the private copy; restoration below is the sole authority.
			delete(owner, "strict")
		}
		return nil
	}
	if err := protectTools(list(body["tools"])); err != nil {
		return nil, nil, err
	}
	for _, value := range list(body["input"]) {
		item := object(value)
		if item["type"] == "additional_tools" {
			if err := protectTools(list(item["tools"])); err != nil {
				return nil, nil, err
			}
		}
	}
	if len(state.roles) == 0 && len(state.tools) == 0 {
		return raw, nil, nil
	}
	encoded, err := json.Marshal(body)
	return encoded, state, err
}

func (state *sdkResponsesChatState) restore(body map[string]any) error {
	if state == nil {
		return nil
	}
	for _, value := range list(body["messages"]) {
		message := object(value)
		role, _ := message["role"].(string)
		if _, exists := state.roles[role]; exists {
			state.roles[role]++
			message["role"] = "developer"
		}
	}
	for _, count := range state.roles {
		if count != 1 {
			return fault.New(400, "protocol conversion cannot preserve developer message identity")
		}
	}
	seen := map[string]int{}
	for _, value := range list(body["tools"]) {
		function := object(object(value)["function"])
		schema := object(function["parameters"])
		token, marked := schema[state.marker].(string)
		if !marked {
			continue
		}
		original, exists := state.tools[token]
		if !exists {
			return fault.New(400, "protocol conversion produced an unknown tool identity")
		}
		seen[token]++
		delete(schema, state.marker)
		expected := original.parameters
		if !original.hadParameters {
			expected = map[string]any{}
		}
		if !reflect.DeepEqual(schema, expected) {
			return fault.New(400, "protocol conversion cannot preserve strict tool schema")
		}
		if original.hadParameters {
			function["parameters"] = original.parameters
		} else {
			delete(function, "parameters")
		}
		if original.hadStrict {
			function["strict"] = original.strict
		}
	}
	for token := range state.tools {
		if seen[token] != 1 {
			return fault.New(400, "protocol conversion cannot preserve duplicate or ambiguous strict tools")
		}
	}
	// Fail closed if a future SDK version copies a private marker elsewhere.
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if strings.Contains(string(encoded), state.marker) {
		return fault.New(400, "protocol conversion retained a private preservation marker")
	}
	return nil
}
