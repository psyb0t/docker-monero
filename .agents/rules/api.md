# API and MCP

Prefix: API. The contract is `api/api.yml`. The workflow for changing it is in `AGENTS.md`.

- **API1:** REST handlers (`internal/pkg/http/server`) and MCP tools (`internal/pkg/mcp`) are transports. Each collects input, calls one method on a core service (`internal/pkg/core/node`, `internal/pkg/core/wallets`) and returns the generated `api` type. Validation, conversion, the write switches and the sync checks live in core, so REST and MCP behave the same.
- **API2:** A new read operation gets a REST route and an MCP tool in the same change. Wallet restore stays out of MCP on purpose, because a seed or spend key must not pass through an agent's context (package comment in `internal/pkg/mcp/mcp.go`).
- **API3:** MCP registration is decided in `toolset.register`. Read tools are always registered. Write tools are registered only when the deployment is not read-only. `monero_generate_blocks` needs regtest. `monero_transfer` needs `AllowTransfers && MCPAllowTransfers`, computed in `internal/pkg/services/monero-server/dependencies.go`. A read-only deployment never shows an agent a tool it cannot use. `monero_open_wallet` and `monero_close_wallet` count as read tools, because reading a wallet needs it open.
- **API4:** Amounts are strings of atomic units (`AtomicAmount`), each paired with an XMR decimal string for display. Conversion goes through `internal/pkg/monero/amount`. No amount passes through a float or a JSON number.
- **API5:** A limit that both the contract and the code use (list limits, destination count, label and password length, blocks per mining call) is an `x-constants` entry in `api/api.yml` and comes out as a constant in `internal/pkg/http/api/constants.gen.go`. Use the generated constant, never the number.
- **API6:** A paged list reuses the `Limit` and `Offset` parameters and answers with `total`, `limit` and `offset`, as `TransferList` does.
- **API7:** Every wallet-scoped route takes the wallet name as `walletName` with the `WalletName` pattern. Names are validated in `walletpool`, never trusted as a path.
