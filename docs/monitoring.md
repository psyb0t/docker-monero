---
title: "Monitoring"
description: "Health and readiness probes, the healthcheck command, every Prometheus metric docker-monero exports on port 9091, and the log fields."
nav_order: 7
---

# Monitoring

## Probes

| Endpoint | Token | Answers |
| --- | --- | --- |
| `GET /healthz` | none | `{"status":"ok"}` while the process runs. |
| `GET /ready` | none | `{"status":"ready"}` once monerod's RPC answers, 503 before that and while the service shuts down. |

Both live on the API listener (8080 in the image). `/ready` says monerod answers RPC, not that the chain is synced. Read `synchronized` and `syncPercent` in `GET /v1/node/info` for that.

The image's `HEALTHCHECK` runs `/app/docker-monero healthcheck`, which calls `/ready` on `DOCKER_MONERO_HTTP_LISTEN_ADDRESS` and exits non-zero unless it answers 200. The compose file runs the same command every 10 seconds. On a slow disk the first open of a large chain can outlast the start period, and the container then reads unhealthy for a while. Docker does not restart it for that.

## Metrics

Prometheus scrapes `http://<host>:9091/metrics`, set by `DOCKER_MONERO_METRICS_LISTEN_ADDRESS`. The listener has no authentication, so the compose file publishes it on `127.0.0.1` only. The metrics are defined in [`internal/pkg/metrics`](https://github.com/psyb0t/docker-monero/tree/main/internal/pkg/metrics).

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `monero_api_http_requests_total` | counter | `method`, `route`, `status` | HTTP requests. `route` is the OpenAPI path template, never a raw path. |
| `monero_api_http_request_duration_seconds` | histogram | `method`, `route` | HTTP request duration. |
| `monero_api_http_requests_in_flight` | gauge | | Requests being served right now. |
| `monero_api_mcp_tool_calls_total` | counter | `tool`, `outcome` | MCP tool calls. `outcome` is `success` or `error`. |
| `monero_api_mcp_tool_call_duration_seconds` | histogram | `tool` | MCP tool call duration. |
| `monero_open_wallets` | gauge | | Wallets open right now. |
| `monero_max_open_wallets` | gauge | | The configured `DOCKER_MONERO_MAX_OPEN_WALLETS`. |
| `monero_node_height` | gauge | | The node's chain height at scrape time, 0 when monerod does not answer. |
| `monero_node_synchronized` | gauge | | 1 when monerod reports itself synchronized. |
| `monero_node_restarts` | gauge | | Times monerod crashed and the supervisor restarted it. |

The Go runtime and process collectors (`go_*`, `process_*`) are registered too. No wallet name, address, amount or transaction hash is ever a label.

## Logs

The service logs to stdout as JSON by default (`LOG_FORMAT`, `LOG_LEVEL` on the [configuration](configuration.md#framework) page). monerod's and the wallet processes' output goes into the same log. monerod's lines carry `child` and `stream`, both set to `monerod`. A wallet process's lines carry `stream` set to `monero-wallet-rpc` and the wallet's name in `wallet`. Wallet passwords, seeds, keys, the token and request bodies are never logged, and request logging leaves out the query string.

```bash
docker logs -f monero
```
