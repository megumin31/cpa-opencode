package plugin

import (
	"testing"

	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
)

// Runtime integration fixtures use the public pure resolver API.
func resolverTestRoutes(t *testing.T, config, data string) map[string]catalog.Route {
	t.Helper()
	routes, err := catalog.ResolveRoutes([]byte(config), []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	return routes
}
