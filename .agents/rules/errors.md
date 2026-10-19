# Errors

Prefix: ERR. Package map: sentinels sit in `errors.go` next to the code that raises them (`internal/pkg/walletpool`, `internal/pkg/core/wallets`, `internal/pkg/core/node`, `internal/pkg/monero/daemonrpc`).

- **ERR1:** Check `ctxerrors/commerr` before defining a sentinel, and wrap the `commerr` class it belongs to when one fits, as `internal/pkg/core/wallets/errors.go` does.
- **ERR2:** Return errors wrapped with `ctxerrors.Wrap` or `Wrapf` and a short action phrase. Do not create a fresh `errors.New` at a call site when a sentinel exists.
- **ERR3:** A service error reaches a client only through `apierrors.Classify`. A new sentinel a client can hit gets a `rules` entry in `internal/pkg/apierrors/apierrors.go` with its status, a stable code and a client-safe message, placed before the generic fallbacks, and a case in `apierrors_test.go`.
- **ERR4:** Error codes are part of the API. They are UPPER_SNAKE constants in `apierrors.go` (`READ_ONLY`, `WALLET_NOT_OPEN`, and the rest), and they are never renamed or reused for another meaning.
- **ERR5:** Client-visible text never comes from the error chain, because `ctxerrors` renders call sites. A caller mistake with a message that is safe to show uses `invalid.Newf`. Everything else gets the mapped generic message. The full chain is logged, by `fail` for REST (warn below 500, error from 500) and by `instrument` for MCP.
- **ERR6:** A failure that crosses to monerod or a wallet process maps timeouts and unavailability onto the `commerr` classes `Classify` already handles (`ErrTimeout`, `ErrNotReady`, `ErrUnavailable`, `ErrConnectFailed`). Do not invent a transport-specific class for them.
