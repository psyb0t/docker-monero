---
title: "Agent integrations"
description: "Install the docker-monero skill and plugins in Claude Code, Codex and OpenClaw, or connect any Streamable HTTP MCP client straight to /mcp."
nav_order: 5
---

# Agent integrations

An agent can drive a running docker-monero instance through a skill (REST routes, MCP tools, and the rules around wallets and sending) and, for stdio-only MCP clients, a bridge plugin. They live under [`.agents/`](https://github.com/psyb0t/docker-monero/tree/main/.agents) in the repository. Restart the agent session after installing.

## Claude Code

Claude Code asks for the instance URL and the API token when you enable the plugin:

```bash
claude plugin marketplace add psyb0t/agents
claude plugin install monero@psyb0t
```

## Codex

```bash
codex plugin marketplace add psyb0t/agents
codex plugin add monero@psyb0t
```

## OpenClaw

The skill and the MCP bridge plugin:

```bash
openclaw skills install @psyb0t/monero
openclaw plugins install clawhub:@psyb0t/monero
```

The skill and the bridge read `MONERO_URL`, for example `http://localhost:8080`, and `MONERO_TOKEN`, the value of `DOCKER_MONERO_API_TOKEN`. The bridge wraps `mcp-remote`, which prints its custom headers, the token included, to stderr at startup, so keep the bridge's stderr out of shared logs.

## Any MCP client

A client that speaks Streamable HTTP can skip the bridge and connect to `http://localhost:8080/mcp` with an `Authorization: Bearer <token>` header. The tools are on the [MCP](mcp.md) page. The server is also listed in the MCP registry as `io.github.psyb0t/monero`.

## Sending from an agent

The MCP server has no send tool unless the operator sets `DOCKER_MONERO_MCP_ALLOW_TRANSFERS=true`, and restoring a wallet from a seed or keys is never a tool. The skill tells the agent to send only a payment the user asked for, to dry-run it first, and never to ask for a seed in the conversation.

This page is for using the service from an agent. To change the repository, read [`AGENTS.md`](https://github.com/psyb0t/docker-monero/blob/main/AGENTS.md).
