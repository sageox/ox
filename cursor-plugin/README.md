# SageOx

Cursor plugin that connects AI coworkers in Grok Bot to [SageOx](https://sageox.ai)
through SageOx's hosted [Model Context Protocol](https://modelcontextprotocol.io/) server.

Search your team's discussions, docs, and sessions, read its knowledge bubbles,
and save a chat to Team Context as a session.

## Install

1. In Grok Bot, open the **Marketplace** and search for **SageOx**.
2. Click **Install**, then connect it and sign in with your SageOx account.

You need a SageOx account on a team. Sign up at [sageox.ai](https://sageox.ai).

## MCP

```json
{
  "mcpServers": {
    "sageox": {
      "type": "http",
      "url": "https://sageox.ai/mcp",
      "placement": "server"
    }
  }
}
```

Auth is OAuth 2.1 with your SageOx account, using PKCE and dynamic client
registration. There is no API key to configure.

`"placement": "server"` makes Grok Bot connect through its hosted MCP path, the
one that completes SageOx sign-in. A SageOx server added to Grok Bot by hand can
instead loop on a sign-in link that says it expired.

## What AI coworkers can do

| Category | Capabilities |
| --- | --- |
| Team context | Load the team's conventions, recent decisions, and coworker roster; search discussions, docs, sessions, and murmurs |
| Knowledge bubbles | List bubbles and read their curated files |
| Discussions | Pull decisions, action items, chapters, and transcripts from recorded discussions |
| Sessions | Save the chat to Team Context, as a summary or turn by turn |
| Coordination | Publish and read murmurs |
| Plans | Save plans and check drafts against the team's context |

The hosted server is the source of truth for tool names and schemas. The
`sageox` skill tells the AI coworker which tool to start with.

## Notes

- Tool calls run as the SageOx user who signs in, and see only what that user
  can see.
- The plugin is listed in Grok Bot only. In the Cursor IDE, use the guided
  Cursor setup under **Settings → Connections** on sageox.ai, or the
  [`ox` CLI](https://sageox.ai/docs/cli/quickstart).
- If you added `https://sageox.ai/mcp` to Grok Bot by hand, remove that entry
  after installing the plugin, so the AI coworker sees one set of SageOx tools.
- Disconnect any time from **Settings → Security** on sageox.ai.

## Docs

- MCP overview: https://sageox.ai/docs/mcp
- Connect any MCP client: https://sageox.ai/docs/mcp/manual
- Getting the most out of SageOx: https://sageox.ai/docs/mcp/getting-the-most
- Server URL: https://sageox.ai/mcp

Logo is SageOx's OX mark, from `https://sageox.ai/sageox_logo_dark.png`.

## License

MIT
