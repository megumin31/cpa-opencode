package plugin

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestWorkerConfigurationKeepsOriginalGuard(t *testing.T) {
	cfg := defaultConfig()
	if err := yaml.Unmarshal([]byte("worker_endpoint: https://gateway.example\nworker_token: fake-entry-token\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	policy := cfg.transportPolicy()
	if policy.WorkerEndpoint != "https://gateway.example" || policy.WorkerToken != "fake-entry-token" {
		t.Fatal("worker config not propagated")
	}
	const endpoint = "https://opencode.ai/inference/v1/chat/completions"
	cfg.TestGuard = &inferenceTestGuard{Enabled: true, AllowedRequests: []inferenceGuardRule{{Model: "fake-model", Endpoint: endpoint}}}
	engine := New(Dependencies{})
	defer engine.Shutdown()
	body := []byte(`{"model":"fake-model","messages":[{"role":"user","content":"fake"}]}`)
	if err := engine.checkInferenceGuard(cfg, "openai", endpoint, body); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"https://gateway.example/opencode/inference/v1/chat/completions", endpoint + "?changed=true"} {
		if err := engine.checkInferenceGuard(cfg, "openai", target, body); err == nil {
			t.Fatal("gateway widened exact guard")
		}
	}
	if err := engine.checkInferenceGuard(cfg, "openai", endpoint, []byte(`{"model":"other-model"}`)); err == nil {
		t.Fatal("gateway widened model guard")
	}
}
