package plugin

import (
	"net/http"
	"testing"
	"time"
)

func TestRetryAfterMillisecondsPrecedence(t *testing.T) {
	engine := newTestEngine(t)

	saved := engine.now
	defer func() { engine.now = saved }()
	current := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	engine.now = func() time.Time { return current }
	c := credential{Key: "retry-ms-fixture", Server: "https://example.invalid"}
	engine.cooldowns.Lock()
	delete(engine.cooldowns.entries, c.fingerprint())
	engine.cooldowns.Unlock()
	engine.cooldowns.rememberHeaders(c, 429, http.Header{"Retry-After-Ms": {"1500"}, "Retry-After": {"99"}})
	if engine.cooldowns.check(c) == nil {
		t.Fatal("missing gate")
	}
	current = current.Add(1499 * time.Millisecond)
	if engine.cooldowns.check(c) == nil {
		t.Fatal("early expiry")
	}
	current = current.Add(time.Millisecond)
	if engine.cooldowns.check(c) != nil {
		t.Fatal("ms did not take precedence")
	}
	engine.cooldowns.rememberHeaders(c, 503, http.Header{"Retry-After-Ms": {"invalid"}, "Retry-After": {"2"}})
	if engine.cooldowns.check(c) == nil {
		t.Fatal("invalid ms lost seconds fallback")
	}
	current = current.Add(2 * time.Second)
	if engine.cooldowns.check(c) != nil {
		t.Fatal("fallback did not expire")
	}
}
