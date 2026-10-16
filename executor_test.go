package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func protocolPackage(protocol string) string {
	switch protocol {
	case "openai":
		return "@opencode/ai/providers/openai/chat"
	case "openai-response":
		return "@opencode/ai/providers/openai"
	case "claude":
		return "@opencode/ai/providers/anthropic"
	case "gemini":
		return "@opencode/ai/providers/google"
	}
	return "unknown"
}
func TestPrepareNativeRouteAndHeaders(t *testing.T) {
	testConfig(t, "https://opencode.ai")
	for _, protocol := range []string{"openai", "openai-response", "claude", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			r := route{Alias: "opencode/p/m", ProviderID: "p", ModelID: "native-model", Package: protocolPackage(protocol), Protocol: protocol, BaseURL: "https://opencode.ai/inference/provider/v1", Compatibility: map[string]any{}, Settings: map[string]any{}, Body: map[string]any{"custom": map[string]any{"nested": true}}, Headers: map[string]string{"x-opencode-org-id": "configured-org"}}
			c := credential{Type: providerID, Key: "test-service-key", OrgID: "org-test"}
			raw, target, headers, err := prepareRequest(context.Background(), c, r, formatName(protocol), []byte(clientRequest(protocol)))
			if err != nil {
				t.Fatal(err)
			}
			if headers.Get("x-org-id") != "" || headers.Get("x-opencode-org-id") != "configured-org" || !strings.Contains(string(raw), `"nested":true`) {
				t.Fatal("inference must preserve configured organization without copying Console credential orgID")
			}
			if headers.Get("Accept") != "*/*" {
				t.Fatalf("native %s Accept differs from official local capture: %q", protocol, headers.Get("Accept"))
			}
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			switch protocol {
			case "openai":
				if headers.Get("Accept") != "*/*" || !strings.HasSuffix(target, "/chat/completions") || headers.Get("Authorization") != "Bearer test-service-key" || body["stream"] != true {
					t.Fatalf("%s %v %s", target, headers, raw)
				}
			case "openai-response":
				if !strings.HasSuffix(target, "/responses") || body["store"] != false {
					t.Fatal(target, string(raw))
				}
			case "claude":
				if !strings.HasSuffix(target, "/messages") || headers.Get("x-api-key") != "test-service-key" || headers.Get("anthropic-version") != "2023-06-01" {
					t.Fatal(target, headers)
				}
			case "gemini":
				if !strings.Contains(target, "/models/native-model:streamGenerateContent?alt=sse") || headers.Get("x-goog-api-key") != "test-service-key" {
					t.Fatal(target, headers)
				}
			}
			if protocol != "openai" && protocol != "openai-response" && headers.Get("Authorization") != "" {
				t.Fatal("wrong universal Bearer")
			}
		})
	}
}
func TestPrepareProtectsCredentialAndSessionHeaders(t *testing.T) {
	testConfig(t, "https://opencode.ai")
	for _, key := range []string{"Authorization", "x-api-key", "x-goog-api-key", "x-org-id", "x-opencode-session", "X-Session-Id", "User-Agent", "Host", "Cookie"} {
		r := route{ModelID: "m", Protocol: "openai", BaseURL: "https://opencode.ai/inference/v1", Headers: map[string]string{key: "override"}}
		_, _, _, err := prepareRequest(context.Background(), credential{Key: "test-service-key"}, r, formatName("openai"), []byte(clientRequest("openai")))
		if err == nil {
			t.Fatalf("accepted %s", key)
		}
	}
}
func TestExecuteMockNativeAndCrossProtocol(t *testing.T) {
	for _, protocol := range []string{"openai", "openai-response", "claude", "gemini"} {
		for _, client := range []string{"openai", "openai-response", "claude", "gemini"} {
			if !responseConversionAllowed(formatName(protocol), formatName(client)) {
				continue
			}
			t.Run(protocol+"_to_"+client, func(t *testing.T) {
				var targetServer *httptest.Server
				inference := 0
				targetServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/api/v2/config") {
						w.Header().Set("Content-Type", "application/json")
						io.WriteString(w, `{"providers":{"p":{"package":"`+protocolPackage(protocol)+`","settings":{"baseURL":"`+targetServer.URL+`/inference/v1"},"models":{"m":{}}}}}`)
						return
					}
					inference++
					if !strings.HasPrefix(r.Header.Get("X-Opencode-Session"), "ses_") {
						t.Error("session header absent")
					}
					if r.Header.Get("User-Agent") != "opencode/latest/2.0.29/cli" {
						t.Errorf("unexpected plugin User-Agent: %q", r.Header.Get("User-Agent"))
					}
					body, _ := io.ReadAll(r.Body)
					if protocol != "gemini" && !strings.Contains(string(body), `"stream":true`) {
						t.Error("upstream not streamed")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, nativeFixture(protocol))
				}))
				defer targetServer.Close()
				testConfig(t, targetServer.URL)
				c := testCredential(targetServer.URL)
				req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "opencode/p/m", Format: client, SourceFormat: client, Payload: []byte(clientRequest(client)), StorageJSON: rawJSON(t, c), Metadata: map[string]any{"caller_scope": "frontend-test", "execution_session_id": "conversation-test"}}}
				result, err := execute(rawJSON(t, req), false)
				if err != nil {
					t.Fatal(err)
				}
				response := result.(pluginapi.ExecutorResponse)
				if !strings.Contains(string(response.Payload), "hello") || !strings.Contains(string(response.Payload), "weather") || inference != 1 {
					t.Fatalf("result=%s calls=%d", response.Payload, inference)
				}
			})
		}
	}
}
func TestExecutePreservesHTTPErrorAndDoesNotRetryInference(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/config") {
					io.WriteString(w, `{"providers":{"p":{"package":"@opencode/ai/providers/openai/chat","settings":{"baseURL":"`+server.URL+`/inference"},"models":{"m":{}}}}}`)
					return
				}
				calls++
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(status)
				io.WriteString(w, `{"error":"test-service-key"}`)
			}))
			defer server.Close()
			testConfig(t, server.URL)
			c := testCredential(server.URL)
			req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "opencode/p/m", Format: "openai", SourceFormat: "openai", Payload: []byte(clientRequest("openai")), StorageJSON: rawJSON(t, c)}}
			_, err := execute(rawJSON(t, req), false)
			if err == nil || err.(*statusError).status != status {
				t.Fatalf("error=%v", err)
			}
			if calls != 1 {
				t.Fatal("inference was retried")
			}
			if status == 429 {
				_, second := execute(rawJSON(t, req), false)
				if second == nil || calls != 1 {
					t.Fatal("Retry-After not honored")
				}
			}
			if strings.Contains(err.Error(), c.Key) {
				t.Fatal("error leaked key")
			}
		})
	}
}

func TestExecutorNonStreamDeadlineAndRetainedHostProxy(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/config") {
			io.WriteString(w, `{"providers":{"p":{"package":"@opencode/ai/providers/openai/chat","settings":{"baseURL":"`+server.URL+`/inference"},"models":{"m":{}}}}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	testConfig(t, server.URL)
	state.Lock()
	state.config.RequestTimeoutSeconds = 1
	state.Unlock()
	c := testCredential(server.URL)
	rememberHostProxy(c, "http://proxy.invalid:1234")
	var proxies []string
	saved := makeHTTPClient
	makeHTTPClient = func(p string) (*http.Client, error) { proxies = append(proxies, p); return newHTTPClient("") }
	defer func() { makeHTTPClient = saved }()
	req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "opencode/p/m", Format: "openai", SourceFormat: "openai", Payload: []byte(clientRequest("openai")), StorageJSON: rawJSON(t, c)}}
	_, err := execute(rawJSON(t, req), false)
	if err == nil || err.(*statusError).status != 504 {
		t.Fatalf("deadline error %v", err)
	}
	if len(proxies) != 2 || proxies[0] != "http://proxy.invalid:1234" || proxies[1] != proxies[0] {
		t.Fatalf("host proxy lost: %v", proxies)
	}
}

func TestCompatibilityFiltersApplyAfterNativeOptions(t *testing.T) {
	testConfig(t, "https://opencode.ai")
	for _, protocol := range []string{"openai", "openai-response"} {
		r := route{ModelID: "m", Package: protocolPackage(protocol), Protocol: protocol, BaseURL: "https://opencode.ai/inference/v1", Compatibility: map[string]any{"supportsPromptCacheKey": false}, Settings: map[string]any{}}
		if protocol == "openai" {
			r.Settings["store"] = true
			r.Compatibility["supportsStore"] = false
			r.Compatibility["supportsUsageInStreaming"] = false
		}
		var obj map[string]any
		json.Unmarshal([]byte(clientRequest(protocol)), &obj)
		obj["prompt_cache_key"] = "cache"
		obj["stream_options"] = map[string]any{"include_usage": true}
		payload, _, _, e := prepareRequest(context.Background(), credential{Key: "fake"}, r, formatName(protocol), rawJSON(t, obj))
		if e != nil {
			t.Fatal(e)
		}
		json.Unmarshal(payload, &obj)
		if strings.Contains(string(payload), `"prompt_cache_key"`) {
			t.Fatal("unsupported cache key", string(payload))
		}
		if protocol == "openai" && (strings.Contains(string(payload), `"store"`) || strings.Contains(string(payload), `"stream_options"`)) {
			t.Fatal("unsupported options", string(payload))
		}
	}
}
