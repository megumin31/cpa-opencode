# Optional CPA host lifecycle remediation proposal

This document is a review draft, not an applied core patch. The plugin leaves the pinned official host unchanged.

## Reproduction and scope

`cmd/hostcheck` uses the official internal host/C ABI and local fake HTTP. It sends 256 SSE frames and verifies a pending `streamBridgeStream.emit` with a full 16-item queue and no downstream consumption.

| Operation | Pinned v8.0.29 result |
| --- | --- |
| Native load, auth, discovery, aggregate, stream, terminal keepalive | Pass |
| `--quiesce`: real native version replacement | Pass |
| `--cancel-first`: cancel request, then ShutdownAll | Pass |
| default: direct ShutdownAll while request is alive | Fails deadline; completes after cleanup cancellation |
| `--disable`: ApplyConfig enabled=false while request is alive | Fails deadline; completes after cleanup cancellation |

Normal CPA Service.Shutdown calls server.Stop first. http.Server.Close cancels ordinary request contexts, matching `--cancel-first`. Therefore normal application shutdown is not demonstrated broken. Live config disable/removal and embedding SDK callers that unload before canceling requests are affected.

## Cause

Pinned `internal/pluginhost/host.go` detachPlugin/ShutdownAllContext call closeHostHTTPPluginResources before native shutdown. It marks hostCallbackInstance.closed. `host_callbacks.go:143` rejects every new callback on a closed instance. An already active stream emit has a background callback context and waits for bridge space/closure. The plugin tries host.stream.close during shutdown but it is rejected; joining the worker then waits for request cancellation.

Removing the plugin worker join would expose freed C host pointers and is unsafe. Buffering an entire tail would degrade real streaming and is not used.

## Smallest architectural change to review

For graceful unload, pre-quiesce the plugin while its callback instance remains live, before resources are disabled. This matches the already working version-replacement path. Keep all host apply-lock/snapshot/rollback semantics and cancellation deadlines intact. Unsupported or failed quiesce still requires a host-owned cancellation path; simply ignoring quiesce errors is not a generic guarantee.

The robust alternative is to associate executor stream bridges with their plugin callback instance and abort those bridges before marking that instance closed. Then blocked emits unblock without asking the plugin to make a cleanup callback. Keep per-instance isolation and prevent new streams during teardown.

A narrower cleanup-only allowlist for host.stream.close during teardown could fix this plugin's join, but needs ownership checks and tests before adoption. It should never reopen arbitrary callbacks after shutdown starts.

Required upstream tests: full bridge plus direct unload, live config disable, retired instance cleanup, quiesce failure/unsupported, already-closed bridge, repeated close while buffered data drains, and instance isolation. No upstream issue/PR has been submitted from this project.
