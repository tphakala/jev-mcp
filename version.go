package jevmcp

// Version is the release string, without the leading "v" the git tag carries.
// It is the single source of truth for the project version: cmd/ binaries
// print it, and the release tooling refuses to cut a tag that disagrees with
// it (scripts/release.sh before tagging, the Release workflow after the push;
// both run scripts/verify-version.sh, which drives TestVersionMatchesReleaseTag).
const Version = "0.1.0"
