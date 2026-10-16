package plugin

import (
	"context"
	"strings"
	"testing"

	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
	wire "github.com/megumin31/cpa-opencode-zen/internal/protocol"
)

func TestConfiguredIdentityHeadersRejected(t *testing.T) {
	engine := newTestEngine(t)

	engine.testConfig(t, "https://opencode.ai")
	for _, name := range []string{"x-opencode-client", "x-opencode-project", "x-parent-session-id", "x-opencode-session-id", "x-opencode-parent-session-id"} {
		for _, header := range []string{name, strings.ToUpper(name)} {
			t.Run(header, func(t *testing.T) {
				r := catalog.Route{ProviderID: "opencode", ModelID: "m", Protocol: "openai", Package: "@opencode/ai/providers/openai-compatible", BaseURL: "https://opencode.ai/inference/v1", Headers: map[string]string{header: "fake-identity"}}
				_, _, _, err := engine.prepareRequest(engine.current.config, context.Background(), credential{Key: "fake-key"}, r, wire.FormatName("openai"), []byte(`{"messages":[{"role":"user","content":"fake"}]}`))
				if err == nil || !strings.Contains(err.Error(), "protected") {
					t.Fatalf("configured identity accepted: %v", err)
				}
			})
		}
	}
	r := catalog.Route{Protocol: "openai", ModelID: "m", BaseURL: "https://opencode.ai/inference/v1", Headers: map[string]string{"x-opencode-org-id": "fake-org"}}
	_, _, headers, err := engine.prepareRequest(engine.current.config, context.Background(), credential{Key: "fake-key"}, r, wire.FormatName("openai"), []byte(`{"messages":[{"role":"user","content":"fake"}]}`))
	if err != nil || headers.Get("x-opencode-org-id") != "fake-org" {
		t.Fatalf("explicit Console-config organization must remain allowed: %v", err)
	}
}
