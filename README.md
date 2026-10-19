# docker-monero

[![CI](https://github.com/psyb0t/docker-monero/actions/workflows/pipeline.yml/badge.svg?branch=main)](https://github.com/psyb0t/docker-monero/actions/workflows/pipeline.yml)
[![coverage](https://raw.githubusercontent.com/psyb0t/docker-monero/badges/coverage.svg)](https://github.com/psyb0t/docker-monero/actions/workflows/pipeline.yml)
[![version](https://raw.githubusercontent.com/psyb0t/docker-monero/badges/version.svg)](https://github.com/psyb0t/docker-monero/releases)
[![license](https://raw.githubusercontent.com/psyb0t/docker-monero/badges/license.svg)](LICENSE)
[![Docker Pulls](https://img.shields.io/docker/pulls/psyb0t/monero?style=flat-square)](https://hub.docker.com/r/psyb0t/monero)

Your own Monero node with programmatic wallets, in one container. It runs the official `monerod` and `monero-wallet-rpc` releases and puts an HTTP API and an MCP server in front of them, so scripts and AI agents can read the chain, open wallets, check balances, and send XMR. The node also serves the restricted RPC that Cake Wallet and other wallets connect to.

## What's inside

| Part | Does |
| --- | --- |
| `monerod` | The node. Syncs the chain, pruned by default. Restricted RPC on 18089 for remote wallets. |
| `monero-wallet-rpc` | One process per open wallet, started and stopped through the API. |
| HTTP API | `/v1` on port 8080. Node state, wallet lifecycle, balances, addresses, transfer history, sends. |
| MCP server | `/mcp` on the same port, the same operations as tools for AI agents. |
| Metrics | Prometheus on port 9091. |

Binaries come from getmonero.org and are checked against pinned SHA-256 hashes at build time. The image runs as a non-root user and works with a read-only root filesystem.

## Quick start

Try it on regtest first: a private offline chain where you mine blocks on demand, so nothing touches real money.

```bash
TOKEN=$(openssl rand -hex 32)
docker run -d --name monero -p 127.0.0.1:8080:8080 \
  -e DOCKER_MONERO_API_TOKEN=$TOKEN -e DOCKER_MONERO_NETWORK=regtest \
  psyb0t/monero
until curl -fs localhost:8080/ready >/dev/null; do sleep 1; done

xmr() { curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' "$@"; }

xmr -X POST localhost:8080/v1/wallets -d '{"name":"alice","password":"pw"}'
ADDR=$(xmr localhost:8080/v1/wallets/alice/addresses | jq -r .primaryAddress)
xmr -X POST localhost:8080/v1/node/regtest/generate-blocks -d "{\"count\":100,\"address\":\"$ADDR\"}"
xmr localhost:8080/v1/wallets/alice/balance
```

For mainnet, use the compose file:

```bash
curl -LO https://raw.githubusercontent.com/psyb0t/docker-monero/main/docker-compose.yml
curl -L -o .env https://raw.githubusercontent.com/psyb0t/docker-monero/main/.env.example
# set DOCKER_MONERO_API_TOKEN in .env, then:
docker compose up -d
```

The first mainnet sync takes days and about 105 GiB of disk pruned. `GET /v1/node/info` shows the progress.

## Using the wallets

A wallet must be open to be read or spent from. Opening starts a wallet process, closing saves the wallet and stops it.

```bash
xmr -X POST localhost:8080/v1/wallets/alice/open -d '{"password":"pw"}'
xmr localhost:8080/v1/wallets/alice/status       # syncing? how far behind?
xmr -X POST localhost:8080/v1/wallets/alice/transfers \
  -d '{"destinations":[{"address":"4...","amount":"1500000000000"}],"dryRun":true}'
xmr -X POST localhost:8080/v1/wallets/alice/close
```

Amounts are strings of atomic units (1 XMR = 10^12). Sends are refused until the node and the wallet are both caught up, and `dryRun` prices a transaction without broadcasting it. Everything else is in [docs/api.md](docs/api.md) and the OpenAPI spec at [api/api.yml](api/api.yml).

## Write switches

Create or restore the wallets you need, then lock the deployment down:

- `DOCKER_MONERO_READ_ONLY=true` refuses every write: new or restored wallets, new subaddresses, sends. Opening, closing, and reading still work.
- `DOCKER_MONERO_ALLOW_TRANSFERS=false` refuses only sends.
- `DOCKER_MONERO_MCP_ALLOW_TRANSFERS=true` is the only way an MCP client can send. It is off by default.

## Cake Wallet and other remote wallets

Publish port 18089 and point the wallet at `http://<your-host>:18089` as a custom node. That is monerod's restricted RPC: it serves what wallets need and none of the commands that control the node. Set `DOCKER_MONERO_PUBLIC_NODE=true` if you also want to advertise it to the network.

## MCP

Connect an MCP client to `http://localhost:8080/mcp` with `Authorization: Bearer <token>`. The tools and which ones each mode registers are in [docs/mcp.md](docs/mcp.md).

## Agent integrations

An agent can drive a running docker-monero instance through a skill (REST routes, MCP tools, and the rules around wallets and sending) and, for stdio-only MCP clients, a bridge plugin. Restart the agent session after installing.

Claude Code asks for the instance URL and the API token when you enable the plugin:

```bash
claude plugin marketplace add psyb0t/agents
claude plugin install monero@psyb0t
```

Codex:

```bash
codex plugin marketplace add psyb0t/agents
codex plugin add monero@psyb0t
```

OpenClaw, the skill and the MCP bridge plugin:

```bash
openclaw skills install @psyb0t/monero
openclaw plugins install clawhub:@psyb0t/monero
```

The skill and the bridge read `MONERO_URL`, for example `http://localhost:8080`, and `MONERO_TOKEN`, the value of `DOCKER_MONERO_API_TOKEN`. A client that speaks Streamable HTTP can skip the bridge and connect to `http://localhost:8080/mcp` with an `Authorization: Bearer <token>` header. The server is also listed in the MCP registry as `io.github.psyb0t/monero`.

This section is for using the service from an agent. To change this repository, read `AGENTS.md`.

## Configuration

All settings are `DOCKER_MONERO_*` environment variables, listed with defaults in [docs/configuration.md](docs/configuration.md). The only required one is `DOCKER_MONERO_API_TOKEN`. Chain and wallets live in the `/data` volume; back up `/data/wallets`.

Give the container time to stop: it saves every open wallet and lets monerod flush its database. Use `docker stop -t 180`; the compose file sets `stop_grace_period`.

## Docs

- [docs/api.md](docs/api.md): amounts, wallet lifecycle, sync, sending, error codes
- [docs/configuration.md](docs/configuration.md): every variable, ports, disk
- [docs/mcp.md](docs/mcp.md): MCP tools
- [docs/security.md](docs/security.md): what the service protects and what you have to
- [docs/architecture.md](docs/architecture.md): processes, startup, shutdown, crash handling
- [docs/development.md](docs/development.md): building and testing from source

## Development

```bash
make build      # static binary in build/
make test       # unit tests
make test-api   # the real image on regtest
make lint
```

Details in [docs/development.md](docs/development.md).

## License

[WTFPL](LICENSE). Monero itself is under its own BSD-3-Clause license.
