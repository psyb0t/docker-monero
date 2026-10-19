# Changelog

All notable changes per release. Versions follow [semver](https://semver.org) pre-1.0 conventions: minor bumps may include breaking API changes, called out explicitly; patch bumps are docs, build, and fixes only.

## v0.1.1

- Data now lives in the host directory set by `DOCKER_MONERO_DATA_PATH` (default `./data`) instead of a Docker volume. The directory must be writable by UID/GID `10001`. Existing deployments must move their data out of the old volume before upgrading.
- Fixed CI builds: v0.1.0's commit was missing `vendor/github.com/moby/moby/api/types/build` because an unanchored `build/` ignore pattern matched it. Root-only ignore patterns are now anchored.
- Updated the servicepack framework to v1.10.1, which brings `ctxerrors` v0.9.0, `goenv` v1.0.11, `testify` v1.12.1, and a `make pkg-upgrade` that upgrades modules again.

## v0.1.0

First release.

- Runs the official Monero v0.18.5.4 `monerod` and `monero-wallet-rpc`, checked against SHA-256 hashes pinned in the Dockerfile, for linux/amd64 and linux/arm64.
- Supervises monerod: restarts it with backoff after a crash and stops it with SIGTERM so the database is flushed. Pruned by default; mainnet, stagenet, testnet, and regtest.
- Restricted RPC on 18089 for remote wallets such as Cake Wallet. The unrestricted RPC stays on the container's loopback.
- Wallet pool: each open wallet runs in its own monero-wallet-rpc process, up to `DOCKER_MONERO_MAX_OPEN_WALLETS`. Create, restore from seed or keys (view-only without a spend key), open, close, status with sync progress, balance, addresses and subaddresses, paginated transfer history, and sends with a dry-run mode.
- Sends are refused until the node and the wallet have caught up.
- HTTP API under `/v1`, generated from `api/api.yml`, with a generated Go client in `pkg/http/api/client`. Bearer token required on every call.
- MCP server at `/mcp` with the same operations as tools. Wallet restore is not offered over MCP.
- Write switches: `DOCKER_MONERO_READ_ONLY` refuses every write, `DOCKER_MONERO_ALLOW_TRANSFERS` turns sends off, and `DOCKER_MONERO_MCP_ALLOW_TRANSFERS` (off by default) lets MCP clients send.
- Agent skill, Claude Code and Codex plugins, and an OpenClaw MCP bridge plugin, installable from the psyb0t/agents marketplace and ClawHub.
- Prometheus metrics on 9091, `/healthz` and `/ready` probes, and a `healthcheck` command the image's HEALTHCHECK runs.
- Hardened `docker-compose.yml`: read-only root filesystem, every capability dropped, resource limits, API and metrics on loopback.
