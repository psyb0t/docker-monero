# Security

Prefix: SEC. This service can open wallets and send funds, so these hold for every change.

- **SEC1:** There is no unauthenticated mode. `config.Validate` requires `DOCKER_MONERO_API_TOKEN` with at least 32 characters. On the API listener only `/healthz` and `/ready` are open. Everything else sits in the protected group of `Router.Serbewr`, behind bearer auth and the body limit, so a new route goes there.
- **SEC2:** The metrics listener has no authentication and stays a separate address. `docker-compose.yml` publishes it on `127.0.0.1` only.
- **SEC3:** monerod's unrestricted RPC and every `monero-wallet-rpc` process bind `127.0.0.1` inside the container and are never published. Wallet processes run with `--disable-rpc-login` because only this service can reach them. Only P2P and the restricted RPC bind every interface (`internal/pkg/node/args.go`). Do not add a published port or a wider bind for the others.
- **SEC4:** No endpoint or tool returns a seed, spend key or view key. Restore accepts them over REST only (API2).
- **SEC5:** Child processes start from argument slices through `process.Launcher`, never through a shell. `DOCKER_MONERO_MONEROD_EXTRA_ARGS` is checked against `extraArgPattern` in `internal/pkg/config/config.go`, and any new free-form value that reaches a command line gets the same treatment.
- **SEC6:** A new write operation checks the read-only switch in its core service (`requireWritable`, `access.ErrReadOnly`), and `DOCKER_MONERO_READ_ONLY` keeps winning over the two transfer switches. Cover it in `TestReadOnlyMode` (`tests/integration/deployments_test.go`).
- **SEC7:** Sending is refused while the node is not synchronized or the wallet is behind (`requireSynced`). A dry run goes through the same checks. Do not add a path that skips them.
- **SEC8:** The deployment floor stays as it is: non-root UID 10001, read-only root filesystem, all capabilities dropped, `no-new-privileges`, memory, CPU and PID limits, and a `0700` wallet directory. `make audit-compose` enforces the compose part. Do not loosen any of it to make something work.
- **SEC9:** Base images are pinned by digest and the Monero tarballs by SHA-256 in `Dockerfile`. Upgrading Monero means verifying the signed `hashes.txt` first, then changing the version and both hashes together.
