package main

import (
	"context"
	"strings"
	"testing"
)

func TestConfiguredIdentityHeadersRejected(t *testing.T) {
	testConfig(t, "https://opencode.ai")
	for _, name := range []string{"x-opencode-client", "x-opencode-project", "x-parent-session-id", "x-opencode-session-id", "x-opencode-parent-session-id"} {
		for _, header := range []string{name, strings.ToUpper(name)} {
			t.Run(header, func(t *testing.T) {
				r := route{ProviderID: "opencode", ModelID: "m", Protocol: "openai", Package: "@opencode/ai/providers/openai-compatible", BaseURL: "https://opencode.ai/inference/v1", Headers: map[string]string{header: "fake-identity"}}
				_, _, _, err := prepareRequest(context.Background(), credential{Key: "fake-key"}, r, formatName("openai"), []byte(`{"messages":[{"role":"user","content":"fake"}]}`))
				if err == nil || !strings.Contains(err.Error(), "protected") {
					t.Fatalf("configured identity accepted: %v", err)
				}
			})
		}
	}
	if forbiddenHeader("x-opencode-org-id") {
		t.Fatal("explicit Console-config organization must remain allowed")
	}
}
