#!/usr/bin/env node
// docker-monero MCP bridge. A thin stdio<->HTTP proxy: forwards MCP over stdio to a
// running docker-monero server's Streamable-HTTP endpoint (`$MONERO_URL/mcp`),
// authenticating with `$MONERO_TOKEN`.
//
// stdout IS the MCP protocol channel, so diagnostics go to stderr only. The sole
// output here is a fatal pre-launch console.error (user-facing CLI output). The
// token is passed to the proxy as an argv header, never logged.
import { spawnSync } from "node:child_process";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);

const MCP_PATH = "/mcp";
const TRANSPORT = "http-only";
const EXIT_FAILURE = 1;

function fail(message) {
  console.error(`[monero-mcp] ${message}`);
  process.exit(EXIT_FAILURE);
}

const base = process.env.MONERO_URL;

if (!base) {
  fail(`Missing MONERO_URL.

Point this bridge at your running docker-monero server, e.g.:
  export MONERO_URL=http://localhost:8080

docker-monero is self-hosted. See https://github.com/psyb0t/docker-monero`);
}

const token = process.env.MONERO_TOKEN;

if (!token) {
  fail(`Missing MONERO_TOKEN.

The server requires a bearer token on every call. Set it to the value of
DOCKER_MONERO_API_TOKEN on the server:
  export MONERO_TOKEN=<your-token>`);
}

const url = `${base.replace(/\/+$/, "")}${MCP_PATH}`;
const proxyEntry = require.resolve("mcp-remote/dist/proxy.js");

const args = [proxyEntry, url, "--transport", TRANSPORT];
args.push("--header", `Authorization: Bearer ${token}`);
args.push(...process.argv.slice(2));

const result = spawnSync(process.execPath, args, { stdio: "inherit" });
process.exit(result.status ?? EXIT_FAILURE);
