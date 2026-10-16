package main

import (
	"net/http"
	"testing"
	"time"
)

func TestRetryAfterGate(t *testing.T) {
	saved := now
	current := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now = func() time.Time { return current }
	defer func() { now = saved }()
	c := credential{Key: "cooldown-fake", Server: "https://opencode.ai/console"}
	rememberRetryAfter(c, 429, "120")
	if e := checkRetryAfter(c); e == nil || e.(*statusError).status != 429 {
		t.Fatalf("gate error %v", e)
	}
	other := c
	other.Key = "other-fake"
	if e := checkRetryAfter(other); e != nil {
		t.Fatal("cross credential restriction")
	}
	current = current.Add(121 * time.Second)
	if e := checkRetryAfter(c); e != nil {
		t.Fatal(e)
	}
	rememberRetryAfter(c, 503, current.Add(time.Hour).Format(http.TimeFormat))
	if e := checkRetryAfter(c); e == nil || e.(*statusError).status != 503 {
		t.Fatal(e)
	}
}
