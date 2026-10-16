package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func testConfig(t *testing.T, target string) {
	t.Helper()
	catalog := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(catalog, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := "allow_loopback_http: true\nrequest_timeout_seconds: 5\ncatalog_file: " + catalog + "\nallowed_destinations:\n  - " + target + "\n"
	raw, _ := json.Marshal(map[string]any{"schema_version": 6, "config_yaml": []byte(cfg)})
	if err := configure(raw); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(shutdown)
}
func testCredential(server string) credential {
	return credential{Type: providerID, Key: "test-service-key", Server: server + "/console", OrgID: "test-org"}
}
func rawJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestCredentialDestinationBoundary(t *testing.T) {
	cfg := defaultConfig()
	for _, target := range []string{"https://opencode.ai/inference/v1", "https://opencode.ai/console/api/v2/config"} {
		if err := allowedURL(cfg, target); err != nil {
			t.Fatalf("%s: %v", target, err)
		}
	}
	for _, target := range []string{"http://opencode.ai/inference/v1", "https://opencode.ai.evil.example/inference", "https://opencode.ai@evil.example/inference", "https://opencode.ai:444/inference", "https://opencode.ai/inference-evil", "https://opencode.ai/inference/../secret", "https://opencode.ai/inference/%2e%2e/secret", "https://evil.example/inference", "https://127.0.0.1/inference"} {
		if err := allowedURL(cfg, target); err == nil {
			t.Fatalf("unsafe URL accepted: %s", target)
		}
	}
}

func TestConfiguredDestinationListReplacesDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		wantOpenAI bool
	}{
		{"default", "", true},
		{"explicit_narrow", "allowed_destinations:\n  - https://opencode.ai/console\n  - https://opencode.ai/inference/v1\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := configure(rawJSON(t, map[string]any{"schema_version": 6, "config_yaml": []byte(tc.yaml)})); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(shutdown)
			cfg := loadedConfig()
			if len(cfg.AllowedDestinations) != 2 {
				t.Fatal("configured list merged with defaults")
			}
			for _, target := range []string{"https://opencode.ai/console/api/v2/config", "https://opencode.ai/inference/v1/chat/completions"} {
				if err := allowedURL(cfg, target); err != nil {
					t.Fatalf("expected allowed endpoint %s: %v", target, err)
				}
			}
			target := "https://opencode.ai/inference/openai/v1"
			if allowed := allowedURL(cfg, target) == nil; allowed != tc.wantOpenAI {
				t.Fatalf("protocol-specific endpoint allowed=%v want=%v", allowed, tc.wantOpenAI)
			}
			routes := resolverTestRoutes(t, `{"providers":{"p":{"package":"aisdk:@ai-sdk/openai-compatible","settings":{"baseURL":"`+target+`"},"models":{"m":{}}}}}`, `{}`)
			if exposed := len(modelInfos(routes)) == 1; exposed != tc.wantOpenAI {
				t.Fatalf("model exposure ignored explicit restriction: exposed=%v", exposed)
			}
			for _, target := range []string{"https://opencode.ai/inference-other/v1", "https://opencode.ai/zen/v1", "https://different.example/inference/v1"} {
				if allowedURL(cfg, target) == nil {
					t.Fatalf("unexpectedly broadened credential destination: %s", target)
				}
			}
		})
	}
}
func TestExampleDestinationRulesMatchDefaults(t *testing.T) {
	// Keep shipped guidance aligned without parsing or opening any user config.
	b, err := os.ReadFile("examples/plugin-config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range defaultConfig().AllowedDestinations {
		if !strings.Contains(string(b), "- "+prefix+"\n") {
			t.Fatal("example does not retain a default credential prefix")
		}
	}
}
func TestTransportRefusesRedirectWithoutCredentialLeak(t *testing.T) {
	var received atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); t.Errorf("redirect followed") }))
	defer evil.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-service-key" {
			t.Error("missing fake key")
		}
		w.Header().Set("Location", evil.URL)
		w.WriteHeader(307)
	}))
	defer server.Close()
	testConfig(t, server.URL)
	_, err := outbound(context.Background(), testCredential(server.URL), "", "POST", server.URL+"/inference", http.Header{"X-Api-Key": {"test-service-key"}}, nil, true)
	if err == nil || !strings.Contains(err.Error(), "redirect refused") {
		t.Fatalf("err=%v", err)
	}
	if received.Load() != 0 {
		t.Fatal("secret leaked")
	}
}
func TestTransportCancellationAndErrorRedaction(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(canceled) }))
	defer server.Close()
	testConfig(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := outbound(ctx, testCredential(server.URL), "", "GET", server.URL+"/inference", http.Header{"Authorization": {"Bearer test-service-key"}}, nil, true)
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil || strings.Contains(err.Error(), "test-service-key") {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation hung")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("upstream context not canceled")
	}
}
func TestAuthParsingAPIKeyOnlyAndUnsupportedMethods(t *testing.T) {
	testConfig(t, "https://opencode.ai")
	c := credential{Type: providerID, Key: "test-service-key", OrgID: "explicit-org"}
	p, err := parseAuth(rawJSON(t, pluginapi.AuthParseRequest{RawJSON: rawJSON(t, c), FileName: "account.json"}))
	if err != nil {
		t.Fatal(err)
	}
	parsed := p.(pluginapi.AuthParseResponse)
	if !parsed.Handled || parsed.Auth.Provider != providerID {
		t.Fatal("not handled")
	}
	decoded, err := decodeCredential(parsed.Auth.StorageJSON, nil)
	if err != nil || decoded.OrgID != "explicit-org" {
		t.Fatal(err)
	}
	for _, method := range []string{"auth.login.start", "auth.login.poll", "executor.count_tokens", "executor.http_request"} {
		_, err := handleMethod(method, []byte(`{}`))
		if err == nil || err.(*statusError).status != 501 {
			t.Fatalf("%s err=%v", method, err)
		}
	}
	_, err = decodeCredential([]byte(`{"type":"opencode","key":"test\nkey"}`), nil)
	if err == nil {
		t.Fatal("header injection accepted")
	}
}
func TestDiscoveryIsolationSingleflightAndAuthInvalidation(t *testing.T) {
	var calls atomic.Int32
	var unauthorized atomic.Bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if unauthorized.Load() {
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("Authorization") == "" || r.Header.Get("x-org-id") != "test-org" {
			t.Error("discovery key or org absent")
		}
		io.WriteString(w, `{"providers":{"p":{"package":"@opencode/ai/providers/openai/chat","settings":{"baseURL":"`+server.URL+`/inference"},"models":{"m":{}}}}}`)
	}))
	defer server.Close()
	testConfig(t, server.URL)
	c := testCredential(server.URL)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			routes, err := discover(context.Background(), c, "", false)
			if err != nil || len(routes) != 1 {
				t.Errorf("discovery %v routes=%d", err, len(routes))
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("singleflight calls=%d", calls.Load())
	}
	other := c
	other.Key = "other-test-key"
	_, _ = discover(context.Background(), other, "", false)
	if calls.Load() != 2 {
		t.Fatal("credentials shared cache")
	}
	unauthorized.Store(true)
	_, err := discover(context.Background(), c, "", true)
	if err == nil || err.(*statusError).status != 401 {
		t.Fatal("unauthorized stale cache accepted")
	}
	_, err = discover(context.Background(), c, "", false)
	if err == nil {
		t.Fatal("unauthorized cache resurrected")
	}
}
func TestDiscoveryCanceledWaiter(t *testing.T) {
	testConfig(t, "https://opencode.ai")
	c := credential{Type: providerID, Key: "test-service-key", Server: "https://opencode.ai/console"}
	entry := &discoveryEntry{gate: make(chan struct{}, 1)}
	entry.gate <- struct{}{}
	state.Lock()
	state.cache[c.fingerprint()+"\x00"] = entry
	state.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := discover(ctx, c, "", false)
	if err == nil || err.(*statusError).status != 499 {
		t.Fatalf("err=%v", err)
	}
}
func TestLifecycleCancelAndShutdownJoin(t *testing.T) {
	testConfig(t, "https://opencode.ai")
	req := pluginapi.RequestInterceptRequest{RequestID: "lifecycle-test", Model: "opencode/p/m"}
	_, err := intercept(rawJSON(t, req))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := executionContext(pluginapi.ExecutorRequest{Headers: http.Header{internalRequestHeader: {req.RequestID}}})
	defer cancel()
	finishRequest(req.RequestID)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("lifecycle cancel failed")
	}
	state.Lock()
	state.workers.Add(1)
	root := state.root
	state.Unlock()
	finished := make(chan struct{})
	go func() { <-root.Done(); close(finished); state.workers.Done() }()
	shutdown()
	select {
	case <-finished:
	default:
		t.Fatal("shutdown returned before detached worker")
	}
}
