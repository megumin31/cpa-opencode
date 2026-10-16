# Console inheritance and catalog refresh

The resolver follows the inspected OpenCode 2.0.29 `Wp` transform's
`Object.entries` ordering, including JavaScript array-index key ordering. It
updates one mutable provider/model registry. Later Console providers can inherit
already-updated providers; sequential remaps within a provider can read earlier
model updates. Canonical model lookup tries the configured model ID, then the
model key, then preserves the target provider's existing model. A new alias does
not implicitly expose all canonical models. Disabled models stay available as
inheritance sources but are not exposed; an explicit enabled copy can use them.

The implementation retains the plugin's destination allowlist, protected header
policy, protocol/package allowlist, credential stripping, and service-key scope.
Only the connected `opencode` baseline and explicit Console providers are exposed.

## Cache contract

- Console routes have a one-minute, demand-driven TTL.
- Catalog metadata has its own five-minute TTL. Its expiry can bypass a still-fresh
  route cache; that discovery pass also reloads Console configuration.
- Configured `catalog_file` data is reread at catalog expiry, using a bounded read.
  Public catalog fetches have a ten-second context covering headers and body,
  further bounded by the caller's context. They never receive the service key or
  organization header.
- Newly read metadata is parsed before replacing the cached catalog. A failed
  refresh uses the previously validated catalog, allowing successful new Console
  overrides to take effect. Failed refreshes back off for one minute without
  advancing the catalog's freshness timestamp.
- A first-load invalid catalog remains an error, including during retry backoff.
  When public metadata is unavailable and there is no cached catalog, explicit
  supported/trusted Console routes may still work. With neither metadata nor
  usable explicit routes, discovery reports unavailability.
- A configured snapshot's initial read/parse failure is an error. There is no
  embedded public snapshot, cross-process disk cache, or digest comparison.
- Existing authentication failure behavior is preserved: Console 401/403 does
  not resurrect cached routes.

## Idle registration limitation

These TTLs are checked on discovery calls (model enumeration, execution, or
explicit auth reload), not by background polling. In the pinned CPA v8.0.29 host,
`auto_refresh_loop.go` excludes ordinary API-key auth from automatic refresh.
Consequently, an idle host's externally registered model list can remain unchanged
until the host requests model enumeration/reload. Execution-time route discovery
alone does not publish model-list changes to the host registry. No background
credential retention or host ABI extension was introduced to hide this limitation.

## Evidence and scope

`configuration_alignment_test.go` exercises mutable inheritance, target fallback,
file/public refresh, stale fallback, invalid-cache handling, and a real loopback
HTTP POST asserting the resolved path, model, body overlays and functional headers.
All inputs and keys are synthetic. These tests establish fixture behavior, not
live upstream authorization, full official-client parity, or a cause of prior 403s.
