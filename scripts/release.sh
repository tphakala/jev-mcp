#!/usr/bin/env bash
# Cut a release: bump the Version constant, run the release-tag guard, commit
# and tag in one step so the constant and the tag cannot disagree.
#
# Usage: scripts/release.sh X.Y.Z[-pre]   (or: task release VERSION=X.Y.Z)
# Pushes nothing; it prints the push command at the end.
set -euo pipefail

version="${1:-}"
if [[ -z "$version" ]]; then
  echo "usage: $0 X.Y.Z[-pre]" >&2
  exit 2
fi
# MAJOR.MINOR.PATCH, each a numeric identifier with no leading zero, plus an
# optional pre-release: a dot-separated list of identifiers, each either numeric
# with no leading zero or carrying a letter or hyphen (semver 2.0.0 rule 9), and
# no leading "v". This bash ERE must stay in exact agreement, case for case,
# with the RE2 semverCore in version_test.go (each in its own dialect);
# TestReleaseShellEREMatchesSemverCore reads this line and runs the shared
# accept/reject table through both.
if [[ ! "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?$ ]]; then
  echo "error: '$version' is not MAJOR.MINOR.PATCH[-prerelease] (no leading v, no leading zeros)" >&2
  exit 2
fi
tag="v$version"

cd "$(git rev-parse --show-toplevel)"
branch="$(git branch --show-current)"
if [[ "$branch" != "main" ]]; then
  echo "error: releases are cut from main (currently on '$branch')" >&2
  exit 1
fi
if [[ -n "$(git status --porcelain)" ]]; then
  echo "error: working tree is not clean" >&2
  exit 1
fi
git fetch --quiet --tags origin
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
  echo "error: tag $tag already exists; if it exists only locally, remove it with 'git tag -d $tag' and re-run" >&2
  exit 1
fi
# Refuse to release from a main that is behind its remote. git updates refs
# independently, so pushing a tag off a stale main would publish a tag whose
# commit is not on origin/main. Only checked when origin/main is known locally.
if git rev-parse -q --verify refs/remotes/origin/main >/dev/null; then
  if ! git merge-base --is-ancestor refs/remotes/origin/main HEAD; then
    echo "error: local main is behind origin/main; pull first" >&2
    exit 1
  fi
fi

file="version.go"
if ! grep -q '^const Version = "' "$file"; then
  echo "error: no 'const Version = \"...\"' line in $file" >&2
  exit 1
fi
# Portable in-place edit (BSD sed has no GNU -i form): rewrite via a temp file
# that lives outside the repo, so it never dirties the tree.
tmp="$(mktemp)"
# One EXIT trap does cleanup and restore. It removes the temp file and, until
# the bump is committed, restores version.go to HEAD on every exit path: a
# nonzero command, an explicit `exit`, or a signal such as SIGINT. `committed`
# flips to 1 only once the bump is committed (or found already present), so a
# failing `git commit` still restores. The restore resets the index first,
# because `git checkout -- <file>` restores from the index, not from HEAD, and
# would otherwise reinstate a staged bump instead of undoing it. The clean-tree
# guard above ran first, so HEAD is the exact pre-bump state and the restore
# can never discard user work.
committed=0
trap 'rm -f "$tmp"; [[ $committed == 1 ]] || { git reset -q -- "$file"; git checkout -q -- "$file"; }' EXIT
sed "s/^const Version = \".*\"\$/const Version = \"$version\"/" "$file" > "$tmp"
# cat, not mv: mktemp creates the temp at 0600, and mv would carry that mode
# onto version.go. Writing through cat keeps the file's existing mode.
cat "$tmp" > "$file"
if [[ -n "$(gofmt -l "$file")" ]]; then
  echo "error: $file is not gofmt clean after the bump" >&2
  exit 1
fi

# Verify the bumped constant against the tag. scripts/verify-version.sh owns
# the RELEASE_TAG wiring and the PASS-line check, shared with the Release
# workflow so the two cannot drift; on failure the EXIT trap above restores
# version.go.
scripts/verify-version.sh "$tag"

git add "$file"
if git diff --cached --quiet; then
  # The constant already read $version, so there is nothing to commit. The
  # point of this run is the tag, so tag HEAD anyway instead of dying on git
  # commit's "nothing to commit, working tree clean".
  echo "Version already reads $version; tagging HEAD without a new commit"
else
  git commit --quiet -m "chore: bump version to $version"
fi
# Only now is the bump safe from the cleanup trap. Setting this before the
# commit would disarm the restore while the change was merely STAGED, so a
# failing commit (a rejecting hook, a full disk) would leave the bump staged
# with nothing to undo it. A failing git tag below keeps committed=1
# deliberately: the commit is real by then, and restoring the file would not
# unmake it.
committed=1
git tag -a "$tag" -m "$tag"
# --atomic so a rejected main (someone pushed first) also rejects the tag,
# instead of leaving a published tag whose commit never reached origin/main.
echo "tagged $tag; publish with:"
echo "  git push --atomic origin main $tag"
