package plugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
	"github.com/megumin31/cpa-opencode-zen/internal/protocol"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// These fixtures measure CPU/allocation cost, not real upstream latency.
func BenchmarkRuntimePreparation(b *testing.B) {
	e := New(Dependencies{Logf: func(string, ...any) {}})
	cfg := defaultConfig()
	r := catalog.Route{Protocol: "openai", ModelID: "fixture", BaseURL: "https://opencode.ai/inference/v1"}
	c := credential{Key: "benchmark-fake-key"}
	raw := []byte(`{"model":"fixture","messages":[{"role":"user","content":"hello"}],"max_tokens":64}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, err := e.prepareRequest(cfg, context.Background(), c, r, protocol.FormatName("openai"), raw, "benchmark-affinity"); err != nil {
			b.Fatal(err)
		}
	}
}

type benchmarkTransport func(*http.Request) (*http.Response, error)

func (f benchmarkTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func BenchmarkRuntimeExecution(b *testing.B) {
	catalogFile := filepath.Join(b.TempDir(), "catalog.json")
	if err := os.WriteFile(catalogFile, []byte(`{}`), 0600); err != nil {
		b.Fatal(err)
	}
	client := &http.Client{Transport: benchmarkTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"providers":{"p":{"package":"@opencode/ai/providers/openai/chat","settings":{"baseURL":"https://opencode.ai/inference/v1"},"models":{"fixture":{}}}}}`
		if r.Method == http.MethodPost {
			body = nativeFixture("openai")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	factory := func(string) (*http.Client, error) { return client, nil }
	e := New(Dependencies{Logf: func(string, ...any) {}, HTTPClientFactory: factory})
	cfgRaw, _ := json.Marshal(map[string]any{"schema_version": 6, "config_yaml": []byte("catalog_file: " + catalogFile + "\n")})
	if err := e.configure(cfgRaw); err != nil {
		b.Fatal(err)
	}
	defer e.Shutdown()
	storage, _ := json.Marshal(credential{Type: providerID, Key: "benchmark-fake-key", Server: "https://opencode.ai/console"})
	raw, _ := json.Marshal(rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "opencode/p/fixture", Format: "openai", SourceFormat: "openai", Payload: []byte(clientRequest("openai")), StorageJSON: storage, Metadata: map[string]any{"caller_scope": "benchmark", "execution_session_id": "one"}}})
	if _, err := e.execute(raw, false); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.execute(raw, false); err != nil {
			b.Fatal(err)
		}
	}
}
