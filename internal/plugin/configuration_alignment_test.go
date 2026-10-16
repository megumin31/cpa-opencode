package plugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	wire "github.com/megumin31/cpa-opencode-zen/internal/protocol"
)

// Wp Object.entries order and mutable K.get/models.update, from the official
// 2.0.29 embedded JS (console-code.txt). All data and wire traffic are synthetic.
func TestConsoleOrderedMutableInheritanceWire(t *testing.T) {
	engine := newTestEngine(t)

	var received bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		received = true
		if req.Method != "POST" || req.URL.Path != "/console-catalog.Route/chat/completions" {
			t.Errorf("wrong target: %s %s", req.Method, req.URL)
		}
		if req.Header.Get("X-Provider") != "console" || req.Header.Get("X-Model") != "updated" || req.Header.Get("Authorization") != "Bearer fake-key" {
			t.Errorf("wrong headers: %v", req.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "a-remap" || body["provider_marker"] != "new" || body["model_marker"] != "updated" {
			t.Errorf("wrong body: %#v", body)
		}
		io.WriteString(w, `{}`)
	}))
	defer server.Close()
	engine.testConfig(t, server.URL)
	catalog := `{"base":{"package":"@opencode/ai/providers/openai/chat","settings":{"baseURL":"` + server.URL + `/old"},"headers":{"X-Provider":"old"},"models":{"seed":{"body":{"model_marker":"old"},"headers":{"X-Model":"old"}}}}}`
	config := `{"providers":{"z-first":{"canonical":"base","settings":{"baseURL":"` + server.URL + `/console-catalog.Route"},"headers":{"X-Provider":"console"},"body":{"provider_marker":"new"},"models":{"z-source":{"modelID":"seed","body":{"model_marker":"updated"},"headers":{"X-Model":"updated"}}}},"m-middle":{"canonical":"z-first","models":{"z-remap":{"modelID":"z-source"},"a-remap":{"modelID":"z-source"}}},"a-last":{"canonical":"m-middle","models":{"final":{"modelID":"a-remap"}}}}}`
	routes := resolverTestRoutes(t, config, catalog)
	r, ok := routes["opencode/a-last/final"]
	if !ok || r.Unsupported != "" || r.BaseURL != server.URL+"/console-catalog.Route" {
		t.Fatalf("catalog.Route: %+v", r)
	}
	if _, ok := routes["opencode/a-last/z-remap"]; ok {
		t.Fatal("new alias inherited unrequested models")
	}
	c := credential{Key: "fake-key"}
	raw, target, headers, err := engine.prepareRequest(engine.current.config, context.Background(), c, r, wire.FormatName("openai"), []byte(clientRequest("openai")))
	if err != nil {
		t.Fatal(err)
	}
	response, err := outbound(engine.current, context.Background(), c, "", "POST", target, headers, raw, true)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !received {
		t.Fatal("no final wire request")
	}
}

func TestConsoleSameProviderOrderedRemaps(t *testing.T) {

	catalog := `{"p":{"package":"@opencode/ai/providers/openai/chat","models":{"seed":{"body":{"marker":"catalog"}}}}}`
	config := `{"providers":{"p":{"models":{"z-source":{"modelID":"seed","body":{"marker":"updated"}},"a-remap":{"modelID":"z-source"},"disabled":{"modelID":"a-remap","disabled":true},"enabled-copy":{"modelID":"disabled"}}}}}`
	routes := resolverTestRoutes(t, config, catalog)
	for _, id := range []string{"a-remap", "enabled-copy"} {
		if routes["opencode/p/"+id].Body["marker"] != "updated" {
			t.Fatalf("ordered model inheritance lost: %+v", routes["opencode/p/"+id])
		}
	}
	if _, ok := routes["opencode/p/disabled"]; ok {
		t.Fatal("disabled model exposed")
	}
	if routes["opencode/p/seed"].Body["marker"] != "catalog" {
		t.Fatal("source mutated by alias")
	}
}

func TestConsoleCanonicalMissingModelPreservesTarget(t *testing.T) {

	for _, canonical := range []string{"other", "absent"} {
		t.Run(canonical, func(t *testing.T) {
			catalog := `{"p":{"package":"@opencode/ai/providers/openai/chat","settings":{"baseURL":"https://opencode.ai/inference/own"},"models":{"keep":{"settings":{"baseURL":"https://opencode.ai/inference/model"},"body":{"retained":true},"headers":{"X-Keep":"yes"},"limit":{"output":99}}}},"other":{"package":"@opencode/ai/providers/openai/chat","models":{}}}`
			routes := resolverTestRoutes(t, `{"providers":{"p":{"canonical":"`+canonical+`","models":{"keep":{"modelID":"missing-deployment"}}}}}`, catalog)
			r := routes["opencode/p/keep"]
			if r.ModelID != "missing-deployment" || r.Body["retained"] != true || r.Headers["X-Keep"] != "yes" || r.Limit["output"] != float64(99) || r.BaseURL != "https://opencode.ai/inference/model" {
				t.Fatalf("target model fallback lost: %+v", r)
			}
		})
	}
}

func TestDiscoveryCatalogIndependentTTLAndFileFallback(t *testing.T) {
	engine := newTestEngine(t)

	consoleCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { consoleCalls++; io.WriteString(w, `{"providers":{}}`) }))
	defer server.Close()
	engine.testConfig(t, server.URL)
	originalNow := engine.now
	clock := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	engine.now = func() time.Time { return clock }
	defer func() { engine.now = originalNow }()
	path := engine.current.config.CatalogFile
	write := func(model string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(`{"opencode":{"package":"@opencode/ai/providers/openai/chat","settings":{"baseURL":"`+server.URL+`/v1"},"models":{"`+model+`":{}}}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	get := func(want string) {
		t.Helper()
		r, e := engine.discover(engine.current, context.Background(), testCredential(server.URL), "", false)
		if e != nil || len(r) != 1 || r["opencode-zen/"+want].ModelID != want {
			t.Fatalf("want %s: %v %+v", want, e, r)
		}
	}
	write("old")
	get("old")
	write("new")
	clock = clock.Add(4*time.Minute + 59*time.Second)
	get("old")
	if consoleCalls != 2 {
		t.Fatal(consoleCalls)
	}
	clock = clock.Add(time.Second)
	get("new") // Console is fresh, catalog is not.
	if consoleCalls != 3 {
		t.Fatal("catalog expiry hidden by fresh Console routes")
	}
	clock = clock.Add(5 * time.Minute)
	if err := os.WriteFile(path, []byte(`{"invalid":null}`), 0600); err != nil {
		t.Fatal(err)
	}
	get("new")
	clock = clock.Add(time.Minute)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	get("new")
	write("recovered")
	clock = clock.Add(time.Minute)
	get("recovered")
}

func TestDiscoveryPublicCatalogRefreshDeadlineAndStaleFallback(t *testing.T) {
	engine := newTestEngine(t)

	engine.testConfig(t, "https://opencode.ai")
	engine.current.Lock()
	engine.current.config.CatalogFile = ""
	engine.current.Unlock()
	originalNow := engine.now
	clock := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	engine.now = func() time.Time { return clock }
	defer func() { engine.now = originalNow }()
	previous := engine.clientFactory
	defer func() { engine.clientFactory = previous }()
	catalogCalls := 0
	broken := false
	marker := "first"
	engine.clientFactory = func(string) (*http.Client, error) {
		return &http.Client{Transport: discoveryRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			status, body := 200, `{"providers":{"opencode":{"body":{"console_marker":"`+marker+`"}}}}`
			if r.URL.Host == "models.opencode.ai" {
				catalogCalls++
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) > catalogFetchTimeout || time.Until(deadline) < 9*time.Second {
					t.Errorf("missing 10-second public fetch bound: %v", deadline)
				}
				if r.Header.Get("Authorization") != "" || r.Header.Get("x-org-id") != "" {
					t.Error("public catalog got credentials")
				}
				body = `{"opencode":{"package":"@opencode/ai/providers/openai/chat","settings":{"baseURL":"https://opencode.ai/inference/v1"},"models":{"m":{}}}}`
				if broken {
					body = `{"bad":null}`
				}
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})}, nil
	}
	c := testCredential("https://opencode.ai")
	for _, step := range []struct {
		advance   time.Duration
		wantCalls int
		fail      bool
		marker    string
	}{{0, 1, false, "first"}, {time.Minute, 1, false, "second"}, {4 * time.Minute, 2, true, "third"}, {time.Minute, 3, false, "fourth"}} {
		clock = clock.Add(step.advance)
		broken = step.fail
		marker = step.marker
		r, e := engine.discover(engine.current, context.Background(), c, "", false)
		if e != nil || r["opencode-zen/m"].Body["console_marker"] != marker || catalogCalls != step.wantCalls {
			t.Fatalf("step %+v: %v %+v calls=%d", step, e, r, catalogCalls)
		}
	}
}

func TestDiscoveryInvalidCatalogRemainsErrorDuringBackoff(t *testing.T) {
	engine := newTestEngine(t)

	for _, public := range []bool{false, true} {
		name := "file"
		if public {
			name = "public"
		}
		t.Run(name, func(t *testing.T) {
			engine.testConfig(t, "https://opencode.ai")
			if public {
				engine.current.Lock()
				engine.current.config.CatalogFile = ""
				engine.current.Unlock()
			} else if err := os.WriteFile(engine.current.config.CatalogFile, []byte(`{"invalid":null}`), 0600); err != nil {
				t.Fatal(err)
			}
			previous := engine.clientFactory
			defer func() { engine.clientFactory = previous }()
			engine.clientFactory = func(string) (*http.Client, error) {
				return &http.Client{Transport: discoveryRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					body := `{"providers":{"p":{"package":"@opencode/ai/providers/openai/chat","settings":{"baseURL":"https://opencode.ai/inference/v1"},"models":{"m":{}}}}}`
					if r.URL.Host == "models.opencode.ai" {
						body = `{"invalid":null}`
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				})}, nil
			}
			for i := 0; i < 2; i++ {
				r, e := engine.discover(engine.current, context.Background(), testCredential("https://opencode.ai"), "", false)
				if e == nil || len(r) != 0 || !strings.Contains(e.Error(), "model catalog is invalid") {
					t.Fatalf("attempt %d bypassed invalid catalog: %v %+v", i, e, r)
				}
			}
		})
	}
}

func TestConsoleNewProviderDoesNotInventCanonicalSource(t *testing.T) {

	r := resolverTestRoutes(t, `{"providers":{"new":{"package":"@opencode/ai/providers/openai/chat","models":{"first":{"body":{"marker":"first"}},"second":{"modelID":"first"}}}}}`, `{}`)
	if r["opencode/new/second"].Body["marker"] != nil {
		t.Fatal("a canonical source absent at provider lookup was invented")
	}
}

func TestConsoleModelArrayIndexOrder(t *testing.T) {

	r := resolverTestRoutes(t, `{"providers":{"p":{"models":{"10":{"modelID":"2"},"2":{"body":{"marker":"second"}}}}}}`, `{"p":{"package":"@opencode/ai/providers/openai/chat","models":{}}}`)
	if r["opencode/p/10"].Body["marker"] != "second" {
		t.Fatal("model object did not use JavaScript array-index order")
	}
}

func TestDiscoveryExpiredCatalogConsoleOutageBackoff(t *testing.T) {
	engine := newTestEngine(t)

	calls := 0
	broken := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if broken {
			w.WriteHeader(503)
			return
		}
		io.WriteString(w, `{"providers":{}}`)
	}))
	defer server.Close()
	engine.testConfig(t, server.URL)
	originalNow := engine.now
	clock := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	engine.now = func() time.Time { return clock }
	defer func() { engine.now = originalNow }()
	c := testCredential(server.URL)
	if _, err := engine.discover(engine.current, context.Background(), c, "", false); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(5 * time.Minute)
	broken = true
	for i := 0; i < 3; i++ {
		if _, err := engine.discover(engine.current, context.Background(), c, "", false); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("expired catalog caused Console retry storm: %d", calls)
	}
}
