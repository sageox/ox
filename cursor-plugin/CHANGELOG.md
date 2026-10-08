# Changelog

All notable changes to this plugin will be documented here. The plugin is
versioned on its own, not with the ox CLI. Cursor reviews every update, so bump
the version only when the plugin itself changes.

## 1.0.0 — initial release

- Adds the `sageox` MCP server at `https://sageox.ai/mcp` with
  `"placement": "server"`, so Grok Bot connects through its hosted MCP path.
- Auth uses OAuth with your SageOx account. There is no API key to configure.
- Lists in Grok Bot only (`"cursor": "never"`).
- Adds the `sageox` skill, which tells the AI coworker which SageOx tool to
  start with.
- Logo: SageOx's OX mark, from `https://sageox.ai/sageox_logo_dark.png`, on a
  192×192 tile.
