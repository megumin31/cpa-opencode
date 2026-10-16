#!/usr/bin/env bash
# Read-only migration preflight. Scan root and platform directories; never delete.
set -euo pipefail
dir=${1:?usage: check-plugin-directory.sh /path/to/plugins [expected-linux-filename]}
expected=${2:-}
if [[ -z "$expected" ]]; then
  root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
  expected=$(cd -- "$root" && "${GO:-go}" run ./cmd/buildmeta --field filename)
fi
[[ "$expected" =~ ^[a-z0-9-]+-v[0-9]+\.[0-9]+\.[0-9]+\.so$ ]] || {
  echo "Invalid expected Linux plugin filename: $expected" >&2
  exit 2
}
provider=${expected%-v*}
stem=${expected%.so}
[[ -d "$dir" ]] || { echo "Plugin directory does not exist: $dir" >&2; exit 1; }
files=$(mktemp)
trap 'rm -f -- "$files"' EXIT
find -L "$dir" \( -type f -o -type l \) -print0 > "$files"
count=0
while IFS= read -r -d '' file; do
  name=${file##*/}
  # CPA accepts case-insensitive library extensions. Be stricter than its
  # version parser: only the documented release filename may pass migration.
  lower=${name,,}
  case "$lower" in
    "$provider"*.so|"$provider"*.dylib|"$provider"*.dll)
      if [[ -L "$file" || ! -f "$file" ]]; then
        echo "OpenCode library must be a regular non-symlink file: $file" >&2
        exit 1
      fi
      case "$name" in
        "$stem.so"|"$stem.dylib"|"$stem.dll")
          count=$((count + 1)) ;;
        *)
          echo "Unexpected OpenCode library: $file. Stop CPA and move legacy, malformed, or other-version copies outside the plugin directory." >&2
          exit 1 ;;
      esac ;;
  esac
done < "$files"
if ((count != 1)); then
  echo "Expected exactly one versioned OpenCode library, found $count. Keep only the intended target-platform build." >&2
  exit 1
fi
echo 'PASS: one versioned OpenCode library; no legacy library names. Also verify one opencode config entry and migrate auth types before restart.'
