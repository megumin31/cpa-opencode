package protocol

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"testing"

	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
	"github.com/megumin31/cpa-opencode-zen/internal/diagnostics"
	"github.com/megumin31/cpa-opencode-zen/internal/fault"
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
		{"long", "error", `{"error":{"code":"invalid_api_key","message":"` + strings.Repeat("private-long-data", 16<<10) + `"}}`, "too_large", "", "", "", "[body unavailable]"},
		{"truncated_prefix", "error", `{"error":{"code":"invalid_api_key","message":"` + strings.Repeat("x", 16<<10), "too_large", "", "", "", "[body unavailable]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			sink := log.New(&output, "", 0).Printf
			ctx := diagnostics.WithSSE(context.Background(), &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}}, nil, sink)
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

func TestSSEErrorsAfterPartialOutputFailStreamAndAggregate(t *testing.T) {
	prefix := "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"item_0\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"item_id\":\"item_0\",\"output_index\":0,\"content_index\":0,\"delta\":\"partial text\"}\n\n"
	for _, tail := range []string{
		"event: error\ndata: {\"error\":{\"message\":\"Generation stopped private-current-key\"}}\n\n",
		"data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"Generation stopped private-current-key\"}}}\n\n",
		"event: error\ndata: {\"message\":\"private-current-key\"\n\n",
	} {
		ctx := diagnostics.WithSSE(context.Background(), &http.Response{StatusCode: 200}, []string{"private-current-key"}, nil)
		body, err := Aggregate(ctx, strings.NewReader(prefix+tail), "openai-response")
		assertSSEDiagnosticFailure(t, err)
		if body != nil || strings.Contains(err.Error(), "private-current-key") {
			t.Fatal("aggregate returned partial success or leaked credentials")
		}
		var emitted bytes.Buffer
		err = Forward(ctx, strings.NewReader(prefix+tail), catalog.Route{Protocol: "openai-response"}, FormatName("openai-response"), []byte(`{}`), []byte(`{}`), func(p []byte) error { _, e := emitted.Write(p); return e })
		assertSSEDiagnosticFailure(t, err)
		if emitted.String() != prefix || strings.Contains(err.Error(), "private-current-key") {
			t.Fatalf("stream lost prefix or emitted raw error: %s", emitted.String())
		}
	}
}

func assertSSEDiagnosticFailure(t *testing.T, err error) {
	t.Helper()
	var status *fault.Error
	if !errors.As(err, &status) || status.Status != http.StatusBadGateway || !strings.Contains(err.Error(), "source=upstream_sse") || !strings.Contains(err.Error(), "http_status=200") {
		t.Fatalf("SSE error must be a distinct 502 with the actual transport status: %v", err)
	}
}
