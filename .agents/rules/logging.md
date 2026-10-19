# Logging and metrics

Prefix: LOG.

- **LOG1:** Log through `ctxscope.GetLogger(ctx)`, so the request scope (request ID, service, wallet) travels with the record. Do not use `slog.Default()`, `log` or `fmt.Print`. The output of the monerod and wallet processes is the exception: `internal/pkg/process` forwards it through the `*slog.Logger` it is given.
- **LOG2:** Fields are structured, with lowercase keys. A key that repeats is a named constant (`scopeKeyWallet` in `internal/pkg/walletpool`, `scopeKeyService`, `scopeKeyChild`).
- **LOG3:** Never log a wallet password, seed, key, token or request body. Passwords arrive in request bodies, so no handler or middleware logs one. The router's `Logger` middleware runs with `WithIncludeQuery(false)`, and a change that adds a body or query field to a log record needs a reason that rules out secrets.
- **LOG4:** Metric labels are bounded. Routes are OpenAPI path templates, tools are MCP tool names, outcomes are a fixed set. A wallet name, address, amount or transaction hash never becomes a label (`internal/pkg/metrics`).
