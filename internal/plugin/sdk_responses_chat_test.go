package plugin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

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
func probeCheck(t *testing.T, in, out map[string]any, source string) {
	t.Helper()
	tools := list(out["tools"])
	if len(tools) != 1 {
		t.Fatalf("tool count %d", len(tools))
	}
	fn := object(object(tools[0])["function"])
	var schema any
	json.Unmarshal([]byte(probeSchema), &schema)
	if !reflect.DeepEqual(fn["parameters"], schema) {
		t.Errorf("schema altered: %#v", fn["parameters"])
	}
	if fn["strict"] != true || fn["name"] != "lookup_document" || fn["description"] != "Read an identified document" {
		t.Errorf("definition altered: %#v", fn)
	}
	if out["parallel_tool_calls"] != in["parallel_tool_calls"] {
		t.Errorf("parallel lost: %#v", out["parallel_tool_calls"])
	}
	wantChoice := in["tool_choice"]
	if source != "openai" {
		if c, ok := wantChoice.(map[string]any); ok {
			wantChoice = map[string]any{"type": "function", "function": map[string]any{"name": c["name"]}}
		}
	}
	if !reflect.DeepEqual(wantChoice, out["tool_choice"]) {
		t.Errorf("choice altered: %#v want %#v", out["tool_choice"], wantChoice)
	}
	msgs := list(out["messages"])
	roles := []string{"system", "developer", "user", "assistant", "tool", "user"}
	if len(msgs) != len(roles) {
		t.Errorf("message count %d: %#v", len(msgs), msgs)
		return
	}
	for i, r := range roles {
		if object(msgs[i])["role"] != r {
			t.Errorf("role[%d]=%v want %s", i, object(msgs[i])["role"], r)
		}
	}
	for i, want := range map[int]string{0: "Follow the document access policy.", 1: "Cite section labels.", 2: "Read document 7.", 4: "Document 7 intro: Hello.", 5: "Summarize it."} {
		value := object(msgs[i])["content"]
		if value != want {
			parts := list(value)
			if len(parts) != 1 || object(parts[0])["text"] != want {
				t.Errorf("content[%d]=%#v", i, value)
			}
		}
	}
	calls := list(object(msgs[3])["tool_calls"])
	if len(calls) != 1 {
		t.Errorf("history calls=%#v", calls)
		return
	}
	call := object(calls[0])
	if call["id"] != "call_doc7" || object(msgs[4])["tool_call_id"] != "call_doc7" || object(call["function"])["name"] != "lookup_document" || object(call["function"])["arguments"] != `{"document_id":"7","format":"plain","filters":{"section":"intro"}}` {
		t.Errorf("history altered %#v %#v", call, msgs[4])
	}
}
func TestResponsesChatToolPreservation(t *testing.T) {
	engine := newTestEngine(t)

	for _, source := range []string{"openai", "openai-response"} {
		for _, parallel := range []bool{false, true} {
			for _, choice := range []any{"auto", "none", "required", map[string]any{"type": "function", "name": "lookup_document"}} {
				if source == "openai" {
					if _, ok := choice.(map[string]any); ok {
						choice = map[string]any{"type": "function", "function": map[string]any{"name": "lookup_document"}}
					}
				}
				t.Run(fmt.Sprintf("%s/%v/%v", source, parallel, choice), func(t *testing.T) {
					engine.testConfig(t, "http://127.0.0.1:18080")
					in := probeRaw(source, choice, parallel)
					raw, _ := json.Marshal(in)
					out, err := engine.prepareSemanticFixture(t, source, "openai", string(raw), nil)
					if err != nil {
						t.Fatal(err)
					}
					probeCheck(t, in, out, source)
				})
			}
		}
	}
}
func TestResponsesChatReasoningSummaryAutoAllowed(t *testing.T) {
	engine := newTestEngine(t)

	engine.testConfig(t, "http://127.0.0.1:18080")
	raw := `{"model":"opencode/p/m","input":[{"role":"user","content":"hello"}],"reasoning":{"summary":"auto","effort":"none"}}`
	out, err := engine.prepareSemanticFixture(t, "openai-response", "openai", raw, nil)
	if err != nil {
		t.Fatalf("expected summary: auto to be allowed, got error: %v", err)
	}
	if out["messages"] == nil {
		t.Fatal("expected messages in converted output")
	}
}
func TestResponsesChatToolWire(t *testing.T) {
	engine := newTestEngine(t)

	for _, source := range []string{"openai", "openai-response"} {
		t.Run(source, func(t *testing.T) {
			received := make(chan map[string]any, 1)
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/api/v2/config") {
					io.WriteString(w, `{"providers":{"p":{"package":"@ai-sdk/openai-compatible","settings":{"baseURL":"`+srv.URL+`/inference/v1"},"models":{"m":{}}}}}`)
					return
				}
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				received <- body
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, nativeFixture("openai"))
			}))
			defer srv.Close()
			engine.testConfig(t, srv.URL)
			in := probeRaw(source, "required", false)
			raw, _ := json.Marshal(in)
			req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "opencode/p/m", Format: source, SourceFormat: source, Payload: raw, StorageJSON: rawJSON(t, testCredential(srv.URL))}}
			_, err := engine.execute(rawJSON(t, req), false)
			if err != nil {
				t.Fatal(err)
			}
			probeCheck(t, in, <-received, source)
		})
	}
}
func TestResponsesChatTypedHistoryOrder(t *testing.T) {
	engine := newTestEngine(t)

	engine.testConfig(t, "http://127.0.0.1:18080")
	in := probeRaw("openai-response", "required", false)
	in["instructions"] = "Global instruction."
	for _, v := range list(in["input"]) {
		m := object(v)
		if m["role"] != nil {
			m["type"] = "message"
			m["content"] = []any{map[string]any{"type": "input_text", "text": m["content"]}}
		}
	}
	raw, _ := json.Marshal(in)
	out, err := engine.prepareSemanticFixture(t, "openai-response", "openai", string(raw), nil)
	if err != nil {
		t.Fatal(err)
	}
	msgs := list(out["messages"])
	if len(msgs) != 7 {
		t.Fatalf("messages %#v", msgs)
	}
	if object(msgs[0])["role"] != "system" || object(msgs[0])["content"] != "Global instruction." {
		t.Errorf("instructions not leading system")
	}
	out["messages"] = msgs[1:]
	probeCheck(t, in, out, "openai-response")
}

func TestResponsesChatStrictValidationAndDuplicates(t *testing.T) {
	engine := newTestEngine(t)

	for _, tc := range []struct {
		name      string
		flag      any
		duplicate bool
		wantErr   bool
	}{
		{"true", true, false, false}, {"false", false, false, false}, {"null", nil, false, true},
		{"string", "true", false, true}, {"number", float64(1), false, true},
		{"duplicate", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine.testConfig(t, "http://127.0.0.1:18080")
			in := probeRaw("openai-response", "auto", false)
			fn := object(list(in["tools"])[0])
			fn["strict"] = tc.flag
			if tc.duplicate {
				in["tools"] = append(list(in["tools"]), catalog.Clone(fn))
			}
			raw, _ := json.Marshal(in)
			out, err := engine.prepareSemanticFixture(t, "openai-response", "openai", string(raw), nil)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected explicit rejection")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if object(object(list(out["tools"])[0])["function"])["strict"] != tc.flag {
				t.Fatal("strict changed", out)
			}
		})
	}
}

func TestResponsesChatStrictSchemaAliases(t *testing.T) {
	engine := newTestEngine(t)

	for _, alias := range []string{"parameters", "parametersJsonSchema", "input_schema", "nested.parameters", "nested.parametersJsonSchema", "absent"} {
		t.Run(alias, func(t *testing.T) {
			engine.testConfig(t, "http://127.0.0.1:18080")
			in := probeRaw("openai-response", "auto", false)
			fn := object(list(in["tools"])[0])
			schema := fn["parameters"]
			delete(fn, "parameters")
			if strings.HasPrefix(alias, "nested.") {
				nested := map[string]any{"strict": true, strings.TrimPrefix(alias, "nested."): schema}
				delete(fn, "strict")
				fn["function"] = nested
			} else if alias != "absent" {
				fn[alias] = schema
			}
			raw, _ := json.Marshal(in)
			out, err := engine.prepareSemanticFixture(t, "openai-response", "openai", string(raw), nil)
			if err != nil {
				t.Fatal(err)
			}
			got := object(object(list(out["tools"])[0])["function"])
			if got["strict"] != true {
				t.Fatal(got)
			}
			if alias == "absent" {
				if _, exists := got["parameters"]; exists {
					t.Fatal("absent schema invented", got)
				}
			} else if !reflect.DeepEqual(got["parameters"], schema) {
				t.Fatal("schema alias lost", got)
			}
		})
	}
}

func TestResponsesChatComplexRoleIdentity(t *testing.T) {
	engine := newTestEngine(t)

	engine.testConfig(t, "http://127.0.0.1:18080")
	in := probeRaw("openai-response", "required", false)
	in["instructions"] = "__cpa_responses_chat_preserve"
	items := list(in["input"])
	items[0] = map[string]any{"role": "system", "content": "same"}
	items[1] = map[string]any{"role": "developer", "content": []any{map[string]any{"type": "input_text", "text": "same"}, map[string]any{"type": "input_text", "text": "second"}}}
	items[2] = map[string]any{"role": "user", "content": "same"}
	items = append(items, map[string]any{"role": "developer", "type": "message", "content": "same"}, map[string]any{"role": "user", "content": "same"})
	in["input"] = items
	raw, _ := json.Marshal(in)
	original := string(raw)
	out, err := engine.prepareSemanticFixture(t, "openai-response", "openai", original, nil)
	if err != nil {
		t.Fatal(err)
	}
	messages := list(out["messages"])
	roles := []string{"system", "system", "developer", "user", "assistant", "tool", "user", "developer", "user"}
	if len(messages) != len(roles) {
		t.Fatal(messages)
	}
	for i, role := range roles {
		if object(messages[i])["role"] != role {
			t.Fatalf("role %d %#v", i, messages[i])
		}
	}
	parts := list(object(messages[2])["content"])
	if len(parts) != 2 || object(parts[0])["text"] != "same" || object(parts[1])["text"] != "second" {
		t.Fatal("developer parts changed", parts)
	}
	again, _ := json.Marshal(in)
	if string(again) != original {
		t.Fatal("input mutated")
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), "__cpa_responses_chat_preserve_") {
		t.Fatal("private marker leaked")
	}
}

func TestResponsesChatNamespacedStrict(t *testing.T) {
	engine := newTestEngine(t)

	engine.testConfig(t, "http://127.0.0.1:18080")
	in := probeRaw("openai-response", "auto", false)
	fn := list(in["tools"])[0]
	in["tools"] = []any{map[string]any{"type": "namespace", "name": "documents", "tools": []any{fn}}}
	in["input"] = []any{map[string]any{"role": "developer", "content": "Cite sources."}, map[string]any{"role": "user", "content": "Read document 7."}}
	raw, _ := json.Marshal(in)
	out, err := engine.prepareSemanticFixture(t, "openai-response", "openai", string(raw), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := object(object(list(out["tools"])[0])["function"])
	if got["strict"] != true || !reflect.DeepEqual(got["parameters"], object(fn)["parameters"]) {
		t.Fatal(got)
	}
}

func TestResponsesChatMixedStrictDuplicateRejected(t *testing.T) {
	engine := newTestEngine(t)

	for _, firstStrict := range []bool{true, false} {
		t.Run(fmt.Sprint(firstStrict), func(t *testing.T) {
			engine.testConfig(t, "http://127.0.0.1:18080")
			in := probeRaw("openai-response", "auto", false)
			fn := object(list(in["tools"])[0])
			other := object(catalog.Clone(fn))
			if firstStrict {
				delete(other, "strict")
			} else {
				delete(fn, "strict")
			}
			in["tools"] = []any{fn, other}
			raw, _ := json.Marshal(in)
			if _, err := engine.prepareSemanticFixture(t, "openai-response", "openai", string(raw), nil); err == nil {
				t.Fatal("ambiguous mixed-strict duplicate accepted")
			}
		})
	}
}

func TestResponsesChatPaddedToolTypes(t *testing.T) {
	engine := newTestEngine(t)

	for _, namespace := range []bool{false, true} {
		t.Run(fmt.Sprint(namespace), func(t *testing.T) {
			engine.testConfig(t, "http://127.0.0.1:18080")
			in := probeRaw("openai-response", "auto", false)
			fn := object(list(in["tools"])[0])
			fn["type"] = " function "
			if namespace {
				in["tools"] = []any{map[string]any{"type": " namespace ", "name": "docs", "tools": []any{fn}}}
			}
			in["input"] = []any{map[string]any{"role": "developer", "content": "Cite sources."}, map[string]any{"role": "user", "content": "Read."}}
			raw, _ := json.Marshal(in)
			out, err := engine.prepareSemanticFixture(t, "openai-response", "openai", string(raw), nil)
			if err != nil {
				t.Fatal(err)
			}
			if object(object(list(out["tools"])[0])["function"])["strict"] != true {
				t.Fatal("padded type lost strict", out)
			}
		})
	}
}

func TestResponsesChatMalformedToolsRejected(t *testing.T) {
	engine := newTestEngine(t)

	for _, bad := range []any{nil, float64(1), "invalid", true, []any{}} {
		t.Run(fmt.Sprintf("%T", bad), func(t *testing.T) {
			for _, namespace := range []bool{false, true} {
				engine.testConfig(t, "http://127.0.0.1:18080")
				in := probeRaw("openai-response", "auto", false)
				fn := list(in["tools"])[0]
				tools := []any{bad, fn}
				if namespace {
					tools = []any{map[string]any{"type": "namespace", "name": "docs", "tools": tools}}
				}
				in["tools"] = tools
				raw, _ := json.Marshal(in)
				if _, err := engine.prepareSemanticFixture(t, "openai-response", "openai", string(raw), nil); err == nil {
					t.Fatal("malformed tool accepted")
				}
			}
		})
	}
}
