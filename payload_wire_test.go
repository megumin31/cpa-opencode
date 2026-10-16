package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestPayloadSemanticsOnActualLoopbackWire(t *testing.T) {
	for _, tc := range []struct {
		target, extra string
		expected      map[string]any
	}{
		{"claude", `,"temperature":0.37,"top_p":0.83`, map[string]any{"temperature": 0.37, "top_p": 0.83}},
		{"gemini", `,"presence_penalty":0.3,"frequency_penalty":0.4,"seed":42,"stop":["END"]`, map[string]any{"presencePenalty": 0.3, "frequencyPenalty": 0.4, "seed": float64(42)}},
		{"openai-response", `,"presence_penalty":0.3,"frequency_penalty":0.4`, map[string]any{"presence_penalty": 0.3, "frequency_penalty": 0.4}},
	} {
		t.Run(tc.target, func(t *testing.T) {
			received := make(chan map[string]any, 1)
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/api/v2/config") {
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `{"providers":{"p":{"package":"`+protocolPackage(tc.target)+`","settings":{"baseURL":"`+server.URL+`/inference/v1"},"models":{"m":{}}}}}`)
					return
				}
				if r.Method != "POST" {
					t.Errorf("unexpected inference method %s", r.Method)
				}
				if r.Header.Get("User-Agent") != "opencode/latest/2.0.29/cli" {
					t.Errorf("unexpected client identity %q", r.Header.Get("User-Agent"))
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				received <- body
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, nativeFixture(tc.target))
			}))
			defer server.Close()
			testConfig(t, server.URL)
			req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "opencode/p/m", Format: "openai", SourceFormat: "openai", Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]` + tc.extra + `}`), StorageJSON: rawJSON(t, testCredential(server.URL))}}
			result, err := execute(rawJSON(t, req), false)
			if err != nil {
				t.Fatal(err)
			}
			response, ok := result.(pluginapi.ExecutorResponse)
			if !ok || !json.Valid(response.Payload) || !strings.Contains(string(response.Payload), "hello") {
				t.Fatalf("invalid converted response: %#v", result)
			}
			body := <-received
			values := body
			if tc.target == "gemini" {
				values = object(body["generationConfig"])
			}
			for key, want := range tc.expected {
				if values[key] != want {
					t.Fatalf("wire %s=%#v want %#v; %#v", key, values[key], want, body)
				}
			}
			if body["metadata"] != nil || body["safetySettings"] != nil {
				t.Fatal("unsolicited metadata/safety reached wire", body)
			}
			if tc.target == "gemini" {
				stops := list(values["stopSequences"])
				if len(stops) != 1 || stops[0] != "END" {
					t.Fatal("lost wire stop sequences", body)
				}
			}
		})
	}
}

func TestMixedAttachmentLossRejectedBeforeInferencePOST(t *testing.T) {
	for _, tc := range []struct{ target, part string }{
		{"gemini", `{"type":"image_url","image_url":{"url":"https://example.invalid/image.png"}}`},
		{"claude", `{"type":"input_audio","input_audio":{"data":"YXVkaW8=","format":"wav"}}`},
	} {
		t.Run(tc.target, func(t *testing.T) {
			var posts atomic.Int32
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/api/v2/config") {
					io.WriteString(w, `{"providers":{"p":{"package":"`+protocolPackage(tc.target)+`","settings":{"baseURL":"`+server.URL+`/inference/v1"},"models":{"m":{}}}}}`)
					return
				}
				posts.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, nativeFixture(tc.target))
			}))
			defer server.Close()
			testConfig(t, server.URL)
			req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "opencode/p/m", Format: "openai", SourceFormat: "openai", Payload: []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"},` + tc.part + `]}]}`), StorageJSON: rawJSON(t, testCredential(server.URL))}}
			_, err := execute(rawJSON(t, req), false)
			if failure, ok := err.(*statusError); !ok || failure.status != 400 {
				t.Fatalf("expected local 400, got %v", err)
			}
			if posts.Load() != 0 {
				t.Fatalf("lossy request was sent: %d inference requests", posts.Load())
			}
		})
	}
}
