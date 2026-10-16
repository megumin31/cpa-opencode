package plugin

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
	"github.com/megumin31/cpa-opencode-zen/internal/fault"
)

func TestDiscoveryBaselineForEmptyConfigAnd404(t *testing.T) {
	engine := newTestEngine(t)

	for _, status := range []int{200, 404} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/console/api/v2/config" {
					t.Errorf("unexpected network request %s", r.URL.Path)
				}
				w.WriteHeader(status)
				io.WriteString(w, `{"providers":{}}`)
			}))
			defer server.Close()
			engine.testConfig(t, server.URL)
			catalog := `{"opencode":{"npm":"@ai-sdk/openai-compatible","api":"` + server.URL + `/inference/v1","models":{"m":{}}},"opencode-go":{"npm":"@ai-sdk/openai-compatible","api":"` + server.URL + `/inference/go/v1","models":{"not-implicitly-bound":{}}}}`
			if e := os.WriteFile(engine.current.config.CatalogFile, []byte(catalog), 0600); e != nil {
				t.Fatal(e)
			}
			routes, e := engine.discover(engine.current, context.Background(), testCredential(server.URL), "", false)
			if e != nil || len(routes) != 1 || routes["opencode-zen/m"].Protocol != "openai" {
				t.Fatalf("routes=%+v error=%v", routes, e)
			}
		})
	}
}
func TestDiscoveryDistinguishesCatalogAndConsoleParseErrors(t *testing.T) {
	engine := newTestEngine(t)

	for _, tc := range []struct{ name, config, catalog, want string }{
		{"catalog", `{"providers":{}}`, `{"p":null}`, "model catalog is invalid"},
		{"console", `{"providers":[]}`, `{}`, "Console returned an invalid provider configuration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tc.config) }))
			defer server.Close()
			engine.testConfig(t, server.URL)
			if e := os.WriteFile(engine.current.config.CatalogFile, []byte(tc.catalog), 0600); e != nil {
				t.Fatal(e)
			}
			_, e := engine.discover(engine.current, context.Background(), testCredential(server.URL), "", false)
			if e == nil || !strings.Contains(e.Error(), tc.want) || strings.Contains(e.Error(), "test-service-key") {
				t.Fatalf("unsafe/misleading error %v", e)
			}
		})
	}
}
func TestCatalogBaselineStillRespectsDestinationAndPackageBoundaries(t *testing.T) {
	engine := newTestEngine(t)

	engine.testConfig(t, "https://opencode.ai")
	routes := resolverTestRoutes(t, `{"providers":{}}`, `{"opencode":{"npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/inference/v1","models":{"safe":{},"untrusted":{"provider":{"api":"https://other.example/v1"}},"unknown":{"provider":{"npm":"unknown-package"}}}},"other":{"npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/inference/v1","models":{"must-not-bind":{}}}}`)
	models := modelInfos(engine.current.config, routes)
	if len(routes) != 3 || len(models) != 1 || models[0].ID != "opencode-zen/safe" {
		t.Fatalf("unexpected exposure: routes=%d models=%+v", len(routes), models)
	}
}

func TestDiscoveryDiagnosticsOnlyExposeStageAndStatus(t *testing.T) {
	engine := newTestEngine(t)

	var output bytes.Buffer
	previous := engine.logf
	engine.logf = log.New(&output, "", 0).Printf
	defer func() { engine.logf = previous }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		io.WriteString(w, `{"secret":"DO-NOT-LOG-BODY"}`)
	}))
	defer server.Close()
	engine.testConfig(t, server.URL)
	_, err := engine.discover(engine.current, context.Background(), testCredential(server.URL), "", false)
	if err == nil || err.(*fault.Error).Status != 403 {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "stage=console_config http_status=403") {
		t.Fatalf("missing HTTP stage: %q", text)
	}
	for _, secret := range []string{"test-service-key", "test-org", "DO-NOT-LOG-BODY", server.URL} {
		if strings.Contains(text, secret) {
			t.Fatalf("diagnostic leaked sensitive content")
		}
	}
}

func TestRouteDiagnosticsOnlyExposeCounts(t *testing.T) {
	engine := newTestEngine(t)

	engine.testConfig(t, "https://opencode.ai/inference")
	var output bytes.Buffer
	previous := engine.logf
	engine.logf = log.New(&output, "", 0).Printf
	defer func() { engine.logf = previous }()
	engine.logRouteCounts(engine.current.config, map[string]catalog.Route{
		"private-model-a": {Unsupported: "private-package-secret"},
		"private-model-b": {BaseURL: "https://private.example/secret"},
		"private-model-c": {BaseURL: "https://opencode.ai/inference/v1"},
	})
	text := output.String()
	if !strings.Contains(text, "stage=resolved_routes candidates=3 unsupported=1 blocked_destinations=1 exposed=1") {
		t.Fatalf("wrong aggregate diagnostic: %q", text)
	}
	for _, secret := range []string{"private", "secret", "https://", "model-a", "model-b", "model-c"} {
		if strings.Contains(text, secret) {
			t.Fatal("diagnostic leaked config data")
		}
	}
}

type discoveryRoundTripFunc func(*http.Request) (*http.Response, error)

func (f discoveryRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUnavailableCatalogRequiresUsableTrustedRoute(t *testing.T) {
	engine := newTestEngine(t)

	for _, tc := range []struct {
		name, config    string
		wantUnavailable bool
	}{
		{"unsupported_canonical_alias", `{"providers":{"alias":{"canonical":"openai","models":{"m":{}}}}}`, true},
		{"untrusted_endpoint", `{"providers":{"p":{"package":"@ai-sdk/openai-compatible","settings":{"baseURL":"https://untrusted.example/v1"},"models":{"m":{}}}}}`, true},
		{"usable_explicit_route", `{"providers":{"p":{"package":"@ai-sdk/openai-compatible","settings":{"baseURL":"https://opencode.ai/inference/v1"},"models":{"m":{}}}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine.testConfig(t, "https://opencode.ai")
			engine.current.Lock()
			engine.current.config.CatalogFile = ""
			engine.current.Unlock()
			previous := engine.clientFactory
			engine.clientFactory = func(string) (*http.Client, error) {
				return &http.Client{Transport: discoveryRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					status, body := 200, tc.config
					if r.URL.Host == "models.opencode.ai" {
						status, body = 503, "unavailable"
						if r.Header.Get("Authorization") != "" || r.Header.Get("x-org-id") != "" {
							t.Error("credential forwarded to public catalog")
						}
					} else if r.URL.String() != "https://opencode.ai/console/api/v2/config" {
						t.Errorf("unexpected request %s", r.URL)
					}
					return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				})}, nil
			}
			defer func() { engine.clientFactory = previous }()
			routes, err := engine.discover(engine.current, context.Background(), credential{Key: "fake", Server: "https://opencode.ai/console"}, "", false)
			if tc.wantUnavailable {
				if err == nil || err.(*fault.Error).Status != 503 || !strings.Contains(err.Error(), "catalog is unavailable") {
					t.Fatalf("want unavailable rather than empty success: %v routes=%d", err, len(routes))
				}
			} else if err != nil || len(modelInfos(engine.current.config, routes)) != 1 {
				t.Fatalf("valid explicit catalog.Route lost: %v", err)
			}
		})
	}
}
