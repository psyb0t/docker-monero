---
name: monero
description: "Operate a running docker-monero instance over REST or MCP: read node sync state, blocks, transactions and fee estimates, create and open wallets, read balances, addresses and transfer history, and send XMR. Use when the user runs docker-monero and wants to check the node, inspect a wallet, get a receiving address, or make a payment."
homepage: https://github.com/psyb0t/docker-monero
user-invocable: true
metadata:
  openclaw:
    primaryEnv: MONERO_TOKEN
    requires:
      bins:
        - curl
      env:
        - MONERO_URL
        - MONERO_TOKEN
permissions:
  network: "Outbound HTTP only to the user-configured MONERO_URL, a docker-monero instance the user runs. Requests carry the bearer token and can carry wallet passwords and payment destinations, so use a loopback address or an HTTPS endpoint."
  shell: "curl against MONERO_URL for ordinary use. Docker, git and openssl commands appear only in references/setup.md, for an install or check the user asked for."
  filesystem: "No filesystem access for ordinary use. Setup writes only the .env file the user asks for."
---

# monero

docker-monero runs the official `monerod` and `monero-wallet-rpc` behind one Go service. It serves a REST API under `/v1` and an MCP endpoint at `/mcp` on the same port (8080 in the image), and every call needs a bearer token. This skill drives an instance the user already runs. It installs nothing on its own. For a new deployment, read [references/setup.md](references/setup.md) when the user asks.

## Security & safety

- Take the URL and token from `MONERO_URL` and `MONERO_TOKEN` in the environment, or ask the user. Do not search the workspace, `.env` files or compose files for a token.
- `MONERO_URL` must be an instance the user runs. Every request carries the token and may carry wallet passwords and payment destinations, so use a loopback address or HTTPS, not plain HTTP over an untrusted network.
- Sending funds is irreversible. Call `POST /v1/wallets/{name}/transfers` or `monero_transfer` only when the user explicitly asks for that payment, for the destination and amount they gave, and run it with `dryRun: true` first to show the fee.
- Work only with the wallets the task names. Wallet passwords travel in the body of create and open calls, so use the password the user supplied for that wallet. Do not choose, store or repeat one.
- Restoring a wallet from a seed or keys exists only in REST (`/v1/wallets/restore/seed` and `/v1/wallets/restore/keys`) and is deliberately not an MCP tool. Do not ask for or accept a seed or spend key in the conversation. Point the user to `api/api.yml` in the repository so they run the restore themselves.
- A transfer result contains a `txKey`, which proves the payment to a recipient. Hand it to the user and nowhere else.

## When to use

- Check node height, sync progress, peers and version, or look up a block, a transaction, the fee estimate or mempool statistics.
- Create, open and close wallets, read a balance, list addresses, create a subaddress to receive funds, or read transfer history.
- Send XMR when the user asks for a specific payment.
- On a regtest deployment, mine blocks to fund a test wallet.

## When NOT to use

- The user has no docker-monero instance, or asks a general question about Monero, prices or other coins.
- The task needs `monero-wallet-cli`, direct `monero-wallet-rpc` access, or seed and key export. This API offers none of them.
- The user wants to deploy or reconfigure the service and has not asked for that. Setup is in [references/setup.md](references/setup.md).

## Connect

```bash
export MONERO_URL=http://localhost:8080
export MONERO_TOKEN=<token-from-the-user>
H="Authorization: Bearer $MONERO_TOKEN"
J="Content-Type: application/json"

curl -s "$MONERO_URL/healthz"    # {"status":"ok"}, no token needed
curl -s "$MONERO_URL/ready"      # {"status":"ready"} once monerod's RPC answers, no token needed
curl -s -H "$H" "$MONERO_URL/v1/node/info"
```

The examples below use `$H` and `$J` from this block. Everything under `/v1` and `/mcp` needs the bearer header, and a missing or wrong token gets 401. Every failure carries `{"code": "...", "message": "..."}`, and `code` is the stable value to branch on.

`DOCKER_MONERO_RPC_LOGIN` optionally protects monerod's RPC with a separate username and password for remote wallets on 18089. REST and MCP clients continue to use the bearer token; the service handles its internal RPC login.

Amounts are strings of atomic units (1 XMR = 10^12), because JSON numbers lose precision above 2^53. Responses add an XMR decimal string next to each amount (`balance` and `balanceXmr`). Requests take atomic units only. Check `network` in `/v1/node/info` before sending, because addresses are network-specific.

## Node

```bash
curl -s -H "$H" "$MONERO_URL/v1/node/info"                    # network, height, targetHeight, synchronized, syncPercent, connections, version
curl -s -H "$H" "$MONERO_URL/v1/node/blocks/latest"
curl -s -H "$H" "$MONERO_URL/v1/node/blocks/height/3000000"
curl -s -H "$H" "$MONERO_URL/v1/node/blocks/hash/<64-hex-block-hash>"
curl -s -H "$H" "$MONERO_URL/v1/node/transactions/<64-hex-tx-hash>"   # inPool, confirmations, blockHeight
curl -s -H "$H" "$MONERO_URL/v1/node/fee-estimate"
curl -s -H "$H" "$MONERO_URL/v1/node/mempool"
```

On regtest only, `POST /v1/node/regtest/generate-blocks` mines up to 1000 blocks and pays the rewards to an address. Any other network answers 409 `NOT_REGTEST`.

```bash
curl -s -X POST -H "$H" -H "$J" \
  -d '{"count": 100, "address": "<regtest-address>"}' \
  "$MONERO_URL/v1/node/regtest/generate-blocks"
```

## Wallets

Each open wallet is its own `monero-wallet-rpc` process, and the number of open wallets is capped (8 by default, 409 `TOO_MANY_OPEN_WALLETS` beyond it). A wallet must be open before its balance, addresses or transfers can be read, otherwise the call answers 409 `WALLET_NOT_OPEN`. Close wallets you opened once the task is done. Wallet names are letters, digits, `_` and `-`, starting with a letter or digit, up to 64 characters.

```bash
curl -s -H "$H" "$MONERO_URL/v1/wallets"                                  # every wallet and whether it is open

curl -s -X POST -H "$H" -H "$J" -d '{"name": "alice", "password": "<wallet-password>"}' \
  "$MONERO_URL/v1/wallets"                                                # 201, creates and opens

curl -s -X POST -H "$H" -H "$J" -d '{"password": "<wallet-password>"}' \
  "$MONERO_URL/v1/wallets/alice/open"                                     # a wrong password is 403 INVALID_WALLET_PASSWORD

curl -s -X POST -H "$H" "$MONERO_URL/v1/wallets/alice/close"              # saves, then closes
curl -s -H "$H" "$MONERO_URL/v1/wallets/alice/status"                     # walletHeight, nodeHeight, syncing, blocksBehind
curl -s -H "$H" "$MONERO_URL/v1/wallets/alice/balance?accountIndex=0"     # balance and unlockedBalance
curl -s -H "$H" "$MONERO_URL/v1/wallets/alice/addresses?accountIndex=0"   # primary address and subaddresses

curl -s -X POST -H "$H" -H "$J" -d '{"accountIndex": 0, "label": "invoice-42"}' \
  "$MONERO_URL/v1/wallets/alice/addresses"                                # 201, a new subaddress to receive funds

curl -s -H "$H" "$MONERO_URL/v1/wallets/alice/transfers?direction=in&direction=pending&limit=50&offset=0"
```

Transfer history is newest first, filtered by any of `in`, `out`, `pending`, `failed`, `pool` (all when omitted), with `limit` 1 to 1000 (default 100) and `offset`. Only `unlockedBalance` is spendable. A wallet whose `syncing` is true is still catching up and refuses to send.

## Sending

`POST /v1/wallets/{name}/transfers` takes 1 to 16 `destinations`, each an `address` and an atomic `amount`. Optional fields are `accountIndex`, `subaddressIndices` (spend only from those subaddresses), `priority` (`default`, `low`, `normal`, `high`, `highest`) and `dryRun`. The result has `txHash`, `txKey`, `amount`, `fee`, `weight` and `relayed`, which is false for a dry run.

```bash
curl -s -X POST -H "$H" -H "$J" \
  -d '{"destinations": [{"address": "<destination-address>", "amount": "1000000000"}], "priority": "normal", "dryRun": true}' \
  "$MONERO_URL/v1/wallets/alice/transfers"
```

Repeat the same body without `dryRun` to broadcast. Refusals to branch on:

- 409 `NODE_NOT_SYNCED` or `WALLET_SYNCING`. Wait and check `/v1/node/info` and the wallet status again.
- 403 `TRANSFERS_DISABLED` or `READ_ONLY`. The operator turned sending off.
- 422 `INSUFFICIENT_FUNDS`, `VIEW_ONLY_WALLET` or `TRANSFER_REJECTED`.

## Deployment modes

The operator sets what is allowed with environment variables on the server.

- `DOCKER_MONERO_READ_ONLY=true` refuses every write with 403 `READ_ONLY`: creating or restoring wallets, new subaddresses, sends and regtest mining. Opening and closing wallets still work.
- `DOCKER_MONERO_ALLOW_TRANSFERS=false` refuses REST sends with 403 `TRANSFERS_DISABLED` and removes the MCP send tool.
- `DOCKER_MONERO_MCP_ALLOW_TRANSFERS` defaults to false, so the MCP server has no send tool unless the operator turns it on. REST sends still work.

## MCP

`/mcp` speaks Streamable HTTP, stateless, with the same bearer token. The tools call the same code as the REST routes and return the same JSON, with the same field names (`accountIndex`, `dryRun`, `txHash`). A client that only speaks stdio can use the `@psyb0t/monero` OpenClaw plugin, which bridges to this endpoint.

```bash
curl -s -X POST "$MONERO_URL/mcp" -H "$H" -H "$J" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}'
```

The server registers its tools conditionally, so `tools/list` is the live answer:

| Tools | Registered |
| --- | --- |
| `monero_node_info`, `monero_get_block`, `monero_get_transaction`, `monero_fee_estimate`, `monero_list_wallets`, `monero_open_wallet`, `monero_close_wallet`, `monero_wallet_status`, `monero_wallet_balance`, `monero_wallet_addresses`, `monero_wallet_transfers` | always |
| `monero_create_wallet`, `monero_create_address` | unless the deployment is read-only |
| `monero_generate_blocks` | not read-only and on regtest |
| `monero_transfer` | not read-only, `DOCKER_MONERO_ALLOW_TRANSFERS` on, and `DOCKER_MONERO_MCP_ALLOW_TRANSFERS=true` |

Mempool statistics and wallet restore have no MCP tool. `monero_transfer` has no `subaddressIndices` field. A failed tool call sets `isError` and its text is `CODE: message`, for example `WALLET_NOT_OPEN: the wallet is not open`.

For installation, configuration variables, ports and troubleshooting, read [references/setup.md](references/setup.md).
