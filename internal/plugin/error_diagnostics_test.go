package plugin

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/megumin31/cpa-opencode-zen/internal/fault"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestExecuteErrorDiagnosticsPreserveRecheckAndCooldown(t *testing.T) {
	engine := newTestEngine(t)

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
			engine.testConfig(t, server.URL)
			var output bytes.Buffer
			previous := engine.logf
			engine.logf = log.New(&output, "", 0).Printf
			defer func() { engine.logf = previous }()
			c := testCredential(server.URL)
			req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "opencode/p/m", Format: "openai", SourceFormat: "openai", Payload: []byte(clientRequest("openai")), StorageJSON: rawJSON(t, c)}}
			_, err := engine.execute(rawJSON(t, req), false)
			if err == nil || err.(*fault.Error).Status != status || inference.Load() != 1 {
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
				_, second := engine.execute(rawJSON(t, req), false)
				if second == nil || second.(*fault.Error).Status != status || inference.Load() != 1 {
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
	engine := newTestEngine(t)

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
	engine.testConfig(t, server.URL)
	var output bytes.Buffer
	previous := engine.logf
	engine.logf = log.New(&output, "", 0).Printf
	defer func() { engine.logf = previous }()
	req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "opencode/p/m", Format: "openai", SourceFormat: "openai", Payload: []byte(clientRequest("openai")), StorageJSON: rawJSON(t, testCredential(server.URL))}}
	if _, err := engine.execute(rawJSON(t, req), false); err != nil {
		t.Fatal(err)
	}
	if inference.Load() != 1 || strings.Contains(output.String(), "OpenCode inference error:") {
		t.Fatal("successful response must not produce an error diagnostic or extra inference")
	}
}
