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
make --no-print-directory -j1 test vet race sdkrepro hostcheck check-plugin-dir
printf '\nPASS: complete offline checks (both Go modules, Python/shell fixtures, SDK reproducer, native host quiesce/cancel-first/reconfigure/reenable)\n'
