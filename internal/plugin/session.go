package plugin

import (
	containerlist "container/list"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// Session IDs are stable for a cached scope throughout an Engine lifetime,
// including ordinary reconfiguration. Eviction is LRU, never a whole-cache reset.
const sessionCacheCapacity = 10000

type sessionIdentity struct{ key, id string }
type sessionManager struct {
	mu       sync.Mutex
	cache    map[string]*containerlist.Element
	lru      *containerlist.List
	counter  uint64
	lastTime int64
	now      func() time.Time
	random   io.Reader
	capacity int
}

func newSessionManager(now func() time.Time, random io.Reader) *sessionManager {
	return &sessionManager{cache: map[string]*containerlist.Element{}, lru: containerlist.New(), now: now, random: random, capacity: sessionCacheCapacity}
}
func (s *sessionManager) readRandom(dst []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := io.ReadFull(s.random, dst)
	return err
}
func (s *sessionManager) format(cacheKey string, seed []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cached := s.cache[cacheKey]; cacheKey != "" && cached != nil {
		s.lru.MoveToFront(cached)
		return cached.Value.(sessionIdentity).id, nil
	}
	timestamp := s.now().UnixMilli()
	if timestamp != s.lastTime {
		s.lastTime = timestamp
		s.counter = 0
	}
	counter := s.counter
	s.counter++
	val := (uint64(timestamp)*4096 + counter) & 0xffffffffffff
	inv := (^val) & 0xffffffffffff
	suffix := make([]byte, 14)
	if len(seed) < 14 {
		seed = make([]byte, 14)
		if _, err := io.ReadFull(s.random, seed); err != nil {
			return "", fail(500, "cannot create request affinity")
		}
	}
	for i := range suffix {
		suffix[i] = base62Alphabet[seed[i]%62]
	}
	id := fmt.Sprintf("ses_%012x%s", inv, string(suffix))
	if cacheKey != "" && s.capacity > 0 {
		s.cache[cacheKey] = s.lru.PushFront(sessionIdentity{cacheKey, id})
		for s.lru.Len() > s.capacity {
			last := s.lru.Back()
			delete(s.cache, last.Value.(sessionIdentity).key)
			s.lru.Remove(last)
		}
	}
	return id, nil
}

// sessionHeaders emits a plugin-owned affinity ID, never an official-client
// identity. Explicit conversation scope is kept only when CPA also provides a
// trusted frontend caller scope. Content-derived cache affinity is not a user
// conversation and is deliberately ignored.
func (s *sessionManager) headers(c credential, req pluginapi.ExecutorRequest) (http.Header, error) {
	for _, name := range []string{"x-opencode-session", "X-Session-Id", "X-Conversation-Id"} {
		values := req.Headers.Values(name)
		if len(values) > 1 {
			return nil, fail(400, "ambiguous conversation header")
		}
		if len(values) == 1 && strings.ContainsAny(values[0], "\r\n\x00") {
			return nil, fail(400, "invalid conversation header")
		}
	}
	caller, _ := req.Metadata["caller_scope"].(string)
	conversation := strings.TrimSpace(req.Headers.Get("x-opencode-session"))
	if conversation == "" {
		conversation, _ = req.Metadata["execution_session_id"].(string)
	}
	if conversation == "" {
		conversation = req.Headers.Get("X-Session-Id")
	}
	if conversation == "" {
		conversation = req.Headers.Get("X-Conversation-Id")
	}
	scope := "conversation"
	if caller == "" || conversation == "" {
		scope = "request"
		conversation = req.Headers.Get(internalRequestHeader)
		if conversation == "" {
			var nonce [24]byte
			if err := s.readRandom(nonce[:]); err != nil {
				return nil, fail(500, "cannot create request affinity")
			}
			conversation = base64.RawURLEncoding.EncodeToString(nonce[:])
		}
	}
	if len(caller) > 8192 || len(conversation) > 8192 {
		return nil, fail(400, "conversation scope exceeds size limit")
	}
	payload, _ := json.Marshal([]string{providerID, c.Server, c.OrgID, scope, caller, conversation})
	mac := hmac.New(sha256.New, []byte(c.Key))
	_, _ = mac.Write(payload)
	sum := mac.Sum(nil)
	id, err := s.format(string(sum), sum)
	if err != nil {
		return nil, err
	}
	return http.Header{"X-Opencode-Session": {id}, "X-Session-Affinity": {id}, "X-Session-Id": {id}}, nil
}
