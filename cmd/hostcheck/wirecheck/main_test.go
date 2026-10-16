package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCanonicalJSONKeepsOrchestrationPayload(t *testing.T) {
	first := []byte(`{"stream":true,"messages":[{"role":"system","content":"<env>\nunchanged</env>"},{"role":"assistant","content":"fixture data only"}],"model":"space-bunny-free","max_completion_tokens":16}`)
	second := []byte(`{ "max_completion_tokens": 16, "model":"space-bunny-free", "messages":[{"content":"\u003cenv\u003e\nunchanged\u003c/env\u003e","role":"system"},{"content":"fixture data only","role":"assistant"}], "stream":true }`)
	a, err := canonicalJSON(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := canonicalJSON(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("serialization-only differences changed canonical body: %s vs %s", a, b)
	}
	changed, err := canonicalJSON(bytes.Replace(second, []byte("fixture data only"), []byte("rewritten"), 1))
	if err != nil || bytes.Equal(a, changed) {
		t.Fatal("orchestration content change must fail body parity")
	}
	largeA, err := canonicalJSON([]byte(`{"seed":9007199254740992}`))
	if err != nil {
		t.Fatal(err)
	}
	largeB, err := canonicalJSON([]byte(`{"seed":9007199254740993}`))
	if err != nil || bytes.Equal(largeA, largeB) {
		t.Fatal("large integer differences must not be lost to float64 rounding")
	}
	for _, invalid := range []string{`null`, `[]`, `{"bad":`, `{} {}`} {
		if _, err := canonicalJSON([]byte(invalid)); err == nil {
			t.Fatalf("accepted invalid object %q", invalid)
		}
	}
}

func TestHeaderComparisonFoldsNamesButPreservesValues(t *testing.T) {
	official := map[string]string{"Content-Type": "application/json", "User-Agent": "Official", "X-Session-ID": "ses_fake"}
	plugin := map[string][]string{"content-type": {"application/json"}, "USER-AGENT": {"official"}, "x-session-id": {"cpa_fake"}, "X-Org-Id": {"fake-org"}}
	differences, matching := compareHeaders(official, plugin)
	if !reflect.DeepEqual(matching, []string{"content-type"}) || len(differences) != 3 {
		t.Fatalf("differences=%+v matching=%v", differences, matching)
	}
	if differences[0].Name != "user-agent" || differences[0].Category != "identity/session/trace" || differences[1].Name != "x-org-id" || differences[1].Category != "credential-org" {
		t.Fatalf("unexpected deterministic comparison: %+v", differences)
	}
	if !reflect.DeepEqual(differences[0].Official, []string{"Official"}) || !reflect.DeepEqual(differences[0].Plugin, []string{"official"}) {
		t.Fatal("header values must retain their original case")
	}
}

func TestInvalidFixtureFailsBeforeLoadingNativePlugin(t *testing.T) {
	for name, fixture := range map[string]string{
		"missing post":      `[]`,
		"missing fake auth": `[{"method":"POST","path":"/inference/openai/v1/chat/completions","headers":{},"body":{}}]`,
		"wrong fake auth":   `[{"method":"POST","path":"/inference/openai/v1/chat/completions","headers":{"Authorization":"Bearer other"},"body":{}}]`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			fixturePath := filepath.Join(dir, "official.json")
			if err := os.WriteFile(fixturePath, []byte(fixture), 0600); err != nil {
				t.Fatal(err)
			}
			err := run(filepath.Join(dir, "missing.so"), fixturePath, filepath.Join(dir, "result.json"), false)
			if err == nil || strings.Contains(err.Error(), "missing.so") {
				t.Fatalf("fixture must fail before plugin access, got %v", err)
			}
			if err := run(filepath.Join(dir, "missing.so"), fixturePath, fixturePath, false); err == nil || !strings.Contains(err.Error(), "overwrite") {
				t.Fatalf("must preserve source input, got %v", err)
			}
		})
	}
}

func TestCheckAlignedHeaders(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string][]string
		wantOK  bool
	}{
		{"aligned", map[string][]string{"Accept": {"*/*"}, "X-Opencode-Org-Id": {"fake-config-org"}}, true},
		{"case insensitive names", map[string][]string{"ACCEPT": {"*/*"}}, true},
		{"missing accept", map[string][]string{}, false},
		{"baseline accept", map[string][]string{"Accept": {"text/event-stream"}}, false},
		{"duplicate accept", map[string][]string{"Accept": {"*/*", "*/*"}}, false},
		{"credential org", map[string][]string{"Accept": {"*/*"}, "x-org-id": {"fake-org"}}, false},
		{"empty credential org", map[string][]string{"Accept": {"*/*"}, "X-ORG-ID": {}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkAlignedHeaders(tc.headers); (err == nil) != tc.wantOK {
				t.Fatalf("checkAlignedHeaders() error=%v, wantOK=%v", err, tc.wantOK)
			}
		})
	}
}

func TestSelectPOSTOrdinal(t *testing.T) {
	requests := []officialRequest{{Method: "GET"}, {Method: "POST"}, {Method: "GET"}, {Method: "POST"}}
	for ordinal, want := range []int{1, 3} {
		got, err := selectPOST(requests, ordinal)
		if err != nil || got != want {
			t.Fatalf("ordinal %d = %d, %v", ordinal, got, err)
		}
	}
	for _, ordinal := range []int{-1, 2, 100} {
		if _, err := selectPOST(requests, ordinal); err == nil {
			t.Fatalf("accepted unavailable ordinal %d", ordinal)
		}
	}
}

func TestProtocolSpecsAndNativeResponseValidation(t *testing.T) {
	responses := map[string]string{
		"chat":      `{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}`,
		"responses": `{"status":"completed","output":[{"content":[{"text":"OK"}]}],"usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11}}`,
		"anthropic": `{"stop_reason":"end_turn","content":[{"text":"OK"}],"usage":{"input_tokens":10,"output_tokens":1}}`,
		"gemini":    `{"candidates":[{"content":{"parts":[{"text":"OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1,"totalTokenCount":11}}`,
	}
	for protocol, response := range responses {
		t.Run(protocol, func(t *testing.T) {
			spec, err := protocolSpecFor(protocol)
			if err != nil || !strings.HasPrefix(spec.path, spec.basePath+"/") || spec.format == "" || spec.authValue == "" || !strings.HasSuffix(spec.stream, "\n\n") {
				t.Fatalf("bad spec: %+v %v", spec, err)
			}
			for _, line := range strings.Split(spec.stream, "\n") {
				if strings.HasPrefix(line, "data: ") && line != "data: [DONE]" && !json.Valid([]byte(strings.TrimPrefix(line, "data: "))) {
					t.Fatalf("bad SSE JSON: %s", line)
				}
			}
			if !responseOK(protocol, []byte(response)) {
				t.Fatal("valid response rejected")
			}
			for _, invalid := range []string{`{}`, strings.ReplaceAll(response, `"OK"`, `"wrong"`), strings.ReplaceAll(response, `:10`, `:9`), strings.ReplaceAll(response, `:10`, `:"10"`)} {
				if responseOK(protocol, []byte(invalid)) {
					t.Fatalf("accepted invalid response %s", invalid)
				}
			}
		})
	}
	if _, err := protocolSpecFor("https://example.com"); err == nil {
		t.Fatal("accepted arbitrary protocol")
	}
}

func TestProtocolCaptureValidationFailsClosed(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic", "gemini"} {
		spec, _ := protocolSpecFor(protocol)
		for _, mutation := range []string{"path", "auth", "duplicate auth", "query"} {
			t.Run(protocol+"/"+mutation, func(t *testing.T) {
				request := officialRequest{Method: "POST", Path: spec.path, Headers: map[string]string{spec.authHeader: spec.authValue}, Body: json.RawMessage(`{}`)}
				switch mutation {
				case "path":
					request.Path = "https://example.com" + spec.path
				case "query":
					request.Path += "&extra=1"
				case "auth":
					request.Headers[spec.authHeader] = "real-key-not-permitted"
				case "duplicate auth":
					request.Headers[strings.ToUpper(spec.authHeader)] = spec.authValue
				}
				data, _ := json.Marshal([]officialRequest{request})
				dir := t.TempDir()
				path := filepath.Join(dir, "capture.json")
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				err := runProtocol(filepath.Join(dir, "missing.so"), path, filepath.Join(dir, "result.json"), false, 0, protocol)
				if err == nil || strings.Contains(err.Error(), "missing.so") {
					t.Fatalf("must reject before loading plugin: %v", err)
				}
			})
		}
	}
}

func TestReplayPayloadNormalizesOnlyResponsesSessionCacheIdentity(t *testing.T) {
	original := []byte(`{"model":"space-bunny-free","prompt_cache_key":"ses_official_fixture","input":[{"role":"user","content":"ses_ in content must stay"}],"seed":9007199254740993}`)
	before := append([]byte(nil), original...)
	replay, normalized, err := replayPayload("responses", original)
	if err != nil || len(normalized) != 1 || !bytes.Contains(replay, []byte(`"prompt_cache_key":"cpa_wirecheck_synthetic_cache"`)) || !bytes.Contains(replay, []byte(`9007199254740993`)) || !bytes.Contains(replay, []byte(`ses_ in content must stay`)) {
		t.Fatalf("bad normalized payload: %s %v %v", replay, normalized, err)
	}
	if !bytes.Equal(original, before) {
		t.Fatal("mutated original capture")
	}
	for _, protocol := range []string{"chat", "anthropic", "gemini"} {
		replay, normalized, err := replayPayload(protocol, original)
		if err != nil || len(normalized) != 0 || !bytes.Equal(replay, original) {
			t.Fatalf("changed unrelated %s payload", protocol)
		}
	}
	for _, body := range []string{`{"prompt_cache_key":"explicit-user-cache"}`, `{"prompt_cache_key":null}`, `{"input":[]}`} {
		replay, normalized, err := replayPayload("responses", []byte(body))
		if err != nil || len(normalized) != 0 || string(replay) != body {
			t.Fatalf("changed semantic key: %s", replay)
		}
	}
}

func TestProtocolHeaders(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic", "gemini"} {
		spec, _ := protocolSpecFor(protocol)
		official := map[string]string{spec.authHeader: spec.authValue}
		plugin := map[string][]string{spec.authHeader: {spec.authValue}}
		if protocol == "anthropic" {
			for k, v := range map[string]string{"anthropic-version": "2023-06-01", "anthropic-beta": "interleaved-thinking-2025-05-14"} {
				official[k] = v
				plugin[k] = []string{v}
			}
		}
		if err := checkProtocolHeaders(protocol, official, plugin); err != nil {
			t.Fatal(err)
		}
		if protocol == "anthropic" {
			plugin["anthropic-beta"] = []string{"wrong"}
			if checkProtocolHeaders(protocol, official, plugin) == nil {
				t.Fatal("accepted incorrect beta")
			}
			plugin["anthropic-beta"] = []string{official["anthropic-beta"]}
		}
		plugin[spec.authHeader] = []string{"wrong"}
		if checkProtocolHeaders(protocol, official, plugin) == nil {
			t.Fatal("accepted incorrect credential")
		}
		plugin[spec.authHeader] = []string{spec.authValue}
		other := "authorization"
		if spec.authHeader == other {
			other = "x-api-key"
		}
		plugin[other] = []string{fakeKey}
		if checkProtocolHeaders(protocol, official, plugin) == nil {
			t.Fatal("accepted additional credential")
		}
	}
}
