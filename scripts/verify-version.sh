#!/usr/bin/env bash
# Verify the Version constant matches the release tag passed as $1, and require
# the guard's PASS line rather than just a green `go test`. This is the single
# home of the release-version check: both scripts/release.sh (pre-tag, on the
# bumped working tree) and .github/workflows/release.yml (post-tag push) call
# it, so the RELEASE_TAG wiring and the PASS anchor live in one place instead
# of a hand-synced copy in each.
#
# Requiring the PASS line matters: renaming TestVersionMatchesReleaseTag (so
# '^TestVersion' no longer selects it) or leaving RELEASE_TAG unset (so it
# SKIPs) would leave `go test` green with the guard never asserting, and a SKIP
# is not a PASS.
set -euo pipefail

tag="${1:-}"
if [[ -z "$tag" ]]; then
  echo "usage: $0 vX.Y.Z[-pre]" >&2
  exit 2
fi

cd "$(git rev-parse --show-toplevel)"

# Write the tee log outside the checked-out workspace so it never dirties the
# tree: $RUNNER_TEMP on GitHub Actions, else $TMPDIR or /tmp locally. A single
# EXIT trap removes it on every exit path.
log="$(mktemp "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/version-guard.XXXXXX")"
trap 'rm -f "$log"' EXIT

echo "verifying Version against $tag"
RELEASE_TAG="$tag" go test -count=1 -v -run '^TestVersion' . | tee "$log"
grep -qF -- '--- PASS: TestVersionMatchesReleaseTag (' "$log"
