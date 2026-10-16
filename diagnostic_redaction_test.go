package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDiagnosticSummaryPreservesUsefulMessages(t *testing.T) {
	for _, body := range []string{
		`{"error":{"error_type":"server_error","message":"Payment required for this model"}}`,
		`{"error":{"details":[{"message":"Payment required for this model"}]}}`,
		`{"detail":{"error":{"message":"Payment required for this model"}}}`,
		`{"error":"Payment required for this model"}`,
		`{"title":"Payment required for this model","metadata":{"message":"do not show metadata"}}`,
	} {
		got := diagnosticSummary([]byte(body), nil)
		if got != "Payment required for this model" {
			t.Fatalf("useful message missing or extra content: %q", got)
		}
	}
}

func TestDiagnosticRedactsKnownSecretEncodingsBeforeTruncation(t *testing.T) {
	secret := "synthetic-secret+/=9"
	percent := ""
	for _, b := range []byte(secret) {
		percent += "%" + hex.EncodeToString([]byte{b})
	}
	unicodeEscape := ""
	for _, b := range []byte(secret) {
		unicodeEscape += `\u00` + hex.EncodeToString([]byte{b})
	}
	for _, encoded := range []string{secret, url.QueryEscape(secret), percent, strings.ToUpper(percent), url.QueryEscape(percent), base64.StdEncoding.EncodeToString([]byte(secret)), base64.RawStdEncoding.EncodeToString([]byte(secret)), base64.URLEncoding.EncodeToString([]byte(secret)), base64.RawURLEncoding.EncodeToString([]byte(secret)), hex.EncodeToString([]byte(secret)), strings.ToUpper(hex.EncodeToString([]byte(secret)))} {
		raw, _ := json.Marshal(map[string]any{"error": map[string]any{"message": "Not enabled: " + encoded + "; contact support"}})
		got := diagnosticSummary(raw, []string{secret})
		if strings.Contains(got, secret) || strings.Contains(got, encoded) || !strings.Contains(got, "[redacted]") {
			t.Fatalf("secret reflection not removed: %q", got)
		}
	}
	got := diagnosticSummary([]byte(`{"error":{"message":"Not enabled: `+unicodeEscape+`"}}`), []string{secret})
	if strings.Contains(got, secret) || !strings.Contains(got, "[redacted]") {
		t.Fatal("JSON escaped secret not removed")
	}
	raw, _ := json.Marshal(map[string]string{"message": strings.Repeat("x", 1015) + secret})
	got = diagnosticSummary(raw, []string{secret})
	if strings.Contains(got, "synthetic") || len(got) > 1024 || !strings.HasSuffix(got, "[truncated]") {
		t.Fatalf("unsafe truncation: %q", got)
	}
}

func TestDiagnosticRedactsSensitiveFieldsAndUntrustedContent(t *testing.T) {
	for _, value := range []string{
		"Authorization: Bearer fake-unknown-secret", "cookie=session=unknown-secret; next=private", "api_key=unknown-secret", "password=unknown multi word secret", "account_id=acct-private", "user_id=private-person", "email=person@example.invalid", "Retry failed at https://host.invalid/path?secret=unknown", "Bearer fake-unknown-secret", "detail: sk-fake-private-key", "password=first\nsecond private", "{\"token\":\"unknown-secret\"}",
	} {
		raw, _ := json.Marshal(map[string]string{"message": value})
		got := diagnosticSummary(raw, nil)
		for _, leak := range []string{"unknown-secret", "unknown multi", "acct-private", "private-person", "person@example", "host.invalid", "fake-private-key", "second private"} {
			if strings.Contains(got, leak) {
				t.Fatalf("leaked %q in %q", leak, got)
			}
		}
	}
	raw := []byte(`{"error":{"message":"Request for person-private failed","account_id":"person-private","metadata":{"message":"hidden"},"api_key":"sibling-secret"},"message":"sibling-secret unavailable"}`)
	got := diagnosticSummary(raw, nil)
	for _, leak := range []string{"person-private", "sibling-secret", "hidden"} {
		if strings.Contains(got, leak) {
			t.Fatalf("sibling/metadata leak: %q", got)
		}
	}
	for _, value := range []string{"<html><script>secret</script></html>", "&lt;script&gt;secret&lt;/script&gt;"} {
		if got := redactDiagnosticText(value, nil); got != "[HTML omitted]" {
			t.Fatalf("HTML not suppressed: %q", got)
		}
	}
	got = redactDiagnosticText("failure\nforged\r\x1b[2J\u202ehidden", nil)
	if strings.ContainsAny(got, "\n\r\x1b\u202e") {
		t.Fatalf("control leaked: %q", got)
	}
}

func TestDiagnosticLimitsAndExplicitOmissions(t *testing.T) {
	for _, body := range []string{`{"metadata":{"message":"private"}}`, `["private"]`, `"private"`, `null`} {
		if got := diagnosticSummary([]byte(body), nil); got != "[body omitted: no supported diagnostic message]" {
			t.Fatalf("unexpected unknown body summary: %q", got)
		}
	}
	raw, _ := json.Marshal(map[string]string{"message": strings.Repeat("你", 2000)})
	got := diagnosticSummary(raw, nil)
	if len(got) > 1024 || !utf8.ValidString(got) || !strings.HasSuffix(got, "[truncated]") {
		t.Fatal("summary limit or UTF8 broken")
	}
	for _, body := range []string{strings.Repeat("x", maxErrorDiagnosticBytes+1), `{"message":"private`} {
		d := captureUpstreamErrorDiagnostic(context.Background(), &http.Response{StatusCode: 402, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))})
		if strings.Contains(d.summary, "private") || d.status != 402 || !strings.Contains(d.clientError().Error(), "upstream HTTP 402") {
			t.Fatal("truncated body or status mishandled")
		}
	}
}

func TestDiagnosticRuntimeSecretsAndHeaders(t *testing.T) {
	c := credential{Key: "runtime-private", OrgID: "org-private", Label: "label-private", ProxyURL: "http://fake-user:fake-password@127.0.0.1:1"}
	secrets := diagnosticSecrets(c, http.Header{"Authorization": {"Bearer bearer-private"}, "X-Api-Key": {"header-private"}}, "https://host.invalid?key=query-private")
	message := strings.Join([]string{"runtime-private", "org-private", "label-private", "fake-user", "fake-password", "bearer-private", "header-private", "query-private"}, " ")
	if got := redactDiagnosticText(message, secrets); strings.Contains(got, "private") || strings.Contains(got, "fake-") {
		t.Fatalf("runtime secret missing: %q", got)
	}
	d := captureUpstreamErrorDiagnostic(context.Background(), &http.Response{StatusCode: 402, Header: http.Header{"Retry-After": {"60"}, "X-Request-Id": {"request-private"}, "Set-Cookie": {"cookie-private"}}, Body: io.NopCloser(strings.NewReader(`{"message":"Payment required request-private cookie-private"}`))})
	if d.retryAfter != "60" || !d.requestIDPresent || strings.Contains(d.summary, "private") || !strings.Contains(d.summary, "Payment required") {
		t.Fatalf("header filtering failed: %#v", d)
	}
	if !strings.HasPrefix(fail(400, "invalid request").Error(), "OpenCode plugin:") {
		t.Fatal("plugin source missing")
	}
}

func TestDiagnosticReviewRegressions(t *testing.T) {
	for _, tc := range []struct{ value, secret, leak string }{
		{"abc%2Fsecret (95% quota)", "abc/secret", "abc"},
		{"alice+smith", "alice smith", "alice"},
		{"authorization: Digest first-secret\nsecond-secret", "", "second-secret"},
		{`reflected \\u0061\\u0062\\u0063`, "abc", "u0061"},
	} {
		got := redactDiagnosticText(tc.value, []string{tc.secret})
		if strings.Contains(got, tc.leak) {
			t.Fatalf("review regression: %q", got)
		}
	}
	for _, body := range []string{
		`{"proxy_authorization":"Digest private-proxy-value","message":"failed Digest private-proxy-value"}`,
		`{"set_cookie":"private-cookie-value","message":"failed private-cookie-value"}`,
		`{"account_id":123456789,"message":"failed for 123456789"}`,
		`{"account":{"id":"nested-private","name":"private-person"},"message":"failed nested-private private-person"}`,
		`{"full_name":"private-person","message":"failed private-person"}`,
	} {
		got := diagnosticSummary([]byte(body), nil)
		if strings.Contains(got, "private") || strings.Contains(got, "123456789") {
			t.Fatalf("sensitive sibling leak: %q", got)
		}
	}
	secrets := diagnosticSecrets(credential{}, nil, "", "http://host-user:host-pass@127.0.0.1:1", "http://cfg-user:cfg-pass@127.0.0.1:2")
	if got := redactDiagnosticText("host-user host-pass cfg-user cfg-pass", secrets); strings.Contains(got, "host-") || strings.Contains(got, "cfg-") {
		t.Fatalf("proxy leak: %q", got)
	}
}

func TestDiagnosticProxyBasicCombinedMaterial(t *testing.T) {
	for _, tc := range []struct{ name, proxy, combined string }{
		{"basic", "http://bob:proxy-p45s@unused.invalid", "bob:proxy-p45s"},
		{"escaped", "http://bob%2Btag:proxy%3Ap45s%2Bend@unused.invalid", "bob+tag:proxy:p45s+end"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, secrets := range [][]string{
				diagnosticSecrets(credential{ProxyURL: tc.proxy}, nil, ""),
				diagnosticSecrets(credential{}, nil, "", tc.proxy),
			} {
				for _, reflected := range []string{
					tc.combined,
					url.QueryEscape(tc.combined),
					base64.StdEncoding.EncodeToString([]byte(tc.combined)),
					base64.RawStdEncoding.EncodeToString([]byte(tc.combined)),
					base64.URLEncoding.EncodeToString([]byte(tc.combined)),
					base64.RawURLEncoding.EncodeToString([]byte(tc.combined)),
					hex.EncodeToString([]byte(tc.combined)),
					strings.ToUpper(hex.EncodeToString([]byte(tc.combined))),
				} {
					raw, _ := json.Marshal(map[string]any{"error": map[string]any{"message": "Upstream rejected value " + reflected}})
					want := "Upstream rejected value [redacted]"
					d := captureUpstreamErrorDiagnostic(context.Background(), &http.Response{StatusCode: 502, Body: io.NopCloser(bytes.NewReader(raw))}, secrets...)
					if d.summary != want {
						t.Fatalf("HTTP diagnostic retained full/partial Basic material: %q", d.summary)
					}
					ctx := withSSEErrorDiagnostic(context.Background(), &http.Response{StatusCode: 200}, secrets...)
					if d := captureSSEErrorDiagnostic(ctx, raw); d.summary != want {
						t.Fatalf("SSE diagnostic retained full/partial Basic material: %q", d.summary)
					}
				}
			}
		})
	}
}
