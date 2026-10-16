package plugin

import "testing"

// Every runtime fixture owns its lifecycle and dependencies. Tests never reset a
// process singleton, so independently constructed engines can run concurrently.
func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	e := New(Dependencies{Logf: func(string, ...any) {}})
	e.testConfig(t, "https://opencode.ai")
	t.Cleanup(e.Shutdown)
	return e
}
