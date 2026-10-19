# Security

This service can spend money. These are the boundaries it keeps and the ones you have to keep.

## What the service enforces

- **Token on everything.** The API and MCP refuse to start without a token of at least 32 characters, and compare it in constant time. Only `/healthz` and `/ready` are open.
- **Unrestricted node RPC stays inside the container.** monerod's full RPC (port 18081), which can stop the daemon, roll back the chain, and ban peers, binds `127.0.0.1` inside the container. Only the restricted RPC (18089) and P2P (18080) bind every interface.
- **Wallet processes stay inside the container.** Each monero-wallet-rpc binds a loopback port with its login disabled. The container's loopback is shared only by this service and its own child processes.
- **Wallet passwords never reach a command line.** Passwords go to the wallet process over its loopback RPC, so they do not show up in `/proc` or `ps`. They are never logged.
- **Sends wait for sync.** A transfer is refused until the node is synchronized and the wallet has caught up with it.
- **Write switches.** `DOCKER_MONERO_READ_ONLY` refuses every write, `DOCKER_MONERO_ALLOW_TRANSFERS=false` refuses sends, and MCP agents cannot send unless `DOCKER_MONERO_MCP_ALLOW_TRANSFERS=true`. See [configuration.md](configuration.md#write-switches).
- **No secrets in error responses.** Errors carry a fixed code and message; internal error text stays in the log.
- **Hardened image.** Non-root UID 10001, works with a read-only root filesystem and every capability dropped. The Monero release tarballs are checked against SHA-256 hashes pinned in the Dockerfile from the signed `hashes.txt`.

## What you have to do

- **Do not publish 8080 to the internet.** Bind it to loopback and put a reverse proxy with TLS in front, or reach it over a tailnet. The token travels in a header, so plain HTTP across a network exposes it.
- **Back up `/data/wallets`.** The `.keys` files are your wallets. Each is encrypted with its password; a lost password cannot be recovered. Keep the seeds offline too.
- **Pick strong wallet passwords.** Anyone with the token and a wallet's password can open and spend it.
- **Run read-only when you can.** Once the wallets you need exist, a watching deployment does not need writes.
- **Treat the token like a private key.** Anyone holding it can open wallets whose passwords they know, create wallets, and send from open wallets unless transfers are disabled.
