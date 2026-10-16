#!/usr/bin/env bash
# Keep the existing hostcheck module's manifest unchanged. Service startup brings
# additional pinned transitive dependencies that the direct ABI harness omits.
set -euo pipefail
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
module=$(dirname -- "$here")
project=$(cd -- "$module/../.." && pwd)
go=${GO:-go}
manifest=$(mktemp -d "${TMPDIR:-/tmp}/opencode-servicecheck-mod.XXXXXX")
trap 'rm -rf -- "$manifest"' EXIT
cp -- "$module/go.mod" "$manifest/servicecheck.mod"
cp -- "$module/go.sum" "$manifest/servicecheck.sum"
cd -- "$module"
# The default is deliberately cache-only. Pre-cache the exact CPA 8.0.29 Go
# dependencies in an authorized development step before running this harness.
export GOPROXY=off GOSUMDB=off
"$go" run -mod=mod -modfile="$manifest/servicecheck.mod" ./servicecheck \
  -plugin "$project/build/opencode-v0.1.0.so" "$@"
