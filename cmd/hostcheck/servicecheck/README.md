# Full Service model registration check

This integration harness uses the unmodified, pinned CPA **v8.0.29** Service
Builder, default file token store, and real file watcher. It compares the exact
sorted model IDs returned by the native plugin's `ModelsForAuth` call against the
actual HTTP `GET /v1/models` response after complete Service startup.

Only the fake credential `servicecheck-fake-key` is used. Auth files and config
are generated in a temporary private directory and removed afterward. The plugin
binary is copied into that directory to avoid host cleanup altering build files.
No existing auth directory or credential file is opened.

## Network isolation

- Both HTTP servers bind loopback; the proxy API binds `127.0.0.1`.
- The mock Console returns HTTP 200 with `{"providers":{}}` by default.
- All three CPA built-in model catalogs are set to local reference files.
- The plugin's public catalog is an explicit local snapshot.
- The mock Console doubles as a fail-closed proxy. It accepts only the exact fake
  Console GET and never forwards any connection, including CONNECT requests.
- CPA-side default HTTP dialing is restricted to literal loopback IPs.
- Management UI download/sync is disabled.
- CPA unconditionally starts a Grok CLI version check even without xAI auth. Its
  CONNECT to the npm registry is rejected locally by the failing proxy and
  reported separately. No upstream connection or TLS handshake is made.
- Any inference, other non-Console proxy request, or external direct dial fails
  the check. No inference endpoint is called by the harness.

## Run

Run from any directory with an already populated Go module cache:

```sh
GO=/workspace/shared/cliproxyapi-toolchain/go/bin/go \
GOCACHE=/workspace/shared/cliproxyapi-toolchain/cache \
GOPATH=/workspace/shared/cliproxyapi-toolchain/gopath \
./cmd/hostcheck/servicecheck/run.sh
```

`run.sh` creates a disposable module manifest and defaults to `GOPROXY=off` and
`GOSUMDB=off`. It does not modify the hostcheck module manifests or download
anything. The full Service path requires more CPA-pinned transitive modules
(including fsnotify, zeroconf, and Pion) than the direct ABI harness. If those are
missing, pre-cache them in a separately authorized development step.

Flags:

- `-plugin`: native versioned `.so` file (the script supplies the project build).
- `-catalog`: trusted local public catalog snapshot.
- `-reference`: CPA source directory containing its local built-in catalogs.
- `-package-override`: have Console explicitly override the `opencode` package
  with `@opencode/ai/providers/openai-compatible`, while keeping catalog models.
- `-watcher-cycle=false`: omit disable/re-enable watcher verification.
- `-debug`: enable CPA debug logs.

The default watcher check rewrites only the generated fake auth file: disable
must remove every model from `/v1/models`, then re-enable must restore them.
A 45-second watchdog bounds startup, registration, and teardown.

## Recorded result (2026-10-15)

Both default empty Console and `-package-override` runs passed:

- Direct native plugin models: **83**.
- Actual Service HTTP `/v1/models`: **83**, with identical sorted IDs.
- Service provider registry: **83**; default file auth store: **1**.
- Real watcher: disable removes all models; re-enable restores **83**.
- Inference requests: **0**; unexpected external/other requests: **0**.
- Unconditional Grok version checks rejected by the loopback proxy: **1**.

See `last-empty-console.log` and `last-package-override.log` for the full local
runs. These results do not reproduce the reported live deployment's empty model
list and do not establish real service-key entitlement or inference success.

Inputs used:

- Plugin SHA-256: `02c1e6a596cc7006284a5c92cf97e937ac606b021553ae20dc1abd998b4ed5a6`
- Public catalog SHA-256: `d89d121bb5511e41bd5e9ddf00873aab132849fd96df549f0d39f6e80d64a4d3`
