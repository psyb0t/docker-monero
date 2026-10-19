# docker-monero setup

Install, configure and check a docker-monero instance. Read this only when the user asks to deploy or reconfigure the service. For calling a running instance, use [../SKILL.md](../SKILL.md).

## What you need

- Docker, and Docker Compose for the compose route.
- Disk for the Monero chain, on a volume you keep. Pruned mode (the default) keeps about a third of the full chain. Choose before the first sync, because switching later means resyncing.
- Memory. The compose file caps the container at 6 GiB, 4 CPUs and 1024 PIDs, and notes that syncing mainnet wants a few GiB.

## Install with Compose

The compose file lives in the repository, and the image is `psyb0t/monero` on Docker Hub.

```bash
git clone https://github.com/psyb0t/docker-monero
cd docker-monero
cp .env.example .env
# Set DOCKER_MONERO_API_TOKEN in .env, at least 32 characters:
openssl rand -hex 32
docker compose up -d
```

`.env.example` holds placeholders only. Keep your real `.env` out of version control.

## Install with docker run

```bash
export DOCKER_MONERO_API_TOKEN="$(openssl rand -hex 32)"

docker run -d --name monero --restart unless-stopped --stop-timeout 180 \
  -e DOCKER_MONERO_API_TOKEN \
  -p 127.0.0.1:8080:8080 -p 127.0.0.1:9091:9091 \
  -p 18080:18080 -p 18089:18089 \
  -v monero-data:/data \
  psyb0t/monero:latest
```

The service refuses to start without a token. Clients send the same value as `MONERO_TOKEN`. The container runs as UID and GID 10001, so a bind-mounted `/data` must be writable by that user. The stop timeout matters because the service saves open wallets and monerod flushes its database on SIGTERM, and the image budgets 170 seconds for that.

## Verify

```bash
curl -s http://localhost:8080/healthz    # {"status":"ok"}
curl -s http://localhost:8080/ready      # {"status":"ready"} once monerod's RPC answers
curl -s -H "Authorization: Bearer $MONERO_TOKEN" http://localhost:8080/v1/node/info
```

`/healthz` and `/ready` need no token. `/ready` reports that monerod answers RPC, not that the chain is synced. Read `synchronized` and `syncPercent` in `/v1/node/info` for that. Sends are refused with 409 until the node is synced and the wallet has caught up.

From another container on the same Docker network, use the service address, for example `http://monero:8080`. In the compose file the API publishes on `127.0.0.1` only.

## Ports

| Port | What | Compose publishes it |
| --- | --- | --- |
| 8080 | REST API (`/v1`) and MCP (`/mcp`), bearer token required | `127.0.0.1` only |
| 9091 | Prometheus metrics at `/metrics`, no authentication | `127.0.0.1` only |
| 18080 | Monero P2P, how the node joins the network | every interface |
| 18089 | Restricted RPC for remote wallets such as Cake Wallet | every interface |
| 18081 | Unrestricted monerod RPC, container loopback only | never |

Remove the 18080 and 18089 lines to keep the node private. To reach 8080 from elsewhere, put a reverse proxy with HTTPS or a private network in front of it.

## Configuration

Every setting is a `DOCKER_MONERO_*` environment variable, read once at startup. A bad value stops the process before monerod starts. Durations are Go durations such as `90s` or `2m`.

| Variable | Default | Meaning |
| --- | --- | --- |
| `DOCKER_MONERO_API_TOKEN` | none, required | Bearer token for the API and MCP, at least 32 characters. There is no unauthenticated mode. |
| `DOCKER_MONERO_NETWORK` | `mainnet` | `mainnet`, `stagenet`, `testnet` or `regtest`. |
| `DOCKER_MONERO_PRUNE` | `true` | Run monerod with a pruned chain. |
| `DOCKER_MONERO_RESTRICTED_RPC_ENABLED` | `true` | Serve monerod's restricted RPC on the restricted port. |
| `DOCKER_MONERO_RESTRICTED_RPC_PORT` | `18089` | Restricted RPC port. |
| `DOCKER_MONERO_PUBLIC_NODE` | `false` | Advertise the restricted RPC to other nodes. Applies only while the restricted RPC is enabled. |
| `DOCKER_MONERO_MONEROD_EXTRA_ARGS` | empty | Extra monerod flags, space-separated. Each must match `[A-Za-z0-9_./:=,@+-]+`. |
| `DOCKER_MONERO_READ_ONLY` | `false` | Refuse every write. Wins over the two transfer switches. |
| `DOCKER_MONERO_ALLOW_TRANSFERS` | `true` | Allow sends. Over REST this is the only switch besides read-only. |
| `DOCKER_MONERO_MCP_ALLOW_TRANSFERS` | `false` | Also register the `monero_transfer` MCP tool. Needs `DOCKER_MONERO_ALLOW_TRANSFERS`. |
| `DOCKER_MONERO_MAX_OPEN_WALLETS` | `8` | Wallets open at once, 1 to 64. Each is a separate process. |
| `DOCKER_MONERO_WALLET_START_TIMEOUT` | `60s` | How long opening a wallet may take. |
| `DOCKER_MONERO_RPC_TIMEOUT` | `60s` | Timeout for one call to monerod or a wallet process. |
| `DOCKER_MONERO_MONEROD_STOP_TIMEOUT` | `90s` | How long monerod gets to stop after SIGTERM. |
| `DOCKER_MONERO_MAX_REQUEST_BYTES` | `1048576` | Largest request body, in bytes. |
| `DOCKER_MONERO_HTTP_LISTEN_ADDRESS` | `127.0.0.1:8080` | API and MCP listener. The image sets `0.0.0.0:8080`. |
| `DOCKER_MONERO_METRICS_LISTEN_ADDRESS` | `127.0.0.1:9091` | Metrics listener, which must differ from the API address. The image sets `0.0.0.0:9091`. |
| `DOCKER_MONERO_P2P_PORT` | `18080` | Monero P2P port. |
| `DOCKER_MONERO_RPC_PORT` | `18081` | Unrestricted monerod RPC port, bound to loopback. |
| `DOCKER_MONERO_WALLET_RPC_PORT_START` | `18100` | First loopback port for wallet processes. The range runs for `DOCKER_MONERO_MAX_OPEN_WALLETS` ports (18100 to 18107 by default) and may not contain any other port. |
| `DOCKER_MONERO_MONEROD_PATH` | `/usr/local/bin/monerod` | monerod binary. |
| `DOCKER_MONERO_WALLET_RPC_PATH` | `/usr/local/bin/monero-wallet-rpc` | monero-wallet-rpc binary. |
| `DOCKER_MONERO_DATA_DIR` | `/data/monerod` | Blockchain directory. |
| `DOCKER_MONERO_WALLET_DIR` | `/data/wallets` | Wallet directory. |

## Write switches

| Goal | Settings |
| --- | --- |
| Watch only, no changes | `DOCKER_MONERO_READ_ONLY=true`. No create, restore, subaddress, send or mining. Opening and closing wallets still work, and MCP registers only the 11 read tools. |
| Agents read and manage wallets, REST sends | The defaults. MCP has no send tool. |
| Agents may also send | `DOCKER_MONERO_MCP_ALLOW_TRANSFERS=true`. |
| No sends anywhere | `DOCKER_MONERO_ALLOW_TRANSFERS=false`. |

## Data

`/data` is a volume. The chain is under `/data/monerod` and the wallets under `/data/wallets`, a directory private to the service user. Each wallet is a `<name>.keys` file plus a cache file, and the keys file is what makes the wallet exist. Back up `/data/wallets` and keep the wallet passwords separately.

## Regtest

`DOCKER_MONERO_NETWORK=regtest` runs monerod offline at a fixed difficulty, and the chain survives restarts. Create a wallet, then mine to its address with `POST /v1/node/regtest/generate-blocks`. A mined reward stays locked for 60 blocks, and a fresh chain needs enough outputs for ring decoys before a transaction can be built. The integration suite mines about 150 blocks before its first send, so watch `unlockedBalance` before trying one.

## Metrics

Prometheus scrapes `http://<host>:9091/metrics`. The listener has no authentication, so keep it on loopback or an internal network.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| The container exits at start | A configuration value failed validation. `docker logs monero` names it. A missing or short `DOCKER_MONERO_API_TOKEN` is the usual one. |
| 401 | The token is missing or wrong. |
| 503 "monerod is not available yet" | monerod is still starting. Wait for `/ready`. |
| Container unhealthy for a while after start | The first open of a large chain can outlast the start period. Compose marks it unhealthy and never restarts it. |
| 409 `NODE_NOT_SYNCED` or `WALLET_SYNCING` | The node or the wallet is still catching up. Check `synchronized` in `/v1/node/info` and `syncing` in the wallet status. |
| 409 `TOO_MANY_OPEN_WALLETS` | Close a wallet, or raise `DOCKER_MONERO_MAX_OPEN_WALLETS`. |
| 503 `WALLET_START_TIMEOUT` | The wallet process did not come up in time. Raise `DOCKER_MONERO_WALLET_START_TIMEOUT`. |
| `restarts` above 0 in `/v1/node/info` | monerod crashed and the service restarted it. Read the container logs. |

The compose file tracks `psyb0t/monero:latest`. Pin a digest in your own copy for a frozen version.
