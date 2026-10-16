package protocol

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
	"github.com/megumin31/cpa-opencode-zen/internal/fault"
)

func TestPrepareBoundaryReturnsCredentialFreeRequests(t *testing.T) {
	for _, name := range []string{"openai", "openai-response", "claude", "gemini"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := catalog.Route{ModelID: "native-model", Protocol: name, BaseURL: "https://provider.example/v1", Headers: map[string]string{"X-Provider-Feature": "enabled"}}
			body, target, headers, err := Prepare(context.Background(), r, FormatName(name), []byte(clientRequest(name)), Options{})
			if err != nil {
				t.Fatal(err)
			}
			if len(body) == 0 || !strings.HasPrefix(target, r.BaseURL+"/") || headers.Get("X-Provider-Feature") != "enabled" {
				t.Fatalf("incomplete request: %s %s %v", target, body, headers)
			}
			for _, key := range []string{"Authorization", "Proxy-Authorization", "X-Api-Key", "X-Goog-Api-Key", "X-Org-Id", "Cookie", "X-Session-Affinity"} {
				if headers.Get(key) != "" {
					t.Fatalf("preparation supplied runtime-only header %s", key)
				}
			}
		})
	}
}

func TestPrepareBoundaryProtectedHeaders(t *testing.T) {
	for _, key := range []string{"authorization", "proxy-authorization", "x-api-key", "x-goog-api-key", "x-org-id", "x-opencode-session", "x-opencode-session-id", "x-opencode-parent-session-id", "x-opencode-client", "x-opencode-project", "x-parent-session-id", "x-session-affinity", "x-session-id", "cookie", "set-cookie", "host", "content-length", "connection", "transfer-encoding", "user-agent", "x-cliproxy-opencode-request", "x-opencode-client-custom"} {
		t.Run(key, func(t *testing.T) {
			r := catalog.Route{ModelID: "m", Protocol: "openai", BaseURL: "https://provider.example/v1", Headers: map[string]string{strings.ToUpper(key): "untrusted-overlay"}}
			_, _, _, err := Prepare(context.Background(), r, FormatName("openai"), []byte(`{"messages":[]}`), Options{})
			var status *fault.Error
			if !errors.As(err, &status) || status.Status != 400 {
				t.Fatalf("protected header accepted: %v", err)
			}
		})
	}
}

func TestPrepareBoundaryOptionsAndOverlayAreRequestLocal(t *testing.T) {
	r := catalog.Route{ProviderID: "opencode", ModelID: "m", Protocol: "openai-response", BaseURL: "https://opencode.ai/v1", Settings: map[string]any{"metadata": map[string]any{"source": "settings"}}, Body: map[string]any{"metadata": map[string]any{"source": "final"}}, Compatibility: map[string]any{}}
	before := catalog.Clone(r.Settings)
	for _, strict := range []bool{true, false, true, false} {
		raw, _, _, err := Prepare(context.Background(), r, FormatName("openai-response"), []byte(`{"input":"hello"}`), Options{StrictTextOnly: strict, Affinity: "session-affinity"})
		if err != nil {
			t.Fatal(err)
		}
		body := nativeTestBody(t, string(raw))
		if body["prompt_cache_key"] != "session-affinity" || object(body["metadata"])["source"] != "final" {
			t.Fatalf("options/overlay lost: %s", raw)
		}
		if (len(list(body["tools"])) == 0) != strict {
			t.Fatalf("strict mode leaked across request: %s", raw)
		}
	}
	if !reflect.DeepEqual(r.Settings, before) {
		t.Fatalf("preparation mutated route settings: %#v", r.Settings)
	}
	raw, _, _, err := Prepare(context.Background(), r, FormatName("openai-response"), []byte(`{"input":"hello","prompt_cache_key":"caller"}`), Options{Affinity: "derived"})
	if err != nil || nativeTestBody(t, string(raw))["prompt_cache_key"] != "caller" {
		t.Fatalf("explicit cache affinity was replaced: %s %v", raw, err)
	}
}

func TestPrepareBoundaryFinalOverlayCannotBypassSafety(t *testing.T) {
	for _, body := range []map[string]any{{"stream": false}, {"extra": "skip_thought_signature_validator"}} {
		r := catalog.Route{ModelID: "m", Protocol: "openai", BaseURL: "https://provider.example/v1", Body: body}
		if _, _, _, err := Prepare(context.Background(), r, FormatName("openai"), []byte(`{"messages":[]}`), Options{}); err == nil {
			t.Fatal("unsafe final overlay accepted", body)
		}
	}
}

func TestPrepareBoundaryAdapterNoticeUsesOnlyProvidedSink(t *testing.T) {
	r := catalog.Route{ModelID: "m", Protocol: "claude", BaseURL: "https://provider.example/v1"}
	raw := []byte(`{"messages":[],"system":[{"type":"text","text":"1","cache_control":{"type":"ephemeral"}},{"type":"text","text":"2","cache_control":{"type":"ephemeral"}},{"type":"text","text":"3","cache_control":{"type":"ephemeral"}},{"type":"text","text":"4","cache_control":{"type":"ephemeral"}},{"type":"text","text":"5","cache_control":{"type":"ephemeral"}}]}`)
	var notices bytes.Buffer
	sink := func(format string, args ...any) { fmt.Fprintf(&notices, format, args...) }
	if _, _, _, err := Prepare(context.Background(), r, FormatName("claude"), raw, Options{Logf: sink}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notices.String(), "dropped 1 cache breakpoint") {
		t.Fatalf("missing adapter notice: %s", &notices)
	}
	notices.Reset()
	if _, _, _, err := Prepare(context.Background(), r, FormatName("claude"), raw, Options{}); err != nil {
		t.Fatal(err)
	}
	if notices.Len() != 0 {
		t.Fatal("sink persisted across requests")
	}
}

func TestResponseConversionBoundaryFailsClosed(t *testing.T) {
	up, out := FormatName("claude"), FormatName("gemini")
	if ConversionAllowed(up, out) || ConversionSupported(up, out, false) || ConversionSupported(up, out, true) {
		t.Fatal("unsafe Claude/Gemini response conversion exposed")
	}
	if _, err := ConvertResponse(context.Background(), catalog.Route{Protocol: "claude"}, out, "m", nil, nil, []byte(`{}`)); err == nil {
		t.Fatal("unsafe response conversion accepted")
	}
	if err := Forward(context.Background(), strings.NewReader(""), catalog.Route{Protocol: "claude"}, out, nil, nil, func([]byte) error { t.Fatal("unsafe conversion emitted data"); return nil }); err == nil {
		t.Fatal("unsafe stream conversion accepted")
	}
	if _, err := ConvertResponse(context.Background(), catalog.Route{Protocol: "openai"}, FormatName("openai"), "m", nil, nil, []byte(`invalid`)); err == nil {
		t.Fatal("invalid native response accepted")
	}
}
