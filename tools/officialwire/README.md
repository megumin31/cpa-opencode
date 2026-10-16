# Official binary / native plugin wire comparison

This is an opt-in integration experiment, not part of `go test`. It requires
Python 3, the separately obtained **official OpenCode 2.0.29 Linux x64 binary**,
Go 1.26, and a built plugin. The script verifies the binary SHA256:

`7e23471f4ebe2313d78baacc9ac96fe9e197ce26d808de4b0fcc37efbe473dad`

It does not download or redistribute that binary. Do not substitute a different
version without first auditing its configuration and network behavior.

## Isolation and inputs

All credentials, organizations, model responses, catalog data, and messages are
synthetic. The script creates a fresh temporary HOME/XDG/work tree under the
specified output directory and supplies a minimal environment. It does not
import an existing account, project, or credential store. Its Console/catalog/
inference URLs are loopback. An explicitly configured loopback HTTP proxy
rejects every external request/CONNECT; update/model fetching and unrelated
agent features are disabled. Ports 18080 and 18082 must be free.

This is **application configuration plus a rejecting proxy**, not a kernel
network namespace or a syscall-level guarantee that a process cannot bypass its
proxy. Run it only in a sandbox where that boundary is acceptable. In the
2026-10-15 experiment the rejecting proxy saw zero requests, and the mock saw
one Console GET and one inference POST. No real key or upstream endpoint was
used. The commands have bounded subprocess timeouts and fail on nonzero exits,
unexpected POST counts, or any proxy attempt.

## Reproduce

From the plugin repository root:

```sh
python3 tools/officialwire/capture_official.py \
  --binary /path/to/verified/opencode \
  --output /path/to/new/fake-captures
```

The script prints the newly generated `fake-official-wire-*` directory. It
contains `official-capture.json`, `proxy-rejections.json`, CLI output, and the
synthetic runtime state. Keep this generated state out of source archives.
The run selects the fake `opencode/space-bunny-free` model, a no-tools agent,
and a 16-token output limit. The mock returns SSE text `OK` and usage 10/1/11.

Replay the first captured POST body through the actual plugin and pinned native
CPA v8.0.29 host, without copying official identity/trace headers:

```sh
cd cmd/hostcheck
go run ./wirecheck \
  -plugin ../../build/opencode-v0.1.0.so \
  -official /path/to/fake-official-wire-*/official-capture.json \
  -output /path/to/plugin-comparison.json \
  -require-aligned-headers
```

Use one concrete capture pathname, not a wildcard that expands to several files.
The harness checks a fixed fake Authorization value before loading the plugin.
It uses a loopback-only target allowlist and rejecting proxy, a separate temporary
host directory, an empty local catalog, and one explicit fake Console model.
It records the loaded binary's SHA256 and embedded revision. It verifies the
native executor result, exactly one GET/POST, canonical JSON equality, credentials,
organization header, and plugin-owned identity. The optional final flag also
requires the corrected Chat Accept header and no credential `x-org-id` on POST;
omit it only when deliberately comparing an older baseline binary.

`go test ./wirecheck` checks the comparison and fixture-validation helpers.
The isolated live-test guard is off in this replay because the captured official
session orchestration is the test input. This command does not authorize a live
account test.

## Interpretation

See [the result and limitations](../../docs/OFFICIAL-WIRE-COMPARISON.md).
Canonical JSON equality permits ordering/escaping differences; it does not mean
byte-identical requests, identity impersonation, or complete protocol parity.

## Continuation and tool-history scenarios

Pass `--scenario continuation` to run a second official CLI invocation with the
first session's actual `--session` ID. The runner asserts stable official session
and affinity headers and replay of the prior assistant answer.

Pass `--scenario tool` for a synthetic `read` tool call against a newly created
fixture file, followed by a second model step. Only the read tool is allowed;
the runner requires the fixture text in the tool result. `--scenario tool-error`
sends a deliberately invalid argument and verifies the official tool execution
error is carried into the next request. Both scenarios assert two inference
POSTs. No shell tool, real account or external endpoint is used.

Replay each POST independently with the native-host runner's `-post 0` or
`-post 1` flag (zero-based POST ordinal, excluding Console GETs). This validates
serialization of the selected historical messages/tools, not execution of a
local tool by CPA. CPA is a provider adapter, not an agent tool runner.

## Native Responses, Anthropic and Gemini probes

Select `--protocol responses`, `--protocol anthropic`, or `--protocol gemini`.
Each supports `--scenario text`, `tool`, and `tool-error`. These run the actual
verified official binary with the protocol's SDK package and native synthetic
SSE events. The tool scenarios require a second POST containing the matching
native tool result and either actual synthetic file content or `tool.execution`.
Every run also requires terminal `OK`, no CLI error event, expected request
counts, and zero rejecting-proxy requests. `experiment.json` records the protocol,
scenario, verified binary hash and request counts. Non-Chat `continuation` is
explicitly unsupported in this batch; it fails before launching a process.

Pass the matching `-protocol responses`, `-protocol anthropic`, or
`-protocol gemini` to the native host runner; it validates the exact captured
POST path. Its default remains `chat` for backward compatibility. Replay each POST using `-post 0` and, for tool scenarios, `-post 1`; keep
`-require-aligned-headers` enabled. Credentials are validated per protocol:
Bearer for Responses, `x-api-key` for Anthropic, `x-goog-api-key` for Gemini.
Anthropic version/beta and wildcard Accept are checked against the capture.

Responses has one explicit comparison normalization: the official session-based
`prompt_cache_key` is replaced with `cpa_wirecheck_synthetic_cache` before replay.
This avoids carrying an official session identity into plugin execution. The
report retains the original capture and original hash, lists `body_normalizations`,
and distinguishes `original_body_equal` from normalized `canonical_body_equal`.
All other request fields are compared without semantic normalization. Anthropic
and Gemini currently require unchanged original canonical equality.

The native replay always returns a terminal text fixture. It proves serialization
of official-generated tools/history, native text aggregation and usage; it does
not itself validate plugin tool-response stream parsing or execute tools in CPA.
The official-side run does execute the synthetic read tool. Every POST is replayed
independently, so this is not multi-turn CPA-session orchestration evidence.

Offline fixture tests:

```sh
python3 -m unittest discover -s tools/officialwire -p 'test_*.py' -v
```
