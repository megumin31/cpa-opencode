# Tag-driven releases

Push a new version tag on the intended commit. You do **not** need a new source
change, a version-bump commit, or a manual **Create release** click. First let
branch/PR CI pass, then tag that tested commit (which must contain this workflow):

```sh
git tag v0.1.2 <tested-commit>
git push origin v0.1.2
```

Use an unused version. Never move/force an existing public tag. Historical tags
use their historical workflow, so this change does not repair or republish old
releases automatically.

## Version and validation

The tag is the release version source. Supported tags are `vX.Y.Z` and
`vX.Y.Z-rc.1` (or another alphanumeric prerelease suffix). Leading zeroes in the
three numeric components and build metadata (`+...`) are rejected. The complete
tag is retained as `build_tag`; `v1.2.3-rc.1` injects numeric plugin ABI version
`1.2.3` and produces `opencode-v1.2.3.so`, preserving the deployment preflight's
numeric filename contract. A suffix automatically marks the GitHub release as a
prerelease. Branch/PR and local default builds use `0.0.0`, `build_tag=dev` and
are not releases. Do not edit `internal/buildinfo` to release a new version.

The same linker flags feed both build metadata and the C-shared library. Native
host tests read the library's actual registration and compare its version with
metadata. CI also checks the requested version and full tag, exact clean Git revision,
ELF architecture, C ABI exports and module checksums. Native Linux amd64 and
arm64 jobs retain the complete offline test/vet/race/fixture/host gate. Packaging
rechecks both architectures, source trees, metadata, ELF headers and all hashes.
The known CPA host disable regression remains separately reproducible with
`make hostcheck-known-bug`; it is not silently counted as a passing gate.

## Publication and retry

Branch pushes and pull requests run CI without publication. A `v*` tag push runs
the native matrix and, only if both jobs pass, a separate publication job with
`contents: write`. There is no release-published trigger or second workflow hop.
Checkout never persists credentials; the short-lived job token is exposed only
to the publication step.

All local packages are validated before creating a draft release. Archives and
checksums are uploaded and digest-verified; the completion manifest is last.
Only after the entire remote set verifies does the workflow publish the draft.
Thus build/packaging failures produce no release; upload failures leave a draft,
not an empty public release. This also supports immutable releases: assets are
attached before publication, with no attempt to disable immutability.

Retry failed jobs in the same workflow run. An identical existing asset is kept,
a missing asset is uploaded, and only an empty failed-upload `starter` record
may be removed. Differing assets are a hard error; there is no `--clobber` or
force push. An already published complete matching release is verified without
writes; an incomplete or different published release is never changed. The tag
must still resolve to the exact build commit before upload and publication.
A lost publish response can mean publication succeeded: retry reads back and
verifies it. Rebuilding on a changed toolchain/runner can change package hashes;
use a new tag rather than replace published assets.

## Download

For `v0.1.2`, expect these four assets:

- `opencode-v0.1.2-linux-amd64.tar.gz`
- `opencode-v0.1.2-linux-arm64.tar.gz`
- `opencode-v0.1.2-SHA256SUMS`
- `opencode-v0.1.2-release.json`

Verify the archive against the external checksum file, extract the correct
architecture, then verify the internal `SHA256SUMS`. Keep the versioned `.so`
filename unchanged and only one plugin version in CPA's directory. Archives
include license, metadata, ELF/glibc requirements and build provenance. These
are native glibc builds, not universal Linux binaries. Checksums detect corruption
but are not independent signatures. See [BUILD.md](BUILD.md).

Offline fixtures use fake GitHub responses and synthetic ELF headers; they do
not claim a real arm64 build or a real GitHub publication. Native matrix and
repository publication permissions are ultimately verified on GitHub.

References: [push triggers](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#push),
[release API](https://docs.github.com/en/rest/releases/releases),
[asset API](https://docs.github.com/en/rest/releases/assets),
[immutable releases](https://docs.github.com/en/code-security/concepts/supply-chain-security/immutable-releases).
