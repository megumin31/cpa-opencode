# Official OpenCode 2.0.29 wire experiment

Date: 2026-10-15. All accounts, keys, organizations, and upstream responses were
local synthetic fixtures. The separately verified official Linux x64 binary ran
its real `auth import` and `run --standalone` paths against a loopback Console and
Chat Completions SSE mock. The plugin comparison used an actual C-shared library
loaded by the unmodified CPA v8.0.29 native host, not a direct Go executor call.

## Observed baseline

The official CLI completed with `OK`. Its Console request used Bearer API-key
authentication and credential metadata `orgID` as `x-org-id`. The inference POST
used Bearer authentication, `Content-Type: application/json`, `Accept: */*`,
and the Console-configured `x-opencode-org-id`. It did **not** copy credential
`orgID` into an inference `x-org-id` header.

The body contained `model`, `messages`, `stream: true`,
`stream_options.include_usage: true`, and `max_completion_tokens: 16`.
There were no tool definitions. Official session orchestration added its own
system context to the user message. That context was replayed only as fixture
input; the plugin must not inject OpenCode agent prompts into unrelated callers.

The previously delivered clean plugin revision `5d560f7` sent canonically equal
JSON for that captured input. Both implementations consumed the local SSE
successfully; the native CPA response preserved text `OK`, finish reason `stop`,
and usage 10/1/11. Two functional header differences were identified:

1. The plugin added credential `x-org-id` on inference. That field now remains
   Console-only; configured `x-opencode-org-id` is still forwarded.
2. The plugin's default Chat Accept was `text/event-stream`. It now uses the
   observed native value `*/*`. The optional strict text test profile checks the
   same value.

A clean rebuilt `8bc0811` library passed the same actual-host replay with
explicit assertions for both corrected headers. The canonical body hash was
`a84604ad2dcdb10f35c519060f3fb82a6830a3787a4fa16a557e47dd6074f25c`.
This hash belongs to the synthetic captured fixture, including its generated
system context; another fresh official session can produce a different hash.

The official and plugin test runners are retained under
[`tools/officialwire`](../tools/officialwire/README.md) and `cmd/hostcheck/wirecheck`.
Captures record binary revisions/hashes and all fake-only headers/body fields.

## Deliberate and unverified differences

- After these recorded experiments, the User-Agent change from `84ff978` was
  reapplied as `opencode/latest/2.0.29/cli`. The experiments below are not a
  new runtime validation of that change. Scoped `cpa_` session identifiers remain
  plugin-owned; it does not copy official project/client/trace identifiers or
  invent official identity or attestation headers.
- Compression offers, connection handling, content length, header casing/order,
  JSON key order/escaping, and ephemeral loopback ports may differ. Canonical
  equality verifies the JSON data, not byte-identical transport serialization.
- No extra provider SDK identification headers were observed in this specific
  native `aisdk:@ai-sdk/openai-compatible` fixture. Other packages may differ.
- This validates a text-only Chat fixture. It does not establish runtime parity
  for other protocols, tools, reasoning, account permissions, or all Console
  model settings. Existing protocol limitations still apply.
- Isolation used fresh state, explicit local destinations, a rejecting proxy,
  and the existing sandbox. The proxy observed zero external attempts. It was
  not independent kernel network-namespace isolation or syscall tracing.

## The earlier live HTTP 403

This experiment neither reproduced nor explained that response. The original
body, content type, and request identifier were not retained and cannot be
recovered. A Console HTTP 200 demonstrates acceptance by that configuration
interface, not proven inference permission. The plugin's new bounded error
classification applies only to future responses and never logs raw error
messages, body fields, IDs, or credentials. No live request was retried as part
of this experiment.

## Expanded continuation and tool evidence (2026-10-15)

The retained runner now has `continuation`, `tool`, and `tool-error` scenarios.
All three passed against the same verified official 2.0.29 binary, fresh fake
credentials, synthetic files, loopback services, and rejecting proxy. Each
scenario made exactly two inference POSTs; no proxy attempts were observed.

- Continuation: a second official CLI process resumed the first actual session
  ID. `x-opencode-session`, `x-opencode-session-id`, `x-session-affinity`, and
  `x-session-id` remained equal across turns. The second request retained the
  prior assistant `OK` answer.
- Successful tool: the fake server streamed one `read` call. The official agent
  executed it against its newly created synthetic file, and its next request
  included the tool-call ID and actual fixture text.
- Tool failure: an intentionally invalid argument produced the official
  structured `tool.execution` error in the following request.
- Every selected POST in these three scenarios was independently replayed
  through the actual native CPA host/C-shared plugin. All six matched canonical
  JSON and corrected functional headers, with text/finish/usage preserved.

These tests validate the adapter's serialization of official-generated history
and tools. They do not make CPA an agent tool runner, verify all tool types,
prove session continuity for unrelated downstream clients, or establish parity
for non-Chat wire protocols. Independent per-POST replay does not test two turns
inside one CPA execution session. Existing scoped-affinity unit tests cover
that separate plugin policy; official identity headers remain comparison-only.
The official agent's step-budget system reminder is fixture input only.

## Static provider-branch corrections

The following are supported by the verified binary's embedded native adapter
code and synthetic regression tests, not new live-service evidence:

- Chat `He`/`VE` select the max-token field using final canonical/provider ID and
  endpoint patterns, then explicit compatibility overrides. Preserving canonical
  identity fixes aliased provider defaults; model names and package
  `settings.provider` do not substitute for final provider identity.
- Chat `Ge`/`Ye` omit `store` for the exact native provider/endpoint exceptions,
  default supported absent values to `false`, and retain explicit compatibility
  support overrides. Configured raw body remains the final semantic overlay.
- Anthropic `ZC` adds `?beta=true` only for the final resolved provider exactly
  equal to `anthropic`, rather than inferring it from package or settings.
- Configured official project/client/parent identity headers are now rejected
  alongside existing credential/session protections. Configured
  `x-opencode-org-id` remains allowed. No official identity is synthesized.
- The native error classifier reads shallow `error.status`, nested/top-level
  `error_type`, and known category aliases. Safe plugin diagnostics now cover
  those positions, with symbolic status distinct from HTTP status. No numeric
  code inference, message regex, raw data, or recursive traversal was added.
  The 16 KiB plus detection-byte and one-second bounds remain unchanged.

Evidence anchors in the retained extracted official `strings.txt`: native Chat
`He`/`Ge`/`VE`/`Ye` at line 279139; final ModelResolver provider update at 278202;
error field extraction at byte 16850874 and category lists at byte 16847486.
The separately verified executable contains those error snippets at byte offsets
139563992 and 139560604. These are version-specific local audit coordinates,
not stable public source line numbers.

## Remaining contract boundaries

The one-minute discovery behavior is a credential-isolated cache expiry with
serialized refresh on demand. It is not guaranteed idle polling: pinned CPA
skips scheduled API-key credential refresh after the refresh time. No OAuth
identity or auth-kind workaround is used. Mutable official registry inheritance
for chained aliases remains a separately documented limitation.

## Subsequent non-Chat runtime batch

The earlier non-Chat coverage limitation above describes the prior experiment.
[The separate non-Chat report](NONCHAT-WIRE-20261015.md) adds nine real-official-
binary loopback scenarios and 15 actual native-host POST replays for Responses,
Anthropic and Gemini. It records the Responses cache-identity normalization,
additional wildcard Accept correction, and remaining unverified boundaries.
