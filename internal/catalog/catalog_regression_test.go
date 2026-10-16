package catalog

import (
	"encoding/json"
	"testing"
)

func TestCatalogModeCollisionMatchesNativeInsertionOrder(t *testing.T) {
	explicit := `"m-fast":{"id":"m-fast","limit":{}}`
	base := `"m":{"id":"m","limit":{},"experimental":{"modes":{"fast":{"provider":{"body":{"service_tier":"priority"}}}}}}`
	for _, tc := range []struct {
		name, models, wantModel string
		wantPriority            bool
	}{
		{"generated_last", explicit + "," + base, "m", true},
		{"explicit_last", base + "," + explicit, "m-fast", false},
		{"deprecated_generated_filtered", explicit + `,"m":{"id":"m","status":"deprecated","experimental":{"modes":{"fast":{"provider":{"body":{"service_tier":"priority"}}}}}}`, "m-fast", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog, e := ParseCatalog([]byte(`{"p":{"id":"p","npm":"@ai-sdk/openai","models":{` + tc.models + `}}}`))
			if e != nil {
				t.Fatal(e)
			}
			got := catalog["p"].Models["m-fast"]
			if got.ModelID != tc.wantModel || (got.Body["service_tier"] == "priority") != tc.wantPriority {
				t.Fatalf("wrong collision winner: %+v", got)
			}
		})
	}
}
func TestCatalogObjectKeysUseJavaScriptEnumeration(t *testing.T) {
	_, keys, e := resolverOrderedObject([]byte(`{"10":{},"2":{},"z":{},"01":{},"a":{},"2":{"last":true}}`))
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(keys)
	if string(b) != `["2","10","z","01","a"]` {
		t.Fatal(string(b))
	}
}
func TestCatalogNativeFiltering(t *testing.T) {
	catalog, e := ParseCatalog([]byte(`{"p":{"models":{"ok":{},"old":{"status":"deprecated"},"audio":{"modalities":{"input":["audio"],"output":["text"]}},"prefix":{"modalities":{"input":["text/plain"],"output":["text"]}}}},"azure-cognitive-services":{"models":{"m":{}}},"google-vertex-anthropic":{"models":{"m":{}}},"amazon-bedrock":{"models":{"old":{"id":"deepseek.r1-v1:0"},"new":{"id":"region.deepseek.r1-v1:0"}}}}`))
	if e != nil {
		t.Fatal(e)
	}
	if len(catalog) != 2 || len(catalog["p"].Models) != 2 || len(catalog["amazon-bedrock"].Models) != 1 {
		t.Fatalf("wrong catalog filtering: %#v", catalog)
	}
}
func TestResolverConnectedOpenCodeBaselineWithoutConsoleOverrides(t *testing.T) {
	routes := resolverTestRoutes(t, `{"providers":{}}`, `{"opencode":{"npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/inference/v1","models":{"chat":{},"responses":{"provider":{"npm":"@ai-sdk/openai"}},"old":{"status":"deprecated"}}},"opencode-go":{"npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/inference/go/v1","models":{"go-only":{}}},"openai":{"npm":"@ai-sdk/openai","models":{"private":{}}}}`)
	if len(routes) != 2 || routes["opencode-zen/chat"].Protocol != "openai" || routes["opencode-zen/responses"].Protocol != "openai-response" {
		t.Fatalf("bad baseline: %#v", routes)
	}
}
func TestResolverExistingProviderRetainedButAliasStartsEmpty(t *testing.T) {
	catalog := `{"p":{"npm":"@ai-sdk/openai","models":{"one":{},"two":{}}}}`
	retained := resolverTestRoutes(t, `{"providers":{"p":{"models":{"one":{"disabled":true}}}}}`, catalog)
	if len(retained) != 1 || retained["opencode/p/two"].ModelID != "two" {
		t.Fatal(retained)
	}
	alias := resolverTestRoutes(t, `{"providers":{"alias":{"canonical":"p","models":{"one":{}}}}}`, catalog)
	if len(alias) != 1 || alias["opencode/alias/one"].ModelID != "one" {
		t.Fatal(alias)
	}
}
func TestResolverProviderOverridesOnlyClearExplicitModelSettings(t *testing.T) {
	routes := resolverTestRoutes(t, `{"providers":{"p":{"package":"@ai-sdk/openai-compatible","settings":{"baseURL":"https://opencode.ai/inference/v1"},"models":{"explicit":{}}}}}`, `{"p":{"npm":"@ai-sdk/openai","models":{"explicit":{"provider":{"npm":"@ai-sdk/anthropic","api":"https://old.example/explicit"}},"retained":{"provider":{"npm":"@ai-sdk/google","api":"https://old.example/retained"}}}}}`)
	if routes["opencode/p/explicit"].Protocol != "openai" || routes["opencode/p/explicit"].BaseURL != "https://opencode.ai/inference/v1" {
		t.Fatal(routes)
	}
	if routes["opencode/p/retained"].Protocol != "gemini" || routes["opencode/p/retained"].BaseURL != "https://old.example/retained" {
		t.Fatal(routes)
	}
}
