package plugin

import (
	"context"
	"testing"

	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
	wire "github.com/megumin31/cpa-opencode-zen/internal/protocol"
)

func TestPrepareRejectsMalformedTransportSettings(t *testing.T) {
	engine := newTestEngine(t)

	engine.testConfig(t, "https://proxy.example")
	r := catalog.Route{ModelID: "m", Protocol: "openai", BaseURL: "https://proxy.example/v1", Settings: map[string]any{"timeout": -1}}
	_, _, _, err := engine.prepareRequest(engine.current.config, context.Background(), credential{Key: "fake-test-key"}, r, wire.FormatName("openai"), []byte(`{"messages":[]}`))
	if err == nil {
		t.Fatal("malformed transport setting silently accepted")
	}
}
