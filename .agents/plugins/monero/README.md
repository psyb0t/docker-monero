# @psyb0t/monero

An OpenClaw/MCP plugin that connects your agent to a self-hosted [docker-monero](https://github.com/psyb0t/docker-monero) node and its wallets over the [Model Context Protocol](https://modelcontextprotocol.io).

docker-monero already serves a Streamable HTTP MCP endpoint at `/mcp`. This package is a thin stdio to HTTP bridge (through [`mcp-remote`](https://www.npmjs.com/package/mcp-remote)) for MCP clients that only speak local stdio servers. It forwards everything to your running instance and sends your bearer token with every call.

> docker-monero is self-hosted. This plugin ships no node and no wallet. It connects to an instance you run. See the [docker-monero repo](https://github.com/psyb0t/docker-monero) to stand one up.

## Tools

The server registers its tools conditionally, so what your agent sees depends on how the instance is configured.

| Tools | Registered |
| --- | --- |
| `monero_node_info`, `monero_get_block`, `monero_get_transaction`, `monero_fee_estimate`, `monero_list_wallets`, `monero_open_wallet`, `monero_close_wallet`, `monero_wallet_status`, `monero_wallet_balance`, `monero_wallet_addresses`, `monero_wallet_transfers` | always |
| `monero_create_wallet`, `monero_create_address` | unless `DOCKER_MONERO_READ_ONLY=true` |
| `monero_generate_blocks` | not read-only and `DOCKER_MONERO_NETWORK=regtest` |
| `monero_transfer` | not read-only, `DOCKER_MONERO_ALLOW_TRANSFERS` on, and `DOCKER_MONERO_MCP_ALLOW_TRANSFERS=true` |

Amounts are strings of atomic units (1 XMR = 10^12). A wallet must be open, through `monero_open_wallet` with its password, before its balance, addresses or transfers can be read. Wallet restore and mempool statistics exist in REST only.

## Configuration

| Env var | Required | Description |
| --- | --- | --- |
| `MONERO_URL` | yes | Base URL of your running docker-monero server, e.g. `http://localhost:8080`. The bridge appends `/mcp`. |
| `MONERO_TOKEN` | yes | Bearer token, the value of `DOCKER_MONERO_API_TOKEN` on the server. |

## Install

Install it into your OpenClaw agent from ClawHub:

```bash
openclaw plugins install clawhub:@psyb0t/monero
```

Then set `MONERO_URL` and `MONERO_TOKEN` in the plugin's environment.

For clients on the Docker host, publish the API on loopback with `-p 127.0.0.1:8080:8080`. The image listens on `0.0.0.0:8080` inside the container. Another container uses a shared-network URL such as `http://monero:8080`, because its own `localhost` is itself. A remote client needs a deliberately reachable endpoint, preferably HTTPS.

## Native remote MCP (no install)

If your MCP client supports remote Streamable HTTP servers, you do not need this bridge. Point the client at `$MONERO_URL/mcp` with an `Authorization: Bearer <token>` header.

## Permissions and state

The bridge stores nothing. It runs on your host, starts one `mcp-remote` child process, and sends the token to `MONERO_URL` as an `Authorization` header, passed as a command-line argument to that child. The pinned `mcp-remote` also prints its custom headers, the token included, to stderr at startup, so keep the bridge's stderr out of shared logs. Every tool input goes to the same URL too, wallet passwords included. Point it only at an instance you run, over loopback or HTTPS. The server keeps the chain and the wallet files in `/data` inside the container, which you bind-mount from a host directory using `DOCKER_MONERO_DATA_PATH` (default `./data`) in Compose.

## License

WTFPL. See [LICENSE](LICENSE).
