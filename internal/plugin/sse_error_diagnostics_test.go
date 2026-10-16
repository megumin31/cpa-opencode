package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
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

func TestExecuteHTTP200SSEErrorDoesNotRecheckRetryOrCooldown(t *testing.T) {
	engine := newTestEngine(t)

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
		_, _ = io.WriteString(w, "data: "+string(raw)+"\n\n")
	}))
	defer server.Close()
	engine.testConfig(t, server.URL)
	var output bytes.Buffer
	previous := engine.logf
	engine.logf = log.New(&output, "", 0).Printf
	defer func() { engine.logf = previous }()
	req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "opencode/p/m", Format: "openai-response", SourceFormat: "openai-response", Payload: []byte(clientRequest("openai-response")), StorageJSON: rawJSON(t, testCredential(server.URL))}}
	for attempt := 1; attempt <= 2; attempt++ {
		result, err := engine.execute(rawJSON(t, req), false)
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
	var status *fault.Error
	if !errors.As(err, &status) || status.Status != http.StatusBadGateway || !strings.Contains(err.Error(), "source=upstream_sse") || !strings.Contains(err.Error(), "http_status=200") {
		t.Fatalf("SSE error must be a distinct 502 with the actual transport status: %v", err)
	}
}
