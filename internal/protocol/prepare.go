// Package protocol builds credential-free provider requests and converts bounded
// native event streams. Runtime policy, URL authorization, credentials, and
// transport ownership remain with the caller.
package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
	"github.com/megumin31/cpa-opencode-zen/internal/fault"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin"
)

// Options contains only request-local protocol decisions. StrictTextOnly is the
// effective strict test mode; a nil Logf deliberately discards adapter notices.
type Options struct {
	Affinity       string
	StrictTextOnly bool
	Logf           func(string, ...any)
}

// registry is initialized once and only read by protocol conversion operations.
var registry = builtin.Registry()

const maxResponseBytes = 32 << 20

// FormatName canonicalizes supported host and provider protocol aliases.
func FormatName(f string) translator.Format {
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

// Prepare creates the final body, endpoint, and credential-free headers.
// The caller validates transport settings and authorizes the final endpoint.
func Prepare(ctx context.Context, r catalog.Route, source translator.Format, raw []byte, options Options) ([]byte, string, http.Header, error) {
	if !json.Valid(raw) {
		return nil, "", nil, fault.New(400, "request must be valid JSON")
	}
	up := FormatName(r.Protocol)
	if field, ok := r.Compatibility["reasoningField"].(string); ok && field != "reasoning_content" && field != "reasoning" && field != "reasoning_text" {
		return nil, "", nil, fault.New(400, "custom reasoningField is unsupported")
	}
	translated, err := translateUpstreamRequest(ctx, source, up, r.ModelID, raw)
	if err != nil {
		return nil, "", nil, err
	}
	var body map[string]any
	if json.Unmarshal(translated, &body) != nil || body == nil {
		return nil, "", nil, fault.New(400, "request body must be an object")
	}
	body["model"] = r.ModelID
	// SDK cross-converters do not consistently retain explicit cache keys.
	// Preserve the caller's canonical OpenAI-family field before deriving a default.
	if (source == FormatName("openai") || source == FormatName("openai-response")) && (r.Protocol == "openai" || r.Protocol == "openai-response") {
		var original map[string]any
		_ = json.Unmarshal(raw, &original)
		if key, exists := original["prompt_cache_key"]; exists {
			body["prompt_cache_key"] = key
		}
	}
	// Native session orchestration derives cache affinity before adapter options.
	// Use our already scoped plugin identity, preserving every explicit caller key.
	if options.Affinity != "" {
		if _, explicit := body["prompt_cache_key"]; !explicit && ((r.Protocol == "openai" && r.Compatibility["supportsPromptCacheKey"] == true) || (r.Protocol == "openai-response" && r.Compatibility["supportsPromptCacheKey"] != false)) {
			body["prompt_cache_key"] = options.Affinity
		}
	}
	target := strings.TrimRight(r.BaseURL, "/")
	u, err := url.Parse(target)
	if err != nil || u.RawQuery != "" {
		return nil, "", nil, fault.New(400, "baseURL must not contain a query")
	}
	// The verified official 2.0.29 binary uses fetch's wildcard Accept for
	// Chat, Responses, Anthropic and Gemini, including streamed responses.
	headers := http.Header{"Content-Type": {"application/json"}, "Accept": {"*/*"}}
	if err := normalizeNativeBody(body, r, options); err != nil {
		return nil, "", nil, err
	}
	switch r.Protocol {
	case "openai":
		target += "/chat/completions"
		body["stream"] = true
		if r.Compatibility["supportsUsageInStreaming"] != false {
			streamOptions, _ := body["stream_options"].(map[string]any)
			body["stream_options"] = catalog.Merge(streamOptions, map[string]any{"include_usage": true})
		} else {
			delete(body, "stream_options")
		}
		field := nativeChatMaxTokensField(r)
		if f, ok := r.Compatibility["maxTokensField"].(string); ok {
			if f != "max_tokens" && f != "max_completion_tokens" {
				return nil, "", nil, fault.New(400, "unsupported maxTokensField compatibility")
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
	case "claude":
		target += "/messages"
		body["stream"] = true
		if _, ok := body["max_tokens"]; !ok {
			body["max_tokens"] = 32000
		}
		headers.Set("anthropic-version", "2023-06-01")
		if nativeModelProvider(r) == "anthropic" {
			target += "?beta=true"
		}
	case "gemini":
		if strings.ContainsAny(r.ModelID, "?#%\\") {
			return nil, "", nil, fault.New(400, "invalid Google model ID")
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
	default:
		return nil, "", nil, fault.New(400, "unsupported upstream protocol")
	}
	// Credential orgID scopes Console discovery only. The official key resolver
	// supplies apiKey to inference; organization headers come from route config.
	for k, v := range r.Headers {
		if forbiddenHeader(k) {
			return nil, "", nil, fault.New(400, "Console config attempted to override a protected transport or credential header")
		}
		if strings.ContainsAny(k+v, "\r\n") {
			return nil, "", nil, fault.New(400, "invalid configured header")
		}
		headers.Set(k, v)
	}
	// Configured provider/model/variant body is the final semantic overlay.
	body = catalog.Merge(body, r.Body)
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
		return nil, "", nil, fault.New(400, "configured body must preserve upstream streaming")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, "", nil, fault.New(400, "invalid configured body")
	}
	if bytes.Contains(payload, []byte("skip_thought_signature_validator")) {
		return nil, "", nil, fault.New(400, "cross-provider replay requires a genuine compatible thought signature; synthetic bypass signatures are not sent")
	}
	return payload, target, headers, nil
}

// He/VE in official OpenCode 2.0.29 select the token field using the final
// model provider (canonical ?? providerID) and endpoint, before applying the
// explicit compatibility override. The resolver's final model provider takes
// precedence over a package's settings.provider. No model-name inference.
func nativeChatMaxTokensField(r catalog.Route) string {
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

func nativeModelProvider(r catalog.Route) string {
	if r.Canonical != "" {
		return r.Canonical
	}
	return r.ProviderID
}

// Ge/VE/Ye in official OpenCode 2.0.29 omit store for these providers and
// endpoint substrings unless compatibility explicitly overrides support. The
// caller/configured settings value is retained when supported; absent values
// default to false. This runs before the final configured body overlay.
func nativeChatSupportsStore(r catalog.Route) bool {
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
	case "x-cpa-gateway-token", "authorization", "proxy-authorization", "x-api-key", "x-goog-api-key", "x-org-id", "x-opencode-session", "x-opencode-session-id", "x-opencode-parent-session-id", "x-opencode-client", "x-opencode-project", "x-parent-session-id", "x-session-affinity", "x-session-id", "cookie", "set-cookie", "host", "content-length", "connection", "transfer-encoding", "user-agent", "x-cliproxy-opencode-request":
		return true
	}
	return strings.HasPrefix(k, "x-opencode-client-")
}
func translationError(param any) error {
	if x, ok := param.(interface{ ToolInputError() error }); ok && x.ToolInputError() != nil {
		return fault.New(502, "upstream tool input could not be translated")
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

// ConversionAllowed rejects SDK conversions that cannot preserve required semantics.
func ConversionAllowed(up, out translator.Format) bool {
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

// ConversionSupported checks the pinned SDK's response converter without exposing
// its registry or allowing callers to change process-wide conversion behavior.
func ConversionSupported(up, out translator.Format, stream bool) bool {
	if up == out {
		return true
	}
	if !ConversionAllowed(up, out) {
		return false
	}
	if stream {
		return registry.HasStreamResponseTransformer(out, sdkUpstream(up, out))
	}
	return registry.HasNonStreamResponseTransformer(out, sdkUpstream(up, out))
}

// ConvertResponse normalizes and converts one fully aggregated native response.
// The original/request pair preserves request-dependent SDK conversion state.
func ConvertResponse(ctx context.Context, r catalog.Route, output translator.Format, model string, original, request, body []byte) ([]byte, error) {
	up := FormatName(r.Protocol)
	if up != output {
		if !ConversionAllowed(up, output) {
			return nil, fault.New(400, "Claude-to-Gemini response conversion is not supported safely by the pinned SDK")
		}
		if !ConversionSupported(up, output, false) {
			return nil, fault.New(400, "requested response protocol conversion is unavailable")
		}
		var err error
		body, err = sdkResponseBody(r, body)
		if err != nil {
			return nil, err
		}
		var param any
		body = registry.TranslateNonStream(ctx, sdkUpstream(up, output), output, model, original, request, sdkNonStreamBody(up, output, body), &param)
		if err := translationError(param); err != nil {
			return nil, err
		}
	}
	if !json.Valid(body) {
		return nil, fault.New(502, "upstream response could not be translated")
	}
	return body, nil
}
