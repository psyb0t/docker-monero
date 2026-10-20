# API

The contract is [`api/api.yml`](../api/api.yml), OpenAPI 3.1. The server and the Go client in [`pkg/http/api/client`](../pkg/http/api/client) are both generated from it. This page covers the concepts the spec does not spell out.

## Basics

- Base path `/v1`. Every call needs `Authorization: Bearer <DOCKER_MONERO_API_TOKEN>`.
- `/healthz` (process alive) and `/ready` (monerod answers) need no token.
- Every failure is the same envelope: `{"code": "...", "message": "..."}`. Switch on `code`; the message is for humans.

## Amounts

Amounts are strings of atomic units. One XMR is 10^12 atomic units, so `"1500000000000"` is 1.5 XMR. JSON numbers lose precision above 2^53, which is about 9007 XMR, so amounts never travel as numbers. Every amount in a response comes with a display string next to it, such as `"amountXmr": "1.500000000000"`.

## Wallet lifecycle

Wallets are files in the wallet directory. A wallet has to be open before you can read or spend from it, and each open wallet runs in its own monero-wallet-rpc process.

```text
create / restore ──▶ open ──▶ status, balance, addresses, transfers, send ──▶ close
                      ▲                                                       │
                      └───────────────────────── open ◀───────────────────────┘
```

- `POST /v1/wallets` creates a wallet and leaves it open.
- `POST /v1/wallets/restore/seed` and `/restore/keys` restore one and leave it open. Without a spend key the result is a view-only wallet that can watch incoming funds but not send. Set `restoreHeight` close to the wallet's first transaction; the wallet scans the chain from there.
- `POST /v1/wallets/{name}/open` with the password, `POST /v1/wallets/{name}/close` to save and stop it.
- `GET /v1/wallets` lists every wallet file and whether it is open.

Opening and closing takes a second or two because a process starts or stops. While that happens the wallet answers `409 WALLET_BUSY`. When the open limit is reached, opening another wallet answers `409 TOO_MANY_OPEN_WALLETS`.

## Sync status

An open wallet scans new blocks in the background, every ten seconds. `GET /v1/wallets/{name}/status` shows where it is:

```json
{
  "name": "shop",
  "open": true,
  "walletHeight": 3412870,
  "nodeHeight": 3412901,
  "nodeSynchronized": true,
  "blocksBehind": 31,
  "syncPercent": 99.99,
  "syncing": true
}
```

`syncing` is true while the wallet is behind the node or the node itself is still syncing. A freshly restored wallet with an old `restoreHeight` can be behind for a long time. `GET /v1/node/info` reports the node's own progress the same way.

## Sending

`POST /v1/wallets/{name}/transfers` builds, signs, and broadcasts a transaction:

```json
{
  "destinations": [{ "address": "4...", "amount": "1500000000000" }],
  "priority": "normal",
  "dryRun": true
}
```

- `dryRun: true` builds and prices the transaction without broadcasting it. The response has `relayed: false` and the fee.
- A send is refused with `409 NODE_NOT_SYNCED` or `409 WALLET_SYNCING` until both the node and the wallet have caught up. A wallet that has not seen recent blocks can pick outputs it does not know are already spent.
- `txKey` in the response proves the payment to the recipient. Keep it.
- Received funds stay locked for 10 blocks (60 for mined rewards). `GET .../balance` shows the total and the unlocked part separately.

## Error codes

| Status | Code | Meaning |
| --- | --- | --- |
| 400 | `BAD_REQUEST` | Malformed input. The message says what. |
| 401 | `UNAUTHORIZED` | Missing or wrong token. |
| 403 | `READ_ONLY` | The deployment runs read-only. |
| 403 | `TRANSFERS_DISABLED` | The deployment disables sends. |
| 403 | `INVALID_WALLET_PASSWORD` | Wrong wallet password. |
| 404 | `NOT_FOUND` | No such wallet, block, transaction, or `/v1` route. |
| 405 | `METHOD_NOT_ALLOWED` | The `/v1` route does not support the method. `Allow` lists supported methods. |
| 409 | `WALLET_EXISTS` | A wallet with that name exists. |
| 409 | `WALLET_NOT_OPEN` | Open the wallet first. |
| 409 | `WALLET_ALREADY_OPEN` | Already open. |
| 409 | `WALLET_BUSY` | Opening or closing right now. |
| 409 | `TOO_MANY_OPEN_WALLETS` | Close a wallet first. |
| 409 | `NODE_NOT_SYNCED`, `WALLET_SYNCING` | Wait for sync before sending. |
| 409 | `NOT_REGTEST` | Block generation outside regtest. |
| 413 | `PAYLOAD_TOO_LARGE` | The request body exceeds `DOCKER_MONERO_MAX_REQUEST_BYTES`. |
| 422 | `INSUFFICIENT_FUNDS` | Not enough unlocked funds for amount plus fee. |
| 422 | `VIEW_ONLY_WALLET` | A view-only wallet cannot send. |
| 422 | `TRANSFER_REJECTED` | The wallet could not build the transaction. |
| 502 | `RPC_AUTHENTICATION_FAILED` | monerod rejected the service's RPC login. Check the node login configuration. |
| 503 | `SERVICE_UNAVAILABLE`, `WALLET_START_TIMEOUT`, `SHUTTING_DOWN` | monerod or a wallet process is not answering yet, or the service is stopping. |
| 504 | `GATEWAY_TIMEOUT` | monerod or the wallet did not answer within `DOCKER_MONERO_RPC_TIMEOUT`. |

## Regtest

On `DOCKER_MONERO_NETWORK=regtest`, `POST /v1/node/regtest/generate-blocks` mines blocks instantly and pays the rewards to an address. Use it to test an integration without real money. A fresh regtest chain needs a hundred or so blocks before transactions can be built, because each transaction picks decoy outputs from the chain.
