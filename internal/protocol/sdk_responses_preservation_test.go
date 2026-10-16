package protocol

import (
	"encoding/json"
	"testing"

	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
)

func TestResponsesChatPreservationFailsClosed(t *testing.T) {
	for _, kind := range []string{"missing-role", "duplicate-role", "missing-tool", "duplicate-tool", "changed-schema", "marker-leak"} {
		t.Run(kind, func(t *testing.T) {
			in := probeRaw("openai-response", "auto", false)
			raw, _ := json.Marshal(in)
			protected, state, err := sdkProtectResponsesChat(raw)
			if err != nil {
				t.Fatal(err)
			}
			var private map[string]any
			json.Unmarshal(protected, &private)
			role := object(list(private["input"])[1])["role"]
			schema := object(list(private["tools"])[0])["parameters"]
			out := map[string]any{"messages": []any{map[string]any{"role": role, "content": "text"}}, "tools": []any{map[string]any{"type": "function", "function": map[string]any{"parameters": schema}}}}
			switch kind {
			case "missing-role":
				out["messages"] = []any{}
			case "duplicate-role":
				out["messages"] = append(list(out["messages"]), catalog.Clone(list(out["messages"])[0]))
			case "missing-tool":
				out["tools"] = []any{}
			case "duplicate-tool":
				out["tools"] = append(list(out["tools"]), catalog.Clone(list(out["tools"])[0]))
			case "changed-schema":
				object(schema)["additionalProperties"] = true
			case "marker-leak":
				out["unexpected"] = state.marker
			}
			if err := state.restore(out); err == nil {
				t.Fatal("corruption accepted")
			}
		})
	}
}

const probeSchema = `{"type":"object","properties":{"document_id":{"type":"string"},"format":{"type":"string","enum":["plain","markdown"]},"filters":{"type":"object","properties":{"section":{"type":"string"}},"required":["section"],"additionalProperties":false}},"required":["document_id","format","filters"],"additionalProperties":false}`

func probeRaw(source string, choice any, parallel bool) map[string]any {
	var schema any
	json.Unmarshal([]byte(probeSchema), &schema)
	fn := map[string]any{"name": "lookup_document", "description": "Read an identified document", "parameters": schema, "strict": true}
	body := map[string]any{"parallel_tool_calls": parallel, "tool_choice": choice}
	if source == "openai" {
		body["tools"] = []any{map[string]any{"type": "function", "function": fn}}
		body["messages"] = []any{map[string]any{"role": "system", "content": "Follow the document access policy."}, map[string]any{"role": "developer", "content": "Cite section labels."}, map[string]any{"role": "user", "content": "Read document 7."}, map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call_doc7", "type": "function", "function": map[string]any{"name": "lookup_document", "arguments": `{"document_id":"7","format":"plain","filters":{"section":"intro"}}`}}}}, map[string]any{"role": "tool", "tool_call_id": "call_doc7", "content": "Document 7 intro: Hello."}, map[string]any{"role": "user", "content": "Summarize it."}}
	} else {
		fn["type"] = "function"
		body["tools"] = []any{fn}
		body["input"] = []any{map[string]any{"role": "system", "content": "Follow the document access policy."}, map[string]any{"role": "developer", "content": "Cite section labels."}, map[string]any{"role": "user", "content": "Read document 7."}, map[string]any{"type": "function_call", "call_id": "call_doc7", "name": "lookup_document", "arguments": `{"document_id":"7","format":"plain","filters":{"section":"intro"}}`}, map[string]any{"type": "function_call_output", "call_id": "call_doc7", "output": "Document 7 intro: Hello."}, map[string]any{"role": "user", "content": "Summarize it."}}
	}
	return body
}
