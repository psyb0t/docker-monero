# MCP

The same port as the API serves the [Model Context Protocol](https://modelcontextprotocol.io/) over Streamable HTTP at `/mcp`. Point any MCP client at `http://<host>:8080/mcp` with the header `Authorization: Bearer <DOCKER_MONERO_API_TOKEN>`.

Each tool calls the same code as the matching REST endpoint and returns the same JSON, so the behavior and the error codes are the ones in [api.md](api.md). A failed tool call returns `isError` with text such as `WALLET_NOT_OPEN: the wallet is not open`.

## Tools

| Tool | Does |
| --- | --- |
| `monero_node_info` | Node height, sync progress, connections, version. |
| `monero_get_block` | Block header by `height` or `hash`, or the latest block. |
| `monero_get_transaction` | A transaction's pool or block status and confirmations. |
| `monero_fee_estimate` | Per-byte fee for each priority. |
| `monero_list_wallets` | Every wallet and whether it is open. |
| `monero_open_wallet` | Open a wallet with its password. |
| `monero_close_wallet` | Save and close a wallet. |
| `monero_wallet_status` | Open or not, and how far it has synced. |
| `monero_wallet_balance` | Total and unlocked balance of one account. |
| `monero_wallet_addresses` | Primary address and subaddresses of one account. |
| `monero_wallet_transfers` | Transfer history, newest first, paginated. |
| `monero_create_wallet` | Create a wallet and leave it open. Not in read-only mode. |
| `monero_create_address` | Create a subaddress. Not in read-only mode. |
| `monero_generate_blocks` | Mine blocks. Regtest only, not in read-only mode. |
| `monero_transfer` | Send XMR. Only with `DOCKER_MONERO_MCP_ALLOW_TRANSFERS=true`, and never in read-only mode. |

Tools that are switched off are not registered at all, so an agent never sees an action it could not take.

Restoring a wallet from a seed or keys is not a tool on purpose: those secrets should not pass through an agent's context. Use the REST endpoints for that.
