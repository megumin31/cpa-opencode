package main

import (
	"net/http"
	"testing"
	"time"
)

func TestRetryAfterMillisecondsPrecedence(t *testing.T) {
	saved := now
	defer func() { now = saved }()
	current := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	now = func() time.Time { return current }
	c := credential{Key: "retry-ms-fixture", Server: "https://example.invalid"}
	cooldowns.Lock()
	delete(cooldowns.entries, c.fingerprint())
	cooldowns.Unlock()
	rememberRetryHeaders(c, 429, http.Header{"Retry-After-Ms": {"1500"}, "Retry-After": {"99"}})
	if checkRetryAfter(c) == nil {
		t.Fatal("missing gate")
	}
	current = current.Add(1499 * time.Millisecond)
	if checkRetryAfter(c) == nil {
		t.Fatal("early expiry")
	}
	current = current.Add(time.Millisecond)
	if checkRetryAfter(c) != nil {
		t.Fatal("ms did not take precedence")
	}
	rememberRetryHeaders(c, 503, http.Header{"Retry-After-Ms": {"invalid"}, "Retry-After": {"2"}})
	if checkRetryAfter(c) == nil {
		t.Fatal("invalid ms lost seconds fallback")
	}
	current = current.Add(2 * time.Second)
	if checkRetryAfter(c) != nil {
		t.Fatal("fallback did not expire")
	}
}
