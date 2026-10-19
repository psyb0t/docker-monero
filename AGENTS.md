# docker-monero

A Docker image, `psyb0t/monero`, that runs the official `monerod` and `monero-wallet-rpc` behind one Go service. The service supervises both processes and puts a REST API (`/v1`, contract in `api/api.yml`) and an MCP endpoint (`/mcp`) in front of them, on one port, behind one bearer token.

It exists so a program or an agent can read a Monero node and manage wallets over HTTP without running `monero-wallet-cli`. The service is built on [servicepack](https://github.com/psyb0t/servicepack), a clone-and-own Go service framework.

## Architecture

- `monero-server`: the one servicepack service, and the owner of startup and shutdown order. It parses config, supervises monerod, builds the wallet pool and serves REST, MCP and metrics. On stop it fails readiness, drains the API listener, saves and closes every wallet, then sends monerod its SIGTERM.
- `internal/pkg/node`: the monerod supervisor. It builds the arguments, restarts monerod with backoff and probes readiness.
- `internal/pkg/walletpool`: one `monero-wallet-rpc` process per open wallet, on loopback ports from `DOCKER_MONERO_WALLET_RPC_PORT_START`, capped by `DOCKER_MONERO_MAX_OPEN_WALLETS`.
- `internal/pkg/process`: the `Launcher` seam for child processes. Production uses commander, tests use `processtest`.
- `internal/pkg/monero/`: typed `daemonrpc` and `walletrpc` clients over `jsonrpc`, and `amount` for atomic units and XMR strings.
- `internal/pkg/core/{node,wallets}`: the services both transports call. They validate, convert and return the generated `api` types.
- `internal/pkg/http/server`: REST handlers over the generated strict interface, plus the router (bearer auth, body limit, `/healthz`, `/ready`).
- `internal/pkg/mcp`: MCP tools over the same core services.
- `internal/pkg/apierrors`: maps service sentinels to a status, a stable code and a client-safe message, for REST and MCP alike.
- `internal/pkg/metrics`: the Prometheus registry, served on its own listener (9091).
- `internal/pkg/config`: `DOCKER_MONERO_*` parsing and validation.

Data flow: client -> bearer auth -> REST handler or MCP tool -> core service -> `daemonrpc` or `walletrpc` -> monerod or `monero-wallet-rpc` on the container loopback.

## Stack

- Language: Go 1.26.9, `CGO_ENABLED=0`.
- Framework: servicepack, with aichteeteapee `serbewr` for HTTP, oapi-codegen (strict server) and oapixconstgen for the contract, and the MCP Go SDK for `/mcp`.
- Runtime image: Debian trixie-slim with tini, the Monero CLI release pinned by SHA-256 in `Dockerfile`, a fixed UID/GID of 10001.
- Deploy: `docker-compose.yml` (read-only root, all capabilities dropped, memory, CPU and PID limits).

## Layout

- `cmd/`: entrypoint. `commands.go` (the `healthcheck` subcommand) and `init.go` are project files.
- `api/api.yml`: the OpenAPI contract and the shared constants (`x-constants`).
- `internal/pkg/services/monero-server/`: startup and shutdown wiring (`dependencies.go`).
- `pkg/http/api/client/`: the generated public Go client.
- `tests/integration/`, `tests/testinfra/`: the suite that runs the real image on regtest.
- `scripts/`: `audit-compose*.sh`, `check-go-age.sh`, and `make/` project overrides of framework steps.
- `.agents/`: the agent skill (`skills/monero/`), the Claude and Codex plugin manifests, the OpenClaw MCP bridge (`plugins/monero/`) and these rules. `CLAUDE.md` and `.claude` are local, gitignored symlinks onto `AGENTS.md` and `.agents`, recreated per checkout.
- `docs/`: overview docs. See `docs/development.md` for tests and API changes.

## Dev workflow

Docker, Make and a shell are enough. Every target runs in the development image.

```bash
make help             # every target
make build            # static binary in build/
make lint             # shfmt, shellcheck, go fix -diff, golangci-lint
make test             # go test -race ./..., without the integration tag
make test-api         # the real image on regtest through testcontainers
make test-coverage    # unit and integration tests, coverage gate (90%)
make sec              # govulncheck and semgrep, writes sec.sarif
make generate         # regenerate code from api/api.yml
make audit-compose    # docker-compose.yml against the hardened floor
```

`pre-commit.sh` runs `make lint && make test-coverage`.

## Project-specific landmines

- Servicepack owns `internal/app/`, `internal/pkg/service-manager/`, `pkg/runner/`, `cmd/main.go`, `Makefile.servicepack`, `scripts/make/servicepack/`, `Dockerfile.servicepack*` and `servicepack.version`. `make servicepack-update` overwrites them. Customize through `cmd/init.go`, `cmd/commands.go`, `Makefile` and `scripts/make/`.
- `scripts/make/service_registration.sh` and `scripts/make/test_coverage.sh` are project overrides of framework steps. Each wraps the unmodified framework script and says in its header comment what it changes and when to delete it. Do not fold their changes into `scripts/make/servicepack/`.
- `make test` and `make test-integration` do not pass `-tags=integration`, so the `tests/` suite is compiled out of them. Use `make test-api`. Only `make test-api` and `make test-coverage` set it.
- The integration suite runs the real image on regtest. It needs the Docker socket, and the first run builds the image, which downloads the Monero release.
- `scripts/audit-compose.sh` is the shared compose policy floor and must stay byte-identical to the shared copy. Never edit it here. Project checks go in `scripts/audit-compose-floor.sh`, which normalizes Compose v5 duration strings and then calls it.
- Generated code is never hand-edited: `internal/pkg/http/api/*.gen.go`, `pkg/http/api/client/client.gen.go` and `internal/pkg/services/services.gen.go`. An API change starts in `api/api.yml`, then `make generate`. `services.gen.go` comes from `make service-registration`.
- Any WebSocket surface added later must be generated with psyb0t/gen-asyncapi, not hand-rolled.

## Rules

Rules for every change: `.agents/rules/`. Read them before editing.
