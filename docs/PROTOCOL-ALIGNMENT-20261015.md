# Protocol alignment follow-up (2026-10-15)

This change addresses the concrete gaps from the OpenCode 2.0.29 communication
audit, against CPA v8.0.29. It does not claim byte-identical clients, equivalent
remote entitlement, or explain the historical HTTP 403. All new execution uses
synthetic credentials and loopback HTTP fixtures; no live inference is required.

## Request semantics

- Cross-protocol generation parameters are mapped from the original caller
  request, rather than trusting incomplete SDK defaults. Supported temperature,
  top-p, top-k, stop, token-limit, penalty and seed mappings are protocol-specific.
  The audited unsupported controls fail before HTTP; this is not an exhaustive
  validator for every future provider extension.
- Converter-generated Gemini safety overrides and Anthropic user metadata are
  removed. Explicit caller/configuration values keep their normal precedence.
- Media/reasoning inventories compare original and converted requests. Mixed text
  cannot hide a lost attachment or prior reasoning/signature. Unsupported
  conversions fail locally rather than returning a successful text-only request.
  Genuine signatures are preserved; synthetic signature bypasses remain blocked.
- Native reasoning/signature policy uses final canonical provider identity.
- Native Chat tools apply evidenced strictness and Z.ai streaming defaults;
  explicit strict requirements are preserved or rejected on incompatible routes.
  Mistral history/tool-ID normalization rejects collisions rather than conflating
  calls.
- Provider/model/variant body remains the final intentional configuration overlay.
  Passing a local serialization test does not prove a remote model accepts every
  parameter that its native adapter can serialize.

## Configuration and catalog

Console application follows JSON insertion order against a mutable provider/model
registry. Later canonical aliases see prior updates, same-provider remaps see
prior model updates, and an unmatched canonical model preserves an existing
model where the native resolver does. Final route tests check target, model,
headers and body, beyond internal merge objects.

Catalog freshness has its own five-minute expiry. Expiry forces a reread of a
configured catalog file or a public fetch capped at ten seconds, even when the
Console route TTL has not expired. A validated last-good catalog can survive a
transient refresh failure. This is in-memory caching, not full equivalence to
OpenCode's disk cache, digest tracking and embedded snapshot. A trusted
catalog_file remains the available explicit snapshot fallback.

Refresh remains demand-driven (execution, model enumeration, explicit auth
reload). The pinned host does not regularly refresh API-key model registration
and exposes no plugin model-change publication callback. Idle background registry
updates therefore remain a host integration limitation. Authentication failures
still fail closed instead of continuing to send inference using stale routes.

## Session and cache

The same scoped cpa_ affinity used by inference session headers now supplies an
absent prompt_cache_key for native Responses and Chat routes that support it.
Explicit caller keys and final configured body values are preserved. IDs remain
isolated by caller, explicit conversation, credential, server and organization.
Without trusted caller/conversation scope the documented request-local fallback
remains request-local, including its cache key. No prompt-content hash is used.

CPA's parent_session_id can be a content-derived Merkle identity, so it is not
exported as an actual conversation parent. There is no invented project, client,
parent/fork hierarchy, official session prefix, or attestation. The User-Agent
change from `84ff978` was subsequently reapplied as `opencode/latest/2.0.29/cli`;
this does not provide official session identity or attestation.
Automatic b3/traceparent generation remains absent; it is observability rather
than an authentication or protocol-completion requirement.

## SSE and completion

The parser recognizes CR, LF and CRLF, preserves SSE last-event ID, dispatches
only at blank boundaries and discards undelimited EOF data. Native null and
keepalive data frames are ignored. Chat completion validates finish reasons:
clean EOF after a valid finish can succeed without DONE, whereas DONE alone does
not bypass the default finish requirement. Explicit requireFinishReason:false
is supported. Error/network_error and unknown finish values fail safely.
Translators still receive a terminal event after validated clean Chat EOF.
Successful response MIME labels no longer override actual SSE parsing and
protocol-completion validation.

The 32 MiB byte-event/aggregate safety limits remain deliberate plugin limits,
rather than copying the official parser's 10 MiB string-based bound. The plugin
still supports its documented protocol-specific response-conversion restrictions.

## HTTP transport and timeouts

Clients/transports are pooled by configured proxy policy and idle connections are
closed during reconfiguration/shutdown. Explicit auth proxy > plugin proxy > host
proxy precedence remains; process environment proxies remain ignored. Destination
allowlists and redirect refusal remain unchanged.

Default total execution timeout is now disabled. The native defaults are separate
five-minute response-header and fifteen-minute incoming-body-idle limits. Explicit
request_timeout_seconds (1–3600) retains a plugin-wide total budget; zero disables
it. Provider timeout/headerTimeout/chunkTimeout settings use milliseconds or false
to disable that component. Caller/lifecycle cancellation continues to take effect.
The idle timer covers awaiting network reads, not downstream backpressure.

The transport advertises only implemented gzip, deflate, br and zstd decoders and
handles explicit configured encoding offers. Response decompression is lazy so
error diagnostics retain their one-second read bound. Provider request encoding
supports none, gzip and zstd; raw configured Content-Encoding is rejected rather
than falsely labelling plain JSON. Compression occurs only after final JSON and
isolated-test guards have passed. The custom native transport option remains
unsupported and fails explicitly.

## Remaining external boundaries

- Service-key auth only; OAuth/device login, token refresh, org SSO/needs_auth
  presentation and Vertex OAuth are separate unimplemented capabilities.
- The plugin does not retry inference. CPA's surrounding configured retry and
  credential-failover logic can still retry errors including 403. Changing that
  policy requires host changes; relabelling HTTP status would hide the truth.
- CPA's error ABI carries status/message at request start but only plain error
  text after streaming starts. Original retry headers and typed late-error
  metadata cannot be preserved through this ABI. Retry-After-Ms now takes precedence in the existing local 429/503 gate, with
  Retry-After fallback. Local cooldown is
  not a claim of full official retry-scheduler parity.
- TLS/ALPN/HTTP2 fingerprints, DNS selection, external proxy behavior and remote
  compression acceptance are unverified. Local HTTP tests cannot establish them.
- Existing six official Chat captured-request replays remain narrow evidence.
  New non-Chat tests validate pinned native-adapter rules against local wire
  fixtures, not new official-binary or live-account runtime equivalence.
- Request hooks, websearch, MCP and agent/tool execution are application features,
  not features of this LLM adapter. Unsupported settings fail explicitly.

## Validation

See the release verification report for exact final commit/build hashes and test
results. Unit tests include final prepared JSON plus actual loopback wire and
response behavior; native host smoke tests exercise the loaded C-shared binary,
quiesce/cancellation and preserved 402/403/502 redacted errors. Race and vet checks
are required before delivery. No historical verification record is rewritten as
though it applied to this change.
