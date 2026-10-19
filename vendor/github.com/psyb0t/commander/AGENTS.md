# AGENTS.md

Commander is a Go library (`github.com/psyb0t/commander`) that wraps `os/exec` with process lifecycle management, real-time output streaming, graceful termination and a mock for tests. It is a single package at the repo root.

## Commands

Docker and Make are the only host requirements. Every Go command runs in the `Dockerfile.dev` image through `DEV_RUN` in the `Makefile`, as your user, with the repo at `/work`. Do not run `go` on the host.

- `make help` lists every target.
- `make dep` tidies and vendors dependencies. `vendor/` is committed, so commit it with `go.mod` and `go.sum`.
- `make lint`, `make lint-fix`, `make format` check, fix and format.
- `make test` runs `go test -race`.
- `make test-coverage` enforces the 85% floor and writes `coverage-percent.txt`.
- `make sec` runs govulncheck and semgrep and writes `sec.sarif`.

Run `make lint` and `make test-coverage` before sending a change. Lint must report zero issues.

## Code

- `commander.go` holds the `Commander` interface and `New`. `mock_commander.go` is a hand-written mock, not generated, so there is no `make generate`.
- Tests live beside the code in the same package. `commander_integration_test.go` runs real processes and needs `ps` from procps, which the dev image provides.
- `.fixtures/` is tracked test data. Leave it in place.
- Wrap errors with `ctxerrors` and keep sentinels in `errors.go`.

## Docs and releases

- Update `README.md` and `CHANGELOG.md` in the same change as any behavior or API change.
- The maintainer cuts releases. A pushed tag runs the pipeline and publishes the release notes. Do not tag or publish.
