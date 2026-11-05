# SageOx for Devin

A [Devin plugin](https://docs.devin.ai/product-guides/plugins) that records Devin sessions to SageOx with the ox CLI. Each session is uploaded under the Devin user who created it, so teammates' work is never attributed to someone else.

## Setup

1. **Each user** creates a token at [sageox.ai/settings/tokens](https://sageox.ai/settings/tokens) and adds it at [app.devin.ai/secrets](https://app.devin.ai/secrets) as a **Personal** secret named `SAGEOX_TOKEN`. Don't save it as an Organization secret: every teammate's sessions would then upload as the token's owner.
2. **Load the plugin**, either way:
   - per repo: `ox init` (select Devin) adds it to `.devin/config.json`, so any Devin session that clones the repo loads it;
   - org-wide: an admin adds `https://github.com/sageox/ox/tree/main/devin-plugin` under Customize → Plugins.

A user without the secret isn't recorded; Devin tells them once per session how to add it.

## How it works

`hooks.json` runs `bin/ox-devin-hook` on Devin's lifecycle events:

| Event | What happens |
|-------|--------------|
| `SessionStart` | Installs the latest ox release if missing |
| `UserPromptSubmit`, `PostToolUse` | Appends the prompt, tool call, or Devin's message to an ox recording (`ox agent prime --agent devin`, generic adapter) |
| `Stop` | Stops the recording, which uploads it to the repo's Ledger |

- **Token:** Devin doesn't pass secrets to hooks; it includes them with each command's `tool_input.injected_env`. The hook caches the token (mode 0600) and strips `injected_env` and `__DEVIN*` keys before anything is recorded. Events before the first command are buffered.
- **One SageOx session per turn:** cloud Devin never sends `SessionEnd` and may delete a sleeping VM, so every turn end uploads.
- **Ledger clone:** a fresh VM clones the Ledger first. If it isn't ready at turn end, the recording stays open and uploads at the next turn end.
- **Which repo:** the session's repo comes from Devin's project dir or the command's working directory, never from other checkouts on the VM. A repo without `.sageox/` is not recorded.
- **Opt-outs:** recording only happens where `ox config get session_recording` is `auto` (the default in `ox init`ed repos). `manual` or `disabled`, at any level or via `OX_SESSION_RECORDING`, turns it off.
- **Fails open:** any error is logged to `~/.ox-devin/hook.log` and never blocks Devin.
