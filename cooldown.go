package main

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The ABI cannot attach headers to an RPC error. Keep upstream Retry-After as a
// credential-scoped local gate so host retries cannot resend early. No sleeps,
// credential rotation, or inference retries are performed by this plugin.
var cooldowns = struct {
	sync.Mutex
	entries map[string]cooldown
}{entries: map[string]cooldown{}}

type cooldown struct {
	until  time.Time
	status int
}

// Native na prefers retry-after-ms over Retry-After. Keep the existing local
// 429/503 gate; the host ABI still cannot convey original retry response headers.
func rememberRetryHeaders(c credential, status int, headers http.Header) {
	if status != 429 && status != 503 {
		return
	}
	if value := strings.TrimSpace(headers.Get("Retry-After-Ms")); value != "" {
		if ms, err := strconv.ParseFloat(value, 64); err == nil && !math.IsNaN(ms) && !math.IsInf(ms, 0) && ms >= 0 {
			max := float64(math.MaxInt64 / int64(time.Millisecond))
			delay := time.Duration(math.MaxInt64)
			if ms < max-1 {
				delay = time.Duration(ms * float64(time.Millisecond))
			}
			rememberCooldown(c, status, now().Add(delay))
			return
		}
	}
	rememberRetryAfter(c, status, headers.Get("Retry-After"))
}

func rememberRetryAfter(c credential, status int, value string) {
	if status != 429 && status != 503 {
		return
	}
	value = strings.TrimSpace(value)
	current := now()
	var until time.Time
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		// Saturate rather than overflowing; retain the server's requested restriction.
		if seconds > int64((time.Duration(1<<63-1))/time.Second) {
			seconds = int64((time.Duration(1<<63 - 1)) / time.Second)
		}
		until = current.Add(time.Duration(seconds) * time.Second)
	} else if date, err := http.ParseTime(value); err == nil {
		until = date
	} else {
		return
	}
	rememberCooldown(c, status, until)
}

func rememberCooldown(c credential, status int, until time.Time) {
	current := now()
	if !until.After(current) {
		return
	}
	cooldowns.Lock()
	defer cooldowns.Unlock()
	for key, old := range cooldowns.entries {
		if !old.until.After(current) {
			delete(cooldowns.entries, key)
		}
	}
	key := c.fingerprint()
	if previous := cooldowns.entries[key]; previous.until.Before(until) {
		cooldowns.entries[key] = cooldown{until, status}
	}
}
func checkRetryAfter(c credential) error {
	cooldowns.Lock()
	defer cooldowns.Unlock()
	key := c.fingerprint()
	item, ok := cooldowns.entries[key]
	if !ok {
		return nil
	}
	if !item.until.After(now()) {
		delete(cooldowns.entries, key)
		return nil
	}
	return fail(item.status, "OpenCode retry-after interval is still active")
}
