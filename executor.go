package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin"
)

var registry = builtin.Registry()

type rpcExecutorRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id"`
	HostCallbackID string `json:"host_callback_id"`
}

func formatName(f string) translator.Format {
	switch f {
	case "chat-completions", "openai":
		return translator.FormatOpenAI
	case "responses", "openai-response":
		return translator.FormatOpenAIResponse
	case "messages", "claude":
		return translator.FormatClaude
	case "gemini":
		return translator.FormatGemini
	}
	return translator.Format(f)
}
func execute(raw []byte, stream bool) (any, error) {
	var req rpcExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fail(400, "invalid executor request")
	}
	if stream && req.StreamID == "" {
		return nil, fail(400, "missing stream ID")
	}
	c, err := decodeCredential(req.StorageJSON, req.AuthMetadata)
	if err != nil {
		return nil, err
	}
	ctx, cancel := executionContext(req.ExecutorRequest)
	keep := false
	defer func() {
		if !keep {
			cancel()
		}
	}()
	proxy := storedHostProxy(c)
	routes, err := discover(ctx, c, proxy, false)
	if err != nil {
		return nil, err
	}
	r, ok := routes[req.Model]
	if !ok && strings.HasPrefix(req.Model, "opencode/opencode/") {
		r, ok = routes["opencode-zen/"+strings.TrimPrefix(req.Model, "opencode/opencode/")]
	}
	if !ok {
		return nil, fail(404, "model is absent from the current OpenCode Console configuration")
	}
	if r.Unsupported != "" {
		return nil, fail(400, "model package or endpoint is unsupported")
	}
	input := formatName(req.SourceFormat)
	if input == "" {
		input = formatName(req.Format)
	}
	output := formatName(req.Format)
	if output == "" {
		output = input
	}
	up := formatName(r.Protocol)
	ctx, transportCancel, err := withTransportOptions(ctx, r.Settings)
	if err != nil {
		return nil, err
	}
	executionCancel := cancel
	cancel = func() { transportCancel(); executionCancel() }
	affinity, err := sessionHeaders(c, req.ExecutorRequest)
	if err != nil {
		return nil, err
	}
	payload, target, headers, err := prepareRequest(ctx, c, r, input, req.Payload, affinity.Get("X-Session-Affinity"))
	if err != nil {
		return nil, err
	}
	for k, v := range affinity {
		headers[k] = v
	}
	if !responseConversionAllowed(up, output) {
		return nil, fail(400, "Claude-to-Gemini response conversion is not supported safely by the pinned SDK")
	}
	if up != output {
		supported := registry.HasNonStreamResponseTransformer(output, sdkUpstream(up, output))
		if stream {
			supported = registry.HasStreamResponseTransformer(output, sdkUpstream(up, output))
		}
		if !supported {
			return nil, fail(400, "requested response protocol conversion is unavailable")
		}
	}
	if err := checkRetryAfter(c); err != nil {
		return nil, err
	}
	// Test-only restriction inspects the exact serialized payload and endpoint
	// after every native/config overlay. It never rewrites protocol semantics.
	guardConfig := loadedConfig()
	if guardConfig.TestGuard != nil && guardConfig.TestGuard.StrictTextOnly && (!strictTextHeaders(headers) || (r.Settings["encoding"] != nil && r.Settings["encoding"] != "none")) {
		log.Printf("OpenCode isolated test guard: enabled=true allowed=false")
		return nil, fail(403, "isolated test guard rejected the final upstream model or endpoint")
	}
	if err := checkInferenceGuard(guardConfig, r.Protocol, target, payload); err != nil {
		return nil, err
	}
	resp, err := outbound(ctx, c, proxy, "POST", target, headers, payload, true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		rememberRetryHeaders(c, resp.StatusCode, resp.Header)
		diagnostic := logUpstreamError(ctx, resp, diagnosticSecrets(c, headers, target, proxy, loadedConfig().ProxyURL)...)
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			invalidate(c)
			_, _ = discover(ctx, c, proxy, true)
		}
		return nil, diagnostic.clientError()
	}
	ctx = withSSEErrorDiagnostic(ctx, resp, diagnosticSecrets(c, headers, target, proxy, loadedConfig().ProxyURL)...)
	// Native adapters parse the requested event stream independently of MIME labels.
	// The bounded SSE parser and protocol completion checks validate the body.
	if stream {
		state.Lock()
		if state.closing {
			state.Unlock()
			resp.Body.Close()
			return nil, fail(503, "plugin shutting down")
		}
		if _, exists := state.streams[req.StreamID]; exists {
			state.Unlock()
			resp.Body.Close()
			return nil, fail(409, "duplicate active stream ID")
		}
		state.streams[req.StreamID] = struct{}{}
		state.workers.Add(1)
		state.Unlock()
		keep = true
		go func() {
			defer state.workers.Done()
			defer cancel()
			defer resp.Body.Close()
			err := forwardStream(ctx, resp.Body, r, output, req.Payload, payload, func(p []byte) error {
				_, e := callHost("host.stream.emit", map[string]any{"stream_id": req.StreamID, "payload": p})
				return e
			})
			message := ""
			if err != nil {
				message = err.Error()
			}
			closeStream(req.StreamID, message)
		}()
		return map[string]any{"headers": responseHeaders(resp.Header, true)}, nil
	}
	defer resp.Body.Close()
	body, err := aggregateStream(ctx, resp.Body, r.Protocol, r)
	if err != nil {
		return nil, err
	}
	var param any
	if up != output {
		body, err = sdkResponseBody(r, body)
		if err != nil {
			return nil, err
		}
		body = registry.TranslateNonStream(ctx, sdkUpstream(up, output), output, req.Model, req.Payload, payload, sdkNonStreamBody(up, output, body), &param)
		if err := translationError(param); err != nil {
			return nil, err
		}
	}
	if !json.Valid(body) {
		return nil, fail(502, "upstream response could not be translated")
	}
	return pluginapi.ExecutorResponse{Payload: body, Headers: responseHeaders(resp.Header, false)}, nil
}
func prepareRequest(ctx context.Context, c credential, r route, source translator.Format, raw []byte, affinity ...string) ([]byte, string, http.Header, error) {
	if !json.Valid(raw) {
		return nil, "", nil, fail(400, "request must be valid JSON")
	}
	up := formatName(r.Protocol)
	if field, ok := r.Compatibility["reasoningField"].(string); ok && field != "reasoning_content" && field != "reasoning" && field != "reasoning_text" {
		return nil, "", nil, fail(400, "custom reasoningField is unsupported")
	}
	translated, err := translateUpstreamRequest(ctx, source, up, r.ModelID, raw)
	if err != nil {
		return nil, "", nil, err
	}
	var body map[string]any
	if json.Unmarshal(translated, &body) != nil || body == nil {
		return nil, "", nil, fail(400, "request body must be an object")
	}
	body["model"] = r.ModelID
	// SDK cross-converters do not consistently retain explicit cache keys.
	// Preserve the caller's canonical OpenAI-family field before deriving a default.
	if (source == formatName("openai") || source == formatName("openai-response")) && (r.Protocol == "openai" || r.Protocol == "openai-response") {
		var original map[string]any
		_ = json.Unmarshal(raw, &original)
		if key, exists := original["prompt_cache_key"]; exists {
			body["prompt_cache_key"] = key
		}
	}
	// Native session orchestration derives cache affinity before adapter options.
	// Use our already scoped plugin identity, preserving every explicit caller key.
	if len(affinity) > 0 && affinity[0] != "" {
		if _, explicit := body["prompt_cache_key"]; !explicit && ((r.Protocol == "openai" && r.Compatibility["supportsPromptCacheKey"] == true) || (r.Protocol == "openai-response" && r.Compatibility["supportsPromptCacheKey"] != false)) {
			body["prompt_cache_key"] = affinity[0]
		}
	}
	target := strings.TrimRight(r.BaseURL, "/")
	u, err := url.Parse(target)
	if err != nil || u.RawQuery != "" {
		return nil, "", nil, fail(400, "baseURL must not contain a query")
	}
	// The verified official 2.0.29 binary uses fetch's wildcard Accept for
	// Chat, Responses, Anthropic and Gemini, including streamed responses.
	headers := http.Header{"Content-Type": {"application/json"}, "Accept": {"*/*"}}
	if err := normalizeNativeBody(body, r); err != nil {
		return nil, "", nil, err
	}
	switch r.Protocol {
	case "openai":
		target += "/chat/completions"
		body["stream"] = true
		if r.Compatibility["supportsUsageInStreaming"] != false {
			streamOptions, _ := body["stream_options"].(map[string]any)
			body["stream_options"] = resolverMerge(streamOptions, map[string]any{"include_usage": true})
		} else {
			delete(body, "stream_options")
		}
		field := nativeChatMaxTokensField(r)
		if f, ok := r.Compatibility["maxTokensField"].(string); ok {
			if f != "max_tokens" && f != "max_completion_tokens" {
				return nil, "", nil, fail(400, "unsupported maxTokensField compatibility")
			}
			field = f
		}
		if v, ok := body["max_tokens"]; ok && field != "max_tokens" {
			if _, exists := body[field]; !exists {
				body[field] = v
			}
			delete(body, "max_tokens")
		}
		if field == "max_tokens" {
			if v, ok := body["max_completion_tokens"]; ok {
				if _, exists := body[field]; !exists {
					body[field] = v
				}
				delete(body, "max_completion_tokens")
			}
		}
		if !nativeChatSupportsStore(r) {
			delete(body, "store")
		} else if _, supplied := body["store"]; !supplied {
			body["store"] = false
		}
		if r.Compatibility["supportsPromptCacheKey"] != true {
			delete(body, "prompt_cache_key")
		}
		if _, ok := body["tools"]; !ok {
			delete(body, "tool_choice")
		}
		headers.Set("Authorization", "Bearer "+c.Key)
	case "openai-response":
		if r.Compatibility["supportsPromptCacheKey"] == false {
			delete(body, "prompt_cache_key")
		}
		target += "/responses"
		body["stream"] = true
		if _, ok := body["store"]; !ok {
			body["store"] = false
		}
		if _, exists := body["include"]; !exists {
			body["include"] = []any{"reasoning.encrypted_content"}
		}
		headers.Set("Authorization", "Bearer "+c.Key)
	case "claude":
		target += "/messages"
		body["stream"] = true
		if _, ok := body["max_tokens"]; !ok {
			body["max_tokens"] = 32000
		}
		headers.Set("x-api-key", c.Key)
		headers.Set("anthropic-version", "2023-06-01")
		if nativeModelProvider(r) == "anthropic" {
			target += "?beta=true"
		}
	case "gemini":
		if strings.ContainsAny(r.ModelID, "?#%\\") {
			return nil, "", nil, fail(400, "invalid Google model ID")
		}
		target += "/models/" + strings.TrimPrefix(r.ModelID, "models/") + ":streamGenerateContent?alt=sse"
		delete(body, "model")
		delete(body, "stream")
		if g, ok := body["generationConfig"].(map[string]any); ok {
			if thinking, ok := g["thinkingConfig"].(map[string]any); ok {
				if _, ok := thinking["includeThoughts"]; !ok {
					thinking["includeThoughts"] = true
				}
			}
		}
		headers.Set("x-goog-api-key", c.Key)
	default:
		return nil, "", nil, fail(400, "unsupported upstream protocol")
	}
	// Credential orgID scopes Console discovery only. The official key resolver
	// supplies apiKey to inference; organization headers come from route config.
	for k, v := range r.Headers {
		if forbiddenHeader(k) {
			return nil, "", nil, fail(400, "Console config attempted to override a protected transport or credential header")
		}
		if strings.ContainsAny(k+v, "\r\n") {
			return nil, "", nil, fail(400, "invalid configured header")
		}
		headers.Set(k, v)
	}
	// Configured provider/model/variant body is the final semantic overlay.
	body = resolverMerge(body, r.Body)
	if err := validateNativeBody(body, r); err != nil {
		return nil, "", nil, err
	}
	for name, values := range nativeHeaders(r, body) {
		for _, value := range values {
			if existing := headers.Get(name); existing != "" {
				headers.Set(name, uniqueHeaderTokens(existing+","+value))
			} else {
				headers.Set(name, value)
			}
		}
	}
	if body["stream"] != true && r.Protocol != "gemini" {
		return nil, "", nil, fail(400, "configured body must preserve upstream streaming")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, "", nil, fail(400, "invalid configured body")
	}
	if bytes.Contains(payload, []byte("skip_thought_signature_validator")) {
		return nil, "", nil, fail(400, "cross-provider replay requires a genuine compatible thought signature; synthetic bypass signatures are not sent")
	}
	if err := allowedURL(loadedConfig(), target); err != nil {
		return nil, "", nil, err
	}
	return payload, target, headers, nil
}

// He/VE in official OpenCode 2.0.29 select the token field using the final
// model provider (canonical ?? providerID) and endpoint, before applying the
// explicit compatibility override. The resolver's final model provider takes
// precedence over a package's settings.provider. No model-name inference.
func nativeChatMaxTokensField(r route) string {
	switch strings.ToLower(nativeModelProvider(r)) {
	case "deepseek", "moonshotai", "togetherai", "zai", "zai-coding-plan", "zhipuai", "zhipuai-coding-plan", "nvidia", "cerebras", "chutes", "cloudflare-ai-gateway", "cloudflare-workers-ai":
		return "max_tokens"
	}
	endpoint := strings.ToLower(r.BaseURL)
	for _, match := range []string{"deepseek.com", "api.moonshot.ai", "api.together.", "api.z.ai", "open.bigmodel.cn", "integrate.api.nvidia.com", "cerebras.ai", "llm.chutes.ai", "gateway.ai.cloudflare.com", "api.cloudflare.com"} {
		if strings.Contains(endpoint, match) {
			return "max_tokens"
		}
	}
	return "max_completion_tokens"
}

func nativeModelProvider(r route) string {
	if r.Canonical != "" {
		return r.Canonical
	}
	return r.ProviderID
}

// Ge/VE/Ye in official OpenCode 2.0.29 omit store for these providers and
// endpoint substrings unless compatibility explicitly overrides support. The
// caller/configured settings value is retained when supported; absent values
// default to false. This runs before the final configured body overlay.
func nativeChatSupportsStore(r route) bool {
	if support, explicit := r.Compatibility["supportsStore"].(bool); explicit {
		return support
	}
	switch strings.ToLower(nativeModelProvider(r)) {
	case "nvidia", "moonshotai", "moonshotai-cn", "togetherai", "together", "zai", "zai-coding-plan", "zhipuai", "zhipuai-coding-plan", "deepseek", "cerebras", "xai", "chutes", "cloudflare-workers-ai", "cloudflare-ai-gateway", "vercel-ai-gateway", "ant-ling", "opencode":
		return false
	}
	endpoint := strings.ToLower(r.BaseURL)
	for _, match := range []string{"generativelanguage.googleapis.com", "integrate.api.nvidia.com", "api.moonshot.", "api.together.", "api.z.ai", "open.bigmodel.cn", "deepseek.com", "cerebras.ai", "api.x.ai", "chutes.ai", "api.cloudflare.com", "gateway.ai.cloudflare.com", "ai-gateway.vercel.sh", "vercel.sh", "api.ant-ling.com", "opencode.ai"} {
		if strings.Contains(endpoint, match) {
			return false
		}
	}
	return true
}

func forbiddenHeader(k string) bool {
	k = strings.ToLower(k)
	switch k {
	case "authorization", "proxy-authorization", "x-api-key", "x-goog-api-key", "x-org-id", "x-opencode-session", "x-opencode-session-id", "x-opencode-parent-session-id", "x-opencode-client", "x-opencode-project", "x-parent-session-id", "x-session-affinity", "x-session-id", "cookie", "set-cookie", "host", "content-length", "connection", "transfer-encoding", "user-agent", strings.ToLower(internalRequestHeader):
		return true
	}
	return strings.HasPrefix(k, "x-opencode-client-")
}
func responseHeaders(in http.Header, stream bool) http.Header {
	out := http.Header{}
	for _, k := range []string{"Retry-After", "X-Request-Id", "Request-Id", "Openai-Processing-Ms"} {
		if v := in.Values(k); len(v) > 0 {
			out[k] = append([]string(nil), v...)
		}
	}
	if stream {
		out.Set("Content-Type", "text/event-stream")
	} else {
		out.Set("Content-Type", "application/json")
	}
	return out
}
func translationError(param any) error {
	if x, ok := param.(interface{ ToolInputError() error }); ok && x.ToolInputError() != nil {
		return fail(502, "upstream tool input could not be translated")
	}
	return nil
}
func streamFrame(data []byte) []byte {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil
	}
	if bytes.HasPrefix(data, []byte("data:")) || bytes.HasPrefix(data, []byte("event:")) || bytes.HasPrefix(data, []byte(":")) {
		return append(append([]byte(nil), data...), '\n', '\n')
	}
	return []byte(fmt.Sprintf("data: %s\n\n", data))
}

func responseConversionAllowed(up, out translator.Format) bool {
	return !(up == translator.FormatClaude && out == translator.FormatGemini)
}

func uniqueHeaderTokens(value string) string {
	seen := map[string]bool{}
	var result []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" && !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return strings.Join(result, ",")
}
