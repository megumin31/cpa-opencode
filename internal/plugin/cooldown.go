package plugin

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
type cooldownStore struct {
	sync.Mutex
	entries map[string]cooldown
	now     func() time.Time
}

type cooldown struct {
	until  time.Time
	status int
}

// Native na prefers retry-after-ms over Retry-After. Keep the existing local
// 429/503 gate; the host ABI still cannot convey original retry response headers.
func (s *cooldownStore) rememberHeaders(c credential, status int, headers http.Header) {
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
			s.remember(c, status, s.now().Add(delay))
			return
		}
	}
	s.rememberAfter(c, status, headers.Get("Retry-After"))
}

func (s *cooldownStore) rememberAfter(c credential, status int, value string) {
	if status != 429 && status != 503 {
		return
	}
	value = strings.TrimSpace(value)
	current := s.now()
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
	s.remember(c, status, until)
}

func (s *cooldownStore) remember(c credential, status int, until time.Time) {
	current := s.now()
	if !until.After(current) {
		return
	}
	s.Lock()
	defer s.Unlock()
	for key, old := range s.entries {
		if !old.until.After(current) {
			delete(s.entries, key)
		}
	}
	key := c.fingerprint()
	if previous := s.entries[key]; previous.until.Before(until) {
		s.entries[key] = cooldown{until, status}
	}
}
func (s *cooldownStore) check(c credential) error {
	s.Lock()
	defer s.Unlock()
	key := c.fingerprint()
	item, ok := s.entries[key]
	if !ok {
		return nil
	}
	if !item.until.After(s.now()) {
		delete(s.entries, key)
		return nil
	}
	return fail(item.status, "OpenCode retry-after interval is still active")
}
