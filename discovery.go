package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type discoveryEntry struct {
	gate              chan struct{}
	routes            map[string]route
	expires           time.Time
	catalogExpires    time.Time
	catalogRetryAfter time.Time
	catalogError      error
	catalog           []byte
	lastError         error
}

func discover(ctx context.Context, c credential, hostProxy string, force bool) (map[string]route, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	key := c.fingerprint() + "\x00" + hostProxy
	state.Lock()
	entry := state.cache[key]
	if entry == nil {
		entry = &discoveryEntry{gate: make(chan struct{}, 1)}
		state.cache[key] = entry
	}
	state.Unlock()
	select {
	case entry.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, fail(499, "discovery request canceled")
	}
	defer func() { <-entry.gate }()
	if !force && entry.routes != nil && now().Before(entry.expires) && !catalogRefreshDue(entry) {
		return entry.routes, nil
	}
	headers := http.Header{"Authorization": {"Bearer " + c.Key}, "Accept": {"application/json"}}
	if c.OrgID != "" {
		headers.Set("x-org-id", c.OrgID)
	}
	resp, err := outbound(ctx, c, hostProxy, "GET", c.Server+"/api/v2/config", headers, nil, true)
	if err != nil {
		return staleDiscovery(entry, err)
	}
	defer resp.Body.Close()
	// Status-only diagnostics distinguish authenticated Console HTTP from public
	// catalog parsing. Never include credentials, headers, response bodies, URLs,
	// organization IDs, or model names in this log. A 2xx alone is not entitlement.
	log.Printf("OpenCode discovery: stage=console_config http_status=%d", resp.StatusCode)
	// Native aR treats 404 as no Console overrides, not no catalog models.
	if resp.StatusCode != 404 && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		err = upstreamError(resp.StatusCode)
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			entry.routes = nil
			entry.expires = time.Time{}
			entry.lastError = err
			return nil, err
		}
		return staleDiscovery(entry, err)
	}
	raw := []byte(`{"providers":{}}`)
	if resp.StatusCode != 404 {
		raw, err = readBounded(resp.Body, maxResponseBytes)
		if err != nil {
			return staleDiscovery(entry, err)
		}
	}
	catalog, cacheCatalog, err := discoveryCatalog(ctx, entry, hostProxy)
	if err != nil {
		return staleDiscovery(entry, err)
	}
	routes, err := resolveRoutes(raw, catalog)
	if err != nil {
		return staleDiscovery(entry, fail(502, "OpenCode Console returned an invalid provider configuration"))
	}
	logRouteCounts(routes)
	if !cacheCatalog && len(modelInfos(routes)) == 0 {
		return staleDiscovery(entry, fail(503, "public model catalog is unavailable and Console supplied no usable routes; configure a trusted catalog_file snapshot"))
	}
	entry.routes = routes
	entry.expires = now().Add(time.Minute)
	entry.lastError = nil
	return routes, nil
}

// Catalog freshness is independent of the one-minute Console route cache.
// Refresh is demand-driven; it does not retain credentials for background work
// or push idle model-registration changes into the host.
const catalogTTL = 5 * time.Minute
const catalogFetchTimeout = 10 * time.Second

func catalogRefreshDue(entry *discoveryEntry) bool {
	return !now().Before(entry.catalogExpires) && !now().Before(entry.catalogRetryAfter)
}

func discoveryCatalog(ctx context.Context, entry *discoveryEntry, hostProxy string) ([]byte, bool, error) {
	if !catalogRefreshDue(entry) {
		if len(entry.catalog) > 0 {
			return entry.catalog, true, nil
		}
		if entry.catalogError != nil {
			return nil, false, entry.catalogError
		}
		return []byte(`{}`), false, nil
	}
	cfg := loadedConfig()
	var catalog []byte
	var err error
	invalid := false
	if cfg.CatalogFile != "" {
		file, openErr := os.Open(cfg.CatalogFile)
		if openErr != nil {
			err = fail(500, "cannot read configured model catalog")
		} else {
			catalog, err = readBounded(file, maxResponseBytes)
			file.Close()
			if err != nil {
				err = fail(400, "cannot read configured model catalog within size limit")
			}
		}
	} else {
		// Bound both headers and body; public metadata never gets credentials.
		fetchCtx, cancel := context.WithTimeout(ctx, catalogFetchTimeout)
		defer cancel()
		public, fetchErr := outbound(fetchCtx, credential{}, hostProxy, "GET", "https://models.opencode.ai/api.json", http.Header{"Accept": {"application/json"}}, nil, false)
		err = fetchErr
		if fetchErr == nil {
			if public.StatusCode == http.StatusOK {
				catalog, err = readBounded(public.Body, maxResponseBytes)
			} else {
				err = fail(503, "public model catalog is unavailable")
			}
			public.Body.Close()
		}
	}
	if err == nil {
		if _, parseErr := parseCatalog(catalog); parseErr != nil {
			invalid = true
			err = fail(502, "model catalog is invalid; Console authentication status cannot be inferred from this parse failure")
		}
	}
	if err != nil {
		// Keep only previously validated metadata. Failure backoff must not
		// advance its actual freshness timestamp or discard new Console data.
		entry.catalogRetryAfter = now().Add(time.Minute)
		if len(entry.catalog) > 0 {
			return entry.catalog, true, nil
		}
		if cfg.CatalogFile != "" || invalid {
			entry.catalogError = err
			return nil, false, err
		}
		entry.catalogError = nil
		return []byte(`{}`), false, nil
	}
	entry.catalog = catalog
	entry.catalogError = nil
	entry.catalogExpires = now().Add(catalogTTL)
	entry.catalogRetryAfter = time.Time{}
	return catalog, true, nil
}

// Keep diagnostics aggregate-only. In particular, never log the Unsupported
// string: it may contain a package name supplied by private workspace config.
func logRouteCounts(routes map[string]route) {
	unsupported, blocked, exposed := 0, 0, 0
	cfg := loadedConfig()
	for _, r := range routes {
		if r.Unsupported != "" {
			unsupported++
		} else if allowedURL(cfg, r.BaseURL) != nil {
			blocked++
		} else {
			exposed++
		}
	}
	log.Printf("OpenCode discovery: stage=resolved_routes candidates=%d unsupported=%d blocked_destinations=%d exposed=%d", len(routes), unsupported, blocked, exposed)
}
func staleDiscovery(entry *discoveryEntry, err error) (map[string]route, error) {
	entry.lastError = err
	if entry.routes != nil {
		entry.expires = now().Add(time.Minute)
		// An expired catalog must not turn a Console outage into a refresh
		// on every request while stale routes are intentionally being served.
		if catalogRefreshDue(entry) {
			entry.catalogRetryAfter = entry.expires
		}
		return entry.routes, nil
	}
	return nil, err
}
func invalidate(c credential) {
	state.Lock()
	defer state.Unlock()
	for key := range state.cache {
		if strings.HasPrefix(key, c.fingerprint()+"\x00") {
			delete(state.cache, key)
		}
	}
}
func modelInfos(routes map[string]route) []pluginapi.ModelInfo {
	keys := make([]string, 0, len(routes))
	for k := range routes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]pluginapi.ModelInfo, 0, len(keys))
	for _, key := range keys {
		r := routes[key]
		if r.Unsupported != "" {
			continue
		}
		if allowedURL(loadedConfig(), r.BaseURL) != nil {
			continue
		}
		m := pluginapi.ModelInfo{ID: r.Alias, Object: "model", OwnedBy: providerID, DisplayName: r.ModelKey, Name: r.ModelID, Description: "OpenCode Console " + r.ProviderID + " (" + r.Protocol + ")", UserDefined: true, SupportedGenerationMethods: []string{"generateContent"}}
		m.ContextLength = integer(r.Limit["context"])
		m.InputTokenLimit = integer(r.Limit["input"])
		m.OutputTokenLimit = integer(r.Limit["output"])
		m.MaxCompletionTokens = m.OutputTokenLimit
		if v, ok := r.Capabilities["reasoning"].(bool); ok && v {
			m.Thinking = &pluginapi.ThinkingSupport{DynamicAllowed: true, ZeroAllowed: true}
		}
		out = append(out, m)
	}
	return out
}
func integer(v any) int64 {
	switch v := v.(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case json.Number:
		i, _ := v.Int64()
		return i
	}
	return 0
}
