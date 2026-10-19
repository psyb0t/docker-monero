# Agent surface

Prefix: AGT. The skill, the plugin manifests and the OpenClaw bridge under `.agents/` describe this service to other agents. They are public.

- **AGT1:** A behavior, route, tool or configuration change updates what describes it in the same change. The skill is `.agents/skills/monero/SKILL.md` with `references/setup.md`. The environment table in `setup.md` mirrors `internal/pkg/config/config.go` and `.env.example`.
- **AGT2:** The MCP tool list appears in `.agents/skills/monero/SKILL.md`, `.agents/skills/monero/references/setup.md` (read-only count), `.agents/plugins/monero/README.md` and the `description` in `.agents/plugins/monero/openclaw.plugin.json`. Take it from `internal/pkg/mcp/tools.go`, never from another document, and update all of them together.
- **AGT3:** The Codex manifest `version` in `.agents/.codex-plugin/plugin.json` moves with the release tag, in the same release commit. The Claude manifest carries no version. The bridge `package.json` and `openclaw.plugin.json` stay at `0.0.0`, and CI sets the version from the tag.
- **AGT4:** The skill keeps one `Security & safety` section and one guardrail for sending funds. Examples use obviously fake tokens, addresses and wallet names. No real token, address, seed or wallet data goes into any file under `.agents/`.
- **AGT5:** The bridge `bin/cli.js` writes nothing to stdout, because stdout is the MCP channel. It never prints the token. It is the only executable code under `.agents/`, and no hook, command, startup script or `.mcp.json` is added to the plugin root.
