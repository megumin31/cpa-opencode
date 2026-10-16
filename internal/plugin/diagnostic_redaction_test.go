package plugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/megumin31/cpa-opencode-zen/internal/diagnostics"
)

func TestDiagnosticRuntimeSecretsAndHeaders(t *testing.T) {

	c := credential{Key: "runtime-private", OrgID: "org-private", Label: "label-private", ProxyURL: "http://fake-user:fake-password@127.0.0.1:1"}
	secrets := diagnosticSecrets(c, http.Header{"Authorization": {"Bearer bearer-private"}, "X-Api-Key": {"header-private"}}, "https://host.invalid?key=query-private")
	message := strings.Join([]string{"runtime-private", "org-private", "label-private", "fake-user", "fake-password", "bearer-private", "header-private", "query-private"}, " ")
	if got := diagnosticErrorText(t, message, secrets); strings.Contains(got, "private") || strings.Contains(got, "fake-") {
		t.Fatalf("runtime secret missing: %q", got)
	}

	if !strings.HasPrefix(fail(400, "invalid request").Error(), "OpenCode plugin:") {
		t.Fatal("plugin source missing")
	}
}

func TestDiagnosticRuntimeProxySecrets(t *testing.T) {

	secrets := diagnosticSecrets(credential{}, nil, "", "http://host-user:host-pass@127.0.0.1:1", "http://cfg-user:cfg-pass@127.0.0.1:2")
	if got := diagnosticErrorText(t, "host-user host-pass cfg-user cfg-pass", secrets); strings.Contains(got, "host-") || strings.Contains(got, "cfg-") {
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
					d := diagnostics.CaptureHTTP(context.Background(), &http.Response{StatusCode: 502, Body: io.NopCloser(bytes.NewReader(raw))}, secrets)
					if !strings.Contains(d.ClientError().Error(), strconv.Quote(want)) {
						t.Fatalf("HTTP diagnostic retained full/partial Basic material: %q", d.ClientError().Error())
					}
					ctx := diagnostics.WithSSE(context.Background(), &http.Response{StatusCode: 200}, secrets, nil)
					if err := diagnostics.SSEError(ctx, raw); !strings.Contains(err.Error(), strconv.Quote(want)) {
						t.Fatalf("SSE diagnostic retained full/partial Basic material: %q", err.Error())
					}
				}
			}
		})
	}
}
func diagnosticErrorText(t *testing.T, message string, secrets []string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"message": message})
	if err != nil {
		t.Fatal(err)
	}
	d := diagnostics.CaptureHTTP(context.Background(), &http.Response{StatusCode: 502, Body: io.NopCloser(bytes.NewReader(raw))}, secrets)
	return d.ClientError().Error()
}
