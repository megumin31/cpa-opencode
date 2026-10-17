#!/usr/bin/env bash
# Offline dependencies; test traffic is synthetic loopback HTTP only.
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
export GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local CGO_ENABLED=1
export GOFLAGS="${GOFLAGS:+$GOFLAGS }-mod=readonly"
: "${GO:=go}" "${PYTHON:=python3}"
export GO PYTHON

# Do not rely on go test ./... to enter the nested host module or execute tools.
# Run serially even when the caller supplied -j: native checks share an artifact directory.
make --no-print-directory -j1 test vet race sdkrepro hostcheck
# build/ also contains packaged artifacts from prior CI runs. It is a workspace,
# not a deployment directory: validate the freshly built candidate in isolation.
# The preflight fixtures above still reject duplicates in real plugin directories.
candidate=$(mktemp -d)
trap 'rm -rf -- "$candidate"' EXIT
filename=$(make --no-print-directory metadata | python3 -c 'import json,sys; print(json.load(sys.stdin)["filename"])')
cp -- "build/$filename" "$candidate/$filename"
make --no-print-directory -j1 check-plugin-dir PLUGIN_DIR="$candidate"
printf '\nPASS: complete offline checks (both Go modules, Python/shell fixtures, SDK reproducer, native host quiesce/cancel-first/reconfigure/reenable)\n'
