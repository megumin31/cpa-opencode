package catalog

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func resolverTestRoutes(t *testing.T, config, catalog string) map[string]Route {
	t.Helper()
	routes, err := ResolveRoutes([]byte(config), []byte(catalog))
	if err != nil {
		t.Fatal(err)
	}
	return routes
}

func TestResolverCanonicalInheritance(t *testing.T) {
	catalog := `{"source":{"id":"canonical","npm":"@ai-sdk/openai-compatible","api":"https://catalog.example/v1","settings":{"apiKey":"catalog-secret","nested":{"provider":1,"shared":1},"timeout":100},"headers":{"X-Scope":"provider","X-Keep":"yes"},"body":{"reasoning":{"effort":"low","other":1},"array":[1,2]},"models":{"irrelevant-json-key":{"id":"upstream/model","name":"Model","provider":{"npm":"@ai-sdk/anthropic","api":"https://model.example/v1"},"settings":{"authToken":"model-secret","nested":{"model":2,"shared":2},"timeout":200},"headers":{"x-scope":"model"},"body":{"reasoning":{"effort":"medium"}},"tool_call":true,"modalities":{"input":["text","image"],"output":["text"]},"limit":{"context":1000,"input":900,"output":100},"interleaved":{"field":"reasoning_content"},"variants":[{"id":"fast","settings":{"accessToken":"variant-secret","nested":{"variant":3}},"body":{"reasoning":{"effort":"high"}},"headers":{"X-Variant":"old"}}]}}}}`
	config := `{"providers":{"team/provider":{"canonical":"canonical","package":"@opencode/ai/providers/openai-compatible","settings":{"apiKey":"override-secret","baseURL":"https://console.example/proxy","nested":{"console":4},"timeout":300},"headers":{"X-SCOPE":"console"},"body":{"array":[3]},"models":{"friendly/model":{"modelID":"upstream/model","settings":{"accessToken":"secret","nested":{"shared":5},"timeout":400},"compatibility":{"maxTokensField":"max_completion_tokens"},"capabilities":{"input":["text"]},"limit":{"output":200},"variants":[{"id":"fast","settings":{"apiKey":"secret","nested":{"variant":6}},"headers":{"x-variant":"new"},"body":{"reasoning":{"additional":true}}},{"id":"slow/slash","body":{"reasoning":{"effort":"max"}}}]}}}}}`
	routes := resolverTestRoutes(t, config, catalog)
	if len(routes) != 3 {
		t.Fatalf("got %d routes", len(routes))
	}
	r := routes["opencode/team%2Fprovider/friendly%2Fmodel"]
	if r.Unsupported != "" || r.Protocol != "openai" || r.BaseURL != "https://console.example/proxy" || r.ModelID != "upstream/model" {
		t.Fatalf("Route: %+v", r)
	}
	if r.Package != "@opencode/ai/providers/openai-compatible" {
		t.Fatalf("inherited package not reset: %q", r.Package)
	}
	wantNested := map[string]any{"provider": float64(1), "model": float64(2), "shared": float64(5), "console": float64(4)}
	if !reflect.DeepEqual(r.Settings["nested"], wantNested) {
		t.Errorf("nested: %#v", r.Settings["nested"])
	}
	if r.Settings["timeout"] != float64(300) {
		t.Errorf("model overrides provider timeout: %#v", r.Settings)
	}
	for _, key := range []string{"apiKey", "authToken", "accessToken"} {
		if _, ok := r.Settings[key]; ok {
			t.Errorf("credential leaked: %s", key)
		}
	}
	// Model headers override the provider result after provider-level merge.
	if len(r.Headers) != 2 || r.Headers["x-scope"] != "model" || r.Headers["X-Keep"] != "yes" {
		t.Errorf("headers: %#v", r.Headers)
	}
	if !reflect.DeepEqual(r.Body["array"], []any{float64(3)}) {
		t.Errorf("array merge: %#v", r.Body)
	}
	if r.Capabilities["tools"] != true || !reflect.DeepEqual(r.Capabilities["input"], []any{"text"}) {
		t.Errorf("caps: %#v", r.Capabilities)
	}
	if r.Limit["context"] != float64(1000) || r.Limit["output"] != float64(200) {
		t.Errorf("limits: %#v", r.Limit)
	}
	if r.Compatibility["reasoningField"] != "reasoning_content" || r.Compatibility["maxTokensField"] != "max_completion_tokens" {
		t.Errorf("compatibility: %#v", r.Compatibility)
	}
	v := routes[r.Alias+"/variant/fast"]
	if v.Headers["x-variant"] != "new" || v.Settings["nested"].(map[string]any)["variant"] != float64(6) {
		t.Errorf("variant: %+v", v)
	}
	if v.Body["reasoning"].(map[string]any)["effort"] != "high" || v.Body["reasoning"].(map[string]any)["additional"] != true {
		t.Errorf("variant body: %#v", v.Body)
	}
	if _, ok := r.Settings["nested"].(map[string]any)["variant"]; ok {
		t.Error("variant mutated default Route")
	}
	if _, ok := routes[r.Alias+"/variant/slow%2Fslash"]; !ok {
		t.Error("variant ID not escaped")
	}
}

func TestResolverModelEndpointAndPackageOverrideWins(t *testing.T) {
	routes := resolverTestRoutes(t, `{"providers":{"p":{"canonical":"c","package":"@opencode/ai/providers/openai-compatible","settings":{"baseURL":"https://provider.example/v1"},"models":{"alias":{"modelID":"m","package":"@opencode/ai/providers/anthropic-compatible","settings":{"baseURL":"https://override.example/v1"}}}}}}`, `{"c":{"package":"@opencode/ai/providers/openai","models":{"m":{"settings":{"baseURL":"https://inherited.example/v1"}}}}}`)
	r := routes["opencode/p/alias"]
	if r.Protocol != "claude" || r.BaseURL != "https://override.example/v1" {
		t.Fatalf("model overrides lost: %+v", r)
	}
}

func TestResolverModelIDLookupFallback(t *testing.T) {
	routes := resolverTestRoutes(t, `{"providers":{"p":{"models":{"key":{"modelID":"deployment"}}}}}`, `{"p":{"npm":"@ai-sdk/openai","models":{"key":{"id":"key","limit":{"context":42},"provider":{"npm":"@ai-sdk/anthropic"}}}}}`)
	r := routes["opencode/p/key"]
	if r.Protocol != "claude" || r.ModelID != "deployment" || r.Limit["context"] != float64(42) {
		t.Fatalf("fallback: %+v", r)
	}
}

func TestResolverExplicitOverridesAndRetainedModels(t *testing.T) {
	routes := resolverTestRoutes(t, `{"providers":{"p":{"models":{"active":{},"disabled":{"disabled":true},"deprecated":{}}}}}`, `{"p":{"npm":"@ai-sdk/openai","models":{"active":{},"disabled":{},"deprecated":{"status":"deprecated"},"not-configured":{}}}}`)
	if len(routes) != 3 {
		t.Fatalf("unexpected routes: %#v", routes)
	}
	if _, ok := routes["opencode/p/deprecated"]; !ok {
		t.Error("explicit Console override did not recreate filtered catalog model")
	}
}

func TestResolverUnknownPackageIsDiagnostic(t *testing.T) {
	routes := resolverTestRoutes(t, `{"providers":{"p":{"models":{"claude-gpt-gemini":{},"ok":{"package":"@opencode/ai/providers/openai/chat"}}}}}`, `{"p":{"npm":"not-a-supported-package","api":"https://example.com/v1","models":{}}}`)
	bad := routes["opencode/p/claude-gpt-gemini"]
	if bad.Unsupported == "" || bad.Protocol != "" {
		t.Fatalf("unknown package inferred protocol: %+v", bad)
	}
	if routes["opencode/p/ok"].Unsupported != "" {
		t.Fatal("unsupported sibling suppressed valid Route")
	}
}

func TestResolverProtocolsUsePackageNotModelName(t *testing.T) {
	tests := []struct{ pkg, model, protocol string }{
		{"@ai-sdk/openai", "claude-name", "openai-response"},
		{"@opencode/ai/providers/openai/chat", "gemini-name", "openai"},
		{"@ai-sdk/openai-compatible", "claude-name", "openai"},
		{"@opencode/ai/providers/openai-compatible/responses", "gemini-name", "openai-response"},
		{"@opencode/ai/providers/openai-compatible-responses", "anything", "openai-response"},
		{"@ai-sdk/anthropic", "gpt-name", "claude"},
		{"@opencode/ai/providers/anthropic-compatible", "anything", "claude"},
		{"@ai-sdk/google", "gpt-name", "gemini"},
	}
	for _, tc := range tests {
		t.Run(tc.pkg, func(t *testing.T) {
			config, _ := json.Marshal(map[string]any{"providers": map[string]any{"p": map[string]any{"package": tc.pkg, "settings": map[string]any{"baseURL": "https://test.example/v1"}, "models": map[string]any{tc.model: map[string]any{}}}}})
			routes, err := ResolveRoutes(config, []byte(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range routes {
				if r.Protocol != tc.protocol || r.Unsupported != "" {
					t.Fatalf("got %+v", r)
				}
			}
		})
	}
}

func TestResolverSpecialSDKProviderFailClosed(t *testing.T) {
	for _, provider := range []string{"meta", "minimax", "moonshotai", "deepseek", "google-vertex", "cloudflare-workers-ai", "cloudflare-ai-gateway"} {
		t.Run(provider, func(t *testing.T) {
			config := `{"providers":{"p":{"canonical":"` + provider + `","models":{"m":{}}}}}`
			catalog := `{"` + provider + `":{"npm":"@ai-sdk/openai-compatible","api":"https://example.com/v1","models":{"m":{}}}}`
			r := resolverTestRoutes(t, config, catalog)["opencode/p/m"]
			if r.Unsupported == "" || r.Protocol != "" {
				t.Fatalf("special provider silently became generic: %+v", r)
			}
		})
	}
}

func TestResolverExperimentalModesKeepWireModel(t *testing.T) {
	catalog := `{"p":{"npm":"@ai-sdk/openai","models":{"m":{"id":"m","limit":{"output":100},"experimental":{"modes":{"fast":{"provider":{"headers":{"X-Mode":"fast"},"body":{"service_tier":"priority"}}}}}}}}}`
	routes := resolverTestRoutes(t, `{"providers":{"p":{"models":{"m-fast":{}}}}}`, catalog)
	r := routes["opencode/p/m-fast"]
	if r.ModelID != "m" || r.Headers["X-Mode"] != "fast" || r.Body["service_tier"] != "priority" {
		t.Fatalf("experimental mode: %+v", r)
	}
}

func TestResolverUnsupportedEndpoints(t *testing.T) {
	for _, endpoint := range []any{"", "${ENDPOINT}/v1", "ftp://example.com", "https://user:password@example.com/v1", "https://example.com/v1#fragment", 123} {
		config, _ := json.Marshal(map[string]any{"providers": map[string]any{"p": map[string]any{"package": "@opencode/ai/providers/openai-compatible", "settings": map[string]any{"baseURL": endpoint}, "models": map[string]any{"m": map[string]any{}}}}})
		routes, err := ResolveRoutes(config, []byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if routes["opencode/p/m"].Unsupported == "" {
			t.Errorf("endpoint not rejected: %#v", endpoint)
		}
	}
}

func TestResolverAliasesDoNotCollide(t *testing.T) {
	routes := resolverTestRoutes(t, `{"providers":{"p":{"package":"@opencode/ai/providers/openai","models":{"a/b":{"variants":[{"id":"x/y"}]},"a%2Fb":{},"a/b/variant/x/y":{}}}}}`, `{}`)
	if len(routes) != 4 {
		t.Fatalf("alias collision: %#v", routes)
	}
	for _, alias := range []string{"opencode/p/a%2Fb", "opencode/p/a%252Fb", "opencode/p/a%2Fb/variant/x%2Fy", "opencode/p/a%2Fb%2Fvariant%2Fx%2Fy"} {
		if _, ok := routes[alias]; !ok {
			t.Errorf("missing %q", alias)
		}
	}
}

func TestResolverVariantMergeAndDefaultSentinel(t *testing.T) {
	routes := resolverTestRoutes(t, `{"providers":{"p":{"package":"@opencode/ai/providers/openai","models":{"m":{"variants":[{"id":"x","body":{"one":1}},{"id":"x","body":{"two":2}},{"id":"default","settings":{"baseURL":"https://ignore.example"}}]}}}}}`, `{}`)
	if len(routes) != 3 {
		t.Fatalf("variant dedup: %d", len(routes))
	}
	if len(routes["opencode/p/m/variant/x"].Body) != 2 {
		t.Fatal("duplicate variant merge failed")
	}
	if routes["opencode/p/m/variant/default"].BaseURL != routes["opencode/p/m"].BaseURL {
		t.Fatal("default sentinel applied an override")
	}
}

func TestResolverMergeReplacesArraysAndNull(t *testing.T) {
	left := map[string]any{"nested": map[string]any{"a": 1, "b": 2}, "array": []any{1, 2}, "remove": "value"}
	right := map[string]any{"nested": map[string]any{"b": 3}, "array": []any{3}, "remove": nil}
	out := Merge(left, right)
	if !reflect.DeepEqual(out["nested"], map[string]any{"a": 1, "b": 3}) || !reflect.DeepEqual(out["array"], []any{3}) || out["remove"] != nil {
		t.Fatalf("merge: %#v", out)
	}
	out["nested"].(map[string]any)["a"] = 9
	if left["nested"].(map[string]any)["a"] != 1 {
		t.Fatal("merge aliases input maps")
	}
}

func TestResolverRejectsMalformedDocuments(t *testing.T) {
	tests := []struct{ config, catalog string }{
		{`{}`, `{}`}, {`null`, `{}`}, {`{"providers":[]}`, `{}`}, {`{"providers":{"p":{"models":{"m":{"disabled":"false"}}}}}`, `{}`},
		{`{"providers":{"p":null}}`, `{}`}, {`{"providers":{"p":{"models":{"m":null}}}}`, `{}`}, {`{"providers":{}}`, `{"p":null}`}, {`{"providers":{}}`, `[]`}, {`{"providers":{}}`, `null`}, {`{"providers":{}}`, `{"p":{"models":[]}}`},
		{`{"providers":{}} {}`, `{}`}, {`{"providers":{"p":{"models":{"m":{"variants":[{"id":""}]}}}}}`, `{}`},
	}
	for _, tc := range tests {
		_, err := ResolveRoutes([]byte(tc.config), []byte(tc.catalog))
		if err == nil {
			t.Errorf("accepted config=%s catalog=%s", tc.config, tc.catalog)
		}
	}
}

func TestResolverDoesNotSynthesizeHeuristicVariants(t *testing.T) {
	routes := resolverTestRoutes(t, `{"providers":{"p":{"models":{"m":{}}}}}`, `{"p":{"npm":"@ai-sdk/openai","models":{"m":{"reasoning_options":[{"type":"effort","values":["low","high"]}]}}}}`)
	if len(routes) != 1 {
		t.Fatal("unverified variant recipes generated")
	}
}

func TestResolverErrorsDoNotIncludeCredentialValues(t *testing.T) {
	_, err := ResolveRoutes([]byte(`{"providers":{"p":{"settings":{"apiKey":"do-not-leak"},"models":{"m":{"variants":[{"id":""}]}}}}}`), []byte(`{}`))
	if err == nil || strings.Contains(err.Error(), "do-not-leak") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestResolverLegacyOptionsPreserveNativeLayerOrder(t *testing.T) {
	config := `{"providers":{"p":{"package":"@ai-sdk/openai","settings":{"headers":{"x-common":"provider-legacy","X-Provider":"yes"},"extraBody":{"nested":{"provider":1,"shared":"provider-legacy"},"array":[1]}},"headers":{"X-Common":"provider-explicit"},"body":{"nested":{"shared":"provider-explicit"}},"models":{"m":{"settings":{"headers":{"x-common":"model-legacy","X-Model":"yes"},"extraBody":{"nested":{"model":2,"shared":"model-legacy"},"array":[2]}},"headers":{"X-Common":"model-explicit"},"body":{"nested":{"shared":"model-explicit"}},"variants":[{"id":"v","settings":{"headers":{"x-common":"variant-legacy","X-Variant":"yes"},"extraBody":{"nested":{"variant":3,"shared":"variant-legacy"},"array":[3]}},"headers":{"X-Common":"variant-explicit"},"body":{"nested":{"shared":"variant-explicit"}}}]}}}}}`
	routes := resolverTestRoutes(t, config, `{}`)
	base := routes["opencode/p/m"]
	variant := routes["opencode/p/m/variant/v"]
	for _, r := range []Route{base, variant} {
		if r.Unsupported != "" {
			t.Fatalf("legacy migration unsupported: %+v", r)
		}
		if _, ok := r.Settings["headers"]; ok {
			t.Fatal("headers left in settings")
		}
		if _, ok := r.Settings["extraBody"]; ok {
			t.Fatal("extraBody left in settings")
		}
	}
	if len(base.Headers) != 3 || base.Headers["X-Common"] != "model-explicit" || base.Headers["X-Provider"] != "yes" || base.Headers["X-Model"] != "yes" {
		t.Errorf("base headers: %#v", base.Headers)
	}
	if len(variant.Headers) != 4 || variant.Headers["X-Common"] != "variant-explicit" || variant.Headers["X-Variant"] != "yes" {
		t.Errorf("variant headers: %#v", variant.Headers)
	}
	wantBase := map[string]any{"provider": float64(1), "model": float64(2), "shared": "model-explicit"}
	wantVariant := map[string]any{"provider": float64(1), "model": float64(2), "variant": float64(3), "shared": "variant-explicit"}
	if !reflect.DeepEqual(base.Body["nested"], wantBase) || !reflect.DeepEqual(variant.Body["nested"], wantVariant) {
		t.Errorf("bodies: %#v, %#v", base.Body, variant.Body)
	}
	if !reflect.DeepEqual(base.Body["array"], []any{float64(2)}) || !reflect.DeepEqual(variant.Body["array"], []any{float64(3)}) {
		t.Error("legacy body arrays were merged instead of replaced")
	}
}

func TestResolverLegacyOptionsSupportedPackages(t *testing.T) {
	for _, pkg := range []string{"@ai-sdk/openai", "@ai-sdk/openai-compatible", "@ai-sdk/anthropic", "@ai-sdk/google", "@opencode/ai/providers/openai", "@opencode/ai/providers/openai-compatible", "@opencode/ai/providers/anthropic", "@opencode/ai/providers/google"} {
		t.Run(pkg, func(t *testing.T) {
			config, _ := json.Marshal(map[string]any{"providers": map[string]any{"p": map[string]any{"package": pkg, "settings": map[string]any{"baseURL": "https://example.com/v1", "headers": map[string]any{"X-Custom": "yes"}, "extraBody": map[string]any{"custom": true}}, "models": map[string]any{"m": map[string]any{}}}}})
			routes, err := ResolveRoutes(config, []byte(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			r := routes["opencode/p/m"]
			if r.Unsupported != "" || r.Headers["X-Custom"] != "yes" || r.Body["custom"] != true {
				t.Fatalf("migration: %+v", r)
			}
		})
	}
}

func TestResolverLegacyOptionsUnsupportedPackages(t *testing.T) {
	for _, pkg := range []string{"@opencode/ai/providers/openai/chat", "@opencode/ai/providers/openai/responses", "@opencode/ai/providers/openai-compatible/responses", "@opencode/ai/providers/anthropic-compatible", "unknown"} {
		t.Run(pkg, func(t *testing.T) {
			config, _ := json.Marshal(map[string]any{"providers": map[string]any{"p": map[string]any{"package": pkg, "settings": map[string]any{"baseURL": "https://example.com/v1", "extraBody": map[string]any{"custom": true}}, "models": map[string]any{"m": map[string]any{"variants": []any{map[string]any{"id": "v", "settings": map[string]any{"baseURL": "https://variant.example/v1"}}}}}}}})
			routes, err := ResolveRoutes(config, []byte(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range routes {
				if !strings.Contains(r.Unsupported, "legacy settings") {
					t.Fatalf("legacy option silently ignored or variant cleared error: %+v", r)
				}
			}
		})
	}
}

func TestResolverLegacyOptionsInvalidTypes(t *testing.T) {
	for _, settings := range []map[string]any{{"headers": []any{"x"}}, {"headers": map[string]any{"x": 1}}, {"headers": nil}, {"extraBody": []any{1}}, {"extraBody": nil}, {"extraBody": "secret-do-not-leak"}} {
		for _, scope := range []string{"provider", "model", "variant"} {
			t.Run(scope, func(t *testing.T) {
				provider := map[string]any{"package": "@opencode/ai/providers/openai"}
				model := map[string]any{}
				provider["models"] = map[string]any{"m": model}
				switch scope {
				case "provider":
					provider["settings"] = settings
				case "model":
					model["settings"] = settings
				case "variant":
					model["variants"] = []any{map[string]any{"id": "v", "settings": settings}}
				}
				config, _ := json.Marshal(map[string]any{"providers": map[string]any{"p": provider}})
				routes, err := ResolveRoutes(config, []byte(`{}`))
				if err != nil {
					t.Fatal(err)
				}
				key := "opencode/p/m"
				if scope == "variant" {
					key += "/variant/v"
					if routes["opencode/p/m"].Unsupported != "" {
						t.Fatal("invalid variant poisoned default Route")
					}
				}
				if routes[key].Unsupported == "" || strings.Contains(routes[key].Unsupported, "secret-do-not-leak") {
					t.Fatalf("invalid legacy option not safely rejected: %+v", routes[key])
				}
			})
		}
	}
}
