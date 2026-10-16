package diagnostics

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/megumin31/cpa-opencode-zen/internal/fault"
	"log"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

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
			var output bytes.Buffer
			sink := log.New(&output, "", 0).Printf
			ctx := WithSSE(context.Background(), &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}}, nil, sink)
			d := captureSSEErrorDiagnostic(ctx, []byte(tc.raw))
			if d.status != 200 || d.parseCategory != tc.parse || d.code != tc.code || d.kind != tc.kind || d.errorStatus != tc.status || d.summary != tc.summary || d.retryAfter != "" {
				t.Fatalf("unexpected SSE diagnostic: %#v", d)
			}
			err := SSEError(ctx, []byte(tc.raw))
			assertSSEDiagnosticFailure(t, err)
			if strings.Count(output.String(), "source=upstream_sse") != 1 || !strings.Contains(output.String(), "http_status=200") {
				t.Fatalf("error log origin mismatch: log=%s", output.String())
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
	const key = "opaque-current-credential"
	secrets := []string{key, "private-current-org", "private-current-label", "private-request-header", "private-query-value", "private-proxy-user", "private-proxy-pass"}
	var output bytes.Buffer
	sink := log.New(&output, "", 0).Printf
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"private-response-id"}, "X-Other": {"private-response-header"}, "Retry-After": {"60"}}}
	ctx := WithSSE(context.Background(), resp, secrets, sink)
	privateValues := []string{key, "private-current-org", "private-current-label", "private-request-header", "private-response-id", "private-response-header", "private-query-value", "private-proxy-user", "private-proxy-pass", "private-sibling-account"}
	variants := append([]string(nil), privateValues...)
	variants = append(variants, url.QueryEscape(key), base64.StdEncoding.EncodeToString([]byte(key)), hex.EncodeToString([]byte(key)))
	raw := diagnosticJSON(t, map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"error":    map[string]any{"code": "server_error", "message": "Generation failed: " + strings.Join(variants, " ")},
			"metadata": map[string]any{"account": "private-sibling-account"},
			"output":   []any{map[string]any{"type": "message", "content": "private-output-content"}},
		},
	})
	err := SSEError(ctx, raw)
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
	raw = diagnosticJSON(t, map[string]any{"error": map[string]any{"message": strings.Repeat("x", 1000) + " " + key + strings.Repeat("y", 3000)}})
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

func assertSSEDiagnosticFailure(t *testing.T, err error) {
	t.Helper()
	var status *fault.Error
	if !errors.As(err, &status) || status.Status != http.StatusBadGateway || !strings.Contains(err.Error(), "source=upstream_sse") || !strings.Contains(err.Error(), "http_status=200") {
		t.Fatalf("SSE error must be a distinct 502 with the actual transport status: %v", err)
	}
}
func diagnosticJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
