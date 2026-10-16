package diagnostics

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/megumin31/cpa-opencode-zen/internal/fault"
)

type sseErrorDiagnosticContextKey struct{}

type sseErrorDiagnosticContext struct {
	base    Diagnostic
	secrets []string
	sink    LogSink
}

// Retain only this execution's in-memory redaction values and safe transport
// metadata. Do not read credentials from disk or retain/log the response body.
// WithSSE attaches safe transport metadata, a copied redaction set, and the request log sink.
func WithSSE(ctx context.Context, resp *http.Response, secrets []string, sink LogSink) context.Context {
	d := sseErrorDiagnosticContext{
		base: Diagnostic{
			status:           resp.StatusCode,
			contentType:      diagnosticContentType(resp.Header.Get("Content-Type")),
			requestIDPresent: hasDiagnosticRequestID(resp.Header),
		},
		secrets: append([]string(nil), secrets...),
		sink:    sink,
	}
	for key, values := range resp.Header {
		if !strings.EqualFold(key, "Content-Type") && !strings.EqualFold(key, "Retry-After") {
			d.secrets = append(d.secrets, values...)
		}
	}
	return context.WithValue(ctx, sseErrorDiagnosticContextKey{}, d)
}

func captureSSEErrorDiagnostic(ctx context.Context, raw []byte) Diagnostic {
	// Direct parser callers have no HTTP response; production execute always
	// attaches the actual transport status before parsing the event stream.
	info, ok := ctx.Value(sseErrorDiagnosticContextKey{}).(sseErrorDiagnosticContext)
	if !ok {
		info.base.status = http.StatusOK
		info.base.contentType = "event_stream"
	}
	d := info.base
	d.parseCategory = "empty"
	d.summary = "[body unavailable]"
	d = parseUpstreamErrorDiagnostic(d, raw, info.secrets, true)
	if !ok {
		// Raw parser callers have no complete credential/session redaction set.
		// Keep safe categories, but fail closed rather than echoing arbitrary
		// upstream prose that might reflect an opaque credential.
		d.summary = "[body omitted: execution context unavailable]"
	}
	return d
}

// SSEError preserves a stream failure as 502 without inferring transport retry state.
func SSEError(ctx context.Context, raw []byte) error {
	d := captureSSEErrorDiagnostic(ctx, raw)
	info, _ := ctx.Value(sseErrorDiagnosticContextKey{}).(sseErrorDiagnosticContext)
	Log("upstream_sse", d, info.sink)
	// A reported code (or HTTP status quoted in upstream prose) is not the
	// transport status and must never trigger HTTP auth rechecks or cooldowns.
	return &fault.Error{Status: http.StatusBadGateway, Message: fmt.Sprintf("OpenCode upstream SSE error: source=upstream_sse http_status=%d redacted_summary=%q (diagnostic=%s)", d.status, d.summary, d.parseCategory)}
}
