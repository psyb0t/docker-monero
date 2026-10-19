# Configuration

Everything is configured through environment variables, read once at startup. A bad value stops the container before monerod starts, with an error naming the variable. `.env.example` lists the ones most deployments touch.

## Service

| Variable | Default | What it does |
| --- | --- | --- |
| `DOCKER_MONERO_API_TOKEN` | none, required | Bearer token for every API and MCP call. At least 32 characters. Generate one with `openssl rand -hex 32`. |
| `DOCKER_MONERO_HTTP_LISTEN_ADDRESS` | `0.0.0.0:8080` in the image | API, MCP, `/healthz`, `/ready`. |
| `DOCKER_MONERO_METRICS_LISTEN_ADDRESS` | `0.0.0.0:9091` in the image | Prometheus `/metrics`. Must differ from the API address. |
| `DOCKER_MONERO_MAX_REQUEST_BYTES` | `1048576` | Largest request body the API accepts. |
| `DOCKER_MONERO_RPC_TIMEOUT` | `60s` | Timeout for one call to monerod or a wallet process. |

Outside the image (running the binary directly) both listeners default to `127.0.0.1`.

## Write switches

| Variable | Default | What it does |
| --- | --- | --- |
| `DOCKER_MONERO_READ_ONLY` | `false` | Refuses every write with `403 READ_ONLY`: creating or restoring a wallet, creating a subaddress, sending, and regtest mining. Opening and closing wallets still work, because a wallet has to be open to read it. MCP registers no write tool. Wins over the two switches below. |
| `DOCKER_MONERO_ALLOW_TRANSFERS` | `true` | `false` refuses sends with `403 TRANSFERS_DISABLED` while every other write keeps working. |
| `DOCKER_MONERO_MCP_ALLOW_TRANSFERS` | `false` | Registers the `monero_transfer` MCP tool, so an AI agent can send funds. Needs `DOCKER_MONERO_ALLOW_TRANSFERS` on as well. |

A typical lifecycle: run with writes on while you create or restore the wallets you need, then restart with `DOCKER_MONERO_READ_ONLY=true` for a deployment that can only watch.

## Node

| Variable | Default | What it does |
| --- | --- | --- |
| `DOCKER_MONERO_NETWORK` | `mainnet` | `mainnet`, `stagenet`, `testnet`, or `regtest`. Regtest is an offline private chain where blocks are mined on demand; it is what the test suite uses. |
| `DOCKER_MONERO_PRUNE` | `true` | Runs monerod with `--prune-blockchain`, keeping about a third of the full chain. Decide before the first sync: switching later means syncing again. |
| `DOCKER_MONERO_DATA_DIR` | `/data/monerod` | monerod's database directory. |
| `DOCKER_MONERO_P2P_PORT` | `18080` | P2P port, bound on every interface. |
| `DOCKER_MONERO_RPC_PORT` | `18081` | Unrestricted RPC, bound to `127.0.0.1` inside the container and never published. The service and the wallet processes use it. |
| `DOCKER_MONERO_RESTRICTED_RPC_ENABLED` | `true` | Starts monerod's restricted RPC listener, the one remote wallets such as Cake Wallet connect to. |
| `DOCKER_MONERO_RESTRICTED_RPC_PORT` | `18089` | Port of that listener, bound on every interface. |
| `DOCKER_MONERO_PUBLIC_NODE` | `false` | Adds `--public-node`, which advertises the restricted RPC to other nodes. Only takes effect with the restricted RPC enabled. |
| `DOCKER_MONERO_MONEROD_EXTRA_ARGS` | empty | Extra monerod flags separated by spaces, for example `--out-peers=32 --limit-rate-up=2048`. Each argument may use letters, digits, and `_ . / : = , @ + -`; anything else is rejected at startup. |
| `DOCKER_MONERO_MONEROD_STOP_TIMEOUT` | `90s` | How long monerod gets to flush its database after SIGTERM before it is killed. |
| `DOCKER_MONERO_MONEROD_PATH` | `/usr/local/bin/monerod` | Binary path. |

## Wallets

| Variable | Default | What it does |
| --- | --- | --- |
| `DOCKER_MONERO_WALLET_DIR` | `/data/wallets` | Wallet files. Created with mode `0700`. |
| `DOCKER_MONERO_MAX_OPEN_WALLETS` | `8` | Wallets open at once. Each open wallet is one monero-wallet-rpc process. Up to 64. |
| `DOCKER_MONERO_WALLET_RPC_PORT_START` | `18100` | First loopback port for wallet processes; the range is as wide as the open-wallet limit and must not overlap the node's ports. |
| `DOCKER_MONERO_WALLET_START_TIMEOUT` | `60s` | How long a new wallet process gets to answer before the open fails. |
| `DOCKER_MONERO_WALLET_RPC_PATH` | `/usr/local/bin/monero-wallet-rpc` | Binary path. |

## Framework

The service is built on [servicepack](https://github.com/psyb0t/servicepack), which reads a few variables of its own.

| Variable | Image default | What it does |
| --- | --- | --- |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. |
| `LOG_FORMAT` | `json` | `json` or `text`. monerod's and the wallet processes' output is forwarded into the same log, tagged with `child` and `stream`. |
| `RUNNER_SHUTDOWNTIMEOUT` | `170s` | Whole-shutdown budget: draining the API, saving every open wallet, and monerod's flush. Give `docker stop` at least this long (`docker stop -t 180`, or `stop_grace_period` in compose). |

## Ports and data directory

| Port | Bound | Purpose |
| --- | --- | --- |
| `8080` | publish to loopback or a proxy | API and MCP |
| `9091` | publish to loopback | Prometheus metrics |
| `18080` | public | Monero P2P |
| `18089` | public if you want remote wallets | Restricted RPC |

Compose bind-mounts the chosen host data directory at `/data`, with the chain in `monerod` and wallets in `wallets`. Plan for about 105 GiB pruned or 265 GiB full on mainnet as of late 2026, plus room to grow; monerod's documentation recommends an SSD.

`DOCKER_MONERO_DATA_PATH` is a Compose-level variable, not an application setting:

| Variable | Default | What it does |
| --- | --- | --- |
| `DOCKER_MONERO_DATA_PATH` | `./data` | Host directory bind-mounted at `/data` for the chain and wallets. |

The image runs as UID/GID `10001`. Create the host directory and make it writable by that user before starting Compose: `mkdir -p ./data && sudo chown 10001:10001 ./data` for the default path. Set a different path in `.env` and prepare that directory instead when needed.

Before upgrading an existing deployment, stop it cleanly and copy the data out of its old Docker volume into the chosen host directory, preserving the `monerod` and `wallets` subdirectories. Ensure UID/GID `10001` can write there before restarting.
