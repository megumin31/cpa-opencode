package plugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
	wire "github.com/megumin31/cpa-opencode-zen/internal/protocol"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestScopedCacheAffinityOnFinalWire(t *testing.T) {
	engine := newTestEngine(t)

	for _, protocol := range []string{"openai", "openai-response"} {
		t.Run(protocol, func(t *testing.T) {
			var server *httptest.Server
			var ids, keys []string
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "/api/v2/config") {
					io.WriteString(w, `{"providers":{"p":{"package":"`+protocolPackage(protocol)+`","settings":{"baseURL":"`+server.URL+`/inference"},"models":{"m":{"compatibility":{"supportsPromptCacheKey":true}}}}}}`)
					return
				}
				var body map[string]any
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				ids = append(ids, req.Header.Get("X-Session-Affinity"))
				key, _ := body["prompt_cache_key"].(string)
				keys = append(keys, key)
				if req.Header.Get("X-Opencode-Client") != "" || req.Header.Get("X-Opencode-Project") != "" {
					t.Error("fabricated official identity")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, nativeFixture(protocol))
			}))
			defer server.Close()
			engine.testConfig(t, server.URL)
			req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "opencode/p/m", Format: protocol, SourceFormat: protocol, Payload: []byte(clientRequest(protocol)), StorageJSON: rawJSON(t, testCredential(server.URL)), Metadata: map[string]any{"caller_scope": "caller", "execution_session_id": "conversation"}}}
			for i := 0; i < 2; i++ {
				if _, err := engine.execute(rawJSON(t, req), false); err != nil {
					t.Fatal(err)
				}
			}
			req.Metadata["caller_scope"] = "other caller"
			if _, err := engine.execute(rawJSON(t, req), false); err != nil {
				t.Fatal(err)
			}
			if len(keys) != 3 || keys[0] == "" || !strings.HasPrefix(keys[0], "ses_") || keys[0] != ids[0] || keys[1] != keys[0] || keys[2] == keys[0] {
				t.Fatalf("affinity=%v cache=%v", ids, keys)
			}
		})
	}
}

func TestScopedCachePreservesExplicitKeysAndSupportPolicy(t *testing.T) {
	engine := newTestEngine(t)

	engine.testConfig(t, "https://opencode.ai")
	for _, protocol := range []string{"openai", "openai-response", "claude", "gemini"} {
		for _, support := range []bool{false, true} {
			r := catalog.Route{Protocol: protocol, ModelID: "m", BaseURL: "https://opencode.ai/inference", Compatibility: map[string]any{"supportsPromptCacheKey": support}}
			// Non-OpenAI protocols do not expose this compatibility option.
			if protocol == "claude" || protocol == "gemini" {
				r.Compatibility = nil
			}
			raw := []byte(clientRequest(protocol))
			payload, _, _, err := engine.prepareRequest(engine.current.config, context.Background(), credential{Key: "fake"}, r, wire.FormatName(protocol), raw, "cpa_scope")
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			json.Unmarshal(payload, &got)
			want := support && (protocol == "openai" || protocol == "openai-response")
			if (got["prompt_cache_key"] != nil) != want {
				t.Fatalf("%s support=%v: %s", protocol, support, payload)
			}
			if !want {
				continue
			}
			var input map[string]any
			json.Unmarshal(raw, &input)
			input["prompt_cache_key"] = "caller-key"
			payload, _, _, err = engine.prepareRequest(engine.current.config, context.Background(), credential{Key: "fake"}, r, wire.FormatName(protocol), rawJSON(t, input), "cpa_scope")
			if err != nil {
				t.Fatal(err)
			}
			json.Unmarshal(payload, &got)
			if got["prompt_cache_key"] != "caller-key" {
				t.Fatalf("caller key overwritten: %s", payload)
			}
			r.Body = map[string]any{"prompt_cache_key": "configured-key"}
			payload, _, _, err = engine.prepareRequest(engine.current.config, context.Background(), credential{Key: "fake"}, r, wire.FormatName(protocol), rawJSON(t, input), "cpa_scope")
			if err != nil {
				t.Fatal(err)
			}
			json.Unmarshal(payload, &got)
			if got["prompt_cache_key"] != "configured-key" {
				t.Fatalf("final body priority changed: %s", payload)
			}
		}
	}
}

func TestExecutorParsesSSEIndependentOfContentType(t *testing.T) {
	engine := newTestEngine(t)

	for _, contentType := range []string{"", "application/json", "text/event-stream"} {
		t.Run(contentType, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "/api/v2/config") {
					io.WriteString(w, `{"providers":{"p":{"package":"`+protocolPackage("openai")+`","settings":{"baseURL":"`+server.URL+`/inference"},"models":{"m":{}}}}}`)
					return
				}
				w.Header()["Content-Type"] = []string{contentType}
				io.WriteString(w, nativeFixture("openai"))
			}))
			defer server.Close()
			engine.testConfig(t, server.URL)
			req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "opencode/p/m", Format: "openai", Payload: []byte(clientRequest("openai")), StorageJSON: rawJSON(t, testCredential(server.URL))}}
			if _, err := engine.execute(rawJSON(t, req), false); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCrossProtocolCacheKeyIsNotReplacedByAffinity(t *testing.T) {
	engine := newTestEngine(t)

	engine.testConfig(t, "https://opencode.ai")
	for _, pair := range [][2]string{{"openai", "openai-response"}, {"openai-response", "openai"}} {
		var input map[string]any
		json.Unmarshal([]byte(clientRequest(pair[0])), &input)
		input["prompt_cache_key"] = "explicit-cross-protocol-key"
		r := catalog.Route{Protocol: pair[1], ModelID: "m", BaseURL: "https://opencode.ai/inference", Compatibility: map[string]any{"supportsPromptCacheKey": true}}
		payload, _, _, err := engine.prepareRequest(engine.current.config, context.Background(), credential{Key: "fake"}, r, wire.FormatName(pair[0]), rawJSON(t, input), "cpa_affinity")
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		json.Unmarshal(payload, &result)
		if result["prompt_cache_key"] != "explicit-cross-protocol-key" {
			t.Fatalf("%v: %s", pair, payload)
		}
	}
}

func TestStrictProfileRejectsTransportRequestCompression(t *testing.T) {
	engine := newTestEngine(t)

	for _, encoding := range []string{"gzip", "zstd"} {
		t.Run(encoding, func(t *testing.T) {
			posts := 0
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "/api/v2/config") {
					io.WriteString(w, `{"providers":{"p":{"package":"`+protocolPackage("openai")+`","settings":{"baseURL":"`+server.URL+`/inference","encoding":"`+encoding+`"},"models":{"m":{"compatibility":{"supportsStore":false}}}}}`)
					return
				}
				posts++
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, nativeFixture("openai"))
			}))
			defer server.Close()
			engine.testConfig(t, server.URL)
			engine.current.Lock()
			engine.current.config.TestGuard = &inferenceTestGuard{Enabled: true, StrictTextOnly: true, AllowedRequests: []inferenceGuardRule{{Model: "m", Endpoint: server.URL + "/inference/chat/completions"}}}
			engine.current.Unlock()
			req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "opencode/p/m", Format: "openai", Payload: []byte(`{"messages":[{"role":"user","content":"Hi"}],"max_tokens":16}`), StorageJSON: rawJSON(t, testCredential(server.URL))}}
			if _, err := engine.execute(rawJSON(t, req), false); err == nil {
				t.Fatal("strict compression accepted")
			}
			if posts != 0 {
				t.Fatal("strict request sent")
			}
		})
	}
}
