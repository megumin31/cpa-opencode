package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxErrorDiagnosticBytes = 16 << 10
	errorDiagnosticTimeout  = time.Second
)

// Only the bounded, redacted summary may contain upstream prose.
type upstreamErrorDiagnostic struct {
	status              int
	contentType         string
	parseCategory       string
	requestIDPresent    bool
	code, kind, tag     string
	errorStatus         string
	summary, retryAfter string
}

func logUpstreamError(ctx context.Context, resp *http.Response, secrets ...string) upstreamErrorDiagnostic {
	d := captureUpstreamErrorDiagnostic(ctx, resp, secrets...)
	logUpstreamErrorDiagnostic("upstream", d)
	return d
}

func logUpstreamErrorDiagnostic(source string, d upstreamErrorDiagnostic) {
	// Unknown fields are omitted instead of printing their untrusted values.
	fields := ""
	if d.code != "" {
		fields += " error_code=" + d.code
	}
	if d.kind != "" {
		fields += " error_type=" + d.kind
	}
	if d.tag != "" {
		fields += " error_tag=" + d.tag
	}
	if d.errorStatus != "" {
		fields += " error_status=" + d.errorStatus
	}
	log.Printf("OpenCode inference error: source=%s http_status=%d content_type=%s parse_category=%s request_id_present=%t%s retry_after=%q redacted_summary=%q", source, d.status, d.contentType, d.parseCategory, d.requestIDPresent, fields, d.retryAfter, d.summary)
}

// captureUpstreamErrorDiagnostic owns and closes the non-success response body.
// A short deadline and a limit independent of normal response sizes prevent an
// error response from holding up the caller or retaining an unbounded body.
// Closing an HTTP response body interrupts an outstanding transport body read.
func captureUpstreamErrorDiagnostic(ctx context.Context, resp *http.Response, secrets ...string) upstreamErrorDiagnostic {
	d := upstreamErrorDiagnostic{
		status:           resp.StatusCode,
		contentType:      diagnosticContentType(resp.Header.Get("Content-Type")),
		parseCategory:    "empty",
		requestIDPresent: hasDiagnosticRequestID(resp.Header),
	}
	if v := resp.Header.Get("Retry-After"); len(v) <= 10 {
		if n, err := strconv.ParseUint(v, 10, 32); err == nil {
			d.retryAfter = strconv.FormatUint(n, 10)
		}
	}
	d.summary = "[body unavailable]"
	for key, values := range resp.Header {
		if !strings.EqualFold(key, "Content-Type") && !strings.EqualFold(key, "Retry-After") {
			secrets = append(secrets, values...)
		}
	}
	if resp.Body == nil {
		return d
	}
	readCtx, cancel := context.WithTimeout(ctx, errorDiagnosticTimeout)
	defer cancel()
	var closeOnce sync.Once
	closeBody := func() { closeOnce.Do(func() { _ = resp.Body.Close() }) }
	stopClose := context.AfterFunc(readCtx, closeBody)
	defer func() { stopClose(); closeBody() }()
	if readCtx.Err() != nil {
		d.parseCategory = diagnosticContextCategory(readCtx)
		return d
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorDiagnosticBytes+1))
	if readCtx.Err() != nil {
		d.parseCategory = diagnosticContextCategory(readCtx)
		return d
	}
	if err != nil {
		d.parseCategory = "read_error"
		return d
	}
	return parseUpstreamErrorDiagnostic(d, raw, secrets, false)
}

// Both HTTP failures and errors inside successful SSE responses use the same
// bounded diagnostic parser. Only SSE errors additionally recognize the
// Responses response.error position; arbitrary response/output data is not
// treated as diagnostic prose.
func parseUpstreamErrorDiagnostic(d upstreamErrorDiagnostic, raw []byte, secrets []string, responseError bool) upstreamErrorDiagnostic {
	if len(raw) > maxErrorDiagnosticBytes {
		d.parseCategory = "too_large"
		return d // Never trust or log categories from a truncated prefix.
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return d
	}
	if !json.Valid(raw) {
		d.parseCategory = "non_json"
		if raw[0] == '{' || raw[0] == '[' || d.contentType == "json" {
			d.parseCategory = "invalid_json"
		}
		return d
	}
	d.summary = diagnosticSummary(raw, secrets, responseError)
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil || envelope == nil {
		d.parseCategory = "json_no_category"
		return d
	}
	if responseError {
		var response map[string]json.RawMessage
		if json.Unmarshal(envelope["response"], &response) == nil && response["error"] != nil && (envelope["error"] == nil || bytes.Equal(bytes.TrimSpace(envelope["error"]), []byte("null"))) {
			envelope["error"] = response["error"]
		}
	}
	// Inspect only these explicit envelope positions. Do not traverse message,
	// metadata, details, arrays, or other private/reflected payload contents.
	var nested map[string]json.RawMessage
	_ = json.Unmarshal(envelope["error"], &nested)
	d.code = diagnosticErrorField(nested, envelope, "code")
	// The official 2.0.29 provider classifier also reads error_type at these
	// two levels and status inside error (for example Google's symbolic status).
	// Keep that status separate from both the HTTP status and error.code; never
	// infer a category from a numeric code or an arbitrary top-level status.
	d.kind = diagnosticErrorField(nested, envelope, "type", "error_type")
	d.tag = diagnosticErrorField(nested, envelope, "tag")
	d.errorStatus = diagnosticErrorField(nested, nil, "status")
	d.parseCategory = "json_no_category"
	if d.code != "" || d.kind != "" || d.tag != "" || d.errorStatus != "" {
		d.parseCategory = "json_category"
	}
	return d
}

func diagnosticContextCategory(ctx context.Context) string {
	if ctx.Err() == context.Canceled {
		return "canceled"
	}
	return "timeout"
}

func diagnosticContentType(value string) string {
	if value == "" {
		return "missing"
	}
	if len(value) > 256 {
		return "other"
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "other"
	}
	switch strings.ToLower(mediaType) {
	case "application/json", "application/problem+json":
		return "json"
	case "text/html":
		return "html"
	case "text/plain":
		return "text"
	case "text/event-stream":
		return "event_stream"
	default:
		return "other"
	}
}

func hasDiagnosticRequestID(header http.Header) bool {
	for _, key := range []string{"X-Request-Id", "Request-Id", "X-Amzn-Requestid", "Cf-Ray"} {
		if len(header.Values(key)) != 0 {
			return true
		}
	}
	return false
}

func diagnosticErrorField(nested, envelope map[string]json.RawMessage, keys ...string) string {
	for _, object := range []map[string]json.RawMessage{nested, envelope} {
		for _, key := range keys {
			var value string
			if json.Unmarshal(object[key], &value) == nil {
				if category := allowlistedErrorCategory(value); category != "" {
					return category
				}
			}
		}
	}
	return ""
}

// Exact matches only. Return literal canonical labels, never a transformed or
// truncated upstream string. A syntactically safe-looking identifier is still
// private until explicitly enumerated here. These labels describe what the
// upstream reported; they do not establish the cause of an HTTP error.
func allowlistedErrorCategory(value string) string {
	switch value {
	case "unauthorized", "UNAUTHENTICATED", "UnauthorizedError":
		return "unauthorized"
	case "forbidden", "ForbiddenError":
		return "forbidden"
	case "permission_denied", "PERMISSION_DENIED", "PermissionDeniedError":
		return "permission_denied"
	case "invalid_api_key", "api_key_invalid":
		return "invalid_api_key"
	case "authentication_error", "AuthenticationError":
		return "authentication_error"
	case "permission_error":
		return "permission_error"
	case "insufficient_quota":
		return "insufficient_quota"
	case "insufficient_balance":
		return "insufficient_balance"
	case "billing_not_active":
		return "billing_not_active"
	case "usage_not_included":
		return "usage_not_included"
	case "billing_error":
		return "billing_error"
	case "rate_limit_exceeded":
		return "rate_limit_exceeded"
	case "rate_limit_error", "RateLimitError":
		return "rate_limit_error"
	case "RESOURCE_EXHAUSTED":
		return "resource_exhausted"
	case "model_not_found", "ModelNotFoundError":
		return "model_not_found"
	case "not_found_error", "NOT_FOUND":
		return "not_found_error"
	case "invalid_request_error", "INVALID_ARGUMENT":
		return "invalid_request_error"
	case "server_error", "INTERNAL":
		return "server_error"
	case "api_error":
		return "api_error"
	case "overloaded_error":
		return "overloaded_error"
	case "UNAVAILABLE":
		return "unavailable"
	default:
		return ""
	}
}

// The upstream status is preserved; no body text controls status or retry logic.
func (d upstreamErrorDiagnostic) clientError() error {
	return &statusError{d.status, fmt.Sprintf("OpenCode upstream HTTP %d: redacted_summary=%q (diagnostic=%s)", d.status, d.summary, d.parseCategory)}
}
