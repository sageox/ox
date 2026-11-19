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
| `PreToolUse` | SageOx: timing ox calls | Notes the start of an `ox` call that moves team data (a search, a murmur, a plan), so its [flow lane](#flow-lanes) can show it running and say how long it took. Every other tool call returns after classification, before dispatch, heartbeat, or recording work. It never inspects arguments beyond the command words, prints nothing, and does not block tool calls. |
| `PostToolUse` | SageOx: capturing activity for session recording | Shows a [flow lane](#flow-lanes) under an `ox` call that moved team data, delivers pending team updates as a fallback and, when a recording exists, drains new native session entries through the recording writer and its redaction pipeline. Captures conversation activity as well as tool activity. |
| `UserPromptSubmit` | SageOx: recalling context and delivering team updates | Looks up local recall context and delivers pending team updates, task notices, recording-pause reminders, and plan/artifact reminders. The opt-in cloud-query gate currently computes a decision; this handler does not dispatch a cloud query. |
| `Stop` | SageOx: saving turns and plans; publishing drafts if configured | Drains recording entries, counts the completed turn, registers the session link and publishes draft metadata according to publishing policy, and saves a completed Codex `<proposed_plan>` through the plan capture path. It prints nothing unless a [flow lane](#flow-lanes) could not be shown under its call. It does not end the recording. |
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


## Flow lanes

Codex has no status line a command can draw: the CLI's `tui.status_line` accepts
built-in items only, and the ChatGPT desktop app has no status bar. The one hook
field both show a coworker is `systemMessage`. ox uses it for one thing: under
each `ox` call (or SageOx MCP tool) that moved team data, a single line saying
which way the data went, how much, and how long it took.

```text
SAGEOX ●━━━━━━━━━━━━━━━━● SESSION  searched team memory · ~1.2k from 5 discussions · 0.8s
SAGEOX ●────────────────● SESSION  pulled plan · ~420 · 0.3s
SAGEOX ●────────────────● SESSION  posted murmur · size unknown · 0.4s
```

- The lane is printed by `PostToolUse`, so it appears under the call that made
  it, once. A solid line is data that flowed: thin under 1k tokens, thick from
  1k. A short stub (`●╶─`) is a call that failed or returned nothing.
- Every other tool call prints no notice: ordinary shell commands, and `ox`
  calls that move no team data (`ox doctor`, `ox status`, `ox session report`).
- Sizes are estimates (about four characters per token) of what the call returned.
  For a call that sends data out through the CLI, ox cannot see the payload and
  says `size unknown`. Only labels, counts and sizes are recorded, never a query's
  results or a murmur's text.
- The duration comes from the `PreToolUse` hook noting when the call started.
- `PostToolUse` output is one JSON document or nothing. When a whisper is also
  pending, the lane (`systemMessage`) and the whisper (`additionalContext`)
  share that document.
- `Stop` prints nothing in the usual case. If a lane could not be shown under
  its call, `Stop` shows it then (the newest five, the rest counted), as one
  `systemMessage`; Codex's `Stop` schema has no field that reaches the model,
  so whispers pending at the end of a turn wait for the next `UserPromptSubmit`.
- Codex shows a notice as text, so these lanes are still. To watch them move,
  run `ox statusline --watch` in a terminal split beside Codex: it draws each
  call as it runs (a dotted lane), lands (dots travel from SageOx to the
  session, or rings back the other way) and settles, under ox's status line.
- ox's status line (`● ox  1 discussion · 1 plan · 2 used  ● rec`) is not shown
  inside Codex. `ox statusline --watch` shows it.
- `ox config set flow_lanes off` hides the lanes; `ox config set statusline off`
  turns off everything here.
