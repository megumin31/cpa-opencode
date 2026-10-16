#!/usr/bin/env bash
set -euo pipefail
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf -- "$tmp"' EXIT
mkdir -p "$tmp/plugins/linux/amd64"
dir=$tmp/plugins
touch "$dir/opencode-v0.1.0.so"
bash "$here/check-plugin-directory.sh" "$dir"
for name in opencode-v2.so opencode-v2.SO opencode-v2-v0.1.0.so opencode-vgarbage.so opencode.so opencode-v0.2.0.so opencode-v0.1.0.so; do
  touch "$dir/linux/amd64/$name"
  if bash "$here/check-plugin-directory.sh" "$dir" > "$tmp/output" 2>&1; then
    echo "FAIL: preflight accepted conflicting $name" >&2
    exit 1
  fi
  rm -- "$dir/linux/amd64/$name"
done
rm -- "$dir/opencode-v0.1.0.so"
if bash "$here/check-plugin-directory.sh" "$dir" > "$tmp/output" 2>&1; then
  echo 'FAIL: preflight accepted empty directory' >&2
  exit 1
fi
touch "$dir/opencode-vgarbage.so"
if bash "$here/check-plugin-directory.sh" "$dir" > "$tmp/output" 2>&1; then
  echo 'FAIL: preflight accepted malformed-only directory' >&2
  exit 1
fi
echo 'PASS: preflight rejects legacy, uppercase extension, malformed, duplicate, other-version, unversioned, and missing libraries'
