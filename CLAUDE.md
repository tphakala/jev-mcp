# Project conventions

Guidance for anyone (or any tool) working in this repository. Private,
machine-specific notes belong in `CLAUDE.local.md`, which is gitignored.

## Commands

- `task check`: the full local gate (build, vet on amd64 and arm64, tidy check,
  lint, ruleguard probe, race tests). Run it before declaring any change done.
- `task lint`, `task fmt`, `task test:race`, `task rules`: the individual steps.
- `task tools`: install the pinned golangci-lint (version in `.golangci-version`)
  and govulncheck.

## Layout

- Root package: public API and the `Version` constant (`version.go`).
- `cmd/<name>/`: one binary per directory. `main` only wires the process into a
  `run(ctx, args, stdout, stderr) int` function that the tests call directly.
- `internal/`: everything that is not public API.
- `rules/`: ruleguard matchers (build tag `ruleguard`). `rules/rulestest/`
  verifies them against `rules/testdata/probe/`.
- `scripts/`: release tooling. `scripts/release.sh` and `scripts/verify-version.sh`
  share one semver definition with `version_test.go`, which cross-checks them.
- `Dockerfile`: distroless static image of the binary; `docker build .` must
  keep working. The toolchain tag in it tracks the go directive in `go.mod`.

## Code

- Context first: functions that block or can be cancelled take `ctx` as the
  first parameter and check it before doing work.
- Errors: package-level `Err*` sentinels, wrapped with `%w`; match with
  `errors.Is` and `errors.AsType`.
- Logging: structured `log/slog` through an injected `*slog.Logger`. Message
  strings are constants; variable data goes in attributes. Logs to stderr,
  program output to stdout.
- No `init` functions. No package-level mutable state without a reason.
- `//nolint` needs a linter name and a reason (`//nolint:gosec // G304: path
  comes from a flag the operator controls`). Prefer fixing the code.
- Formatting is gofumpt plus goimports; `task fmt` applies both.

## Linting and ruleguard

- The linter version is pinned once in `.golangci-version`; CI and `task tools`
  read it. Change it there, then run `golangci-lint config verify` and `task lint`.
- A new matcher goes in the `rules/*.go` file for its subject and needs a
  `// rule: Name` section in the probe with one planted line per `Match`
  group, each carrying a `want` annotation. `task rules` must pass.
- ruleguard reports one finding per AST node; when two matchers match the same
  node the first in load order wins. The probe file comment explains how to
  plant overlapping patterns.
- Do not enable `modernize`: the matchers carry those patterns, and both
  running would leave the reported message to run order.

## Tests

- Table-driven, `t.Parallel()`, `t.Context()` instead of `context.Background()`.
- Every new test must be seen to fail: break the production line it pins,
  watch it go red, restore it. A green test proves nothing on its own.
- A test that runs an external tool (like the ruleguard probe) skips when the
  tool is missing locally and fails in CI (`GOLANGCI_LINT_REQUIRED=1`).

## Comments and docs

- A comment that describes behaviour outside the file being edited (another
  package, a library, a tool) must be verified in the same session, or say so
  is not (`NOT MEASURED`), or not be written.
- Countable claims ("the three callers", "the only place") need a search before
  they are written; prefer stating the invariant over the count.
- When a change makes a documented claim false, update every instance of the
  claim, not only the one next to the edit.

## Releases

- `Version` in `version.go` is the source of truth. Cut a release with
  `task release VERSION=X.Y.Z`, then `git push --atomic origin main vX.Y.Z`.
- Never tag by hand; the workflow rejects a tag that disagrees with `Version`.
