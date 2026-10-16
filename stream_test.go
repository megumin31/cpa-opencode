package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

type byteReader struct{ r io.Reader }

func (b byteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return b.r.Read(p)
}
func TestSSEFragmentationMultilineAndCRLF(t *testing.T) {
	input := ": comment\r\nevent: message\r\ndata: {\"text\":\r\ndata: \"你好\"}\r\n\r\ndata: {\"last\":true}"
	var events []sseEvent
	err := readSSE(context.Background(), byteReader{strings.NewReader(input)}, func(e sseEvent) error { events = append(events, e); return nil })
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	if !json.Valid(events[0].Data) || !bytes.Contains(events[0].Data, []byte("你好")) {
		t.Fatal(string(events[0].Data))
	}
}
func TestStreamStopsOnDefinitiveTerminalWithoutEOF(t *testing.T) {
	for _, protocol := range []string{"openai", "claude", "openai-response"} {
		t.Run(protocol, func(t *testing.T) {
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			done := make(chan error, 1)
			go func() { _, err := aggregateStream(context.Background(), reader, protocol); done <- err }()
			go func() { _, _ = writer.Write([]byte(nativeFixture(protocol))) }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("terminal waited for EOF")
			}
		})
	}
}
func TestNativeAggregationPreservesToolsReasoningAndUsage(t *testing.T) {
	for _, protocol := range []string{"openai", "claude", "openai-response", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			out, err := aggregateStream(context.Background(), byteReader{strings.NewReader(nativeFixture(protocol))}, protocol)
			if err != nil {
				t.Fatal(err)
			}
			for _, expected := range []string{"hello", "weather", "Paris"} {
				if !bytes.Contains(out, []byte(expected)) {
					t.Fatalf("missing %s: %s", expected, out)
				}
			}
			if !bytes.Contains(out, []byte("usage")) && !bytes.Contains(out, []byte("usageMetadata")) {
				t.Fatalf("missing usage: %s", out)
			}
			var decoded map[string]any
			if json.Unmarshal(out, &decoded) != nil {
				t.Fatal(string(out))
			}
		})
	}
}
func TestStreamRejectsErrorsAndTruncation(t *testing.T) {
	for _, fixture := range []string{`data: {"error":{"message":"test-service-key"}}` + "\n\n", `event: error` + "\n" + `data: {"type":"error"}` + "\n\n", `data: {"choices":[{"index":0,"delta":{"content":"partial"}}]}` + "\n\n"} {
		_, err := aggregateStream(context.Background(), strings.NewReader(fixture), "openai")
		if err == nil {
			t.Fatal("accepted incomplete/error stream")
		}
		if strings.Contains(err.Error(), "test-service-key") {
			t.Fatal("error leaked body")
		}
	}
}
func TestAllAvailableProtocolPairs(t *testing.T) {
	protocols := []string{"openai", "openai-response", "claude", "gemini"}
	for _, source := range protocols {
		for _, target := range protocols {
			t.Run(source+"_to_"+target, func(t *testing.T) {
				up, out := formatName(source), formatName(target)
				if !responseConversionAllowed(up, out) {
					t.Skip("explicitly rejected SDK conversion")
				}
				if source != target && !registry.HasNonStreamResponseTransformer(out, sdkUpstream(up, out)) {
					t.Skip("SDK has no declared conversion")
				}
				body, err := aggregateStream(context.Background(), strings.NewReader(nativeFixture(source)), source)
				if err != nil {
					t.Fatal(err)
				}
				var param any
				if source != target {
					body = registry.TranslateNonStream(context.Background(), sdkUpstream(up, out), out, "opencode/p/m", []byte(clientRequest(target)), []byte(clientRequest(source)), sdkNonStreamBody(up, out, body), &param)
				}
				if err := translationError(param); err != nil {
					t.Fatal(err)
				}
				if !json.Valid(body) || !bytes.Contains(body, []byte("hello")) || !bytes.Contains(body, []byte("weather")) {
					t.Fatalf("invalid or lossy response %s", body)
				}
				if source != target && !registry.HasStreamResponseTransformer(out, sdkUpstream(up, out)) {
					return
				}
				var result bytes.Buffer
				err = forwardStream(context.Background(), strings.NewReader(nativeFixture(source)), route{Protocol: source, Alias: "opencode/p/m"}, out, []byte(clientRequest(target)), []byte(clientRequest(source)), func(chunk []byte) error { result.Write(chunk); return nil })
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(result.String(), "hello") || !strings.Contains(result.String(), "weather") {
					t.Fatalf("stream conversion lost content/tools: %s", result.String())
				}
			})
		}
	}
}
func clientRequest(protocol string) string {
	switch protocol {
	case "openai":
		return `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]}`
	case "claude":
		return `{"model":"m","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}]}`
	case "openai-response":
		return `{"model":"m","input":[{"role":"user","content":"hi"}],"tools":[{"type":"function","name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}`
	case "gemini":
		return `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}]}`
	}
	return `{}`
}
func nativeFixture(protocol string) string {
	switch protocol {
	case "openai":
		return `data: {"id":"chat-test","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hello","reasoning_content":"think"}}]}

data: {"id":"chat-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-test","type":"function","function":{"name":"weather","arguments":"{\"city\":"}}]}}]}

data: {"id":"chat-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Paris\"}"}}]},"finish_reason":"tool_calls"}]}

data: {"id":"chat-test","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":7,"total_tokens":19,"prompt_tokens_details":{"cached_tokens":3}}}

data: [DONE]

`
	case "claude":
		return `event: message_start
data: {"type":"message_start","message":{"id":"msg-test","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":12,"output_tokens":0,"cache_read_input_tokens":3}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"think"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"test-signature"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call-test","name":"weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Paris\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}

event: message_stop
data: {"type":"message_stop"}

`
	case "openai-response":
		return `event: response.created
data: {"type":"response.created","response":{"id":"resp-test","object":"response","model":"m","created_at":1,"output":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg-test","type":"message","role":"assistant","content":[]}}

event: response.content_part.added
data: {"type":"response.content_part.added","item_id":"msg-test","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg-test","output_index":0,"content_index":0,"delta":"hello"}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":1,"item":{"id":"fn-test","type":"function_call","call_id":"call-test","name":"weather","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fn-test","output_index":1,"delta":"{\"city\":\"Paris\"}"}

event: response.function_call_arguments.done
data: {"type":"response.function_call_arguments.done","item_id":"fn-test","output_index":1,"arguments":"{\"city\":\"Paris\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":1,"item":{"id":"fn-test","type":"function_call","call_id":"call-test","name":"weather","arguments":"{\"city\":\"Paris\"}"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp-test","object":"response","status":"completed","model":"m","output":[{"id":"msg-test","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]},{"id":"fn-test","type":"function_call","call_id":"call-test","name":"weather","arguments":"{\"city\":\"Paris\"}"},{"type":"reasoning","id":"rs-test","summary":[{"type":"summary_text","text":"think"}],"encrypted_content":"test-encrypted"}],"usage":{"input_tokens":12,"output_tokens":7,"total_tokens":19,"input_tokens_details":{"cached_tokens":3}}}}

`
	case "gemini":
		return `data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"hello"},{"text":"think","thought":true,"thoughtSignature":"test-signature"}]}}],"modelVersion":"m","responseId":"gemini-test"}

data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"id":"call-test","name":"weather","args":{"city":"Paris"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":7,"totalTokenCount":19,"cachedContentTokenCount":3}}

`
	}
	return ""
}
