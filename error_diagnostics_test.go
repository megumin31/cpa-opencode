package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestUpstreamErrorDiagnosticCategories(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body, parse, code, kind, tag string
	}{
		{"nested_known", "application/json; charset=utf-8", `{"error":{"code":"insufficient_quota","type":"permission_error","tag":"ForbiddenError","message":"private-message"}}`, "json_category", "insufficient_quota", "permission_error", "forbidden"},
		{"top_level_known", "APPLICATION/JSON", `{"code":"invalid_api_key","type":"authentication_error","tag":"UnauthorizedError"}`, "json_category", "invalid_api_key", "authentication_error", "unauthorized"},
		{"problem_json", "application/problem+json", `{"type":"permission_error","detail":"private-detail"}`, "json_category", "", "permission_error", ""},
		{"known_uppercase", "application/json", `{"error":{"code":"PERMISSION_DENIED"}}`, "json_category", "permission_denied", "", ""},
		{"unknown", "application/json", `{"error":{"code":"sk-fake-private-key","type":"private-project","tag":"https://private.invalid/path","message":"private-message"}}`, "json_no_category", "", "", ""},
		{"not_substring_matching", "application/json", `{"error":{"code":"invalid_api_key:sk-fake-private-key","type":" permission_error ","tag":"ForbiddenError\nprivate-message"}}`, "json_no_category", "", "", ""},
		{"message_not_inspected", "application/json", `{"error":{"message":"permission_error","details":{"code":"invalid_api_key"}},"metadata":{"type":"authentication_error"}}`, "json_no_category", "", "", ""},
		{"unknown_types", "application/json", `{"error":{"code":403,"type":["permission_error"],"tag":{"tag":"ForbiddenError"}}}`, "json_no_category", "", "", ""},
		{"string_error", "application/json", `{"error":"sk-fake-private-key"}`, "json_no_category", "", "", ""},
		{"html", "text/html; charset=utf-8", `<html>sk-fake-private-key: permission_error</html>`, "non_json", "", "", ""},
		{"plain_text", "text/plain", `private-message`, "non_json", "", "", ""},
		{"malformed", "application/json", `{"error":{"code":"invalid_api_key"},`, "invalid_json", "", "", ""},
		{"trailing_data", "application/json", `{"error":{"code":"invalid_api_key"}} private-message`, "invalid_json", "", "", ""},
		{"array", "application/json", `[{"code":"invalid_api_key"}]`, "json_no_category", "", "", ""},
		{"null", "application/json", `null`, "json_no_category", "", "", ""},
		{"empty", "", ``, "empty", "", "", ""},
		{"whitespace", "application/json", " \r\n\t", "empty", "", "", ""},
		{"oversized", "application/json", `{"error":{"code":"invalid_api_key","error_type":"permission_error","status":"PERMISSION_DENIED"},"message":"` + strings.Repeat("x", maxErrorDiagnosticBytes) + `"}`, "too_large", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &countedDiagnosticBody{Reader: strings.NewReader(tc.body)}
			resp := &http.Response{StatusCode: 403, Header: http.Header{"Content-Type": {tc.contentType}}, Body: body}
			d := captureUpstreamErrorDiagnostic(context.Background(), resp)
			if d.status != 403 || d.parseCategory != tc.parse || d.code != tc.code || d.kind != tc.kind || d.tag != tc.tag || d.errorStatus != "" {
				t.Fatalf("unexpected categorical result: %#v", d)
			}
			if body.closed != 1 || body.read > maxErrorDiagnosticBytes+1 {
				t.Fatalf("body close/read bounds: closes=%d bytes=%d", body.closed, body.read)
			}
		})
	}
}

// These shallow field positions and exact labels are present in the official
// 2.0.29 provider classifier. Fixtures are synthetic; they do not establish
// any account's entitlement or the cause of a historical upstream error.
func TestUpstreamErrorDiagnosticOfficialErrorSchemas(t *testing.T) {
	for _, tc := range []struct {
		name, body, code, kind, errorStatus string
	}{
		{"google_symbolic_status", `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"private-message"}}`, "", "", "permission_denied"},
		{"google_quota_status", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED"}}`, "", "", "resource_exhausted"},
		{"nested_error_type", `{"error":{"error_type":"permission_error"}}`, "", "permission_error", ""},
		{"top_level_error_type", `{"error_type":"authentication_error"}`, "", "authentication_error", ""},
		{"native_invalid_key_alias", `{"error":{"code":"api_key_invalid"}}`, "invalid_api_key", "", ""},
		{"native_usage_category", `{"error":{"error_type":"usage_not_included"}}`, "", "usage_not_included", ""},
		{"native_billing_category", `{"code":"billing_error"}`, "billing_error", "", ""},
		{"keep_separate_categories", `{"error":{"code":"insufficient_quota","type":"permission_error","status":"PERMISSION_DENIED"}}`, "insufficient_quota", "permission_error", "permission_denied"},
		{"nested_alias_before_top_level", `{"error":{"error_type":"permission_error"},"type":"authentication_error"}`, "", "permission_error", ""},
		{"type_before_error_type", `{"error":{"type":"permission_error","error_type":"authentication_error"}}`, "", "permission_error", ""},
		{"unknown_type_known_alias", `{"error":{"type":"private-project","error_type":"permission_error"}}`, "", "permission_error", ""},
		{"unknown_nested_known_top_level", `{"error":{"error_type":"private-project"},"error_type":"authentication_error"}`, "", "authentication_error", ""},
		{"unknown_values", `{"error":{"status":"sk-fake-private-key","error_type":"private-project"},"error_type":"https://private.invalid/path"}`, "", "", ""},
		{"exact_match_only", `{"error":{"status":" PERMISSION_DENIED ","error_type":"permission_error\nprivate-message","code":"api_key_invalid:sk-fake-private-key"},"error_type":"billing_error private-message"}`, "", "", ""},
		{"numeric_code_is_not_http_status", `{"error":{"code":401,"status":403,"error_type":429}}`, "", "", ""},
		{"wrong_field_types", `{"error":{"status":["PERMISSION_DENIED"],"error_type":{"type":"permission_error"}},"error_type":true}`, "", "", ""},
		{"null_values", `{"error":{"status":null,"error_type":null},"error_type":null}`, "", "", ""},
		{"top_level_status_not_inspected", `{"status":"PERMISSION_DENIED"}`, "", "", ""},
		{"local_error_tags_not_inferred", `{"_tag":"ForbiddenError","error":{"_tag":"ForbiddenError"}}`, "", "", ""},
		{"nested_payloads_not_inspected", `{"error":{"details":[{"reason":"PERMISSION_DENIED"}],"metadata":{"error_type":"permission_error"},"innererror":{"code":"invalid_api_key"}},"response":{"error":{"code":"invalid_api_key"}},"exception":{"type":"authentication_error"}}`, "", "", ""},
		{"message_json_not_inspected", `{"message":"{\"error_type\":\"authentication_error\"}","error":{"message":"{\"status\":\"PERMISSION_DENIED\"}"}}`, "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &countedDiagnosticBody{Reader: strings.NewReader(tc.body)}
			d := captureUpstreamErrorDiagnostic(context.Background(), &http.Response{
				StatusCode: 403,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       body,
			})
			wantParse := "json_no_category"
			if tc.code != "" || tc.kind != "" || tc.errorStatus != "" {
				wantParse = "json_category"
			}
			if d.status != 403 || d.parseCategory != wantParse || d.code != tc.code || d.kind != tc.kind || d.errorStatus != tc.errorStatus || d.tag != "" {
				t.Fatalf("unexpected categorical result: %#v", d)
			}
			if body.closed != 1 || body.read > maxErrorDiagnosticBytes+1 {
				t.Fatalf("body close/read bounds: closes=%d bytes=%d", body.closed, body.read)
			}
		})
	}
}

func TestUpstreamErrorDiagnosticAtSizeBoundary(t *testing.T) {
	base := `{"error":{"code":"invalid_api_key","error_type":"permission_error","status":"PERMISSION_DENIED"},"message":""}`
	body := strings.Replace(base, `"message":""`, `"message":"`+strings.Repeat("x", maxErrorDiagnosticBytes-len(base))+`"`, 1)
	if len(body) != maxErrorDiagnosticBytes {
		t.Fatal("invalid boundary fixture")
	}
	d := captureUpstreamErrorDiagnostic(context.Background(), &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(body))})
	if d.parseCategory != "json_category" || d.code != "invalid_api_key" || d.kind != "permission_error" || d.errorStatus != "permission_denied" {
		t.Fatalf("bounded valid body was not parsed: %#v", d)
	}
}

func TestDiagnosticContentTypeIsFixedCategory(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{"", "missing"},
		{"application/json; private=sk-fake-private-key", "json"},
		{"text/html; charset=utf-8", "html"},
		{"text/plain", "text"},
		{"text/event-stream", "event_stream"},
		{"application/private-project+json", "other"},
		{"https://private.invalid/path", "other"},
		{"application/json; invalid\nprivate", "other"},
		{strings.Repeat("x", 257), "other"},
	} {
		if got := diagnosticContentType(tc.value); got != tc.want {
			t.Fatalf("category=%q want=%q", got, tc.want)
		}
	}
}

func TestUpstreamErrorDiagnosticNeverLogsReflectedSecrets(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	for _, body := range []string{
		`{"error":{"code":"invalid_api_key","type":"permission_error","message":"sk-fake-private-key","request_id":"private-request-id","private-field":"private-project"},"url":"https://private.invalid/path"}`,
		`{"error":{"code":"sk-fake-private-key","type":"private-project","tag":"https://private.invalid/path"},"message":"private-message"}`,
		`{"error":{"code":403,"status":"PERMISSION_DENIED","error_type":"permission_error","message":"private-message"}}`,
		`{"error":{"status":"sk-fake-private-key","error_type":"private-project"},"error_type":"https://private.invalid/path"}`,
		`<html>sk-fake-private-key private-message https://private.invalid/path</html>`,
	} {
		logUpstreamError(context.Background(), &http.Response{
			StatusCode: 403,
			Header: http.Header{
				"Content-Type": {"application/json; private=sk-fake-private-key"},
				"X-Request-Id": {"private-request-id"},
				"Set-Cookie":   {"private-cookie"},
			},
			Body: io.NopCloser(strings.NewReader(body)),
		})
	}
	got := output.String()
	for _, secret := range []string{"sk-fake-private-key", "private-request-id", "private-project", "private-cookie", "https://private.invalid/path", "private-field", "error_tag="} {
		if strings.Contains(got, secret) {
			t.Fatalf("diagnostic contained excluded value or field: %q", secret)
		}
	}
	for _, expected := range []string{"http_status=403", "content_type=json", "request_id_present=true", "parse_category=json_category", "parse_category=json_no_category", "parse_category=invalid_json", "error_code=invalid_api_key", "error_type=permission_error", "error_status=permission_denied"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("missing safe diagnostic %q", expected)
		}
	}
}

func TestUpstreamErrorDiagnosticReadFailureDiscardsPartialBody(t *testing.T) {
	d := captureUpstreamErrorDiagnostic(context.Background(), &http.Response{StatusCode: 403, Body: io.NopCloser(&failingDiagnosticReader{})})
	if d.parseCategory != "read_error" || d.code != "" || d.kind != "" || d.tag != "" || d.errorStatus != "" {
		t.Fatalf("partial failing body was trusted: %#v", d)
	}
	d = captureUpstreamErrorDiagnostic(context.Background(), &http.Response{StatusCode: 403})
	if d.parseCategory != "empty" {
		t.Fatal("nil body did not yield empty category")
	}
}

func TestUpstreamErrorDiagnosticAlreadyCanceledDoesNotRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := &countedDiagnosticBody{Reader: strings.NewReader(`{"error":{"code":"invalid_api_key"}}`)}
	d := captureUpstreamErrorDiagnostic(ctx, &http.Response{StatusCode: 403, Body: body})
	if d.status != 403 || d.parseCategory != "canceled" || body.read != 0 || body.closed != 1 {
		t.Fatalf("already canceled diagnostic read body: %#v read=%d closes=%d", d, body.read, body.closed)
	}
}

func TestUpstreamErrorDiagnosticInterruptsMidRead(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "diagnostic_timeout"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var deadlineCancel context.CancelFunc
			if mode == "deadline" {
				ctx, deadlineCancel = context.WithTimeout(ctx, 20*time.Millisecond)
				defer deadlineCancel()
			}
			body := newBlockingDiagnosticBody()
			done := make(chan upstreamErrorDiagnostic, 1)
			start := time.Now()
			go func() {
				done <- captureUpstreamErrorDiagnostic(ctx, &http.Response{StatusCode: 403, Body: body})
			}()
			<-body.started
			if mode == "cancel" {
				cancel()
			}
			select {
			case d := <-done:
				want := "timeout"
				if mode == "cancel" {
					want = "canceled"
				}
				if d.status != 403 || d.parseCategory != want || d.code != "" || d.kind != "" || d.tag != "" || d.errorStatus != "" {
					t.Fatalf("canceled read lost status or leaked partial categories: %#v", d)
				}
				if body.closes.Load() != 1 {
					t.Fatal("body must be closed exactly once")
				}
				if mode == "diagnostic_timeout" && time.Since(start) < errorDiagnosticTimeout {
					t.Fatal("fixture did not wait for the independent diagnostic timeout")
				}
			case <-time.After(errorDiagnosticTimeout + time.Second):
				_ = body.Close()
				t.Fatal("error body read did not stop promptly")
			}
		})
	}
}

func TestUpstreamErrorDiagnosticClosesStalledHTTPBody(t *testing.T) {
	serverCanceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		_, _ = io.WriteString(w, `{"error":{"code":"permission_denied"},`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(serverCanceled)
	}))
	defer server.Close()
	resp, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	d := captureUpstreamErrorDiagnostic(ctx, resp)
	if d.status != 403 || d.parseCategory != "timeout" || d.code != "" {
		t.Fatalf("stalled HTTP body was not canceled safely: %#v", d)
	}
	select {
	case <-serverCanceled:
	case <-time.After(time.Second):
		t.Fatal("transport response close did not cancel stalled server")
	}
}

func TestExecuteErrorDiagnosticsPreserveRecheckAndCooldown(t *testing.T) {
	for _, status := range []int{401, 402, 403, 429, 500, 502, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var configs, inference atomic.Int32
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/config") {
					configs.Add(1)
					_, _ = io.WriteString(w, `{"providers":{"p":{"package":"@opencode/ai/providers/openai/chat","settings":{"baseURL":"`+server.URL+`/inference"},"models":{"m":{}}}}}`)
					return
				}
				inference.Add(1)
				w.Header().Set("Retry-After", "60")
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-Id", "private-request-id")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":{"type":"permission_error","message":"test-service-key private-message"}}`)
			}))
			defer server.Close()
			testConfig(t, server.URL)
			var output bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&output)
			defer log.SetOutput(previous)
			c := testCredential(server.URL)
			req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "opencode/p/m", Format: "openai", SourceFormat: "openai", Payload: []byte(clientRequest("openai")), StorageJSON: rawJSON(t, c)}}
			_, err := execute(rawJSON(t, req), false)
			if err == nil || err.(*statusError).status != status || inference.Load() != 1 {
				t.Fatalf("status or single-attempt behavior changed: error=%v inference=%d", err, inference.Load())
			}
			wantConfigs := int32(1)
			if status == 401 || status == 403 {
				wantConfigs = 2
			}
			if configs.Load() != wantConfigs {
				t.Fatalf("config recheck calls=%d want=%d", configs.Load(), wantConfigs)
			}
			if status == 429 || status == 503 {
				_, second := execute(rawJSON(t, req), false)
				if second == nil || second.(*statusError).status != status || inference.Load() != 1 {
					t.Fatal("Retry-After cooldown was not preserved")
				}
			}
			if strings.Count(output.String(), "OpenCode inference error:") != 1 || !strings.Contains(output.String(), "error_type=permission_error") {
				t.Fatal("missing or repeated non-2xx diagnostic")
			}
			for _, value := range []string{c.Key, "private-request-id"} {
				if strings.Contains(output.String()+err.Error(), value) {
					t.Fatal("private upstream value escaped diagnostics")
				}
			}
			if status == 403 && !strings.Contains(err.Error(), "OpenCode upstream HTTP 403:") {
				t.Fatal("403 must not guess service-key or organization requirements")
			}
		})
	}
}

func TestExecuteSuccessfulResponseHasNoErrorDiagnostic(t *testing.T) {
	var inference atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/config") {
			_, _ = io.WriteString(w, `{"providers":{"p":{"package":"@opencode/ai/providers/openai/chat","settings":{"baseURL":"`+server.URL+`/inference"},"models":{"m":{}}}}}`)
			return
		}
		inference.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, nativeFixture("openai"))
	}))
	defer server.Close()
	testConfig(t, server.URL)
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "opencode/p/m", Format: "openai", SourceFormat: "openai", Payload: []byte(clientRequest("openai")), StorageJSON: rawJSON(t, testCredential(server.URL))}}
	if _, err := execute(rawJSON(t, req), false); err != nil {
		t.Fatal(err)
	}
	if inference.Load() != 1 || strings.Contains(output.String(), "OpenCode inference error:") {
		t.Fatal("successful response must not produce an error diagnostic or extra inference")
	}
}

type countedDiagnosticBody struct {
	*strings.Reader
	read, closed int
}

func (b *countedDiagnosticBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}
func (b *countedDiagnosticBody) Close() error { b.closed++; return nil }

type failingDiagnosticReader struct{}

func (*failingDiagnosticReader) Read(p []byte) (int, error) {
	return copy(p, `{"error":{"code":"invalid_api_key","error_type":"permission_error","status":"PERMISSION_DENIED"}}`), errors.New("sk-fake-private-key private-read-failure")
}

type blockingDiagnosticBody struct {
	started, closed chan struct{}
	startOnce       sync.Once
	closeOnce       sync.Once
	closes          atomic.Int32
}

func newBlockingDiagnosticBody() *blockingDiagnosticBody {
	return &blockingDiagnosticBody{started: make(chan struct{}), closed: make(chan struct{})}
}

func (b *blockingDiagnosticBody) Read(p []byte) (int, error) {
	first := false
	b.startOnce.Do(func() { first = true; close(b.started) })
	if first {
		return copy(p, `{"error":{"code":"invalid_api_key","error_type":"permission_error","status":"PERMISSION_DENIED"}}`), nil
	}
	<-b.closed
	return 0, errors.New("sk-fake-private-key private-close-error")
}

func (b *blockingDiagnosticBody) Close() error {
	b.closes.Add(1)
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}
