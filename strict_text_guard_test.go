package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const strictTextTestModel = "free-text-test-model"
const strictTextTestEndpoint = "https://opencode.ai/inference/v1/chat/completions"
const strictTextTestRejection = "isolated test guard rejected the final upstream model or endpoint"

func strictTextTestConfig() pluginConfig {
	cfg := guardTestConfig(strictTextTestModel, strictTextTestEndpoint)
	cfg.TestGuard.StrictTextOnly = true
	return cfg
}

func strictTextTestBody() map[string]any {
	return map[string]any{
		"model":    strictTextTestModel,
		"messages": []any{map[string]any{"role": "user", "content": "Reply with OK."}},
		"stream":   true, "max_tokens": 1,
	}
}

func strictTextTestReject(t *testing.T, cfg pluginConfig, protocol, target string, body []byte) {
	t.Helper()
	err := checkInferenceGuard(cfg, protocol, target, body)
	if err == nil {
		t.Fatal("strict pure-text guard accepted a disallowed final payload")
	}
	if err.Error() != "OpenCode plugin: "+strictTextTestRejection {
		t.Fatalf("guard rejection must stay fixed and generic: %q", err.Error())
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusForbidden {
		t.Fatalf("guard rejection must be HTTP 403: %v", err)
	}
}

func TestStrictTextGuardConfigBooleanOnly(t *testing.T) {
	const prefix = "isolated_test_guard:\n  enabled: true\n  allowed_requests:\n    - model: free-text-test-model\n      endpoint: https://opencode.ai/inference/v1/chat/completions\n"
	for _, tc := range []struct {
		name, field string
		want, valid bool
	}{
		{"omitted_defaults_false", "", false, true},
		{"explicit_false", "  strict_text_only: false\n", false, true},
		{"explicit_true", "  strict_text_only: true\n", true, true},
		{"null", "  strict_text_only: null\n", false, false},
		{"implicit_null", "  strict_text_only:\n", false, false},
		{"string_true", "  strict_text_only: \"true\"\n", false, false},
		{"string_false", "  strict_text_only: \"false\"\n", false, false},
		{"number", "  strict_text_only: 1\n", false, false},
		{"object", "  strict_text_only: {}\n", false, false},
		{"array", "  strict_text_only: []\n", false, false},
		{"case_alias", "  Strict_Text_Only: true\n", false, false},
		{"duplicate", "  strict_text_only: true\n  strict_text_only: false\n", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := configure(rawJSON(t, map[string]any{"schema_version": 6, "config_yaml": []byte(prefix + tc.field)}))
			if (err == nil) != tc.valid {
				t.Fatalf("configure error=%v, want valid=%t", err, tc.valid)
			}
			if tc.valid {
				t.Cleanup(shutdown)
				if g := loadedConfig().TestGuard; g == nil || g.StrictTextOnly != tc.want {
					t.Fatalf("strict_text_only was not retained: %#v", g)
				}
			}
		})
	}
	// JSON is also valid YAML, so it must obey the same non-null bool contract.
	for _, value := range []string{"true", "false", "null", `"true"`, "0", "[]", "{}"} {
		t.Run("json_"+value, func(t *testing.T) {
			config := fmt.Sprintf(`{"isolated_test_guard":{"enabled":true,"strict_text_only":%s,"allowed_requests":[{"model":%q,"endpoint":%q}]}}`, value, strictTextTestModel, strictTextTestEndpoint)
			err := configure(rawJSON(t, map[string]any{"schema_version": 6, "config_yaml": []byte(config)}))
			valid := value == "true" || value == "false"
			if (err == nil) != valid {
				t.Fatalf("JSON config error=%v, want valid=%t", err, valid)
			}
			if valid {
				t.Cleanup(shutdown)
			}
		})
	}
	encoded := rawJSON(t, strictTextTestConfig().TestGuard)
	var serialized map[string]any
	if err := json.Unmarshal(encoded, &serialized); err != nil || serialized["strict_text_only"] != true {
		t.Fatalf("strict flag is missing its JSON contract: %s (%v)", encoded, err)
	}
}

func TestStrictTextGuardAcceptsBoundedPlainText(t *testing.T) {
	for _, tokenField := range []string{"max_tokens", "max_completion_tokens"} {
		for _, tc := range []struct {
			name     string
			messages []any
			limit    int
			usage    bool
		}{
			{"minimum", []any{map[string]any{"role": "user", "content": "x"}}, 1, false},
			{"maximum_tokens", []any{map[string]any{"role": "user", "content": "x"}}, 32, true},
			{"four_messages", []any{map[string]any{"role": "system", "content": "Brief answers."}, map[string]any{"role": "user", "content": "Hi"}, map[string]any{"role": "assistant", "content": "Hello"}, map[string]any{"role": "user", "content": "Reply OK"}}, 8, true},
			{"single_4096_bytes", []any{map[string]any{"role": "user", "content": strings.Repeat("x", 4096)}}, 1, false},
			{"utf8_4096_bytes", []any{map[string]any{"role": "user", "content": strings.Repeat("界", 1365) + "x"}}, 1, false},
			{"escaped_total_8192_bytes", []any{map[string]any{"role": "system", "content": strings.Repeat("<", 4096)}, map[string]any{"role": "user", "content": strings.Repeat("<", 4096)}}, 1, false},
			{"total_8192_bytes", []any{map[string]any{"role": "system", "content": strings.Repeat("x", 4096)}, map[string]any{"role": "user", "content": strings.Repeat("y", 4096)}}, 1, false},
		} {
			t.Run(tokenField+"/"+tc.name, func(t *testing.T) {
				body := strictTextTestBody()
				delete(body, "max_tokens")
				body[tokenField], body["messages"] = tc.limit, tc.messages
				if tc.usage {
					body["stream_options"] = map[string]any{"include_usage": true}
				}
				if err := checkInferenceGuard(strictTextTestConfig(), "openai", strictTextTestEndpoint, rawJSON(t, body)); err != nil {
					t.Fatalf("bounded plain-text request rejected: %v", err)
				}
			})
		}
	}
	t.Run("decoded_escaped_content_length", func(t *testing.T) {
		body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"%s"}],"stream":true,"max_tokens":1}`, strictTextTestModel, strings.Repeat(`\u0061`, 4096))
		if err := checkInferenceGuard(strictTextTestConfig(), "openai", strictTextTestEndpoint, []byte(body)); err != nil {
			t.Fatalf("guard counted JSON escape bytes instead of decoded UTF-8 text: %v", err)
		}
	})
}

func TestStrictTextGuardRejectsMessageShapesAndLengths(t *testing.T) {
	for _, tc := range []struct {
		name     string
		messages any
	}{
		{"null", nil}, {"object", map[string]any{}}, {"string", "text"}, {"empty", []any{}},
		{"null_message", []any{nil}}, {"string_message", []any{"text"}}, {"array_message", []any{[]any{"user", "text"}}},
		{"missing_role", []any{map[string]any{"content": "text"}}},
		{"missing_content", []any{map[string]any{"role": "user"}}},
		{"null_role", []any{map[string]any{"role": nil, "content": "text"}}},
		{"numeric_role", []any{map[string]any{"role": 1, "content": "text"}}},
		{"null_content", []any{map[string]any{"role": "user", "content": nil}}},
		{"numeric_content", []any{map[string]any{"role": "user", "content": 1}}},
		{"bool_content", []any{map[string]any{"role": "user", "content": true}}},
		{"object_content", []any{map[string]any{"role": "user", "content": map[string]any{"text": "hello"}}}},
		{"text_array", []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hello"}}}}},
		{"image_array", []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://invalid.example/image"}}}}}},
		{"empty_content", []any{map[string]any{"role": "user", "content": ""}}},
		{"one_byte_over", []any{map[string]any{"role": "user", "content": strings.Repeat("x", 4097)}}},
		{"unicode_bytes_over", []any{map[string]any{"role": "user", "content": strings.Repeat("界", 1366)}}},
		{"total_one_byte_over", []any{map[string]any{"role": "system", "content": strings.Repeat("x", 4096)}, map[string]any{"role": "assistant", "content": "x"}, map[string]any{"role": "user", "content": strings.Repeat("x", 4096)}}},
		{"five_messages", []any{map[string]any{"role": "user", "content": "1"}, map[string]any{"role": "assistant", "content": "2"}, map[string]any{"role": "user", "content": "3"}, map[string]any{"role": "assistant", "content": "4"}, map[string]any{"role": "user", "content": "5"}}},
		{"no_user", []any{map[string]any{"role": "system", "content": "text"}}},
		{"last_assistant", []any{map[string]any{"role": "user", "content": "text"}, map[string]any{"role": "assistant", "content": "text"}}},
		{"last_system", []any{map[string]any{"role": "user", "content": "text"}, map[string]any{"role": "system", "content": "text"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strictTextTestBody()
			body["messages"] = tc.messages
			strictTextTestReject(t, strictTextTestConfig(), "openai", strictTextTestEndpoint, rawJSON(t, body))
		})
	}
	for _, role := range []string{"developer", "tool", "function", "User", "USER", "user ", "", "unknown"} {
		t.Run("role_"+role, func(t *testing.T) {
			body := strictTextTestBody()
			body["messages"] = []any{map[string]any{"role": role, "content": "text"}, map[string]any{"role": "user", "content": "text"}}
			strictTextTestReject(t, strictTextTestConfig(), "openai", strictTextTestEndpoint, rawJSON(t, body))
		})
	}
	for _, key := range []string{"name", "tool_calls", "tool_call_id", "function_call", "reasoning_content", "refusal", "audio", "metadata", "Role", "Content", "unknown"} {
		t.Run("message_field_"+key, func(t *testing.T) {
			body := strictTextTestBody()
			body["messages"] = []any{map[string]any{"role": "user", "content": "text", key: nil}}
			strictTextTestReject(t, strictTextTestConfig(), "openai", strictTextTestEndpoint, rawJSON(t, body))
		})
	}
}

func TestStrictTextGuardRejectsTopLevelExtensionsAndMissingFields(t *testing.T) {
	for _, key := range []string{"tools", "tool_choice", "functions", "function_call", "parallel_tool_calls", "response_format", "modalities", "audio", "image", "input", "prompt", "temperature", "top_p", "n", "stop", "logprobs", "logit_bias", "seed", "user", "metadata", "store", "reasoning", "reasoning_effort", "prediction", "service_tier", "streamOptions", "Messages", "Stream", "Max_Tokens", "unknown"} {
		t.Run(key, func(t *testing.T) {
			body := strictTextTestBody()
			// Even inert/null extension fields are outside the strict whitelist.
			body[key] = nil
			strictTextTestReject(t, strictTextTestConfig(), "openai", strictTextTestEndpoint, rawJSON(t, body))
		})
	}
	for _, key := range []string{"model", "messages", "stream", "max_tokens"} {
		t.Run("missing_"+key, func(t *testing.T) {
			body := strictTextTestBody()
			delete(body, key)
			strictTextTestReject(t, strictTextTestConfig(), "openai", strictTextTestEndpoint, rawJSON(t, body))
		})
	}
}

func TestStrictTextGuardRequiresStreamingAndOneSmallIntegerLimit(t *testing.T) {
	for _, value := range []any{false, nil, 1, "true", []any{}, map[string]any{}} {
		t.Run(fmt.Sprintf("stream_%T_%v", value, value), func(t *testing.T) {
			body := strictTextTestBody()
			body["stream"] = value
			strictTextTestReject(t, strictTextTestConfig(), "openai", strictTextTestEndpoint, rawJSON(t, body))
		})
	}
	for _, value := range []any{nil, true, "true", []any{}, map[string]any{}, map[string]any{"include_usage": false}, map[string]any{"include_usage": nil}, map[string]any{"include_usage": "true"}, map[string]any{"include_usage": true, "extra": nil}, map[string]any{"Include_Usage": true}} {
		t.Run(fmt.Sprintf("stream_options_%T_%v", value, value), func(t *testing.T) {
			body := strictTextTestBody()
			body["stream_options"] = value
			strictTextTestReject(t, strictTextTestConfig(), "openai", strictTextTestEndpoint, rawJSON(t, body))
		})
	}
	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		for _, value := range []any{nil, true, "1", 0, -1, 33, 1.5, 1e100, []any{1}, map[string]any{"value": 1}, json.Number("1.0"), json.Number("1e1")} {
			t.Run(fmt.Sprintf("%s_%T_%v", field, value, value), func(t *testing.T) {
				body := strictTextTestBody()
				delete(body, "max_tokens")
				body[field] = value
				strictTextTestReject(t, strictTextTestConfig(), "openai", strictTextTestEndpoint, rawJSON(t, body))
			})
		}
	}
	t.Run("both_token_fields", func(t *testing.T) {
		body := strictTextTestBody()
		body["max_completion_tokens"] = 1
		strictTextTestReject(t, strictTextTestConfig(), "openai", strictTextTestEndpoint, rawJSON(t, body))
	})
}

func TestStrictTextGuardRejectsDuplicateKeysAtEveryLevel(t *testing.T) {
	const valid = `{"model":"free-text-test-model","messages":[{"role":"user","content":"text"}],"stream":true,"stream_options":{"include_usage":true},"max_tokens":1}`
	for _, tc := range []struct{ name, from, to string }{
		{"model", `"model":"free-text-test-model"`, `"model":"free-text-test-model","model":"free-text-test-model"`},
		{"messages", `"messages":[{"role":"user","content":"text"}]`, `"messages":[{"role":"user","content":"text"}],"messages":[{"role":"user","content":"text"}]`},
		{"stream", `"stream":true`, `"stream":true,"stream":true`},
		{"stream_options", `"stream_options":{"include_usage":true}`, `"stream_options":{"include_usage":true},"stream_options":{"include_usage":true}`},
		{"max_tokens", `"max_tokens":1`, `"max_tokens":1,"max_tokens":1`},
		{"max_completion_tokens", `"max_tokens":1`, `"max_completion_tokens":1,"max_completion_tokens":1`},
		{"role", `"role":"user"`, `"role":"user","role":"user"`},
		{"content", `"content":"text"`, `"content":"text","content":"text"`},
		{"include_usage", `"include_usage":true`, `"include_usage":true,"include_usage":true`},
		{"escaped_top_key", `"stream":true`, `"stream":true,"\u0073tream":true`},
		{"escaped_message_key", `"role":"user"`, `"role":"user","\u0072ole":"user"`},
		{"escaped_nested_key", `"include_usage":true`, `"include_usage":true,"\u0069nclude_usage":true`},
		{"case_model", `"model":`, `"Model":`},
		{"case_messages", `"messages":`, `"Messages":`},
		{"case_role", `"role":`, `"Role":`},
		{"case_content", `"content":`, `"Content":`},
		{"case_usage", `"include_usage":`, `"Include_Usage":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			strictTextTestReject(t, strictTextTestConfig(), "openai", strictTextTestEndpoint, []byte(strings.Replace(valid, tc.from, tc.to, 1)))
		})
	}
	for _, invalid := range []string{"null", "[]", `"text"`, valid + "{}", valid[:len(valid)-1], valid + " trailing"} {
		t.Run(fmt.Sprintf("malformed_%q", invalid[:min(len(invalid), 20)]), func(t *testing.T) {
			strictTextTestReject(t, strictTextTestConfig(), "openai", strictTextTestEndpoint, []byte(invalid))
		})
	}
}

func TestStrictTextGuardRequiresNativeChatCompletionsAndExactPair(t *testing.T) {
	body := rawJSON(t, strictTextTestBody())
	for _, protocol := range []string{"openai-response", "claude", "gemini", "chat-completions", "OpenAI", "", "unknown"} {
		t.Run("protocol_"+protocol, func(t *testing.T) {
			strictTextTestReject(t, strictTextTestConfig(), protocol, strictTextTestEndpoint, body)
		})
	}
	for _, target := range []string{"https://opencode.ai/inference/v1/responses", "https://opencode.ai/inference/v1/messages", strictTextTestEndpoint + "/", strictTextTestEndpoint + "?x=1", strictTextTestEndpoint + "/extra"} {
		t.Run("self_allowlisted_"+target, func(t *testing.T) {
			cfg := strictTextTestConfig()
			cfg.TestGuard.AllowedRequests[0].Endpoint = target
			strictTextTestReject(t, cfg, "openai", target, body)
		})
	}
	t.Run("wrong_model", func(t *testing.T) {
		payload := strictTextTestBody()
		payload["model"] = "another-model"
		strictTextTestReject(t, strictTextTestConfig(), "openai", strictTextTestEndpoint, rawJSON(t, payload))
	})
	t.Run("cross_product", func(t *testing.T) {
		cfg := strictTextTestConfig()
		other := "https://opencode.ai/inference/other/chat/completions"
		cfg.TestGuard.AllowedRequests = append(cfg.TestGuard.AllowedRequests, inferenceGuardRule{Model: "another-model", Endpoint: other})
		strictTextTestReject(t, cfg, "openai", other, body)
	})
}

func TestStrictTextGuardFalsePreservesPreviousPayloadContract(t *testing.T) {
	for _, protocol := range []string{"openai", "openai-response", "claude", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			endpoint := guardTestEndpoint("https://opencode.ai/inference/v1", protocol, strictTextTestModel)
			cfg := guardTestConfig(strictTextTestModel, endpoint)
			if cfg.TestGuard.StrictTextOnly {
				t.Fatal("strict mode must default to false")
			}
			body := rawJSON(t, map[string]any{"model": strictTextTestModel, "messages": []any{}, "tools": []any{}, "max_tokens": 1000, "custom": map[string]any{"nested": true}})
			if err := checkInferenceGuard(cfg, protocol, endpoint, body); err != nil {
				t.Fatalf("strict=false restricted a previously accepted request: %v", err)
			}
		})
	}
}

func TestStrictTextGuardDiagnosticsAreGeneric(t *testing.T) {
	var logs bytes.Buffer
	writer, flags, prefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(&logs)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() { log.SetOutput(writer); log.SetFlags(flags); log.SetPrefix(prefix) })
	body := strictTextTestBody()
	body["messages"] = []any{map[string]any{"role": "user", "content": "private-prompt-sentinel", "private-extension-sentinel": true}}
	strictTextTestReject(t, strictTextTestConfig(), "openai", strictTextTestEndpoint, rawJSON(t, body))
	if logs.String() != "OpenCode isolated test guard: enabled=true allowed=false\n" {
		t.Fatalf("strict-mode rejection diagnostics must expose only booleans: %q", logs.String())
	}
}

func TestStrictTextGuardExecuteRejectsFinalOverlaysBeforeAnyPost(t *testing.T) {
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"tools", map[string]any{"tools": []any{}}},
		{"tool_choice", map[string]any{"tool_choice": "none"}},
		{"functions", map[string]any{"functions": []any{}}},
		{"temperature", map[string]any{"temperature": 0}},
		{"metadata", map[string]any{"metadata": map[string]any{"private": "value"}}},
		{"store", map[string]any{"store": false}},
		{"reasoning_effort", map[string]any{"reasoning_effort": "low"}},
		{"wrong_model", map[string]any{"model": "unapproved-model"}},
		{"null_messages", map[string]any{"messages": nil}},
		{"content_array", map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Hello"}}}}}},
		{"message_name", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "Hello", "name": "private-name"}}}},
		{"message_tool_calls", map[string]any{"messages": []any{map[string]any{"role": "assistant", "content": "Hello", "tool_calls": []any{}}, map[string]any{"role": "user", "content": "Reply"}}}},
		{"long_content", map[string]any{"messages": []any{map[string]any{"role": "user", "content": strings.Repeat("界", 1366)}}}},
		{"last_assistant", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "Hi"}, map[string]any{"role": "assistant", "content": "Hello"}}}},
		{"limit_null", map[string]any{"max_completion_tokens": nil}},
		{"limit_zero", map[string]any{"max_completion_tokens": 0}},
		{"limit_large", map[string]any{"max_completion_tokens": 33}},
		{"limit_fraction", map[string]any{"max_completion_tokens": 1.5}},
		{"limit_string", map[string]any{"max_completion_tokens": "1"}},
		{"both_limit_fields", map[string]any{"max_tokens": 1}},
		{"stream_options_extension", map[string]any{"stream_options": map[string]any{"extension": true}}},
		{"stream_options_false", map[string]any{"stream_options": map[string]any{"include_usage": false}}},
		{"unknown_null", map[string]any{"private-extension-sentinel": nil}},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream_%t", tc.name, stream), func(t *testing.T) {
				var posts, discoveries atomic.Int32
				var server *httptest.Server
				server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet && r.URL.Path == "/console/api/v2/config" {
						discoveries.Add(1)
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(map[string]any{"providers": map[string]any{"p": map[string]any{
							"package": protocolPackage("openai"), "settings": map[string]any{"baseURL": server.URL + "/inference/v1"},
							"models": map[string]any{"alias": map[string]any{"modelID": strictTextTestModel, "body": tc.body, "compatibility": map[string]any{"supportsStore": false}}},
						}}})
						return
					}
					if r.Method == http.MethodPost {
						posts.Add(1)
					}
					w.WriteHeader(http.StatusInternalServerError)
				}))
				defer server.Close()
				testConfig(t, server.URL)
				state.Lock()
				state.config.TestGuard = &inferenceTestGuard{Enabled: true, StrictTextOnly: true, AllowedRequests: []inferenceGuardRule{{Model: strictTextTestModel, Endpoint: server.URL + "/inference/v1/chat/completions"}}}
				state.Unlock()
				req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "opencode/p/alias", Format: "openai", SourceFormat: "openai", Payload: rawJSON(t, strictTextTestBody()), StorageJSON: rawJSON(t, guardTestCredential(t, server.URL))}, StreamID: "strict-text-" + t.Name()}
				_, err := execute(rawJSON(t, req), stream)
				if err == nil || err.Error() != "OpenCode plugin: "+strictTextTestRejection {
					t.Fatalf("wanted generic final strict guard rejection, got %v", err)
				}
				if discoveries.Load() != 1 || posts.Load() != 0 {
					t.Fatalf("guard must reject after discovery but before inference: GET=%d POST=%d", discoveries.Load(), posts.Load())
				}
			})
		}
	}
}

func TestStrictTextGuardExecuteAllowsPositiveFinalOverlayControl(t *testing.T) {
	for _, tokenField := range []string{"max_tokens", "max_completion_tokens"} {
		for _, usage := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/usage_%t", tokenField, usage), func(t *testing.T) {
				var posts atomic.Int32
				var server *httptest.Server
				server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet && r.URL.Path == "/console/api/v2/config" {
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(map[string]any{"providers": map[string]any{"p": map[string]any{
							"package": protocolPackage("openai"), "settings": map[string]any{"baseURL": server.URL + "/inference/v1"},
							"models": map[string]any{"alias": map[string]any{"modelID": "discovered-unapproved-model", "body": map[string]any{"model": strictTextTestModel, tokenField: 32}, "compatibility": map[string]any{"supportsStore": false, "supportsUsageInStreaming": usage, "maxTokensField": tokenField}}},
						}}})
						return
					}
					if r.Method != http.MethodPost {
						t.Error("unexpected mock request")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					posts.Add(1)
					if r.URL.Path != "/inference/v1/chat/completions" || r.URL.RawQuery != "" {
						t.Error("unexpected final inference endpoint")
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body["model"] != strictTextTestModel || body[tokenField] != float64(32) || body["stream"] != true {
						t.Errorf("final overlay was not retained: %#v", body)
					}
					wantFields := 4
					if usage {
						wantFields++
						options, ok := body["stream_options"].(map[string]any)
						if !ok || len(options) != 1 || options["include_usage"] != true {
							t.Error("invalid final stream options")
						}
					}
					if len(body) != wantFields {
						t.Errorf("strict mock received unexpected final fields: %#v", body)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"id\":\"plain-text-test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				}))
				defer server.Close()
				testConfig(t, server.URL)
				state.Lock()
				state.config.TestGuard = &inferenceTestGuard{Enabled: true, StrictTextOnly: true, AllowedRequests: []inferenceGuardRule{{Model: strictTextTestModel, Endpoint: server.URL + "/inference/v1/chat/completions"}}}
				state.Unlock()
				req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "opencode/p/alias", Format: "openai", SourceFormat: "openai", Payload: rawJSON(t, strictTextTestBody()), StorageJSON: rawJSON(t, guardTestCredential(t, server.URL))}}
				if _, err := execute(rawJSON(t, req), false); err != nil {
					t.Fatalf("valid strict final overlay rejected: %v", err)
				}
				if posts.Load() != 1 {
					t.Fatalf("positive control POST count=%d, want 1", posts.Load())
				}
			})
		}
	}
}

func TestStrictTextGuardHeaderWhitelist(t *testing.T) {
	valid := http.Header{"Content-Type": {"application/json"}, "Accept": {"*/*"}, "Authorization": {"Bearer fake-strict-text-test-key"}}
	for _, tc := range []struct {
		name  string
		edit  func(http.Header)
		valid bool
	}{
		{"minimal", func(http.Header) {}, true},
		{"native_org_and_sessions", func(h http.Header) {
			for _, name := range []string{"X-Org-Id", "X-Opencode-Org-Id", "X-Opencode-Session", "X-Session-Affinity", "X-Session-Id"} {
				h.Set(name, "fake-native-value")
			}
		}, true},
		{"lowercase_names", func(h http.Header) {
			for name, values := range h.Clone() {
				delete(h, name)
				h[strings.ToLower(name)] = values
			}
		}, true},
		{"missing_content_type", func(h http.Header) { h.Del("Content-Type") }, false},
		{"missing_accept", func(h http.Header) { h.Del("Accept") }, false},
		{"missing_auth", func(h http.Header) { h.Del("Authorization") }, false},
		{"wrong_content_type", func(h http.Header) { h.Set("Content-Type", "application/octet-stream") }, false},
		{"parameterized_content_type", func(h http.Header) { h.Set("Content-Type", "application/json; charset=utf-8") }, false},
		{"wrong_accept", func(h http.Header) { h.Set("Accept", "application/json") }, false},
		{"compressed", func(h http.Header) { h.Set("Content-Encoding", "gzip") }, false},
		{"unknown", func(h http.Header) { h.Set("X-Provider-Mode", "tool") }, false},
		{"empty_values", func(h http.Header) { h["Authorization"] = nil }, false},
		{"multiple_values", func(h http.Header) { h.Add("Authorization", "Bearer second-fake-key") }, false},
		{"case_duplicate", func(h http.Header) { h["authorization"] = []string{"Bearer second-fake-key"} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := valid.Clone()
			tc.edit(headers)
			if got := strictTextHeaders(headers); got != tc.valid {
				t.Fatalf("header whitelist accepted=%t, want %t", got, tc.valid)
			}
		})
	}
}

func TestStrictTextGuardExecuteRejectsConfiguredHeadersBeforeAnyPost(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"Content-Type", "application/octet-stream"},
		{"Accept", "application/json"},
		{"Content-Encoding", "gzip"},
		{"X-Provider-Mode", "tool"},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream_%t", tc.name, stream), func(t *testing.T) {
				var posts, discoveries atomic.Int32
				var server *httptest.Server
				server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet && r.URL.Path == "/console/api/v2/config" {
						discoveries.Add(1)
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(map[string]any{"providers": map[string]any{"p": map[string]any{
							"package": protocolPackage("openai"), "settings": map[string]any{"baseURL": server.URL + "/inference/v1"},
							"headers": map[string]any{tc.name: tc.value},
							"models":  map[string]any{"alias": map[string]any{"modelID": strictTextTestModel, "compatibility": map[string]any{"supportsStore": false}}},
						}}})
						return
					}
					if r.Method == http.MethodPost {
						posts.Add(1)
					}
					w.WriteHeader(http.StatusInternalServerError)
				}))
				defer server.Close()
				testConfig(t, server.URL)
				state.Lock()
				state.config.TestGuard = &inferenceTestGuard{Enabled: true, StrictTextOnly: true, AllowedRequests: []inferenceGuardRule{{Model: strictTextTestModel, Endpoint: server.URL + "/inference/v1/chat/completions"}}}
				state.Unlock()
				req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "opencode/p/alias", Format: "openai", SourceFormat: "openai", Payload: rawJSON(t, strictTextTestBody()), StorageJSON: rawJSON(t, guardTestCredential(t, server.URL))}, StreamID: "strict-text-headers-" + t.Name()}
				_, err := execute(rawJSON(t, req), stream)
				if err == nil || err.Error() != "OpenCode plugin: "+strictTextTestRejection {
					t.Fatalf("wanted generic strict header rejection, got %v", err)
				}
				if discoveries.Load() != 1 || posts.Load() != 0 {
					t.Fatalf("strict header rejection must occur before inference: GET=%d POST=%d", discoveries.Load(), posts.Load())
				}
			})
		}
	}
}
