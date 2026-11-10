# docker-monero

[![CI](https://github.com/psyb0t/docker-monero/actions/workflows/pipeline.yml/badge.svg?branch=main)](https://github.com/psyb0t/docker-monero/actions/workflows/pipeline.yml)
[![coverage](https://raw.githubusercontent.com/psyb0t/docker-monero/badges/coverage.svg)](https://github.com/psyb0t/docker-monero/actions/workflows/pipeline.yml)
[![version](https://raw.githubusercontent.com/psyb0t/docker-monero/badges/version.svg)](https://github.com/psyb0t/docker-monero/releases)
[![license](https://raw.githubusercontent.com/psyb0t/docker-monero/badges/license.svg)](https://github.com/psyb0t/docker-monero/blob/main/LICENSE)
[![Docker Pulls](https://img.shields.io/docker/pulls/psyb0t/monero?style=flat-square)](https://hub.docker.com/r/psyb0t/monero)

Your own Monero node with programmatic wallets, in one container. It runs the official `monerod` and `monero-wallet-rpc` releases and puts an HTTP API and an MCP server in front of them, so scripts and AI agents can read the chain, open wallets, check balances, and send XMR. The node also serves the restricted RPC that Cake Wallet and other wallets connect to.

## Contents

- [What's inside](#whats-inside)
- [Quick start](#quick-start)
- [Using the wallets](#using-the-wallets)
- [Write switches](#write-switches)
- [Cake Wallet and other remote wallets](#cake-wallet-and-other-remote-wallets)
- [MCP](#mcp)
- [Agent integrations](#agent-integrations)
- [Configuration](#configuration)
- [Documentation](#documentation)
- [Development](#development)
- [Sponsor](#sponsor)
- [License](#license)

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

docker run -d --name monero \
  -p 127.0.0.1:8080:8080 \
  -e DOCKER_MONERO_API_TOKEN=$TOKEN \
  -e DOCKER_MONERO_NETWORK=regtest \
  psyb0t/monero

until curl -fs localhost:8080/ready >/dev/null; do
  sleep 1
done

xmr() {
  curl -s \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    "$@"
}

xmr -X POST localhost:8080/v1/wallets -d '{
  "name": "alice",
  "password": "pw"
}'

ADDR=$(
  xmr localhost:8080/v1/wallets/alice/addresses |
    jq -r .primaryAddress
)

xmr -X POST localhost:8080/v1/node/regtest/generate-blocks -d "{
  \"count\": 100,
  \"address\": \"$ADDR\"
}"

xmr localhost:8080/v1/wallets/alice/balance
```

For mainnet, use the compose file:

```bash
curl -LO https://raw.githubusercontent.com/psyb0t/docker-monero/main/docker-compose.yml
curl -L -o .env https://raw.githubusercontent.com/psyb0t/docker-monero/main/.env.example
# Set DOCKER_MONERO_API_TOKEN in .env. For the default DOCKER_MONERO_DATA_PATH:
mkdir -p ./data
sudo chown 10001:10001 ./data
docker compose up -d
```

The first mainnet sync takes days and about 105 GiB of disk pruned. `GET /v1/node/info` shows the progress.

## Using the wallets

A wallet must be open to be read or spent from. Opening starts a wallet process, closing saves the wallet and stops it.

```bash
xmr -X POST localhost:8080/v1/wallets/alice/open -d '{
  "password": "pw"
}'

# syncing? how far behind?
xmr localhost:8080/v1/wallets/alice/status

xmr -X POST localhost:8080/v1/wallets/alice/transfers -d '{
  "destinations": [
    {
      "address": "4...",
      "amount": "1500000000000"
    }
  ],
  "dryRun": true
}'

xmr -X POST localhost:8080/v1/wallets/alice/close
```

Amounts are strings of atomic units (1 XMR = 10^12). Sends are refused until the node and the wallet are both caught up, and `dryRun` prices a transaction without broadcasting it. Everything else is in the [API docs](https://psyb0t.github.io/docker-monero/api/) and the OpenAPI spec at [api/api.yml](https://github.com/psyb0t/docker-monero/blob/main/api/api.yml).

## Write switches

Create or restore the wallets you need, then lock the deployment down:

- `DOCKER_MONERO_READ_ONLY=true` refuses every write: new or restored wallets, new subaddresses, sends. Opening, closing, and reading still work.
- `DOCKER_MONERO_ALLOW_TRANSFERS=false` refuses only sends.
- `DOCKER_MONERO_MCP_ALLOW_TRANSFERS=true` is the only way an MCP client can send. It is off by default.

## Cake Wallet and other remote wallets

Publish port 18089 and point the wallet at `http://<your-host>:18089` as a custom node. That is monerod's restricted RPC: it serves what wallets need and none of the commands that control the node. Set `DOCKER_MONERO_PUBLIC_NODE=true` if you also want to advertise it to the network.

To require a login, set `DOCKER_MONERO_RPC_LOGIN=username:password` and enter the same username and password in the wallet's custom node settings. Empty keeps login disabled. The rules for the value are in [configuration](https://psyb0t.github.io/docker-monero/configuration/#node).

## MCP

Connect an MCP client to `http://localhost:8080/mcp` with `Authorization: Bearer <token>`. The tools and which ones each mode registers are in the [MCP docs](https://psyb0t.github.io/docker-monero/mcp/).

## Agent integrations

An agent can drive a running instance through a skill and, for stdio-only MCP clients, a bridge plugin. Claude Code:

```bash
claude plugin marketplace add psyb0t/agents
claude plugin install monero@psyb0t
```

Codex, OpenClaw and plain MCP clients are covered in [agent integrations](https://psyb0t.github.io/docker-monero/agents/). To change this repository, read `AGENTS.md`.

## Configuration

All settings are `DOCKER_MONERO_*` environment variables, listed with defaults in the [configuration docs](https://psyb0t.github.io/docker-monero/configuration/). The only required one is `DOCKER_MONERO_API_TOKEN`. Compose mounts the host directory `DOCKER_MONERO_DATA_PATH` (default `./data`) at `/data`. It must be writable by UID/GID `10001`; back up its `wallets` subdirectory.

Give the container time to stop: it saves every open wallet and lets monerod flush its database. Use `docker stop -t 180`; the compose file sets `stop_grace_period`.

## Documentation

The full documentation is at [psyb0t.github.io/docker-monero](https://psyb0t.github.io/docker-monero/).

| Page | What it covers |
| --- | --- |
| [API](https://psyb0t.github.io/docker-monero/api/) | Amounts, wallet lifecycle, sync, sending, error codes |
| [Configuration](https://psyb0t.github.io/docker-monero/configuration/) | Every variable, ports, disk |
| [MCP](https://psyb0t.github.io/docker-monero/mcp/) | MCP tools |
| [Agent integrations](https://psyb0t.github.io/docker-monero/agents/) | Skill and plugins for Claude Code, Codex and OpenClaw |
| [Security](https://psyb0t.github.io/docker-monero/security/) | What the service protects and what you have to |
| [Monitoring](https://psyb0t.github.io/docker-monero/monitoring/) | Probes, metrics, logs |
| [Troubleshooting](https://psyb0t.github.io/docker-monero/troubleshooting/) | Startup refusals and common API errors |
| [Architecture](https://psyb0t.github.io/docker-monero/architecture/) | Processes, startup, shutdown, crash handling |
| [Development](https://psyb0t.github.io/docker-monero/development/) | Building and testing from source |

## Development

```bash
make build      # static binary in build/
make test       # unit tests
make test-api   # the real image on regtest
make lint
```

Details in the [development docs](https://psyb0t.github.io/docker-monero/development/).

## Sponsor

<a href="https://www.buymeacoffee.com/psyb0t"><img src="https://cdn.buymeacoffee.com/buttons/v2/default-blue.png" alt="Buy Me a Coffee" height="60" width="217"></a>

## License

[WTFPL](https://github.com/psyb0t/docker-monero/blob/main/LICENSE). Monero itself is under its own BSD-3-Clause license.
