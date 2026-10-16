// servicecheck exercises the unmodified CPA 8.0.29 Service startup and real
// file watcher using only a fake auth file, a local public catalog snapshot,
// and loopback HTTP servers. It never sends inference requests.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const pluginID = "opencode"

func main() {
	watchdog := time.AfterFunc(45*time.Second, func() { fmt.Fprintln(os.Stderr, "FAIL: servicecheck exceeded 45 seconds"); os.Exit(1) })
	defer watchdog.Stop()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

func run() error {
	pluginPath := flag.String("plugin", "../../build/opencode-v0.1.0.so", "native plugin binary")
	catalogPath := flag.String("catalog", "/workspace/shared/opencode-v2-inspection/public-catalog-20261015.json", "trusted public catalog snapshot")
	referencePath := flag.String("reference", "/workspace/shared/cliproxyapi-reference", "CPA reference checkout for local catalog files")
	packageOverride := flag.Bool("package-override", false, "mock explicit opencode provider package override")
	watcherCycle := flag.Bool("watcher-cycle", true, "verify actual file watcher disable and re-enable publication")
	debug := flag.Bool("debug", false, "enable CPA debug logs")
	flag.Parse()
	log.SetLevel(log.WarnLevel)
	if *debug {
		log.SetLevel(log.DebugLevel)
	}
	os.Setenv("GIN_MODE", "release")

	tmp, err := os.MkdirTemp("", "opencode-servicecheck-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	authDir, pluginDir := filepath.Join(tmp, "auth"), filepath.Join(tmp, "plugins")
	for _, dir := range []string{authDir, pluginDir} {
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
	}
	binary, err := os.ReadFile(*pluginPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "opencode-v0.1.0.so"), binary, 0700); err != nil {
		return err
	}
	catalog, err := filepath.Abs(*catalogPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(catalog); err != nil {
		return err
	}

	var consoleCalls, inferenceCalls, blockedRequests, blockedVersionChecks atomic.Int32
	var consoleHost string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This server is also the only allowed outbound proxy. It never forwards
		// anything: even accidental remote requests and CONNECTs stop here.
		if r.Method == http.MethodGet && r.URL.Path == "/console/api/v2/config" && r.Host == consoleHost {
			if r.Header.Get("Authorization") != "Bearer servicecheck-fake-key" {
				http.Error(w, "wrong fake credential", 401)
				return
			}
			consoleCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if *packageOverride {
				io.WriteString(w, `{"providers":{"opencode":{"package":"@opencode/ai/providers/openai-compatible"}}}`)
			} else {
				io.WriteString(w, `{"providers":{}}`)
			}
			return
		}
		// CPA unconditionally starts its Grok version updater even without xAI
		// auth. Reject its CONNECT locally; there is no upstream socket or TLS.
		if r.Method == http.MethodConnect && r.Host == "registry.npmjs.org:443" {
			blockedVersionChecks.Add(1)
			http.Error(w, "offline: Grok version lookup disabled by proxy", http.StatusBadGateway)
			return
		}
		if strings.Contains(r.URL.Path, "inference") || r.Method == http.MethodPost {
			inferenceCalls.Add(1)
		}
		blockedRequests.Add(1)
		http.Error(w, "servicecheck blocks all non-Console outbound requests", http.StatusBadGateway)
	}))
	defer mock.Close()
	consoleHost = strings.TrimPrefix(mock.URL, "http://")

	// Block any CPA-side accidental external dial. The native plugin has a
	// separate Go runtime, so its explicitly configured proxy above supplies
	// the equivalent isolation. All three CPA catalogs use local paths.
	oldTransport := http.DefaultTransport
	safeTransport := oldTransport.(*http.Transport).Clone()
	safeTransport.Proxy = nil
	safeTransport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			blockedRequests.Add(1)
			return nil, fmt.Errorf("non-loopback dial prohibited")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	http.DefaultTransport = safeTransport
	defer func() { safeTransport.CloseIdleConnections(); http.DefaultTransport = oldTransport }()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return err
	}
	var node yaml.Node
	pluginYAML := fmt.Sprintf("enabled: true\nallow_loopback_http: true\nrequest_timeout_seconds: 5\ncatalog_file: %q\nproxy_url: %q\nallowed_destinations:\n  - %q\n  - https://opencode.ai/inference\n", catalog, mock.URL, mock.URL)
	if err := yaml.Unmarshal([]byte(pluginYAML), &node); err != nil {
		return err
	}
	enabled := true
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{ProxyURL: mock.URL, APIKeys: []string{"servicecheck-client-key"}},
		Host:      "127.0.0.1", Port: port, AuthDir: authDir,
		RemoteManagement: config.RemoteManagement{DisableControlPanel: true, DisableAutoUpdatePanel: true},
		Plugins:          config.PluginsConfig{Enabled: true, Dir: pluginDir, Configs: map[string]config.PluginInstanceConfig{pluginID: {Enabled: &enabled, Raw: *node.Content[0]}}},
		Models: config.ModelCatalogs{
			Catalog:      filepath.Join(*referencePath, "internal/registry/models/models.json"),
			CodexCatalog: filepath.Join(*referencePath, "internal/registry/models/codex_client_models.json"),
			DevinCatalog: filepath.Join(*referencePath, "internal/registry/models/devin_models.json"),
		},
	}
	for _, p := range []string{cfg.Models.Catalog, cfg.Models.CodexCatalog, cfg.Models.DevinCatalog} {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("CPA catalog path must be absolute: %s", p)
		}
		if _, err := os.Stat(p); err != nil {
			return err
		}
	}
	configPath := filepath.Join(tmp, "config.yaml")
	configBytes, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(configPath, configBytes, 0600); err != nil {
		return err
	}
	authPath := filepath.Join(authDir, "servicecheck-fake.json")
	fakeAuth := map[string]any{"type": pluginID, "key": "servicecheck-fake-key", "server": mock.URL + "/console"}
	writeFakeAuth := func(disabled bool) error {
		fakeAuth["disabled"] = disabled
		raw, err := json.Marshal(fakeAuth)
		if err != nil {
			return err
		}
		return os.WriteFile(authPath, raw, 0600)
	}
	if err := writeFakeAuth(false); err != nil {
		return err
	}
	raw, _ := json.Marshal(fakeAuth)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host := pluginhost.New()
	defer host.ShutdownAll()
	host.ApplyConfig(ctx, cfg)
	registered := host.RegisteredPlugins()
	if len(registered) != 1 || registered[0].ID != pluginID {
		return fmt.Errorf("plugin failed registration: %+v", registered)
	}
	auth, handled, err := host.ParseAuth(ctx, pluginapi.AuthParseRequest{Provider: pluginID, FileName: filepath.Base(authPath), RawJSON: raw})
	if err != nil || !handled || auth == nil {
		return fmt.Errorf("direct auth parsing handled=%v error=%v", handled, err)
	}
	result := host.ModelsForAuth(ctx, auth)
	if result.Err != nil || !result.Handled {
		return fmt.Errorf("direct model discovery handled=%v error=%v", result.Handled, result.Err)
	}
	directIDs := make([]string, 0, len(result.Models))
	for _, m := range result.Models {
		directIDs = append(directIDs, m.ID)
	}
	sort.Strings(directIDs)
	fmt.Printf("DIRECT: models=%d Console_calls=%d package_override=%v\n", len(directIDs), consoleCalls.Load(), *packageOverride)
	if len(directIDs) == 0 {
		return fmt.Errorf("direct discovery returned zero models before Service startup")
	}

	// No custom manager, auth store, token provider, or watcher. These are the
	// exact default Builder and Service paths used by the pinned CPA host.
	service, err := cliproxy.NewBuilder().WithConfig(cfg).WithConfigPath(configPath).WithPluginHost(host).Build()
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	defer func() {
		cancel()
		shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := service.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintln(os.Stderr, "shutdown:", err)
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			fmt.Fprintln(os.Stderr, "shutdown did not return within 5 seconds")
		}
	}()
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/models", port)
	client := &http.Client{Transport: safeTransport, Timeout: 2 * time.Second}
	getModels := func() ([]string, error) {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer servicecheck-client-key")
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("/v1/models HTTP %d", resp.StatusCode)
		}
		var body struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(body.Data))
		for _, model := range body.Data {
			ids = append(ids, model.ID)
		}
		sort.Strings(ids)
		return ids, nil
	}
	awaitModels := func(want int) ([]string, error) {
		deadline := time.Now().Add(8 * time.Second)
		var ids []string
		var lastErr error
		for time.Now().Before(deadline) {
			select {
			case err := <-done:
				return nil, fmt.Errorf("Service exited early: %w", err)
			default:
			}
			ids, lastErr = getModels()
			if lastErr == nil && len(ids) == want {
				return ids, nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		return ids, fmt.Errorf("/v1/models count=%d want=%d last_error=%v", len(ids), want, lastErr)
	}
	endpointIDs, endpointErr := awaitModels(len(directIDs))
	stored, storeErr := sdkAuth.GetTokenStore().List(ctx)
	registryCount := len(cliproxy.GlobalModelRegistry().GetAvailableModelsByProvider(pluginID))
	fmt.Printf("SERVICE: /v1/models=%d provider_registry=%d file_store_auths=%d Console_calls=%d store_error=%v\n", len(endpointIDs), registryCount, len(stored), consoleCalls.Load(), storeErr)
	for _, a := range stored {
		fmt.Printf("AUTH: provider=%s auth_kind=%s disabled=%v registry_models=%d\n", a.Provider, a.AuthKind(), a.Disabled, len(cliproxy.GlobalModelRegistry().GetModelsForClient(a.ID)))
	}
	if endpointErr != nil {
		return endpointErr
	}
	if strings.Join(endpointIDs, "\n") != strings.Join(directIDs, "\n") {
		return fmt.Errorf("endpoint model IDs differ from direct plugin model IDs")
	}
	if *watcherCycle {
		// Allow watcher startup after Service's documented 100 ms server delay.
		time.Sleep(300 * time.Millisecond)
		if err := writeFakeAuth(true); err != nil {
			return err
		}
		if _, err := awaitModels(0); err != nil {
			return fmt.Errorf("watcher disable: %w", err)
		}
		if err := writeFakeAuth(false); err != nil {
			return err
		}
		if _, err := awaitModels(len(directIDs)); err != nil {
			return fmt.Errorf("watcher re-enable: %w", err)
		}
		fmt.Println("WATCHER: auth disable removes models; re-enable restores all models")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		done <- nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("Service did not shut down")
	}
	if inferenceCalls.Load() != 0 || blockedRequests.Load() != 0 {
		return fmt.Errorf("unexpected requests: inference=%d blocked_external_or_other=%d", inferenceCalls.Load(), blockedRequests.Load())
	}
	fmt.Printf("PASS: direct and full Service model IDs match (%d); actual default file watcher; inference_calls=0; blocked_external_or_other=0; locally_rejected_Grok_version_checks=%d; localhost_only=true\n", len(directIDs), blockedVersionChecks.Load())
	return nil
}
