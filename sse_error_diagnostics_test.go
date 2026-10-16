package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

import "github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

func TestSSEErrorDiagnosticsSchemasAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name, event, raw, parse, code, kind, status, summary string
	}{
		{"nested", "", `{"error":{"code":"insufficient_quota","type":"permission_error","message":"Service quota exhausted"}}`, "json_category", "insufficient_quota", "permission_error", "", "Service quota exhausted"},
		{"top_level", "error", `{"code":"invalid_api_key","error_type":"authentication_error","message":"Authentication rejected"}`, "json_category", "invalid_api_key", "authentication_error", "", "Authentication rejected"},
		{"type_error", "", `{"type":"error","code":"rate_limit_exceeded","message":"Please wait"}`, "json_category", "rate_limit_exceeded", "", "", "Please wait"},
		{"response_failed", "", `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"Generation could not finish"}}}`, "json_category", "server_error", "", "", "Generation could not finish"},
		{"response_event_only", "response.failed", `{"response":{"error":{"status":"PERMISSION_DENIED","message":"Upstream denied this operation"}}}`, "json_category", "", "", "permission_denied", "Upstream denied this operation"},
		{"no_status_inference", "error", `{"code":403,"status":403,"message":"HTTP 403 or 429 was mentioned"}`, "json_no_category", "", "", "", "HTTP 403 or 429 was mentioned"},
		{"unknown_category", "error", `{"error":{"code":"private-category","message":"A supported message"},"metadata":{"message":"private-metadata"}}`, "json_no_category", "", "", "", "A supported message"},
		{"malformed", "error", `{"error":{"message":"private-malformed-data"}`, "invalid_json", "", "", "", "[body unavailable]"},
		{"malformed_unnamed", "", `{"type":"error","message":"private-malformed-data"`, "invalid_json", "", "", "", "[body unavailable]"},
		{"non_json", "error", `private-plaintext-data`, "non_json", "", "", "", "[body unavailable]"},
		{"empty", "error", ``, "empty", "", "", "", "[body unavailable]"},
		{"array", "error", `[{"message":"private-array-data"}]`, "json_no_category", "", "", "", "[body omitted: no supported diagnostic message]"},
		{"long", "error", `{"error":{"code":"invalid_api_key","message":"` + strings.Repeat("private-long-data", maxErrorDiagnosticBytes) + `"}}`, "too_large", "", "", "", "[body unavailable]"},
		{"truncated_prefix", "error", `{"error":{"code":"invalid_api_key","message":"` + strings.Repeat("x", maxErrorDiagnosticBytes), "too_large", "", "", "", "[body unavailable]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := withSSEErrorDiagnostic(context.Background(), &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}})
			d := captureSSEErrorDiagnostic(ctx, []byte(tc.raw))
			if d.status != 200 || d.parseCategory != tc.parse || d.code != tc.code || d.kind != tc.kind || d.errorStatus != tc.status || d.summary != tc.summary || d.retryAfter != "" {
				t.Fatalf("unexpected SSE diagnostic: %#v", d)
			}
			var output bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&output)
			defer log.SetOutput(previous)
			obj, err := eventObject(ctx, sseEvent{Event: tc.event, Data: []byte(tc.raw)})
			assertSSEDiagnosticFailure(t, err)
			if obj != nil || strings.Count(output.String(), "source=upstream_sse") != 1 || !strings.Contains(output.String(), "http_status=200") {
				t.Fatalf("error object/log origin mismatch: obj=%v log=%s", obj, output.String())
			}
			for _, private := range []string{"private-category", "private-metadata", "private-malformed-data", "private-plaintext-data", "private-array-data", "private-long-data"} {
				if strings.Contains(output.String()+err.Error(), private) {
					t.Fatalf("raw/non-diagnostic data leaked: %q", private)
				}
			}
		})
	}
}

func TestSSEErrorDiagnosticsRedactExecutionValuesAndBoundSummary(t *testing.T) {
	c := credential{Key: "opaque-current-credential", OrgID: "private-current-org", Label: "private-current-label"}
	headers := http.Header{"Authorization": {"Bearer " + c.Key}, "X-Configured-Value": {"private-request-header"}}
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"private-response-id"}, "X-Other": {"private-response-header"}, "Retry-After": {"60"}}}
	secrets := diagnosticSecrets(c, headers, "https://unused.invalid?key=private-query-value", "http://private-proxy-user:private-proxy-pass@unused.invalid")
	ctx := withSSEErrorDiagnostic(context.Background(), resp, secrets...)
	privateValues := []string{c.Key, c.OrgID, c.Label, "private-request-header", "private-response-id", "private-response-header", "private-query-value", "private-proxy-user", "private-proxy-pass", "private-sibling-account"}
	variants := append([]string(nil), privateValues...)
	variants = append(variants, url.QueryEscape(c.Key), base64.StdEncoding.EncodeToString([]byte(c.Key)), hex.EncodeToString([]byte(c.Key)))
	raw := rawJSON(t, map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"error":    map[string]any{"code": "server_error", "message": "Generation failed: " + strings.Join(variants, " ")},
			"metadata": map[string]any{"account": "private-sibling-account"},
			"output":   []any{map[string]any{"type": "message", "content": "private-output-content"}},
		},
	})
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	_, err := eventObject(ctx, sseEvent{Data: raw})
	assertSSEDiagnosticFailure(t, err)
	d := captureSSEErrorDiagnostic(ctx, raw)
	for _, value := range append(variants, "private-output-content") {
		if strings.Contains(output.String()+err.Error()+d.summary, value) {
			t.Fatalf("execution/response value escaped redaction: %q", value)
		}
	}
	if !strings.Contains(d.summary, "Generation failed:") || !strings.Contains(d.summary, "[redacted]") || !d.requestIDPresent || d.retryAfter != "" {
		t.Fatalf("lost safe detail or adopted Retry-After: %#v", d)
	}
	// Redact before the output truncation so no partial credential prefix is
	// exposed at the summary boundary; the input remains under 16 KiB.
	raw = rawJSON(t, map[string]any{"error": map[string]any{"message": strings.Repeat("x", 1000) + " " + c.Key + strings.Repeat("y", 3000)}})
	d = captureSSEErrorDiagnostic(ctx, raw)
	if len(d.summary) > maxDiagnosticSummaryBytes || !strings.HasSuffix(d.summary, " [truncated]") || strings.Contains(d.summary, "opaque-current") {
		t.Fatalf("summary truncation/redaction boundary failed: %q", d.summary)
	}
}

func TestSSEErrorDiagnosticsWithoutExecutionContextOmitProse(t *testing.T) {
	raw := []byte(`{"error":{"code":"server_error","message":"test-service-key private-arbitrary-credential"}}`)
	d := captureSSEErrorDiagnostic(context.Background(), raw)
	if d.code != "server_error" || d.summary != "[body omitted: execution context unavailable]" {
		t.Fatalf("parser without execution redaction context exposed prose: %#v", d)
	}
}

func TestSSEErrorsAfterPartialOutputFailStreamAndAggregate(t *testing.T) {
	prefix := "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"item_0\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"item_id\":\"item_0\",\"output_index\":0,\"content_index\":0,\"delta\":\"partial text\"}\n\n"
	for _, tail := range []string{
		"event: error\ndata: {\"error\":{\"message\":\"Generation stopped private-current-key\"}}\n\n",
		"data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"Generation stopped private-current-key\"}}}\n\n",
		"event: error\ndata: {\"message\":\"private-current-key\"\n\n",
	} {
		ctx := withSSEErrorDiagnostic(context.Background(), &http.Response{StatusCode: 200}, "private-current-key")
		body, err := aggregateStream(ctx, strings.NewReader(prefix+tail), "openai-response")
		assertSSEDiagnosticFailure(t, err)
		if body != nil || strings.Contains(err.Error(), "private-current-key") {
			t.Fatal("aggregate returned partial success or leaked credentials")
		}
		var emitted bytes.Buffer
		err = forwardStream(ctx, strings.NewReader(prefix+tail), route{Protocol: "openai-response"}, formatName("openai-response"), []byte(`{}`), []byte(`{}`), func(p []byte) error { _, e := emitted.Write(p); return e })
		assertSSEDiagnosticFailure(t, err)
		if emitted.String() != prefix || strings.Contains(err.Error(), "private-current-key") {
			t.Fatalf("stream lost prefix or emitted raw error: %s", emitted.String())
		}
	}
}

func TestExecuteHTTP200SSEErrorDoesNotRecheckRetryOrCooldown(t *testing.T) {
	var configs, inference atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/config") {
			configs.Add(1)
			_, _ = io.WriteString(w, `{"providers":{"p":{"package":"@opencode/ai/providers/openai","settings":{"baseURL":"`+server.URL+`/inference"},"models":{"m":{}}}}}`)
			return
		}
		inference.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Retry-After", "60")
		w.Header().Set("X-Request-Id", "private-response-id")
		message := "Generation failed; HTTP 403 or 429; test-service-key test-org private-response-id " + r.Header.Get("X-Opencode-Session")
		raw, _ := json.Marshal(map[string]any{"type": "response.failed", "response": map[string]any{"error": map[string]any{"code": "rate_limit_exceeded", "status": 403, "message": message}}})
		_, _ = w.Write(sseEvent{Data: raw}.wire())
	}))
	defer server.Close()
	testConfig(t, server.URL)
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "opencode/p/m", Format: "openai-response", SourceFormat: "openai-response", Payload: []byte(clientRequest("openai-response")), StorageJSON: rawJSON(t, testCredential(server.URL))}}
	for attempt := 1; attempt <= 2; attempt++ {
		result, err := execute(rawJSON(t, req), false)
		assertSSEDiagnosticFailure(t, err)
		if result != nil || inference.Load() != int32(attempt) || configs.Load() != 1 {
			t.Fatalf("SSE failure changed retry/recheck/cooldown: result=%v inference=%d config=%d", result, inference.Load(), configs.Load())
		}
		for _, secret := range []string{"test-service-key", "test-org", "private-response-id", "cpa_"} {
			if strings.Contains(output.String()+err.Error(), secret) {
				t.Fatalf("execute leaked current credential/session/header: %q", secret)
			}
		}
	}
	if strings.Count(output.String(), "source=upstream_sse") != 2 || !strings.Contains(output.String(), "error_code=rate_limit_exceeded") || strings.Contains(output.String(), "http_status=403") {
		t.Fatalf("SSE diagnostic origin/category/status incorrect: %s", output.String())
	}
}

func assertSSEDiagnosticFailure(t *testing.T, err error) {
	t.Helper()
	var status *statusError
	if !errors.As(err, &status) || status.status != http.StatusBadGateway || !strings.Contains(err.Error(), "source=upstream_sse") || !strings.Contains(err.Error(), "http_status=200") {
		t.Fatalf("SSE error must be a distinct 502 with the actual transport status: %v", err)
	}
}
