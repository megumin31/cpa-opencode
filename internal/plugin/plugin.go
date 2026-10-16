package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/megumin31/cpa-opencode-zen/internal/buildinfo"
	"github.com/megumin31/cpa-opencode-zen/internal/fault"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const providerID = buildinfo.ProviderID
const version = buildinfo.Version

const internalRequestHeader = "X-Cliproxy-Opencode-Request"

func fail(status int, message string) error { return fault.New(status, message) }

func registration() any {
	return map[string]any{
		"schema_version": pluginabi.SchemaVersion,
		"metadata": pluginapi.Metadata{Name: buildinfo.ProviderID, Version: buildinfo.Version, Author: "Megumin", GitHubRepository: "megumin31/cpa-opencode-zen", ConfigFields: []pluginapi.ConfigField{
			{Name: "allowed_destinations", Type: pluginapi.ConfigFieldTypeArray, Default: defaultConfig().AllowedDestinations, Description: "Exact HTTPS URL prefixes allowed to receive service keys. No redirects."},
			{Name: "catalog_file", Type: pluginapi.ConfigFieldTypeString, Description: "Optional trusted models.opencode.ai catalog JSON snapshot."},
			{Name: "proxy_url", Type: pluginapi.ConfigFieldTypeString, Description: "Optional outbound HTTP(S)/SOCKS5 proxy; otherwise host proxy is used."},
			{Name: "isolated_test_guard", Type: pluginapi.ConfigFieldTypeObject, Description: "Optional isolated-test restriction: enabled:true with exact final model/endpoint allowed_requests pairs. Does not determine price. Omit for normal operation."},
		}},
		"capabilities": map[string]any{"auth_provider": true, "model_provider": true, "executor": true, "executor_model_scope": "oauth", "executor_input_formats": []string{"openai", "openai-response", "claude", "gemini"}, "executor_output_formats": []string{"openai", "openai-response", "claude", "gemini"}, "request_interceptor": true, "request_lifecycle_plugin": true},
	}
}

// Handle implements the host RPC contract. Lifecycle operations have their own
// admission path so completion can still release requests while quiescing.
func (e *Engine) Handle(method string, raw []byte) (any, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		if err := e.configure(raw); err != nil {
			return nil, err
		}
		return registration(), nil
	case "plugin.quiesce":
		e.quiesce(false)
		return struct{}{}, nil
	case "plugin.shutdown":
		e.Shutdown()
		return struct{}{}, nil
	case "auth.identifier", "executor.identifier":
		return map[string]string{"identifier": buildinfo.ProviderID}, nil
	case "auth.parse":
		return e.parseAuth(raw)
	case "auth.login.start", "auth.login.poll":
		return nil, fail(501, "API-key auth only: import the documented service-key auth file; OAuth login is not supported")
	case "auth.refresh":
		return e.reloadAuth(raw)
	case "model.static":
		return pluginapi.ModelResponse{Provider: buildinfo.ProviderID, Models: []pluginapi.ModelInfo{}}, nil
	case "model.for_auth":
		return e.modelsForAuth(raw)
	case "executor.execute":
		return e.execute(raw, false)
	case "executor.execute_stream":
		return e.execute(raw, true)
	case "executor.count_tokens":
		return nil, fail(501, "token counting is not implemented; no estimated or fabricated count is returned")
	case "executor.http_request":
		return nil, fail(501, "arbitrary authenticated HTTP forwarding is not supported")
	case "request.intercept_before", "request.intercept_after":
		return e.intercept(raw)
	case "request.complete":
		var req pluginapi.RequestCompletion
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, fail(400, "invalid completion")
		}
		e.finishRequest(req.RequestID)
		return struct{}{}, nil
	default:
		return nil, fail(400, "unsupported plugin method")
	}
}

func (e *Engine) intercept(raw []byte) (any, error) {
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
	g, done, err := e.begin()
	if err != nil {
		return nil, err
	}
	defer done()
	g.Lock()
	defer g.Unlock()
	if g.closing {
		return nil, fail(503, "plugin shutting down")
	}
	if _, ok := g.active[req.RequestID]; !ok {
		ctx, cancel := context.WithCancel(g.root)
		g.active[req.RequestID] = &activeRequest{ctx, cancel}
	}
	return pluginapi.RequestInterceptResponse{Headers: http.Header{internalRequestHeader: {req.RequestID}}}, nil
}

func (e *Engine) finishRequest(id string) {
	e.mu.Lock()
	g := e.current
	e.mu.Unlock()
	if g == nil {
		return
	}
	g.Lock()
	a := g.active[id]
	delete(g.active, id)
	g.Unlock()
	if a != nil {
		a.cancel()
	}
}

// A stream belongs to the exact generation that admitted it. A late worker from
// an old generation can never close a new stream reusing the same external ID.
func (e *Engine) closeStream(g *generation, id, message string) {
	g.Lock()
	_, exists := g.streams[id]
	delete(g.streams, id)
	g.Unlock()
	if exists && e.streams != nil {
		_ = e.streams.Close(id, message)
	}
}
