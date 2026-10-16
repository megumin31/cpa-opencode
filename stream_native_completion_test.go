package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestNativeSSESeparatorsAndBoundaries(t *testing.T) {
	for _, sep := range []string{"\n", "\r", "\r\n"} {
		for _, fragmented := range []bool{false, true} {
			t.Run(fmt.Sprintf("%q/fragmented=%v", sep, fragmented), func(t *testing.T) {
				raw := strings.Join([]string{"\ufeff: comment", "id: one", "event: custom", "data: first", "data: second", "", "data: third", "", "id:", "", "id: bad\x00id", "data: fourth", "", "data: discarded", ""}, sep)
				var reader io.Reader = strings.NewReader(raw)
				if fragmented {
					reader = byteReader{reader}
				}
				var events []sseEvent
				err := readSSE(context.Background(), reader, func(e sseEvent) error { events = append(events, e); return nil })
				if err != nil || len(events) != 3 {
					t.Fatalf("events=%+v err=%v", events, err)
				}
				if string(events[0].Data) != "first\nsecond" || events[0].Event != "custom" || events[0].ID != "one" {
					t.Fatalf("first=%+v", events[0])
				}
				if string(events[1].Data) != "third" || events[1].Event != "" || events[1].ID != "one" {
					t.Fatalf("second=%+v", events[1])
				}
				if events[2].ID != "" || !events[2].IDSet || !strings.Contains(string(events[2].wire()), "id: \n") {
					t.Fatalf("reset=%+v", events[2])
				}
			})
		}
	}
}

func TestNativeSSETerminalCRDoesNotWaitForLF(t *testing.T) {
	for _, sep := range []string{"\r", "\r\n"} {
		t.Run(fmt.Sprintf("%q", sep), func(t *testing.T) {
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			result := make(chan error, 1)
			go func() { _, err := aggregateStream(context.Background(), reader, "openai"); result <- err }()
			fixture := "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}" + sep + sep + "data: [DONE]" + sep + "\r"
			go func() { _, _ = io.WriteString(writer, fixture) }()
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("terminal blocked waiting for another byte")
			}
		})
	}
}

func TestNativeChatCompletionAcceptanceAndWire(t *testing.T) {
	frame := func(reason string) string {
		return "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":" + reason + "}]}\n\n"
	}
	cases := []struct {
		name, input       string
		optional, success bool
	}{
		{"finish EOF", frame(`"stop"`), false, true},
		{"finish DONE", frame(`"stop"`) + "data: [DONE]\n\n", false, true},
		{"missing EOF", frame(`null`), false, false},
		{"missing DONE", frame(`null`) + "data: [DONE]\n\n", false, false},
		{"DONE alone", "data: [DONE]\n\n", false, false},
		{"pending finish EOF", strings.TrimSuffix(frame(`"stop"`), "\n"), false, false},
		{"error", frame(`"error"`) + "data: [DONE]\n\n", false, false},
		{"network error", frame(`"network_error"`), false, false},
		{"unknown reason", frame(`"private-unknown-value"`), false, false},
		{"invalid type", frame(`{}`), false, false},
		{"optional EOF", frame(`null`), true, true},
		{"optional DONE", frame(`null`) + "data: [DONE]\n\n", true, true},
		{"optional error", frame(`"error"`), true, false},
		{"optional network error", frame(`"network_error"`), true, false},
	}
	for _, reason := range []string{"end", "length", "content_filter", "function_call", "tool_calls"} {
		cases = append(cases, struct {
			name, input       string
			optional, success bool
		}{reason, frame(fmt.Sprintf("%q", reason)), false, true})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := route{Protocol: "openai", Alias: "opencode/p/m"}
			if tc.optional {
				r.Compatibility = map[string]any{"requireFinishReason": false}
			}
			aggregate, err := aggregateStream(context.Background(), byteReader{strings.NewReader(tc.input)}, "openai", r)
			if (err == nil) != tc.success {
				t.Fatalf("aggregate=%s err=%v success=%v", aggregate, err, tc.success)
			}
			if tc.success && !bytes.Contains(aggregate, []byte("hello")) {
				t.Fatalf("content lost: %s", aggregate)
			}
			var wire bytes.Buffer
			err = forwardStream(context.Background(), byteReader{strings.NewReader(tc.input)}, r, formatName("openai"), []byte(clientRequest("openai")), nil, func(p []byte) error { _, _ = wire.Write(p); return nil })
			if (err == nil) != tc.success {
				t.Fatalf("wire=%s err=%v success=%v", wire.String(), err, tc.success)
			}
			if tc.success && strings.Count(wire.String(), "data: [DONE]") != 1 {
				t.Fatalf("missing/duplicated final output: %s", wire.String())
			}
			if !tc.success && strings.Contains(wire.String(), "[DONE]") {
				t.Fatalf("forwarded false success: %s", wire.String())
			}
			if err != nil && strings.Contains(err.Error(), "private-unknown-value") {
				t.Fatalf("unsafe diagnostic: %v", err)
			}
		})
	}
}

func TestNativeSSEIgnoresKeepalivesForEveryProtocol(t *testing.T) {
	for _, protocol := range []string{"openai", "openai-response", "claude", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			prefix := "data:\n\ndata: null\n\ndata: : keepalive\n\n: comment\n\n"
			if protocol != "openai" {
				prefix += "data: [DONE]\n\n"
			}
			input := prefix + nativeFixture(protocol)
			body, err := aggregateStream(context.Background(), strings.NewReader(input), protocol)
			if err != nil || !bytes.Contains(body, []byte("hello")) {
				t.Fatalf("aggregate=%s err=%v", body, err)
			}
			var wire bytes.Buffer
			err = forwardStream(context.Background(), strings.NewReader(input), route{Protocol: protocol}, formatName(protocol), nil, nil, func(p []byte) error { _, _ = wire.Write(p); return nil })
			if err != nil || !strings.Contains(wire.String(), "hello") || strings.Contains(wire.String(), "keepalive") || strings.Contains(wire.String(), "data: null") {
				t.Fatalf("wire=%s err=%v", wire.String(), err)
			}
		})
	}
}

func TestNativeChatEOFFlushesTranslatedTerminalsAndUsage(t *testing.T) {
	input := strings.Replace(nativeFixture("openai"), "data: [DONE]\n\n", "", 1)
	for _, target := range []string{"openai", "openai-response", "claude", "gemini"} {
		t.Run(target, func(t *testing.T) {
			up, out := formatName("openai"), formatName(target)
			if up != out && (!responseConversionAllowed(up, out) || !registry.HasStreamResponseTransformer(out, sdkUpstream(up, out))) {
				t.Skip("unsupported SDK pair")
			}
			var wire bytes.Buffer
			err := forwardStream(context.Background(), strings.NewReader(input), route{Protocol: "openai", Alias: "opencode/p/m"}, out, []byte(clientRequest(target)), []byte(clientRequest("openai")), func(p []byte) error { _, _ = wire.Write(p); return nil })
			if err != nil {
				t.Fatal(err)
			}
			result, err := aggregateStream(context.Background(), strings.NewReader(wire.String()), target)
			if err != nil || !bytes.Contains(result, []byte("hello")) || !bytes.Contains(result, []byte("weather")) {
				t.Fatalf("aggregate=%s err=%v wire=%s", result, err, wire.String())
			}
		})
	}
}

type streamFailureReader struct{ err error }

func (r streamFailureReader) Read([]byte) (int, error) { return 0, r.err }

func TestNativeStreamFinishDoesNotHideInterruptedRead(t *testing.T) {
	input := strings.Replace(nativeFixture("openai"), "data: [DONE]\n\n", "", 1)
	expected := fail(504, "upstream stream chunk timeout")
	reader := io.MultiReader(strings.NewReader(input), streamFailureReader{expected})
	_, err := aggregateStream(context.Background(), reader, "openai")
	if !errors.Is(err, expected) {
		t.Fatalf("timeout lost: got %v want %v", err, expected)
	}
	reader = io.MultiReader(strings.NewReader(input), streamFailureReader{io.ErrUnexpectedEOF})
	var wire bytes.Buffer
	err = forwardStream(context.Background(), reader, route{Protocol: "openai"}, formatName("openai"), nil, nil, func(p []byte) error { _, _ = wire.Write(p); return nil })
	if err == nil || strings.Contains(wire.String(), "[DONE]") {
		t.Fatalf("interrupted stream succeeded: %v %s", err, wire.String())
	}
}

func TestNativeChatOptionalFinishNormalizesEveryOutput(t *testing.T) {
	for _, tools := range []bool{false, true} {
		for _, done := range []bool{false, true} {
			for _, target := range []string{"openai", "openai-response", "claude", "gemini"} {
				t.Run(fmt.Sprintf("tools=%v/DONE=%v/%s", tools, done, target), func(t *testing.T) {
					input := "data: {\"id\":\"chat-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n"
					want := "stop"
					if tools {
						input = strings.ReplaceAll(nativeFixture("openai"), `"finish_reason":"tool_calls"`, `"finish_reason":null`)
						input = strings.ReplaceAll(input, "data: [DONE]\n\n", "")
						want = "tool_calls"
					}
					if done {
						input += "data: [DONE]\n\n"
					}
					r := route{Protocol: "openai", Alias: "opencode/p/m", Compatibility: map[string]any{"requireFinishReason": false}}
					body, err := aggregateStream(context.Background(), strings.NewReader(input), "openai", r)
					if err != nil || !bytes.Contains(body, []byte(`"finish_reason":"`+want+`"`)) {
						t.Fatalf("fallback body=%s err=%v", body, err)
					}
					up, out := formatName("openai"), formatName(target)
					if up != out && (!responseConversionAllowed(up, out) || !registry.HasStreamResponseTransformer(out, sdkUpstream(up, out))) {
						t.Skip("unsupported SDK pair")
					}
					var wire bytes.Buffer
					err = forwardStream(context.Background(), strings.NewReader(input), r, out, []byte(clientRequest(target)), []byte(clientRequest("openai")), func(p []byte) error { _, _ = wire.Write(p); return nil })
					if err != nil {
						t.Fatal(err)
					}
					result, err := aggregateStream(context.Background(), strings.NewReader(wire.String()), target)
					if err != nil || !bytes.Contains(result, []byte("hello")) {
						t.Fatalf("normalized result=%s err=%v wire=%s", result, err, wire.String())
					}
					if target == "openai" && !bytes.Contains(result, []byte(`"finish_reason":"`+want+`"`)) {
						t.Fatalf("wrong normalized finish: %s", result)
					}
				})
			}
		}
	}
}
