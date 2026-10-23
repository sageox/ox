# SageOx hooks in Codex

SageOx installs six local lifecycle hooks in `.codex/hooks.json` (project
scope) or `~/.codex/hooks.json` (user scope). Each runs
`AGENT_ENV=codex ox agent hook <event>`. These are local commands, not incoming
webhooks.

Every handler has a `statusMessage` identifying SageOx and describing its
purpose. Codex uses this field for running status, and its desktop hook review
also uses it to label approval rows. Labels describe possible work; recording
and publishing still follow the coworker's configuration.

## Behavior audit

The event mappings, active-phase gate, and handlers are in
[`cmd/ox/agent_hook.go`](../../cmd/ox/agent_hook.go). The labels and installation
logic are in [`cmd/ox-adapter-codex/hooks.go`](../../cmd/ox-adapter-codex/hooks.go).

| Event | Review label | Actual behavior |
| --- | --- | --- |
| `SessionStart` | SageOx: loading context; auto-recording if configured | Primes Team Context, starts recording when recording mode is `auto`, and handles recording boundaries on clear or compaction. An already-primed session skips redundant priming. |
| `PreToolUse` | SageOx: pre-tool hook (currently inactive) | Maps to `phaseBeforeTool`, which is absent from `activePhaseBehavior`. Returns before dispatch, heartbeat, or recording work. Kept as an existing integration entry; it does not inspect or block tool calls. |
| `PostToolUse` | SageOx: capturing activity for session recording | Delivers pending team updates as a fallback and, when a recording exists, drains new native session entries through the recording writer and its redaction pipeline. Captures conversation activity as well as tool activity. |
| `UserPromptSubmit` | SageOx: recalling context and delivering team updates | Looks up local recall context and delivers pending team updates, task notices, recording-pause reminders, and plan/artifact reminders. The opt-in cloud-query gate currently computes a decision; this handler does not dispatch a cloud query. |
| `Stop` | SageOx: saving turns and plans; publishing drafts if configured | Drains recording entries, counts the completed turn, registers the session link and publishes draft metadata according to publishing policy, and saves a completed Codex `<proposed_plan>` through the plan capture path. It does not end the recording. |
| `SessionEnd` | SageOx: finalizing recordings; publishing if configured | Stops an active recording and finalizes it directly or hands it to the daemon. Publishing follows configuration; manual publishing holds the recording locally. Repeated calls are safe when the recording is already stopped. |

Active phases also send a heartbeat to the local daemon when an AI coworker ID
is available. The inactive pre-tool phase exits before that step. All hooks
exit quietly if SageOx is not initialized. A label does not change any of these
behaviors.

## Updating existing installations

After installing an updated `ox` **and its bundled Codex adapter**, run:

```sh
ox integrate install --codex
# For an existing user-wide installation:
ox integrate install --codex --user
```

The installer replaces existing SageOx handlers in place and adds missing
events. Repeated installs produce the same configuration. Other integrations'
handlers, matcher-group options, top-level metadata, and Codex's explicit
`features.hooks` setting are preserved.

Rerunning `ox init` with Codex selected also uses this installer and refreshes
existing project hooks.

`ox doctor` detects missing or outdated SageOx labels in both scopes without
rewriting configuration. `ox doctor --fix` offers the scoped reinstall through
the adapter's safe repair path. Merely replacing the binary does not rewrite
hook files in every repository.

Codex tracks trust against the hook definition. After refreshing labels,
review and trust the changed SageOx hooks again in Codex's hook review UI or
with `/hooks` in the CLI. SageOx does not grant trust on the coworker's behalf.

## Verification

`cmd/ox-adapter-codex/hooks_labels_test.go` checks labels for fresh and existing
project/user installations, duplicate prevention, repeat-install stability,
preservation of unrelated MCP handlers and unknown configuration fields, and
read-only diagnosis followed by a scoped repair for each event.
