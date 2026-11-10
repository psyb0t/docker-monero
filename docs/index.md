---
title: "Home"
description: "docker-monero runs the official monerod and monero-wallet-rpc in one container, with a REST API and an MCP server for wallets, balances and sends."
nav_order: 1
permalink: /
seo:
  type: WebSite
---

# monero

Your own Monero node with programmatic wallets, in one container. The image `psyb0t/monero` runs the official `monerod` and `monero-wallet-rpc` releases and puts an HTTP API and an MCP server in front of them, so scripts and AI agents can read the chain, open wallets, check balances, and send XMR. The node also serves the restricted RPC that Cake Wallet and other wallets connect to.

## What's inside

| Part | Does |
| --- | --- |
| `monerod` | The node. Syncs the chain, pruned by default. Restricted RPC on 18089 for remote wallets. |
| `monero-wallet-rpc` | One process per open wallet, started and stopped through the API. |
| HTTP API | `/v1` on port 8080. Node state, wallet lifecycle, balances, addresses, transfer history, sends. |
| MCP server | `/mcp` on the same port, the same operations as tools for AI agents. |
| Metrics | Prometheus on port 9091. |

Binaries come from getmonero.org and are checked against SHA-256 hashes pinned in the [`Dockerfile`](https://github.com/psyb0t/docker-monero/blob/main/Dockerfile) at build time. The image runs as a non-root user and works with a read-only root filesystem.

## Quick start on regtest

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
```

A small helper keeps the calls short:

```bash
xmr() {
  curl -s \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    "$@"
}
```

Create a wallet, mine to its address and read the balance:

```bash
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

Mined rewards stay locked for 60 blocks, so the balance shows them before `unlockedBalance` does.

## Mainnet with Compose

The repository ships a hardened [`docker-compose.yml`](https://github.com/psyb0t/docker-monero/blob/main/docker-compose.yml) and an [`.env.example`](https://github.com/psyb0t/docker-monero/blob/main/.env.example) to copy:

```bash
curl -LO https://raw.githubusercontent.com/psyb0t/docker-monero/main/docker-compose.yml
curl -L -o .env https://raw.githubusercontent.com/psyb0t/docker-monero/main/.env.example
# Set DOCKER_MONERO_API_TOKEN in .env. For the default DOCKER_MONERO_DATA_PATH:
mkdir -p ./data
sudo chown 10001:10001 ./data
docker compose up -d
```

The first mainnet sync takes days and about 105 GiB of disk pruned. `GET /v1/node/info` shows the progress. Every setting is on the [configuration](configuration.md) page.

Give the container time to stop: it saves every open wallet and lets monerod flush its database. Use `docker stop -t 180`; the compose file sets `stop_grace_period`.

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

Amounts are strings of atomic units (1 XMR = 10^12). Sends are refused until the node and the wallet are both caught up, and `dryRun` prices a transaction without broadcasting it. The [API](api.md) page covers the rest.

## Cake Wallet and other remote wallets

Publish port 18089 and point the wallet at `http://<your-host>:18089` as a custom node. That is monerod's restricted RPC: it serves what wallets need and none of the commands that control the node. Set `DOCKER_MONERO_PUBLIC_NODE=true` if you also want to advertise it to the network.

To require a login, set `DOCKER_MONERO_RPC_LOGIN=username:password` and enter the same username and password in the wallet's custom node settings. The rules for the value are in [configuration](configuration.md#node).

## Where to go next

| Page | What it covers |
| --- | --- |
| [API](api.md) | Amounts, wallet lifecycle, sync, sending, error codes |
| [Configuration](configuration.md) | Every variable, ports, disk |
| [MCP](mcp.md) | MCP tools and when each one is registered |
| [Agent integrations](agents.md) | The skill and plugins for Claude Code, Codex and OpenClaw |
| [Security](security.md) | What the service protects and what you have to |
| [Monitoring](monitoring.md) | Probes, Prometheus metrics, logs |
| [Troubleshooting](troubleshooting.md) | Startup refusals and common API errors |
| [Architecture](architecture.md) | Processes, startup, shutdown, crash handling |
| [Development](development.md) | Building and testing from source |
