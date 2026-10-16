#!/usr/bin/env bash
set -euo pipefail
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
expected=$(cd -- "$here/.." && "${GO:-go}" run ./cmd/buildmeta --field filename)
provider=${expected%-v*}
tmp=$(mktemp -d)
trap 'rm -rf -- "$tmp"' EXIT
mkdir -p "$tmp/plugins/linux/amd64"
dir=$tmp/plugins
touch "$dir/$expected"
bash "$here/check-plugin-directory.sh" "$dir" "$expected"
for name in "$provider-v2.so" "$provider-v2.SO" "$provider-v2-${expected#"$provider-"}" "$provider-vgarbage.so" "$provider.so" "$provider-v9999.0.0.so" "$expected"; do
  touch "$dir/linux/amd64/$name"
  if bash "$here/check-plugin-directory.sh" "$dir" "$expected" > "$tmp/output" 2>&1; then
    echo "FAIL: preflight accepted conflicting $name" >&2
    exit 1
  fi
  rm -- "$dir/linux/amd64/$name"
done
rm -- "$dir/$expected"
if bash "$here/check-plugin-directory.sh" "$dir" "$expected" > "$tmp/output" 2>&1; then
  echo 'FAIL: preflight accepted empty directory' >&2
  exit 1
fi
touch "$dir/$provider-vgarbage.so"
if bash "$here/check-plugin-directory.sh" "$dir" "$expected" > "$tmp/output" 2>&1; then
  echo 'FAIL: preflight accepted malformed-only directory' >&2
  exit 1
fi
rm -- "$dir/$provider-vgarbage.so"
touch "$dir/$expected"
ln -s -- "$dir/$expected" "$dir/linux/amd64/$expected"
if bash "$here/check-plugin-directory.sh" "$dir" "$expected" > "$tmp/output" 2>&1; then
  echo 'FAIL: preflight accepted a symlink library' >&2
  exit 1
fi
rm -- "$dir/linux/amd64/$expected"
if bash "$here/check-plugin-directory.sh" "$dir" '../unsafe.so' > "$tmp/output" 2>&1; then
  echo 'FAIL: preflight accepted an invalid expected filename' >&2
  exit 1
fi
# A deployed preflight with an explicit intended filename needs no Go compiler.
GO=/nonexistent/go bash "$here/check-plugin-directory.sh" "$dir" "$expected"
echo 'PASS: preflight rejects legacy, uppercase extension, malformed, duplicate, other-version, unversioned, missing, and symlink libraries; explicit filenames need no Go compiler'
