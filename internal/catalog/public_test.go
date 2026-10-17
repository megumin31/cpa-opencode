package catalog

import (
	"fmt"
	"testing"
)

func TestPublicCatalogRequiresExplicitFreePrices(t *testing.T) {
	for _, tc := range []struct {
		name, cost string
		want       bool
	}{
		{"free", `{"input":0,"output":0,"cache_read":0}`, true},
		{"missing", `null`, false}, {"empty", `{}`, false},
		{"input_only", `{"input":0}`, false}, {"output_only", `{"output":0}`, false},
		{"null_input", `{"input":null,"output":0}`, false},
		{"string", `{"input":"0","output":0}`, false},
		{"tiny_fee", `{"input":1e-999,"output":0}`, false},
		{"paid", `{"input":1,"output":0}`, false},
		{"paid_output", `{"input":0,"output":1}`, false},
		{"paid_cache", `{"input":0,"output":0,"cache_write":1}`, false},
		{"negative", `{"input":-1,"output":0}`, false},
		{"tiered", `{"input":0,"output":0,"context_over_200k":{"input":1}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := fmt.Sprintf(`{"opencode":{"npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/inference/v1","models":{"m":{"cost":%s}}},"other":{"models":{"other":{"cost":{"input":0,"output":0}}}}}`, tc.cost)
			routes, err := ResolvePublicRoutes([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			if (len(routes) == 1) != tc.want {
				t.Fatalf("routes=%v want free=%v", routes, tc.want)
			}
		})
	}
}

func TestPublicRejectsPaidModelRedirections(t *testing.T) {
	for _, extra := range []string{
		`"modelID":"paid"`,
		`"body":{"model":"paid"}`,
		`"settings":{"extraBody":{"model":"paid"}}`,
	} {
		data := fmt.Sprintf(`{"opencode":{"npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/inference/v1","models":{"free":{"cost":{"input":0,"output":0},%s},"paid":{"cost":{"input":1,"output":1}}}}}`, extra)
		routes, err := ResolvePublicRoutes([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		if len(routes) != 0 {
			t.Fatalf("paid redirection exposed: %s: %v", extra, routes)
		}
	}
	data := `{"opencode":{"npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/inference/v1","models":{"free":{"cost":{"input":0,"output":0},"variants":[{"id":"paid","body":{"model":"paid"}},{"id":"extra","settings":{"extraBody":{"model":"paid"}}}],"experimental":{"modes":{"paid":{"provider":{"body":{"model":"paid"}}}}}},"paid":{"cost":{"input":1,"output":1}}}}}`
	routes, err := ResolvePublicRoutes([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes["opencode-zen/free"].ModelID != "free" {
		t.Fatalf("unsafe generated alias: %v", routes)
	}
}

func TestPublicRejectsConflictingCanonicalIDs(t *testing.T) {
	for _, cost := range []string{`{"input":1,"output":1}`, `{"input":0,"output":0}`, `null`, `{"input":0}`} {
		data := fmt.Sprintf(`{"opencode":{"npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/inference/v1","models":{"alias":{"id":"paid","cost":{"input":0,"output":0}},"paid":{"id":"paid","cost":%s}}}}`, cost)
		routes, err := ResolvePublicRoutes([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		if len(routes) != 0 {
			t.Fatalf("ambiguous canonical ID exposed: %s %v", cost, routes)
		}
	}
}
