# Architecture refactor verification — 2026-10-16

Baseline: `d4c77115e4f1ec3db26397940c4ec349a875b8bd`.
All fixtures use synthetic credentials and local/in-memory upstreams. No real
service authentication, inference, deployment, or pricing eligibility was tested.

## Scope and ownership

The C ABI remains at the repository root. Business logic is an instance-owned
Engine with validated immutable configuration generations; focused internal
packages own catalog resolution, protocol transformation, transport, diagnostics,
status errors, and release metadata. A dependency-direction test prevents upper
layers from leaking back into those helpers. Python inspection tools now live
under `tools/officialwire`; the legal CPA-internal hostcheck module and executable
SDK reproducer remain distinct and are explicitly included in verification.

All original 221 named Go tests remain represented. New tests cover component
boundaries, concurrent instances, generation retirement, cancellation, native
reload, metadata, and tool preflight. Existing safety/strict-text restrictions
were not relaxed to make fixtures pass.

## Completed validation

On Linux amd64 with Go 1.26.9:

- Baseline ordinary tests, race tests, and vet passed for both Go modules.
- Refactored `make check-offline` passed: both modules' ordinary/race/vet,
  Python fixtures, shell/preflight tests, SDK reproducer, fresh C-shared build,
  and native quiesce, cancel-first, same-instance reconfigure, and same-file
  disable/re-enable modes.
- New Engine lifecycle tests passed 30 shuffled race-enabled repetitions.
- Transport external-package tests passed 10 race-enabled repetitions.
- Both module checksum verifications passed.
- The built amd64 library's ELF type/architecture and all four C exports were
  verified. Release provenance additionally requires a clean committed checkout;
  the shared CI/local verifier checks the exact embedded revision and clean flag.
- The production Go helper packages and Go commands cross-compiled for
  Linux arm64 with CGO disabled. This is **not** an arm64 C-shared build or
  native-host execution result. No arm64 C toolchain/runtime was available here;
  the CI matrix retains a full native arm64 job, which has not been run remotely
  as part of this local refactor.

The gate was run against a recorded source-hash manifest and the manifest was
checked unchanged afterward. Subsequent documentation/test additions and the
clean-commit provenance build are checked again before delivery.

## Review findings with regression evidence

1. Request cancellation originally only canceled HTTP while `host.stream.emit`
   could remain blocked under its independent host context. The Engine now
   closes that stream on request timeout/completion, and joins an already-started
   cancellation callback before releasing its worker lease. Persistent dual-gate
   tests prove both Emit release and full Close-callback completion; a separate
   review reproduction failed before the fix and passed afterward under race.
2. An Engine made terminal by shutdown must not be reused after another ABI
   initialization. Go ELF `NODELETE` can preserve package state across
   `dlclose`/`dlopen`. The ABI now serializes lifecycle changes and creates a new
   instance per load; active duplicate initialization is rejected. The persistent
   native same-file re-enable test failed with a 503 before this fix and passed
   afterward, including fresh auth/discovery and aggregate/stream results.
3. Same-instance native reconfiguration under backpressure passes on both the
   original baseline and the refactor. It is preservation coverage, not evidence
   of a reproduced baseline native deadlock. Deterministic Engine tests separately
   prove the stronger explicit Close-before-join retirement contract.

CPA's pinned request lifecycle generates unique UUIDs and sends completion via
`sync.Once`. Arbitrary reuse of an outstanding external request ID is outside
that host contract; the refactor does not claim to distinguish indistinguishable
completion messages from a different host implementation.

## Performance comparison

Synthetic microbenchmarks used Go 1.26.9, `GOMAXPROCS=1`, one pinned CPU, and five
interleaved one-second baseline/refactor samples. The second runtime measurement
was taken after the complete gate; timings remain subject to shared-machine
variation. These are CPU/allocation observations, not upstream latency claims.

| Fixture | Baseline median | Refactor median | Change | Allocations |
| --- | ---: | ---: | ---: | --- |
| Runtime preparation, including auth/URL checks | 18.49 µs | 19.48 µs | +5.4% | 150 / 9,344 B, unchanged |
| Warm aggregate execution, in-memory HTTP | 95.63 µs | 101.93 µs | +6.6% | 642 / 46,548 B, unchanged |
| Protocol Responses aggregation | 64.07 µs | 65.49 µs | +2.2% | 511 / 29,737 B, unchanged |
| Protocol Chat→Responses forwarding | 160.79 µs | 159.30 µs | −0.9% | 1,014 / 105,529 B, unchanged |

The runtime samples show a modest CPU cost, not a performance improvement. No
allocation regression was measured in these fixtures. The generation admission
and explicit lifetime guarantees were retained rather than removed to optimize
these small synthetic timings. Re-run the checked-in benchmarks on a quiet,
representative deployment machine before making a latency commitment:

```sh
go test ./internal/plugin -run '^$' -bench '^BenchmarkRuntime' -benchmem -count=5
go test ./internal/protocol -run '^$' -bench . -benchmem -count=5
```

Protocol-only preparation also improved in its isolated fixture, but excludes
credential and destination checks moved into runtime, so it is intentionally not
presented as an end-to-end speedup.

## Explicit remaining limitation

The pinned CPA v8.0.29 **active-request disable/unload** defect still reproduces.
The separate strict `make hostcheck-known-bug` returns a failure while a live
caller and blocked stream are present. It is not swallowed or counted as passing.
Idle disable/re-enable, normal quiescence, cancellation-first unload, and
same-instance reconfiguration are separately passing scenarios. Fixing the host
requires the host-side lifecycle work documented in
[HOST-LIFECYCLE-PROPOSAL.md](HOST-LIFECYCLE-PROPOSAL.md).
