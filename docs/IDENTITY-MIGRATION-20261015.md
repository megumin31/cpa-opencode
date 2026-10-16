# OpenCode plugin identity migration validation

Base: `1d55e212abc65d4b5189ad3a73ec85098fde4a1e`, independent development branch `dev/nonchat-official-wire-4c4c362`.

The plugin ID, configuration key, auth type, executor ID and metadata name are now `opencode`. Plugin version remains independently `0.1.0`; library is `opencode-v0.1.0.so`. The repository remains `megumin31/cpa-opencode`. Existing `opencode/...` model IDs and the independent `cliproxyapi-opencode/0.1.0` User-Agent are unchanged. No main-branch UA change was imported.

That statement records the migration test revision. Current source subsequently
reapplies `84ff978`'s User-Agent value `opencode/latest/2.0.29/cli` while retaining
the `opencode` plugin identity and version `0.1.0`. The earlier test results are
not a new runtime or upstream verification of that later header change.

## Verified

- Go 1.26.9: ordinary tests, race tests, vet, shuffled tests, and Linux amd64 C-shared build pass.
- Separate pinned hostcheck module tests pass, including wirecheck tests.
- Real CPA v8.0.29 native host loads `opencode`, metadata version `0.1.0`; fake auth parses and mock discovery works.
- Old configuration key cannot enable the new plugin. Legacy auth type stays unhandled for old, current, and unspecified provider routing; no legacy provider alias exists.
- Native C ABI quiesce/version-replacement and request-cancellation-first shutdown checks pass. Aggregate/stream error handling and mocked execution checks pass.
- Full unmodified Service startup with empty Console providers: direct discovery and `/v1/models` contain exactly the same 83 IDs, one file-store auth with provider `opencode`, and 83 provider-registry models.
- Full Service startup with explicit provider package override: same 83-ID agreement. Both cases exercise the actual default auth watcher; disable removes models and re-enable restores all 83.
- Service checks report zero inference calls, zero unexpected external attempts, and only the deliberately locally rejected background Grok version check. All credentials are fake and all network endpoints are loopback. No real upstream request was sent.
- Read-only directory preflight rejects legacy, uppercase-extension legacy, malformed, unversioned, other-version, duplicate (including platform-subdirectory), and missing plugin binaries.
- Independent review examined actual host filename parsing, auth routing, watcher fallback and executor registration. Its test-isolation and preflight false-positive findings were fixed and rechecked.

## Migration boundaries

Follow the README's stop/back up/move old libraries/rename config/edit auth type/restart procedure. No deployed configuration, real auth file, key, or permission was read or changed. There is no automatic migration or alias: host routing and configuration isolation make an in-library alias insufficient and risky.

The old short filename `opencode-v2.so` is parsed as `opencode` version `2`, which can outrank `0.1.0`; the longer old filename is a separate plugin ID and can double-load. The preflight is a conservative read-only aid, not an installer or a host-wide lock. It does not inspect configuration or auth files. Keep a single plugin instance and avoid a manually configured OpenAI-compatible entry with raw name `opencode`: although its own auth normally has a namespace, CPA's executor fallback compares the raw name and may seize the plugin executor identity.

CPA may show unmigrated `opencode-v2` files as generic auth even though this plugin does not handle them. Existing pinned-host live-disable/direct-unload limitation remains documented; these tests do not claim production readiness or real-upstream compatibility. Historical validation documents retain the identities and outcomes actually used at the time.
