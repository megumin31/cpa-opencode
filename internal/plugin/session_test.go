package plugin

import (
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestSessionAffinityScopedAndStable(t *testing.T) {
	engine := newTestEngine(t)

	c := credential{Type: providerID, Key: "test-service-key", Server: "https://opencode.ai/console", OrgID: "org-a"}
	req := pluginapi.ExecutorRequest{Metadata: map[string]any{"caller_scope": "caller-a", "execution_session_id": "conversation-a"}, Model: "opencode/p/model-a"}
	h, err := engine.sessions.headers(c, req)
	if err != nil {
		t.Fatal(err)
	}
	id := h.Get("x-opencode-session")
	if !strings.HasPrefix(id, "ses_") {
		t.Fatal("not own identifier")
	}
	if h.Get("x-session-affinity") != id || h.Get("X-Session-Id") != id {
		t.Fatal("affinity mismatch")
	}
	req.Model = "opencode/p/model-b"
	h2, _ := engine.sessions.headers(c, req)
	if h2.Get("x-opencode-session") != id {
		t.Fatal("model change broke same conversation")
	}
	for _, change := range []string{"caller", "conversation", "key", "org", "server"} {
		cc := c
		rr := req
		rr.Metadata = map[string]any{"caller_scope": "caller-a", "execution_session_id": "conversation-a"}
		switch change {
		case "caller":
			rr.Metadata["caller_scope"] = "caller-b"
		case "conversation":
			rr.Metadata["execution_session_id"] = "conversation-b"
		case "key":
			cc.Key = "other-test-key"
		case "org":
			cc.OrgID = "org-b"
		case "server":
			cc.Server = "https://other.example/console"
		}
		hh, _ := engine.sessions.headers(cc, rr)
		if hh.Get("x-opencode-session") == id {
			t.Fatalf("session not isolated by %s", change)
		}
	}
}
func TestSessionMissingScopeIsRequestBoundNotGlobal(t *testing.T) {
	engine := newTestEngine(t)

	c := credential{Type: providerID, Key: "test-service-key"}
	req := pluginapi.ExecutorRequest{Metadata: map[string]any{"canonical_session_id": "content-derived", "derived_session_id": "content-derived", "lcp_affinity_session_id": "content-derived"}}
	h, _ := engine.sessions.headers(c, req)
	other, _ := engine.sessions.headers(c, req)
	if h.Get("x-opencode-session") == other.Get("x-opencode-session") {
		t.Fatal("missing scope created shared session")
	}
	req.Headers = http.Header{internalRequestHeader: {"request-1"}}
	h, _ = engine.sessions.headers(c, req)
	other, _ = engine.sessions.headers(c, req)
	if h.Get("x-opencode-session") != other.Get("x-opencode-session") {
		t.Fatal("same request retry unstable")
	}
	req.Headers.Set(internalRequestHeader, "request-2")
	other, _ = engine.sessions.headers(c, req)
	if h.Get("x-opencode-session") == other.Get("x-opencode-session") {
		t.Fatal("different requests shared session")
	}
}
