# Changelog

All notable changes per release. Versions follow [semver](https://semver.org).

## v0.6.0 (2026-10-10)

Fixes output lost from fast commands and adds `WithStream`.

- `Start` read output through `cmd.StdoutPipe` and `cmd.StderrPipe`, whose read ends `cmd.Wait` closes when the child exits. A command that exited quickly lost output the readers had not consumed yet, and `echo hello` in a loop lost the line about 85% of the time. Commander now creates the pipes itself and keeps the read ends until the streams are drained.
- A deferred `Stop` at the end of `Wait` could close the stream channels and drop lines already read but not yet forwarded. `Wait` now waits for the streams to drain first, for at most 2 seconds, so a grandchild that keeps the pipes open cannot hang it.
- Added the `WithStream(stdout, stderr chan<- string)` option. `Start` subscribes the channels before the process launches, so a `Stream` call right after `Start` can no longer miss a line. `Process.Stream` still works as before. `Run`, `Output` and `CombinedOutput` ignore the option.
- The module now requires Go 1.26.9 (`go 1.26.9` in `go.mod`).
- Every Make target runs in a pinned dev container built from `Dockerfile.dev`, so contributors need only Docker and Make. `make help` lists the targets, and `make test` now includes `TestCommander_ProcessGroupKilling` because the image has `ps`.
- Added `make sec`, which runs govulncheck and semgrep and writes `sec.sarif`, plus `make format`. `govulncheck` is pinned as a `go tool` dependency.
- `make test-coverage` still enforces 85% and now fails when no coverage is reported.
- The CI pipeline moved to the shared `code-workflow.yml`, uploads the security scan to the Security tab, and publishes release notes when a tag is pushed.
- Added `AGENTS.md` for contributors and the Go Reference badge to the README.

## v0.5.9 (2026-10-10)

Fixes `Stream` losing output when one of a process's streams closes before the other.

- Stderr lines still queued when stdout reached end of file were read and dropped instead of reaching `Stream` subscribers. A command that wrote both streams and exited quickly, such as `printf 'ok\n'; echo failed >&2`, could report no stderr at all.
- When stderr closed first, the output loop kept receiving from the closed channel and spun at full CPU until stdout closed. It now waits on the open stream only.
- Added a `.gitleaks.toml` secret-scanning config and ignored local `.env` files.

## v0.5.8 — 2026-08-08

Repository infrastructure only. No library code changed.

- Added the imported-by badge: a count of the public packages importing this
  module, linking to `importers.md` on the `badges` branch — the importing
  repositories, grouped, package counts descending, and flagged when the owner
  differs from this repo's.
- It measures **blast radius, not adoption**. The number tells you how much
  breaks when an exported name moves; the external mark tells you whether any of
  that is someone else's problem, which is what decides how strictly the module
  has to be versioned. Stars answer neither — nobody stars an `os/exec` wrapper,
  they just import it.
- Refreshed weekly rather than daily, because pkg.go.dev's crawl lags
  publication by days and each run drags the full test suite along (the badges
  job needs the coverage artifact). The whole pipeline runs rather than a
  badges-only job: the badge publisher republishes only what a run produced, so
  a badge-only refresh would delete the coverage, version and license badges.
- The cron slot is derived from a hash of the repository name rather than
  chosen — GitHub cron has no randomness, and its scheduler sheds queued runs
  hardest at the round times a human would pick.

## v0.5.7 — 2026-08-01

Infrastructure only — no library code changed.

- Every branch and tag push is now mirrored to GitLab and Codeberg.
- The default branch and tags are archived to the Wayback Machine and Software Heritage, on push and monthly.
- Issues opened on the mirrors are pulled back into GitHub every six hours.

## v0.5.6 — 2026-07-27

- Added a GitHub Actions CI status badge to the README.

## v0.5.5 — 2026-07-27

Fix badges CI job — add needs dependency so the coverage badge waits for the coverage artifact.

## v0.5.4 — 2026-07-27

Modernize the toolchain and CI.

- Go 1.26.
- `make lint` now runs `go fix -diff ./...` before `golangci-lint` (was `go tool modernize`, which is dropped along with its dependency).
- Added a coverage badge — `test-coverage` writes `coverage-percent.txt`, wired into the `badges` CI job and README.
- Logging (`log/slog`) and error wrapping (`github.com/psyb0t/ctxerrors`) were already in place; verified, no changes needed.

## v0.5.3 — 2026-07-27

Add README status badges.

- Added self-hosted version and license badges (rendered as SVGs on the `badges` branch by the `create-badges` CI job, no third-party render service). Wired a badges job into pipeline.yml.
