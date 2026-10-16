package plugin

import (
	"os"
	"testing"

	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
)

// Optional local validation of an already downloaded PUBLIC catalog. The normal
// suite is fully offline and uses minimized synthetic regressions instead.
func TestPublicCatalogSnapshot(t *testing.T) {
	engine := newTestEngine(t)

	path := os.Getenv("OPENCODE_TEST_PUBLIC_CATALOG")
	if path == "" {
		t.Skip("optional public catalog snapshot path not supplied")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	providers, e := catalog.ParseCatalog(b)
	if e != nil {
		t.Fatal(e)
	}
	routes, e := catalog.ResolveRoutes([]byte(`{"providers":{}}`), b)
	if e != nil {
		t.Fatal(e)
	}
	// Use the production defaults rather than the broader test helper allowlist.
	engine.current.Lock()
	previous := engine.current.config
	engine.current.config = defaultConfig()
	engine.current.Unlock()
	defer func() { engine.current.Lock(); engine.current.config = previous; engine.current.Unlock() }()
	models := modelInfos(engine.current.config, routes)
	t.Logf("catalog providers=%d; connected opencode candidate routes=%d; supported trusted models=%d", len(providers), len(routes), len(models))
	for _, r := range routes {
		if r.ProviderID != "opencode" {
			t.Fatalf("unexpected credential binding %q", r.ProviderID)
		}
	}
	if len(models) == 0 {
		t.Fatal("public snapshot yielded no usable baseline")
	}
	// Public Console API documentation example, with a fake organization value.
	// Its aisdk prefix and API-key placeholder must not erase the catalog baseline.
	documented := []byte(`{"providers":{"opencode":{"name":"OpenCode","env":["OPENCODE_CONSOLE_TOKEN"],"package":"aisdk:@ai-sdk/openai-compatible","settings":{"baseURL":"https://opencode.ai/inference/openai/v1","apiKey":"{env:OPENCODE_CONSOLE_TOKEN}"},"headers":{"x-opencode-org-id":"fake-org"},"models":{"kimi-k2.6":{"name":"Kimi K2.6"}}}}}`)
	routes, e = catalog.ResolveRoutes(documented, b)
	if e != nil || len(modelInfos(engine.current.config, routes)) == 0 {
		t.Fatalf("documented Console override yielded no usable models: %v", e)
	}
	t.Logf("documented Console config candidate routes=%d; supported trusted models=%d", len(routes), len(modelInfos(engine.current.config, routes)))
}
