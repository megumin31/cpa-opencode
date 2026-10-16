package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
	"github.com/megumin31/cpa-opencode-zen/internal/transport"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type discoveryEntry struct {
	gate              chan struct{}
	routes            map[string]catalog.Route
	expires           time.Time
	catalogExpires    time.Time
	catalogRetryAfter time.Time
	catalogError      error
	catalog           []byte
	lastError         error
}

func (e *Engine) discover(g *generation, ctx context.Context, c credential, hostProxy string, force bool) (map[string]catalog.Route, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	key := c.fingerprint() + "\x00" + hostProxy
	g.Lock()
	entry := g.cache[key]
	if entry == nil {
		entry = &discoveryEntry{gate: make(chan struct{}, 1)}
		g.cache[key] = entry
	}
	g.Unlock()
	select {
	case entry.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, fail(499, "discovery request canceled")
	}
	defer func() { <-entry.gate }()
	if !force && entry.routes != nil && e.now().Before(entry.expires) && !e.catalogRefreshDue(entry) {
		return entry.routes, nil
	}
	headers := http.Header{"Authorization": {"Bearer " + c.Key}, "Accept": {"application/json"}}
	if c.OrgID != "" {
		headers.Set("x-org-id", c.OrgID)
	}
	resp, err := outbound(g, ctx, c, hostProxy, "GET", c.Server+"/api/v2/config", headers, nil, true)
	if err != nil {
		return e.staleDiscovery(entry, err)
	}
	defer resp.Body.Close()
	// Status-only diagnostics distinguish authenticated Console HTTP from public
	// catalog parsing. Never include credentials, headers, response bodies, URLs,
	// organization IDs, or model names in this log. A 2xx alone is not entitlement.
	e.logf("OpenCode discovery: stage=console_config http_status=%d", resp.StatusCode)
	// Native aR treats 404 as no Console overrides, not no catalog models.
	if resp.StatusCode != 404 && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		err = transport.UpstreamError(resp.StatusCode)
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			entry.routes = nil
			entry.expires = time.Time{}
			entry.lastError = err
			return nil, err
		}
		return e.staleDiscovery(entry, err)
	}
	raw := []byte(`{"providers":{}}`)
	if resp.StatusCode != 404 {
		raw, err = transport.ReadBounded(resp.Body, transport.MaxResponseBytes)
		if err != nil {
			return e.staleDiscovery(entry, err)
		}
	}
	catalogData, cacheCatalog, err := e.discoveryCatalog(g, ctx, entry, hostProxy)
	if err != nil {
		return e.staleDiscovery(entry, err)
	}
	routes, err := catalog.ResolveRoutes(raw, catalogData)
	if err != nil {
		return e.staleDiscovery(entry, fail(502, "OpenCode Console returned an invalid provider configuration"))
	}
	e.logRouteCounts(g.config, routes)
	if !cacheCatalog && len(modelInfos(g.config, routes)) == 0 {
		return e.staleDiscovery(entry, fail(503, "public model catalog is unavailable and Console supplied no usable routes; configure a trusted catalog_file snapshot"))
	}
	entry.routes = routes
	entry.expires = e.now().Add(time.Minute)
	entry.lastError = nil
	return routes, nil
}

// Catalog freshness is independent of the one-minute Console catalog.Route cache.
// Refresh is demand-driven; it does not retain credentials for background work
// or push idle model-registration changes into the host.
const catalogTTL = 5 * time.Minute
const catalogFetchTimeout = 10 * time.Second

func (e *Engine) catalogRefreshDue(entry *discoveryEntry) bool {
	return !e.now().Before(entry.catalogExpires) && !e.now().Before(entry.catalogRetryAfter)
}

func (e *Engine) discoveryCatalog(g *generation, ctx context.Context, entry *discoveryEntry, hostProxy string) ([]byte, bool, error) {
	if !e.catalogRefreshDue(entry) {
		if len(entry.catalog) > 0 {
			return entry.catalog, true, nil
		}
		if entry.catalogError != nil {
			return nil, false, entry.catalogError
		}
		return []byte(`{}`), false, nil
	}
	cfg := g.config
	var catalogData []byte
	var err error
	invalid := false
	if cfg.CatalogFile != "" {
		file, openErr := os.Open(cfg.CatalogFile)
		if openErr != nil {
			err = fail(500, "cannot read configured model catalog")
		} else {
			catalogData, err = transport.ReadBounded(file, transport.MaxResponseBytes)
			file.Close()
			if err != nil {
				err = fail(400, "cannot read configured model catalog within size limit")
			}
		}
	} else {
		// Bound both headers and body; public metadata never gets credentials.
		fetchCtx, cancel := context.WithTimeout(ctx, catalogFetchTimeout)
		defer cancel()
		public, fetchErr := outbound(g, fetchCtx, credential{}, hostProxy, "GET", "https://models.opencode.ai/api.json", http.Header{"Accept": {"application/json"}}, nil, false)
		err = fetchErr
		if fetchErr == nil {
			if public.StatusCode == http.StatusOK {
				catalogData, err = transport.ReadBounded(public.Body, transport.MaxResponseBytes)
			} else {
				err = fail(503, "public model catalog is unavailable")
			}
			public.Body.Close()
		}
	}
	if err == nil {
		if _, parseErr := catalog.ParseCatalog(catalogData); parseErr != nil {
			invalid = true
			err = fail(502, "model catalog is invalid; Console authentication status cannot be inferred from this parse failure")
		}
	}
	if err != nil {
		// Keep only previously validated metadata. Failure backoff must not
		// advance its actual freshness timestamp or discard new Console data.
		entry.catalogRetryAfter = e.now().Add(time.Minute)
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
	entry.catalog = catalogData
	entry.catalogError = nil
	entry.catalogExpires = e.now().Add(catalogTTL)
	entry.catalogRetryAfter = time.Time{}
	return catalogData, true, nil
}

// Keep diagnostics aggregate-only. In particular, never log the Unsupported
// string: it may contain a package name supplied by private workspace config.
func (e *Engine) logRouteCounts(cfg pluginConfig, routes map[string]catalog.Route) {
	unsupported, blocked, exposed := 0, 0, 0
	for _, r := range routes {
		if r.Unsupported != "" {
			unsupported++
		} else if transport.AllowedURL(cfg.transportPolicy(), r.BaseURL) != nil {
			blocked++
		} else {
			exposed++
		}
	}
	e.logf("OpenCode discovery: stage=resolved_routes candidates=%d unsupported=%d blocked_destinations=%d exposed=%d", len(routes), unsupported, blocked, exposed)
}
func (e *Engine) staleDiscovery(entry *discoveryEntry, err error) (map[string]catalog.Route, error) {
	entry.lastError = err
	if entry.routes != nil {
		entry.expires = e.now().Add(time.Minute)
		// An expired catalog must not turn a Console outage into a refresh
		// on every request while stale routes are intentionally being served.
		if e.catalogRefreshDue(entry) {
			entry.catalogRetryAfter = entry.expires
		}
		return entry.routes, nil
	}
	return nil, err
}
func invalidate(g *generation, c credential) {
	g.Lock()
	defer g.Unlock()
	for key := range g.cache {
		if strings.HasPrefix(key, c.fingerprint()+"\x00") {
			delete(g.cache, key)
		}
	}
}
func modelInfos(cfg pluginConfig, routes map[string]catalog.Route) []pluginapi.ModelInfo {
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
		if transport.AllowedURL(cfg.transportPolicy(), r.BaseURL) != nil {
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
