---
title: "Troubleshooting"
description: "Why docker-monero refuses to start, what each configuration error means, and how to clear the common 401, 409 and 503 API answers."
nav_order: 8
---

# Troubleshooting

## The container exits at start

Configuration is checked once, before monerod starts, and the first bad value stops the process. `docker logs monero` shows the error. The texts below come from [`internal/pkg/config/config.go`](https://github.com/psyb0t/docker-monero/blob/main/internal/pkg/config/config.go).

| Error contains | Fix |
| --- | --- |
| `DOCKER_MONERO_API_TOKEN: required config value is not set` | Set the token. There is no unauthenticated mode. |
| `DOCKER_MONERO_API_TOKEN must be at least 32 characters` | Use a longer token, for example `openssl rand -hex 32`. |
| `... is not host:port` | Fix `DOCKER_MONERO_HTTP_LISTEN_ADDRESS` or `DOCKER_MONERO_METRICS_LISTEN_ADDRESS`. |
| `the metrics listener must not share an address with the API` | Give the two listeners different addresses. |
| `DOCKER_MONERO_NETWORK must be one of mainnet, stagenet, testnet, regtest` | Fix the network name. |
| `DOCKER_MONERO_RPC_LOGIN requires username:password, password minimum 16 characters` | Use `username:password` with a non-empty username and a password of 16 characters or more. |
| `DOCKER_MONERO_RPC_LOGIN disallows control characters, # and surrounding whitespace` | Remove `#`, control characters and leading or trailing spaces from the login. |
| `DOCKER_MONERO_MONEROD_EXTRA_ARGS contains an unsupported argument` | Each extra argument may use only letters, digits and `_ . / : = , @ + -`. Drop quotes and shell syntax. |
| `... is not a valid port` | Ports run from 1 to 65535. |
| `wallet-rpc ports ... run past 65535` | Lower `DOCKER_MONERO_WALLET_RPC_PORT_START` or `DOCKER_MONERO_MAX_OPEN_WALLETS`. |
| `... share port ...` or `... falls inside the wallet-rpc port range ...` | Move the node port or the wallet port range so they do not overlap. |
| `DOCKER_MONERO_MAX_OPEN_WALLETS must be between 1 and 64` | Pick a value in range. |
| `... must be positive` | A timeout or `DOCKER_MONERO_MAX_REQUEST_BYTES` is zero or negative. |

A data directory the container cannot write also stops it. The image runs as UID/GID `10001`, so the host directory behind `/data` needs `sudo chown 10001:10001 <dir>`.

## API answers

| Answer | Cause and fix |
| --- | --- |
| 401 `UNAUTHORIZED` | The bearer token is missing or wrong. |
| 503 `SERVICE_UNAVAILABLE`, "monerod is not available yet" | monerod is still starting or restarting. Wait for `/ready`. |
| 409 `NODE_NOT_SYNCED` or `WALLET_SYNCING` | The node or the wallet is still catching up. Check `synchronized` in `GET /v1/node/info` and `syncing` in the wallet status. |
| 409 `WALLET_NOT_OPEN` | Open the wallet with its password first. |
| 409 `WALLET_BUSY` | The wallet is opening or closing. Retry in a second or two. |
| 409 `TOO_MANY_OPEN_WALLETS` | Close a wallet, or raise `DOCKER_MONERO_MAX_OPEN_WALLETS`. |
| 503 `WALLET_START_TIMEOUT` | The wallet process did not answer in time. Raise `DOCKER_MONERO_WALLET_START_TIMEOUT`. |
| 504 `GATEWAY_TIMEOUT` | monerod or the wallet did not answer within `DOCKER_MONERO_RPC_TIMEOUT`. |
| 502 `RPC_AUTHENTICATION_FAILED` | monerod rejected the service's RPC login. Check `DOCKER_MONERO_RPC_LOGIN`. |
| 403 `READ_ONLY` or `TRANSFERS_DISABLED` | A write switch refuses the call. See [write switches](configuration.md#write-switches). |
| 422 `INSUFFICIENT_FUNDS` | Not enough unlocked funds for the amount plus the fee. Received funds unlock after 10 blocks, mined rewards after 60. |

Every code is listed on the [API](api.md#error-codes) page.

## Other symptoms

| Symptom | Cause and fix |
| --- | --- |
| The container reads unhealthy for a while after start | The first open of a large chain can outlast the health check's start period. Docker does not restart it; wait for `/ready`. |
| `restarts` above 0 in `GET /v1/node/info` | monerod crashed and the supervisor restarted it. Read the container logs around the restart. |
| A wallet reads as closed without a close call | Its process died and the pool dropped it. Open it again. |
| A regtest send fails with `TRANSFER_REJECTED` | A fresh regtest chain needs enough outputs for ring decoys. Mine more blocks, about 150 before the first send. |
