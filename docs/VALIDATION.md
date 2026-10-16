# Validation record — experimental v0.1.0

Original archive validation, checked 2026-10-15 UTC at source commit `12ba9512331daf862bc0bb7fdaa2647e020afc76`. All authenticated calls in that validation were local mock HTTP with literal fake keys. No real account or paid/free-model live inference validation was performed as part of that record.

Publication status: subsequent real-model testing exposed compatibility issues that remain under diagnosis. Successful upstream compatibility validation is incomplete. The passing checks below describe the original archived build, not a successful live-service acceptance test or a fresh run for the repository metadata update.

## Passing checks

- `go test -race ./... -count=1`: 65 top-level tests; 225 passing test/subtest events; one explicit unsupported Claude-to-Gemini skip; no race report
- `go vet ./...`
- `go -C cmd/hostcheck vet ./...`
- Fresh `CGO_ENABLED=1 go build -buildmode=c-shared`
- Real official native host/C ABI load, registration, auth parsing, config discovery, OAuth rejection
- Actual aggregate and streamed executor calls, headers/session, text/finish/usage, SSE terminal before connection EOF
- `--quiesce`: real version replacement under full host-bridge backpressure
- `--cancel-first`: request cancellation before direct unload under the same backpressure
- Direct public SDK reproduction confirms the same-frame Chat tool-delta/finish conversion defect; equivalent split record succeeds

## Known failing regression, retained deliberately

`go -C cmd/hostcheck run . --disable /absolute/build` times out with the active request context still alive. Actual ApplyConfig per-plugin disable rejects cleanup callbacks before native shutdown. Direct ShutdownAll with an uncanceled blocked stream has the same issue. Cleanup request cancellation then releases the blocked operation. Normal application shutdown closes the HTTP server first; its cancellation-first ordering passes. See HOST-LIFECYCLE-PROPOSAL.md.

## Artifact

Linux x86-64 C-shared library, Go 1.26.9. Built on glibc 2.41; dynamic imported symbol versions require at most GLIBC_2.34. No compatibility claim for musl/Alpine, old glibc, macOS, Windows, or arm64 without rebuilding/testing.

Production readiness remains blocked by untested real service behavior, unsupported Claude-upstream/Gemini-client conversion, explicit rejected compatibility features, and live config-disable/unload preconditions. The response matrix and session fallback are documented in COMPATIBILITY.md and README.md.

## Native wire alignment follow-up (2026-10-15)

Production source `8bc0811` adds the bounded error diagnostics from `bb01bb4`
and the two runtime-observed Chat header corrections. This record is a local
mock validation; the earlier live 403 remains unexplained and was not retried.

- Main-module `go test -race ./... -count=1`, with the recorded public catalog
  fixture supplied: 744 passing test/subtest events and one explicit unsupported
  Claude-upstream/Gemini-client skip; no race report.
- Main-module `go vet ./...` passed.
- Nested host module ordinary and explicit readonly race tests, and readonly vet
  passed offline. Its checked-in module metadata now includes the already
  pinned CPA servicecheck dependency graph; no dependency versions were upgraded.
- Clean Linux amd64 C-shared build succeeded.
- The actual official 2.0.29 binary completed local synthetic authentication and
  generation. One Console GET and one inference POST were captured; the rejecting
  proxy saw zero external requests. This is not kernel-level network isolation.
- The actual native CPA host replay preserved the captured final JSON, text,
  finish reason, and usage. Optional strict header assertions confirmed wildcard
  Chat Accept and no credential `x-org-id` on inference.
- Actual native execution with the strict text guard passed aggregation,
  streaming terminal-before-EOF, cancellation-first unload, and quiesced version
  replacement under backpressure.

See OFFICIAL-WIRE-COMPARISON.md for deliberate identity/transport differences
and the narrowly verified fixture. These tests do not establish live account
entitlement, pricing, or full official-client parity.

## Redacted upstream error details (2026-10-15)

Built on `28ff528`. Local synthetic tests only; no real upstream inference,
credential-file reads, client identity changes, or authentication/network policy
relaxations were performed. Previously discarded response bodies are not recoverable.

- Main `go test ./... -count=1`, `go test -race ./... -count=1`, and `go vet ./...` passed.
- Fresh Linux amd64 C-shared build and native host `--quiesce` / `--cancel-first` passed.
- Nested host-module race tests and vet passed offline.
- Synthetic 402/server_error messages survive into bounded redacted summaries;
  status, 401/403 config recheck, cooldown and single-inference behavior remain tested.
- Fake key reflection covers raw, percent/QueryEscape, HTML/JSON escapes, base64,
  URL-safe base64 and hex; review regressions cover invalid-percent surrounding
  prose, multiline authorization, numeric/nested personal fields, cookies and
  credential/host/config proxy values. Truncation occurs after redaction.
- Existing size-boundary, blocked-read cancellation/timeout and partial-read
  discard tests remain passing. HTML and unsupported bodies are omitted.
- Independent code/security review found and verified fixes for the above
  edge cases; final targeted suite passed with no remaining concrete blocker.

Redaction is best-effort for untrusted natural language: arbitrary unlabelled
personal prose cannot be reliably recognized. Review logs before external sharing.

The follow-up native host harness now runs synthetic 402, 403 and 502 responses
through both `Execute` and `ExecuteStream` using the actual C-shared plugin and
pinned CPA host. It verifies reconstructed status, readable diagnosis text,
redacted fake credentials/account data and exact request counts (nine inference
POSTs including existing success/lifecycle checks, three config GETs including
the two expected 403 rechecks). Both quiesce and cancellation-first runs passed.
Main ordinary/race suites were also rerun with the existing public catalog
snapshot supplied; nested host race/vet and main vet passed.

## Non-Chat official-binary coverage batch

See [NONCHAT-WIRE-20261015.md](NONCHAT-WIRE-20261015.md). Nine official binary
loopback runs (three protocols × text/tool/tool-error), 15 native-host replays
with aligned headers, and the refactored Chat smoke passed. Responses uses an
explicit cache-identity normalization rather than copying official session IDs.
Three offline Python fixture tests and independent code/evidence review passed.
The independent development branch preserves the plugin's own client identity;
no live inference or identity-replacement branch was tested.
