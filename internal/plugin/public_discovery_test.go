package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
	wire "github.com/megumin31/cpa-opencode-zen/internal/protocol"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestPublicDiscoverySkipsConsoleAndIsolatesCredentials(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			engine := newTestEngine(t)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(status) }))
			defer server.Close()
			engine.testConfig(t, server.URL)
			data := `{"opencode":{"npm":"@ai-sdk/openai-compatible","api":"` + server.URL + `/inference/v1","models":{"free":{"cost":{"input":0,"output":0}},"paid":{"cost":{"input":1,"output":2}},"missing":{}}}}`
			if err := os.WriteFile(engine.current.config.CatalogFile, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			c := testCredential(server.URL)
			c.Key = "public"
			routes, err := engine.discover(engine.current, context.Background(), c, "", false)
			if err != nil || len(routes) != 1 || routes["opencode-zen/free"].ModelID != "free" {
				t.Fatalf("routes=%v err=%v", routes, err)
			}
			if calls != 0 {
				t.Fatalf("public called Console %d times", calls)
			}
			real := testCredential(server.URL)
			routes, err = engine.discover(engine.current, context.Background(), real, "", false)
			if err == nil || routes != nil || calls != 1 {
				t.Fatalf("real key reused public cache: routes=%v err=%v calls=%d", routes, err, calls)
			}
			routes, err = engine.discover(engine.current, context.Background(), c, "", false)
			if err != nil || len(routes) != 1 || calls != 1 {
				t.Fatal("real key failure damaged isolated public cache")
			}
		})
	}
}

func TestPublicDiscoveryAndInferenceDirectOrWorker(t *testing.T) {
	for _, worker := range []bool{false, true} {
		t.Run(fmt.Sprint(worker), func(t *testing.T) {
			engine := newTestEngine(t)
			engine.current.config = defaultConfig()
			if worker {
				engine.current.config.WorkerEndpoint = "https://gateway.example"
				engine.current.config.WorkerToken = "fake-worker-token"
			}
			calls := 0
			engine.clientFactory = func(string) (*http.Client, error) {
				return &http.Client{Transport: discoveryRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					expected := "https://models.opencode.ai/api.json"
					if worker {
						expected = "https://gateway.example/models/api.json"
					}
					if calls == 1 {
						if r.URL.String() != expected || r.Header.Get("Authorization") != "" || r.Header.Get("X-Org-Id") != "" {
							t.Fatalf("unexpected public discovery: %v", r.URL)
						}
					} else {
						expected = "https://opencode.ai/inference/v1/chat/completions"
						if worker {
							expected = "https://gateway.example/opencode/inference/v1/chat/completions"
						}
						if r.URL.String() != expected || r.Header.Get("Authorization") != "Bearer public" {
							t.Fatalf("wrong inference route/auth: %v", r.URL)
						}
					}
					if worker && r.Header.Get("X-CPA-Gateway-Token") != "fake-worker-token" {
						t.Fatal("missing worker token")
					}
					if !worker && r.Header.Get("X-CPA-Gateway-Token") != "" {
						t.Fatal("worker token leaked in direct mode")
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"opencode":{"npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/inference/v1","models":{"free":{"cost":{"input":0,"output":0}}}}}`))}, nil
				})}, nil
			}
			c := credential{Key: "public", Server: "https://opencode.ai/console"}
			routes, err := engine.discover(engine.current, context.Background(), c, "", false)
			if err != nil || len(routes) != 1 {
				t.Fatalf("routes %v err %v", routes, err)
			}
			payload, target, headers, err := engine.prepareRequest(engine.current.config, context.Background(), c, routes["opencode-zen/free"], wire.FormatName("openai"), []byte(clientRequest("openai")))
			if err != nil {
				t.Fatal(err)
			}
			response, err := outbound(engine.current, context.Background(), c, "", "POST", target, headers, payload, true)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if calls != 2 {
				t.Fatalf("unexpected requests: %d", calls)
			}
		})
	}
}

func TestPublicFinalBodyCannotRedirectModel(t *testing.T) {
	engine := newTestEngine(t)
	routes, err := catalog.ResolvePublicRoutes([]byte(`{"opencode":{"npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/inference/v1","models":{"free":{"cost":{"input":0,"output":0}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	route := routes["opencode-zen/free"]
	c := credential{Key: "public"}
	payload, _, _, err := engine.prepareRequest(engine.current.config, context.Background(), c, route, wire.FormatName("openai"), []byte(`{"model":"paid","messages":[{"role":"user","content":"synthetic"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	json.Unmarshal(payload, &body)
	if body["model"] != "free" {
		t.Fatal("caller redirected model")
	}
	route.Body = map[string]any{"model": "paid"}
	if _, _, _, err := engine.prepareRequest(engine.current.config, context.Background(), c, route, wire.FormatName("openai"), []byte(clientRequest("openai"))); err == nil {
		t.Fatal("configured body redirected public model")
	}
}

func TestRealKeyAuthFailureClearsRoutesWithoutPublicFallback(t *testing.T) {
	for _, failure := range []int{401, 403} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			engine := newTestEngine(t)
			status := 200
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				io.WriteString(w, `{"providers":{}}`)
			}))
			defer server.Close()
			engine.testConfig(t, server.URL)
			if err := os.WriteFile(engine.current.config.CatalogFile, []byte(`{"opencode":{"npm":"@ai-sdk/openai-compatible","api":"`+server.URL+`/inference/v1","models":{"paid":{"cost":{"input":1,"output":1}},"free":{"cost":{"input":0,"output":0}}}}}`), 0600); err != nil {
				t.Fatal(err)
			}
			c := testCredential(server.URL)
			routes, err := engine.discover(engine.current, context.Background(), c, "", false)
			if err != nil || len(routes) != 2 {
				t.Fatalf("real-key baseline %v %v", routes, err)
			}
			status = failure
			for _, force := range []bool{true, false} {
				routes, err = engine.discover(engine.current, context.Background(), c, "", force)
				if err == nil || routes != nil {
					t.Fatal("stale or public fallback exposed after authentication failure")
				}
			}
			if calls != 3 {
				t.Fatalf("auth failure cached as success: %d", calls)
			}
		})
	}
}
