package plugin

import (
	"encoding/json"
	"testing"

	"github.com/megumin31/cpa-opencode-zen/internal/buildinfo"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestPluginIdentityIndependentOfApplicationVersion(t *testing.T) {
	engine := newTestEngine(t)

	if providerID != "opencode" || version != buildinfo.Version {
		t.Fatalf("unexpected identity %s/%s", providerID, version)
	}
	for _, method := range []string{"auth.identifier", "executor.identifier"} {
		got, err := engine.Handle(method, nil)
		if err != nil || got.(map[string]string)["identifier"] != "opencode" {
			t.Fatalf("%s: %v, %v", method, got, err)
		}
	}
	raw, _ := json.Marshal(registration())
	var r struct {
		Metadata pluginapi.Metadata `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	if r.Metadata.Name != "opencode" || r.Metadata.Version != buildinfo.Version || r.Metadata.GitHubRepository != "megumin31/cpa-opencode-zen" {
		t.Fatalf("metadata: %+v", r.Metadata)
	}
}

func TestLegacyAuthRequiresExplicitMigration(t *testing.T) {
	engine := newTestEngine(t)

	engine.testConfig(t, "https://opencode.ai")
	for _, typ := range []string{"opencode", "opencode-v2", "other"} {
		raw, _ := json.Marshal(map[string]any{"type": typ, "key": "migration-fake-key", "disabled": true})
		request, _ := json.Marshal(pluginapi.AuthParseRequest{FileName: "existing.json", RawJSON: raw})
		result, err := engine.parseAuth(request)
		if err != nil {
			t.Fatal(err)
		}
		parsed := result.(pluginapi.AuthParseResponse)
		if parsed.Handled != (typ == "opencode") {
			t.Fatalf("type %s handled=%v", typ, parsed.Handled)
		}
		if parsed.Handled && (parsed.Auth.Provider != "opencode" || parsed.Auth.FileName != "existing.json" || !parsed.Auth.Disabled) {
			t.Fatalf("migration changed auth state: %+v", parsed.Auth)
		}
	}
	if _, err := decodeCredential(engine.current.config, []byte(`{"type":"opencode-v2","key":"migration-fake-key"}`), nil); err == nil {
		t.Fatal("legacy credential unexpectedly accepted")
	}
}
