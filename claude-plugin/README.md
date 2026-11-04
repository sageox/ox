# SageOx plugin for Claude Code

Give Claude Code your team's shared memory. SageOx loads conventions,
architectural decisions, and prior sessions from your teammates at the start of
every session, so Claude does not start from zero.

## What you get

- **Team context at session start.** A `SessionStart` hook runs `ox agent prime`
  and a `PreCompact` hook reloads context before Claude compacts the conversation.
- **Session recording.** Record a coding session to your team's ledger with
  `/ox:session-start` and `/ox:session-stop`. Teammates can learn from it later.
- **Slash commands.** `/ox:prime`, `/ox:status`, `/ox:doctor`, `/ox:init`,
  `/ox:session-list`, `/ox:session-start`, `/ox:session-stop`.
- **Skills.** `sageox` explains the product; `ox` lists the commands and code
  search verbs.
- **SageOx MCP server.** Registered in `.mcp.json` at
  `https://sageox.ai/api/v1/mcp`. Claude Code asks you to sign in with SageOx
  the first time it connects.

## Install

1. Install the `ox` CLI: `brew install sageox/tap/ox`, or see
   https://sageox.ai/install.
2. Add the marketplace and install the plugin:

   ```
   /plugin marketplace add sageox/ox
   /plugin install ox@sageox
   ```

3. In your repository, run `ox login`, then `ox init`.

If `ox` is not installed, the hooks print install instructions and do not block
your session.

## Privacy

By default `ox agent prime` starts recording the session, and Claude Code shows
a notice when it does. Recordings go to your team's SageOx ledger. Choose a mode
with `ox config set session_recording manual` (start only on request) or
`disabled`. Privacy policy: https://sageox.ai/privacy

## More

- Docs and sign-up: https://sageox.ai
- Source and issues: https://github.com/sageox/ox
- License: MIT
