# Architecture

One container, one Go process, a few children.

```text
                   ┌────────────────────────── container ───────────────────────────┐
  API / MCP :8080 ─┤ docker-monero (Go)                                             │
  metrics   :9091 ─┤   ├─ supervises ─▶ monerod ── P2P :18080, restricted RPC :18089 ├─▶ Monero network
                   │   │                  ▲ unrestricted RPC 127.0.0.1:18081        │
                   │   └─ wallet pool ─▶ monero-wallet-rpc (one per open wallet)     │
                   │                      127.0.0.1:18100+ ──────┘                   │
                   └─────────────────────────── /data ───────────────────────────────┘
```

tini is PID 1 and reaps children. The Go service is a [servicepack](https://github.com/psyb0t/servicepack) application with one service, `monero-server`, which owns startup and shutdown order.

## Packages

| Package | Role |
| --- | --- |
| `internal/pkg/services/monero-server` | Wires everything and runs the lifecycle. |
| `internal/pkg/config` | `DOCKER_MONERO_*` parsing and validation. |
| `internal/pkg/node` | monerod supervisor: builds its command line, restarts it with backoff when it crashes, reports when its RPC first answers, stops it with SIGTERM and a deadline. |
| `internal/pkg/walletpool` | One monero-wallet-rpc process per open wallet: port allocation, start and readiness, create/open/restore, save and stop on close, and dropping a process that dies on its own. |
| `internal/pkg/process` | Starts children through `psyb0t/commander` and forwards their output into the log. Tests swap in a fake launcher. |
| `internal/pkg/monero/{jsonrpc,daemonrpc,walletrpc}` | Typed RPC clients for monerod and monero-wallet-rpc. |
| `internal/pkg/core/{node,wallets}` | The business rules both transports share: validation, sync gating, write switches, and conversion to the API types. |
| `internal/pkg/http` | Generated OpenAPI server (`api/`) and the handlers and aichteeteapee router (`server/`). |
| `internal/pkg/mcp` | MCP tools over the same core services. |
| `internal/pkg/apierrors` | Maps errors to a status and a stable code for both transports. |
| `internal/pkg/metrics` | The Prometheus registry. |

## Startup

1. Parse and validate configuration. A bad value stops here.
2. Start monerod. The API starts listening right away; `/ready` reports 503 until monerod's RPC answers, and node and wallet calls answer 503 until then.
3. Wallets open on request. Nothing is opened automatically at boot.

## Shutdown

On SIGTERM, in order:

1. `/ready` starts failing and the API listener drains.
2. Every open wallet is saved and its process stopped.
3. monerod gets SIGTERM and up to `DOCKER_MONERO_MONEROD_STOP_TIMEOUT` to flush its database.
4. The metrics listener stops.

## If monerod crashes

The supervisor restarts it after 2 seconds, doubling the wait up to a minute for repeated crashes, and resets the backoff after ten minutes of stable running. `restarts` in `GET /v1/node/info` and the `monero_node_restarts` metric count these. If monerod cannot be started at all, the service exits.

## If a wallet process dies

The pool drops it and the wallet reads as closed. Open it again.
