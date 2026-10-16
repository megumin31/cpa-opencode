package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Exact He conditions from the official 2.0.29 static OpenAIChat chunk.
func TestNativeChatMaxTokensProviderDefaults(t *testing.T) {
	for _, provider := range []string{"deepseek", "moonshotai", "togetherai", "zai", "zai-coding-plan", "zhipuai", "zhipuai-coding-plan", "nvidia", "cerebras", "chutes", "cloudflare-ai-gateway", "cloudflare-workers-ai"} {
		t.Run(provider, func(t *testing.T) {
			for _, value := range []string{provider, strings.ToUpper(provider)} {
				r := route{ProviderID: value, BaseURL: "https://proxy.example/v1", ModelID: "unrelated-model"}
				if got := nativeChatMaxTokensField(r); got != "max_tokens" {
					t.Fatalf("provider %q: %s", value, got)
				}
			}
		})
	}
	// Nearby names deliberately differ between native He and the other
	// compatibility defaults. Do not broaden this list from those helpers.
	for _, provider := range []string{"openai", "opencode", "moonshotai-cn", "together", "nvidia-alias", "custom"} {
		r := route{ProviderID: provider, BaseURL: "https://proxy.example/v1", ModelID: "deepseek-model"}
		if got := nativeChatMaxTokensField(r); got != "max_completion_tokens" {
			t.Fatalf("provider %q inferred an unsupported exception: %s", provider, got)
		}
	}
}

func TestNativeChatMaxTokensEndpointDefaults(t *testing.T) {
	for _, host := range []string{"api.deepseek.com", "api.moonshot.ai", "api.together.xyz", "api.z.ai", "open.bigmodel.cn", "integrate.api.nvidia.com", "inference.cerebras.ai", "llm.chutes.ai", "gateway.ai.cloudflare.com", "api.cloudflare.com"} {
		t.Run(host, func(t *testing.T) {
			for _, value := range []string{host, strings.ToUpper(host)} {
				r := route{ProviderID: "custom", BaseURL: "https://" + value + "/v1"}
				if got := nativeChatMaxTokensField(r); got != "max_tokens" {
					t.Fatalf("endpoint %q: %s", r.BaseURL, got)
				}
			}
		})
	}
	for _, host := range []string{"opencode.ai", "api.moonshot.cn", "chutes.ai", "nvidia.com", "example.com"} {
		r := route{ProviderID: "custom", BaseURL: "https://" + host + "/v1"}
		if got := nativeChatMaxTokensField(r); got != "max_completion_tokens" {
			t.Fatalf("endpoint %q inferred an unsupported exception: %s", r.BaseURL, got)
		}
	}
}

func TestNativeChatMaxTokensUsesResolvedCanonicalProvider(t *testing.T) {
	catalog := `{"nvidia":{"package":"@opencode/ai/providers/openai-compatible","settings":{"baseURL":"https://proxy.example/v1"},"models":{"m":{}}},"inherited":{"canonical":"nvidia","package":"@opencode/ai/providers/openai-compatible","settings":{"baseURL":"https://proxy.example/v1"},"models":{"m":{}}}}`
	config := `{"providers":{"alias":{"canonical":"nvidia","models":{"m":{}}},"inherited":{"models":{"m":{}}}}}`
	routes := resolverTestRoutes(t, config, catalog)
	for _, id := range []string{"alias", "inherited"} {
		r := routes["opencode/"+id+"/m"]
		if r.ProviderID != id || r.Canonical != "nvidia" || r.Unsupported != "" {
			t.Fatalf("canonical identity lost: %+v", r)
		}
		if got := nativeChatMaxTokensField(r); got != "max_tokens" {
			t.Fatalf("canonical provider ignored: %s", got)
		}
	}
	// Native ModelResolver updates model.provider after package construction;
	// package settings.provider does not override the final canonical identity.
	r := route{ProviderID: "nvidia", Canonical: "custom", Settings: map[string]any{"provider": "nvidia"}, BaseURL: "https://proxy.example/v1"}
	if got := nativeChatMaxTokensField(r); got != "max_completion_tokens" {
		t.Fatalf("canonical provider did not win: %s", got)
	}
}

func TestPrepareNativeChatMaxTokensDefaultsAndOverrides(t *testing.T) {
	testConfig(t, "https://proxy.example")
	for _, tc := range []struct {
		name, provider, canonical, override, supplied, expected string
	}{
		{"provider-default", "nvidia", "", "", "max_completion_tokens", "max_tokens"},
		{"canonical-default", "alias", "nvidia", "", "max_completion_tokens", "max_tokens"},
		{"ordinary-default", "custom", "", "", "max_tokens", "max_completion_tokens"},
		{"override-completion", "nvidia", "", "max_completion_tokens", "max_tokens", "max_completion_tokens"},
		{"override-legacy", "custom", "", "max_tokens", "max_completion_tokens", "max_tokens"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := route{ProviderID: tc.provider, Canonical: tc.canonical, ModelID: "m", Package: "@opencode/ai/providers/openai-compatible", Protocol: "openai", BaseURL: "https://proxy.example/v1", Compatibility: map[string]any{}}
			if tc.override != "" {
				r.Compatibility["maxTokensField"] = tc.override
			}
			input := []byte(`{"messages":[{"role":"user","content":"test"}],"` + tc.supplied + `":23}`)
			raw, _, _, err := prepareRequest(context.Background(), credential{Key: "test-service-key"}, r, formatName("openai"), input)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			if body[tc.expected] != float64(23) {
				t.Fatalf("token limit not preserved in %s: %s", tc.expected, raw)
			}
			other := "max_tokens"
			if tc.expected == other {
				other = "max_completion_tokens"
			}
			if _, exists := body[other]; exists {
				t.Fatalf("conflicting token field retained: %s", raw)
			}
		})
	}
}

func TestNativeChatStoreProviderDefaults(t *testing.T) {
	for _, provider := range []string{"nvidia", "moonshotai", "moonshotai-cn", "togetherai", "together", "zai", "zai-coding-plan", "zhipuai", "zhipuai-coding-plan", "deepseek", "cerebras", "xai", "chutes", "cloudflare-workers-ai", "cloudflare-ai-gateway", "vercel-ai-gateway", "ant-ling", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			for _, value := range []string{provider, strings.ToUpper(provider)} {
				if nativeChatSupportsStore(route{ProviderID: value, BaseURL: "https://proxy.example/v1"}) {
					t.Fatalf("store unexpectedly supported for %q", value)
				}
			}
		})
	}
	for _, provider := range []string{"openai", "custom", "nvidia-alias", "opencode-go"} {
		if !nativeChatSupportsStore(route{ProviderID: provider, BaseURL: "https://proxy.example/v1"}) {
			t.Fatalf("store support unexpectedly inferred for %q", provider)
		}
	}
	if nativeChatSupportsStore(route{ProviderID: "alias", Canonical: "nvidia", BaseURL: "https://proxy.example/v1"}) {
		t.Fatal("canonical identity ignored")
	}
	if !nativeChatSupportsStore(route{ProviderID: "nvidia", Canonical: "custom", Settings: map[string]any{"provider": "nvidia"}, BaseURL: "https://proxy.example/v1"}) {
		t.Fatal("canonical identity did not override package settings.provider")
	}
}

func TestNativeChatStoreEndpointDefaults(t *testing.T) {
	for _, host := range []string{"generativelanguage.googleapis.com", "integrate.api.nvidia.com", "api.moonshot.cn", "api.together.xyz", "api.z.ai", "open.bigmodel.cn", "api.deepseek.com", "inference.cerebras.ai", "api.x.ai", "chutes.ai", "api.cloudflare.com", "gateway.ai.cloudflare.com", "ai-gateway.vercel.sh", "test.vercel.sh", "api.ant-ling.com", "opencode.ai"} {
		t.Run(host, func(t *testing.T) {
			for _, value := range []string{host, strings.ToUpper(host)} {
				if nativeChatSupportsStore(route{ProviderID: "custom", BaseURL: "https://" + value + "/v1"}) {
					t.Fatalf("store unexpectedly supported for %q", value)
				}
			}
		})
	}
	for _, host := range []string{"api.openai.com", "custom.example", "moonshot.example", "nvidia.com", "opencode.example"} {
		if !nativeChatSupportsStore(route{ProviderID: "custom", BaseURL: "https://" + host + "/v1"}) {
			t.Fatalf("store support unexpectedly inferred for %q", host)
		}
	}
}

func TestPrepareNativeChatStoreDefaultsAndOverlays(t *testing.T) {
	testConfig(t, "https://proxy.example")
	for _, tc := range []struct {
		name, provider                    string
		caller, setting, support, overlay any
		want                              any
		present                           bool
	}{
		{"compatible-default-false", "custom", nil, nil, nil, nil, false, true},
		{"caller-true-preserved", "custom", true, nil, nil, nil, true, true},
		{"caller-false-preserved", "custom", false, nil, nil, nil, false, true},
		{"settings-overrides-caller", "custom", true, false, nil, nil, false, true},
		{"settings-true-preserved", "custom", false, true, nil, nil, true, true},
		{"unsupported-default-omitted", "nvidia", nil, nil, nil, nil, nil, false},
		{"unsupported-caller-stripped", "nvidia", true, nil, nil, nil, nil, false},
		{"unsupported-setting-stripped", "nvidia", false, true, nil, nil, nil, false},
		{"explicit-support-defaults-false", "nvidia", nil, nil, true, nil, false, true},
		{"explicit-support-keeps-true", "nvidia", true, nil, true, nil, true, true},
		{"explicit-no-support-strips-true", "custom", true, nil, false, nil, nil, false},
		{"final-body-overrides-unsupported", "nvidia", nil, nil, nil, true, true, true},
		{"final-body-overrides-explicit-no-support", "custom", nil, nil, false, true, true, true},
		{"final-body-overrides-setting", "custom", true, true, nil, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := route{ProviderID: tc.provider, ModelID: "m", Package: "@opencode/ai/providers/openai-compatible", Protocol: "openai", BaseURL: "https://proxy.example/v1", Settings: map[string]any{}, Compatibility: map[string]any{}, Body: map[string]any{}}
			body := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "test"}}}
			if tc.caller != nil {
				body["store"] = tc.caller
			}
			if tc.setting != nil {
				r.Settings["store"] = tc.setting
			}
			if tc.support != nil {
				r.Compatibility["supportsStore"] = tc.support
			}
			if tc.overlay != nil {
				r.Body["store"] = tc.overlay
			}
			raw, _, _, err := prepareRequest(context.Background(), credential{Key: "test-service-key"}, r, formatName("openai"), rawJSON(t, body))
			if err != nil {
				t.Fatal(err)
			}
			body = nil
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			value, present := body["store"]
			if present != tc.present || value != tc.want {
				t.Fatalf("store=(%v,%v), want=(%v,%v), body=%s", value, present, tc.want, tc.present, raw)
			}
		})
	}
}

func TestPrepareAnthropicBetaUsesFinalModelProvider(t *testing.T) {
	testConfig(t, "https://proxy.example")
	for _, tc := range []struct {
		name, provider, canonical, pkg, setting string
		beta                                    bool
	}{
		{"native-anthropic", "anthropic", "", "anthropic", "", true},
		{"native-custom", "custom", "", "anthropic", "", false},
		{"compatible-canonical-anthropic", "alias", "anthropic", "anthropic-compatible", "", true},
		{"native-canonical-custom", "anthropic", "custom", "anthropic", "", false},
		{"settings-cannot-add-beta", "custom", "", "anthropic-compatible", "anthropic", false},
		{"settings-cannot-remove-beta", "alias", "anthropic", "anthropic-compatible", "custom", true},
		{"provider-comparison-exact", "ANTHROPIC", "", "anthropic", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := route{ProviderID: tc.provider, Canonical: tc.canonical, ModelID: "m", Package: "@opencode/ai/providers/" + tc.pkg, Protocol: "claude", BaseURL: "https://proxy.example/v1", Settings: map[string]any{}}
			if tc.setting != "" {
				r.Settings["provider"] = tc.setting
			}
			_, target, _, err := prepareRequest(context.Background(), credential{Key: "test-service-key"}, r, formatName("claude"), []byte(`{"messages":[{"role":"user","content":"test"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			want := "https://proxy.example/v1/messages"
			if tc.beta {
				want += "?beta=true"
			}
			if target != want {
				t.Fatalf("endpoint=%q, want=%q", target, want)
			}
		})
	}
}
