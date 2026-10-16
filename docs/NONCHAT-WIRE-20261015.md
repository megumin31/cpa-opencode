# Non-Chat official binary comparison, 2026-10-15

## Scope and evidence

Independent development branch based on `4c4c362`, retaining plugin-owned
User-Agent and scoped identities. The later main-branch official-CLI User-Agent
change is neither incorporated nor tested. No real credentials, upstream,
user computer, or remote deployment were used.

This describes the tested revision. The User-Agent change from `84ff978` was
subsequently reapplied to current source; this historical batch was not rerun
with that value and does not establish real upstream or free-model acceptance.

Official OpenCode 2.0.29 Linux x64 SHA256:
`7e23471f4ebe2313d78baacc9ac96fe9e197ce26d808de4b0fcc37efbe473dad`.
The binary is not redistributed. The retained runner uses fresh state, all-fake
credentials/catalog/Console, loopback inference and a rejecting proxy. This is
application/sandbox isolation, not independently verified kernel isolation.

| Protocol | Official real binary / local mock | Actual CPA v8.0.29 C-shared replay |
| --- | --- | --- |
| Responses | Text, successful read tool, invalid tool arguments; 5 POSTs | All 5 POST bodies; terminal text/status and 10/1/11 usage |
| Anthropic | Text, successful read tool, invalid tool arguments; 5 POSTs | All 5 original canonical POST bodies; text/end_turn and 10/1 usage |
| Gemini | Text, successful read tool, invalid tool arguments; 5 POSTs | All 5 original canonical POST bodies; text/STOP and 10/1/11 usage |

All nine official runs reached terminal `OK`. Each tool scenario executed only
a read of its newly generated synthetic file, or produced the expected native
`tool.execution` argument error, and retained the matching result in the next
request. All rejecting proxies recorded zero attempts. Chat text also passed as
a regression check after refactoring the fixture generator.

Responses comparison explicitly substitutes only the official session-derived
`prompt_cache_key` with `cpa_wirecheck_synthetic_cache`; originals and original
hashes remain in reports. Therefore normalized canonical equality is true while
original canonical equality is false for these Responses replays. This is an
intentional identity boundary, not an unreported payload mismatch.

## Evidence-supported correction

The actual binary sends `Accept: */*` for all three protocols, as already observed
for Chat. The plugin previously sent `text/event-stream` for these three. Its
default now uses wildcard Accept across supported native protocols. Explicit
configured headers retain their existing precedence. Regression tests assert
this for all four protocols. Identity restrictions, credential separation,
response-completion checks, redaction and destination restrictions remain intact.

The actual native-host replays require corrected Accept, no Console-only
credential `x-org-id`, protocol-specific authentication, Anthropic version/beta,
configured organization, plugin-owned User-Agent/session headers, no official
identity/trace headers, expected paths/counts, canonical request semantics and
successful native text/usage aggregation.

## Coverage boundaries

- Official real-binary evidence is limited to these synthetic text and read-tool
  histories; it does not establish real service permission, pricing or availability.
- Native replay uses terminal text SSE for every selected POST. It validates tool
  definitions/history serialization, not plugin parsing of a returned tool-call
  stream or CPA tool execution. Broader stream/translation behavior retains its
  unit-test evidence and explicit unsupported-conversion failures.
- Replay POSTs are independent executions. Cross-turn CPA session orchestration,
  non-Chat CLI resume, streaming HTTP errors, retry scheduling, cancellation,
  multimodal inputs, reasoning signatures and all provider variants are not
  newly covered by this official-binary batch.
- Earlier static provider audits and synthetic tests remain static/test evidence;
  this report does not upgrade them to official runtime observations.
- TLS/HTTP2 fingerprints, real proxies, external transport and upstream enforcement
  remain unverified. No claim about the historical live HTTP 403 is made.

See `cmd/officialwire/README.md` for reproduction. Generated fake runtime state
is excluded from source archives; a compact evidence package retains captures,
CLI output, manifests and native comparison reports without shipping auth stores.
