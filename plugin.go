package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const providerID = "opencode"
const version = "0.1.0"
const internalRequestHeader = "X-Cliproxy-Opencode-Request"

type statusError struct {
	status  int
	message string
}

func (e *statusError) Error() string   { return e.message }
func (e *statusError) StatusCode() int { return e.status }
func fail(status int, message string) error {
	return &statusError{status, "OpenCode plugin: " + message}
}

type pluginConfig struct {
	AllowedDestinations   []string            `yaml:"allowed_destinations"`
	AllowLoopbackHTTP     bool                `yaml:"allow_loopback_http"`
	CatalogFile           string              `yaml:"catalog_file"`
	ProxyURL              string              `yaml:"proxy_url"`
	RequestTimeoutSeconds int                 `yaml:"request_timeout_seconds"`
	TestGuard             *inferenceTestGuard `yaml:"isolated_test_guard"`
}

var state = struct {
	sync.RWMutex
	config     pluginConfig
	cache      map[string]*discoveryEntry
	active     map[string]*activeRequest
	closing    bool
	root       context.Context
	rootCancel context.CancelFunc
	proxies    map[string]string
	workers    sync.WaitGroup
	streams    map[string]struct{}
}{config: defaultConfig(), cache: map[string]*discoveryEntry{}, active: map[string]*activeRequest{}, root: context.Background(), proxies: map[string]string{}, streams: map[string]struct{}{}}

type activeRequest struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func defaultConfig() pluginConfig {
	return pluginConfig{AllowedDestinations: []string{"https://opencode.ai/console", "https://opencode.ai/inference"}}
}
func loadedConfig() pluginConfig { state.RLock(); defer state.RUnlock(); return state.config }
func configure(raw []byte) error {
	var req struct {
		ConfigYAML    []byte `json:"config_yaml"`
		SchemaVersion uint32 `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return fail(400, "invalid registration request")
	}
	if req.SchemaVersion < 2 {
		return fail(400, "CPA schema version 2 or newer required")
	}
	cfg := defaultConfig()
	if len(req.ConfigYAML) > 0 {
		if err := validateInferenceGuardYAML(req.ConfigYAML); err != nil {
			return err
		}
		if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err != nil {
			return fail(400, "invalid plugin YAML")
		}
	}
	if err := validateConfig(cfg); err != nil {
		return err
	}
	state.Lock()
	defer state.Unlock()
	if state.rootCancel != nil {
		state.rootCancel()
	}
	closeHTTPClients()
	state.root, state.rootCancel = context.WithCancel(context.Background())
	state.proxies = map[string]string{}
	state.config = cfg
	state.cache = map[string]*discoveryEntry{}
	state.closing = false
	// Verify opt-in activation before sending an isolated test request.
	log.Printf("OpenCode isolated test guard: configured=true enabled=%t strict_text_only=%t", cfg.TestGuard != nil, cfg.TestGuard != nil && cfg.TestGuard.StrictTextOnly)
	return nil
}
func registration() any {
	return map[string]any{
		"schema_version": pluginabi.SchemaVersion,
		"metadata": pluginapi.Metadata{Name: providerID, Version: version, Author: "Megumin", GitHubRepository: "megumin31/cpa-opencode-zen", ConfigFields: []pluginapi.ConfigField{
			{Name: "allowed_destinations", Type: pluginapi.ConfigFieldTypeArray, Default: defaultConfig().AllowedDestinations, Description: "Exact HTTPS URL prefixes allowed to receive service keys. No redirects."},
			{Name: "catalog_file", Type: pluginapi.ConfigFieldTypeString, Description: "Optional trusted models.opencode.ai catalog JSON snapshot."},
			{Name: "proxy_url", Type: pluginapi.ConfigFieldTypeString, Description: "Optional outbound HTTP(S)/SOCKS5 proxy; otherwise host proxy is used."},
			{Name: "isolated_test_guard", Type: pluginapi.ConfigFieldTypeObject, Description: "Optional isolated-test restriction: enabled:true with exact final model/endpoint allowed_requests pairs. Does not determine price. Omit for normal operation."},
		}},
		"capabilities": map[string]any{"auth_provider": true, "model_provider": true, "executor": true, "executor_model_scope": "oauth", "executor_input_formats": []string{"openai", "openai-response", "claude", "gemini"}, "executor_output_formats": []string{"openai", "openai-response", "claude", "gemini"}, "request_interceptor": true, "request_lifecycle_plugin": true},
	}
}
func handleMethod(method string, raw []byte) (any, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		if err := configure(raw); err != nil {
			return nil, err
		}
		return registration(), nil
	case "plugin.quiesce", "plugin.shutdown":
		shutdown()
		return struct{}{}, nil
	case "auth.identifier", "executor.identifier":
		return map[string]string{"identifier": providerID}, nil
	case "auth.parse":
		return parseAuth(raw)
	case "auth.login.start", "auth.login.poll":
		return nil, fail(501, "API-key auth only: import the documented service-key auth file; OAuth login is not supported")
	case "auth.refresh":
		return reloadAuth(raw)
	case "model.static":
		return pluginapi.ModelResponse{Provider: providerID, Models: []pluginapi.ModelInfo{}}, nil
	case "model.for_auth":
		return modelsForAuth(raw)
	case "executor.execute":
		return execute(raw, false)
	case "executor.execute_stream":
		return execute(raw, true)
	case "executor.count_tokens":
		return nil, fail(501, "token counting is not implemented; no estimated or fabricated count is returned")
	case "executor.http_request":
		return nil, fail(501, "arbitrary authenticated HTTP forwarding is not supported")
	case "request.intercept_before", "request.intercept_after":
		return intercept(raw)
	case "request.complete":
		var req pluginapi.RequestCompletion
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, fail(400, "invalid completion")
		}
		finishRequest(req.RequestID)
		return struct{}{}, nil
	default:
		return nil, fail(400, "unsupported plugin method")
	}
}
func intercept(raw []byte) (any, error) {
	var req pluginapi.RequestInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(req.Model, "opencode/") && !strings.HasPrefix(req.RequestedModel, "opencode/") {
		return pluginapi.RequestInterceptResponse{}, nil
	}
	if req.RequestID == "" {
		return nil, fail(400, "missing lifecycle request ID")
	}
	state.Lock()
	defer state.Unlock()
	if state.closing {
		return nil, fail(503, "plugin shutting down")
	}
	if _, ok := state.active[req.RequestID]; !ok {
		ctx, cancel := context.WithCancel(state.root)
		state.active[req.RequestID] = &activeRequest{ctx, cancel}
	}
	return pluginapi.RequestInterceptResponse{Headers: http.Header{internalRequestHeader: {req.RequestID}}}, nil
}
func executionContext(req pluginapi.ExecutorRequest) (context.Context, context.CancelFunc) {
	timeout := time.Duration(loadedConfig().RequestTimeoutSeconds) * time.Second
	state.RLock()
	active := state.active[req.Headers.Get(internalRequestHeader)]
	root := state.root
	state.RUnlock()
	if active != nil {
		if timeout > 0 {
			return context.WithTimeout(active.ctx, timeout)
		}
		return context.WithCancel(active.ctx)
	}
	if timeout > 0 {
		return context.WithTimeout(root, timeout)
	}
	return context.WithCancel(root)
}
func finishRequest(id string) {
	state.Lock()
	a := state.active[id]
	delete(state.active, id)
	state.Unlock()
	if a != nil {
		a.cancel()
	}
}
func shutdown() {
	state.Lock()
	state.closing = true
	if state.rootCancel != nil {
		state.rootCancel()
	}
	for k, a := range state.active {
		a.cancel()
		delete(state.active, k)
	}
	state.cache = map[string]*discoveryEntry{}
	streams := make([]string, 0, len(state.streams))
	for id := range state.streams {
		streams = append(streams, id)
	}
	state.Unlock()
	// The host emit callback has an independent context. Close streams before
	// joining workers so a full downstream queue cannot deadlock hot reload.
	for _, id := range streams {
		closeStream(id, "plugin shutting down")
	}
	state.workers.Wait()
	closeHTTPClients()
}

func jsonDecode(raw []byte, v any) error {
	if len(raw) == 0 {
		return fmt.Errorf("empty JSON")
	}
	return json.Unmarshal(raw, v)
}

var now = time.Now

func discoveryContext() (context.Context, context.CancelFunc) {
	state.RLock()
	root := state.root
	state.RUnlock()
	return context.WithTimeout(root, 30*time.Second)
}
func rememberHostProxy(c credential, proxy string) {
	state.Lock()
	defer state.Unlock()
	state.proxies[c.fingerprint()] = proxy
}
func storedHostProxy(c credential) string {
	state.RLock()
	defer state.RUnlock()
	return state.proxies[c.fingerprint()]
}

// Host close is not idempotent while queued chunks await delivery. Claim each
// stream close exactly once; a second close can otherwise deadlock draining.
func closeStream(id, message string) {
	state.Lock()
	_, exists := state.streams[id]
	delete(state.streams, id)
	state.Unlock()
	if exists {
		_, _ = callHost("host.stream.close", map[string]any{"stream_id": id, "error": message})
	}
}
