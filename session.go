package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

var (
	sessionEIDMu    sync.Mutex
	sessionEIDCache = map[string]string{}
	eidCounter      uint64
	lastEidTime     int64
)

func formatOpenCodeSessionEID(cacheKey string, seed []byte) string {
	sessionEIDMu.Lock()
	defer sessionEIDMu.Unlock()

	if cacheKey != "" {
		if cached, ok := sessionEIDCache[cacheKey]; ok {
			return cached
		}
	}

	now := time.Now().UnixMilli()
	if now != lastEidTime {
		lastEidTime = now
		eidCounter = 0
	}
	counter := eidCounter
	eidCounter++

	val := (uint64(now)*4096 + counter) & 0xffffffffffff
	inv := (^val) & 0xffffffffffff
	hexPart := fmt.Sprintf("%012x", inv)

	randPart := make([]byte, 14)
	if len(seed) >= 14 {
		for i := range 14 {
			randPart[i] = base62Alphabet[seed[i]%62]
		}
	} else {
		var rb [14]byte
		_, _ = rand.Read(rb[:])
		for i := range 14 {
			randPart[i] = base62Alphabet[rb[i]%62]
		}
	}

	id := fmt.Sprintf("ses_%s%s", hexPart, string(randPart))
	if cacheKey != "" {
		if len(sessionEIDCache) > 10000 {
			sessionEIDCache = make(map[string]string)
		}
		sessionEIDCache[cacheKey] = id
	}
	return id
}

// sessionHeaders emits a plugin-owned affinity ID, never an official-client
// identity. Explicit conversation scope is kept only when CPA also provides a
// trusted frontend caller scope. Content-derived cache affinity is not a user
// conversation and is deliberately ignored.
func sessionHeaders(c credential, req pluginapi.ExecutorRequest) (http.Header, error) {
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
			if _, err := rand.Read(nonce[:]); err != nil {
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
	id := formatOpenCodeSessionEID(string(sum), sum)
	return http.Header{"X-Opencode-Session": {id}, "X-Session-Affinity": {id}, "X-Session-Id": {id}}, nil
}
