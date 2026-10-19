# Development

Docker, Make, and a shell are all you need on the host. Every target runs in the development image.

```bash
make help            # every target
make build           # static binary in build/
make lint            # shfmt, shellcheck, go fix -diff, golangci-lint
make test            # unit tests with -race
make test-api        # the production image on regtest, through testcontainers
make test-coverage   # unit and integration tests, coverage gate (90%)
make sec             # govulncheck and semgrep, writes sec.sarif
make generate        # regenerate code from api/api.yml
make audit-compose   # docker-compose.yml against the hardened floor
```

## Tests

Unit tests never start Monero. The wallet pool and the core services run against an in-memory monero-wallet-rpc (`internal/pkg/monero/walletrpc/walletrpctest`) started by a fake process launcher (`internal/pkg/process/processtest`).

`make test-api` builds the production image and runs it on regtest with real monerod and monero-wallet-rpc: it creates wallets, mines and matures funds, sends a transaction, watches it confirm and arrive, and checks read-only mode, the transfer switches, and that a restart keeps the chain and the wallets. The first run downloads the Monero release while building the image. It needs the Docker socket, which the target provides.

`make test-coverage` builds an instrumented image for the same suite, so the service's own code counts toward the coverage gate.

## Changing the API

The contract is `api/api.yml`. Edit it, run `make generate`, and implement the new methods the generated `StrictServerInterface` asks for in `internal/pkg/http/server`. Never edit a `*.gen.go` file.

## Framework

The project is built on [servicepack](https://github.com/psyb0t/servicepack). `make servicepack-update` replaces the framework-owned files (`internal/app/`, `internal/pkg/service-manager/`, `pkg/runner/`, `cmd/main.go`, `Makefile.servicepack`, `scripts/make/servicepack/`, `Dockerfile.servicepack*`). Customize through `cmd/init.go`, `cmd/commands.go`, `Makefile`, and `scripts/make/` instead.

## Upgrading Monero

`Dockerfile` pins the release version and the SHA-256 of each tarball. Download the new `hashes.txt` from getmonero.org, verify its signature against binaryFate's key, then update `MONERO_VERSION` and the two hashes together.
