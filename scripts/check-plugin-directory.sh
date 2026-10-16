#!/usr/bin/env bash
# Read-only migration preflight. Scan root and platform directories; never delete.
set -euo pipefail
dir=${1:?usage: check-plugin-directory.sh /path/to/plugins}
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
    opencode*.so|opencode*.dylib|opencode*.dll)
      if [[ -L "$file" || ! -f "$file" ]]; then
        echo "OpenCode library must be a regular non-symlink file: $file" >&2
        exit 1
      fi
      case "$name" in
        opencode-v0.1.0.so|opencode-v0.1.0.dylib|opencode-v0.1.0.dll)
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
