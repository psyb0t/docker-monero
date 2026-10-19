# Testing

Prefix: TEST. How to run each suite is in `AGENTS.md` and `docs/development.md`.

- **TEST1:** Unit tests sit beside the code and never start Monero. The wallet pool and the core services run against the in-memory wallet RPC in `internal/pkg/monero/walletrpc/walletrpctest`, started by the fake launcher in `internal/pkg/process/processtest`. REST handler tests use the harness in `internal/pkg/http/server/harness_test.go`.
- **TEST2:** Behavior that needs the real monerod or `monero-wallet-rpc` (sync, coinbase maturity, transfers, restarts, the deployment switches, the MCP tool list) is tested in `tests/integration/` through `testinfra.StartApp`. The shared app is the default deployment. A test that needs another configuration starts its own app, and every test uses its own wallet names.
- **TEST3:** Tests use testify (`assert`, `require`).
- **TEST4:** A change to the registered MCP tools updates `TestMCPToolList` in `tests/integration/mcp_test.go` and the tool lists in the agent surface (AGT2).
- **TEST5:** The coverage floor is 90%. Raise coverage for the code you add rather than lowering `MIN_TEST_COVERAGE` or excluding files.
