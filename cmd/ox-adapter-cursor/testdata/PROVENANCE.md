# Cursor fixture provenance

## Desktop capture status

The real, human-operated desktop scenarios (CHAT-A, reopening that chat,
separate CHAT-B, delayed tool, and manual summarization) were captured on
2026-09-14. The coworker signed in, opened the controlled project in the Cursor
Agents Window on This Mac, and submitted the probe prompts. No
computer-control permissions were needed. The controlled probe matrix and
fixture redaction are complete, with unavailable events/settings and source
omissions documented below. The approved first-release source is the native
JSONL export plus hook evidence: it preserves observed prompt, response, and
tool-input order, but does not claim native tool results, failures, call IDs,
or call/result pairing. Adapter integration and desktop qualification remain
separate gates; this document makes no adapter compatibility claim.

The `desktop/` fixture namespace contains the reviewed redacted capture package:
30 hook stdin/stdout/metadata triplets, 20 hook-time JSONL snapshots, and 16
delayed snapshot/observation files, plus the manifest and redaction report.
Official documentation was used to design the probe; it does not substitute for
native hook stdin and sequential Session source files.

## Fixture transformation and validation

`desktop/manifest.json` maps every source SHA-256 to its transformed file hash,
lists scenario sequences and transformation categories, and references the
summarization screenshot by exact relative path and SHA without copying pixels.
Personal home/workspace/project-key/email values and native identity UUIDs are
replaced consistently. Controlled output tokens and unpredictable context
markers are replaced consistently across prompts, hook responses, and source
files; native field presence, nulls, types, ordering, and result syntax remain.
Tool-call aliases occur only in captured hook fields: no IDs or results were
invented for native JSONL blocks that lack them.

Captured stat/hash measurements refer to original native bytes. Redaction
changes byte lengths; consumers must compute candidate offsets and use the
manifest's transformed hashes for the candidate files. They must not reuse
captured native byte offsets against redacted JSONL.

Deterministic regeneration and an independent read-only comparison validated
all 126 transformed captures, 265 source-prefix relations, 63 identity/call
alias correlations, schema/key-order/type/null preservation, complete lines,
and scans for the controlled personal values in literal/escaped/encoded forms.
The private generator is `evidence/build_redacted_fixtures.py`; raw evidence
remains ignored. No claim is made that a finite pattern scan detects every
possible encoding of an undisclosed identifier.

## Prepared environment

- Surface: Cursor desktop for macOS, installed from the official Homebrew cask
- Cursor version: `3.20.17` (stable)
- Cursor internal commit: `0c32194e3fb5ffaced9fb36430b860ec301e1fc0`
- Cursor build date: `2026-09-12T03:16:10.634Z`
- Application bundle ID: `com.todesktop.230313mzl4w4u92`
- Signing identity: `Developer ID Application: Hilary Stout (VDXQ22DGB9)`
- Notarization: stapled ticket present
- macOS: `26.4.1` (`25E253`), Apple silicon (`arm64`)
- Capture workspace: `<WORKSPACE>/.context/cursor-adapter/evidence/control-project`
- Scratch output: `<WORKSPACE>/.context/cursor-adapter/evidence/capture`
- Hook configuration schema: version `1`

The app version and commit were read from the installed bundle's `Info.plist`
and `Contents/Resources/app/product.json`. The app executable SHA-256 was
`6b6d543049885b06f2d39e042e7e8c3fd3359fd2069ecf39ce377555883be76c`.

## First live observation (CHAT-A; partial evidence)

The preparation version above is historical. Every captured hook reports
Cursor `3.20.21`; the current bundle reports commit
`f09fca384ceca23f7bf21f9c23655b162641d740`, build date
`2026-09-13T19:02:51.781Z`, stable. The selected model was
`cursor-grok-4.6-medium`, with medium effort, in local agent mode.

- Twelve hook invocations were captured, including the two generic pre-tool
  hooks, success, failure, final response, and completed stop. All three
  unpredictable `additional_context` values match the actual final response
  and the coworker's screenshot. No file-read/search tool was observed.
- In this chat, `session_id` equals `conversation_id`. The generation ID is
  empty on `sessionStart` and nonempty on the other captured events. The
  follow-up reopen and separate-chat observations are below.
- The native path was initially null, then became
  `<HOME>/.cursor/projects/<project-key>/agent-transcripts/<conversation-id>/<conversation-id>.jsonl`.
  Initial null does not establish missing or disabled native export.
- Eight hook-time snapshots preserve a 688-byte prefix growing to 1707 bytes
  on the same inode. Two separately labeled delayed snapshots, three seconds
  apart, match the completed stop snapshot in size, mtime, and SHA-256.
  This constrains this run only; it does not establish a general flush bound.
- The five final JSONL rows contain a user message, three assistant messages,
  and a `turn_ended` record. Tool-use blocks have no call IDs; structured
  tool results are absent. Generic hook payloads do have matching
  `tool_use_id` values for each pre/success-or-failure pair. Neither source
  alone yet establishes the complete recording contract.
- Session-start environment propagation was not instrumented and is not
  claimed by this first turn. Delayed-tool and compaction checks remain.

Private raw evidence remains under `evidence/capture/`. The delayed observation
manifest is
`capture/delayed-snapshots/chat-a-20260914T183731.628435Z/observations.json`.
Capture counters and local timestamps describe probe observation order, not a
guaranteed native event ordering. No raw personal identifiers are copied into
the committed fixture namespace.

## Reopened CHAT-A observation

The coworker followed the quit/reopen recipe and submitted the exact
`RESUMED_A_NO_TOOL` prompt to the existing chat. Hooks 0013–0015 contain
`beforeSubmitPrompt`, `afterAgentResponse`, and completed `stop`. The
conversation and session IDs remain identical to the first turn; the generation
ID changes. The native source keeps the same path and inode, preserving all
1707 original bytes and appending exactly three rows: user prompt, the exact
tool-free assistant response, and successful `turn_ended` (2050 bytes total).
Two delayed snapshots, three seconds apart, match the new stop snapshot.

No `sessionEnd`, `workspaceOpen`, or new `sessionStart` was captured around this
reopen. The hook parent process changed, consistent with relaunch; the probe
did not independently observe app exit. Absence of these captured events is not
an unsupported-capability conclusion. The known session environment marker was
added to probe metadata after the first turn and is empty in all three reopened
hooks. The fresh CHAT-B observation below tests propagation to later hook
processes without changing this historical first-turn limitation.

The private delayed observation manifest is
`capture/delayed-snapshots/chat-a-reopen-20260914T184344.609332Z/observations.json`.

## Separate CHAT-B observation

A second fresh chat in the same Cursor window emitted `sessionStart`,
`beforeSubmitPrompt`, `afterAgentResponse`, and completed `stop` (hooks
0016–0019). Its conversation and session IDs are equal to each other and
distinct from CHAT-A. Its generation ID is empty on `sessionStart` and a new,
consistent nonempty value on the remaining hooks. The Session path is initially
null, then resolves under a directory named for the new conversation ID with a
new inode. This is direct evidence that separate chats use separate identities
and Session sources in this observed window.

The 550-byte final source has exactly three rows: the CHAT-B user prompt, the
tool-free assistant response containing the exact unpredictable
`OX_CURSOR_START_CONTEXT` value and `FINAL_B_NO_TOOL`, and successful
`turn_ended`. The response, stop, and two delayed snapshots are byte-identical.
No CHAT-A rows, tool blocks, or duplicate rows appear in the CHAT-B source. The
delayed reads were three seconds apart; the exact CHAT-A source also remained
byte-identical to its reopened stop snapshot.

`sessionStart` and `beforeSubmitPrompt` began at the same recorded instant; the
environment marker is empty in both recorder processes. The marker returned by
`sessionStart` is present with its exact expected value in the later
`afterAgentResponse` and `stop` recorder processes. This proves propagation to
later hook processes for this fresh session. It does not prove availability to
concurrently launched hooks, durability across restart, or a general ordering
guarantee between `sessionStart` and `beforeSubmitPrompt`.

The private delayed observation manifest is
`capture/delayed-snapshots/chat-b-20260914T185227.523536Z/observations.json`.

## Delayed-tool CHAT-DELAY observation

A third fresh chat ran the exact delayed shell command
`sh -c 'sleep 3; printf DELAYED_OK'` and then returned the exact tool-free final
response `FINAL_DELAY_NO_TOOL`. Hooks 0020–0027 cover `sessionStart`,
`beforeSubmitPrompt`, the generic and shell-specific pre/post events,
`afterAgentResponse`, and completed `stop`. They share one new conversation and
session ID and one nonempty generation ID outside `sessionStart`.

The generic `preToolUse` and `postToolUse` payloads share the exact
`tool_use_id`, command, and reported duration of 4813.318 ms. The successful
result is `DELAYED_OK` with exit code zero. The shell-specific events agree on
the command, output, and duration but do not carry the call ID. The hook
`tool_input` uses `timeout: 10000`; the saved native `tool_use` instead uses
`block_until_ms: 10000` and adds a description. Consumers cannot assume these
two input shapes are byte-identical.

At `afterShellExecution` and `postToolUse`, the native source is the same
314-byte, one-row user-message prefix. Its recorded mtime falls during the
delayed command interval. By `afterAgentResponse`, the source preserves those
314 bytes exactly and appends three rows: assistant text plus one `tool_use`,
the exact final assistant text, and successful `turn_ended` (755 bytes total).
The response and stop snapshots and two delayed reads three seconds apart are
byte-identical. The saved JSONL still has no call ID or structured tool result.

This run demonstrates that the tool-use and final rows were absent through the
post-tool snapshot and present by `afterAgentResponse`. It observed no write
after `stop`, so it does not establish a universal flush or late-drain bound.
The private delayed observation manifest is
`capture/delayed-snapshots/chat-delay-20260914T185654.744214Z/observations.json`.

## CHAT-A manual summarization marker observation

The coworker invoked manual summarization in the original CHAT-A and then sent
the exact tool-free marker prompt. The UI screenshot at
`<WORKSPACE>/.context/attachments/TMAClx/Screenshot 2026-09-14 at 12.00.01 PM.png`
visibly places `Chat context summarized` between the prior
`RESUMED_A_NO_TOOL` response and the new prompt and `AFTER_COMPACT_A` response.
This screenshot establishes that Cursor's UI reported successful
summarization; hook and source observations below are separate evidence.

Hooks 0028–0030 contain only `beforeSubmitPrompt`, `afterAgentResponse`, and
completed `stop`. They retain CHAT-A's conversation and session IDs and use a
new, consistent generation ID. The native source retains the same path and
inode as CHAT-A. Its 2050-byte, eight-row reopened-chat snapshot is an exact
prefix of the 2389-byte, eleven-row result. The only appended rows are the user
marker prompt, exact tool-free assistant response, and successful
`turn_ended`; no summary marker, duplicate, reorder, or tool record appears in
the saved JSONL. Two delayed reads three seconds apart match the stop snapshot.

No `preCompact` hook was captured on this successful manual-summarization path,
despite the controlled project registering that event through the same recorder
as the observed hooks. This is a tested-path observation for Cursor 3.20.21,
not a universal claim that the hook is unsupported. The evidence also does not
expose the generated summary, prove what model context was discarded, or show
whether another compaction trigger emits a different event. The private delayed
manifest was written before the screenshot clarification and retains that
earlier uncertainty:
`capture/delayed-snapshots/chat-a-after-compact-marker-20260914T185948.255764Z/observations.json`.

## Native export configuration observation

No documented or exposed native JSONL enable/disable setting was found for the
installed `3.20.21` build. Read-only inspection of bundled configuration
manifests, localization metadata, product metadata, and scoped static code found
the transcript writer configured with `writeText: false`, `writeJsonl: true`.
The disabled-export case was therefore not exercised through a user setting.
Internal flags were neither changed nor treated as supported configuration.
This does not rule out undisclosed managed policy or server experiments.

The private search scope, exact bundle paths, and official source references are
recorded in `evidence/native-export-settings-observation.md`. Initial null paths
in the live hook captures remain evidence of a pending source, not proof that
export was disabled.

## Deferred native-store experiment

The controlled interface is **Agents Window, This Mac**, on Cursor `3.20.21`.
Editor chat and Cursor CLI are not qualified by these captures.

A separately reviewed, redacted native-store experiment remains ignored under
`evidence/native-store-redacted-candidate/`. It sampled selected
`cursorDiskKV` values from a read-only SQLite snapshot and demonstrated richer
native fields, including opaque/encrypted state and tool result data. It is
preserved only as deferred research evidence: it is not copied into this
scaffold, is not a required fixture, and does not define the first-release
source contract. No native database capture, decryption, live-store read, or
tool-result/failure/call-ID requirement is implied here.

The experiment is an eventual state snapshot, not a live commit or flush fence.
It cannot prove that final status makes every field durably visible. Any future
native-store integration needs a separate reviewed contract and qualification.

## Controlled probe

The ignored capture workspace contains project hooks for `workspaceOpen`,
`sessionStart`, `sessionEnd`, `beforeSubmitPrompt`, `preToolUse`, `postToolUse`,
`postToolUseFailure`, `beforeShellExecution`, `afterShellExecution`,
`beforeReadFile`, `afterFileEdit`, `afterAgentResponse`, `preCompact`, and
`stop`. Each invocation records exact stdin, timing, working directory, parent
executable, and a same-event Session source snapshot when the path is present and
passes the controlled-path check.

The project hook is a thin wrapper for a recorder outside the opened workspace:
`<WORKSPACE>/.context/cursor-adapter/evidence/capture-hook.sh`.
Fresh unpredictable response values are recorded outside that workspace in
`evidence/probe-response-markers.json`. The response fields use these names:

- `OX_CURSOR_START_CONTEXT`
- `OX_CURSOR_POST_TOOL_CONTEXT`
- `OX_CURSOR_TOOL_FAILURE_CONTEXT`
- `OX_CURSOR_SESSION_MARKER` (hook environment)

Expected values must not be pasted into Cursor or read through its tools.
Hook stdout is saved alongside stdin for comparison. Snapshot paths are
canonicalized before checking the existing narrow allowlist; refused paths are
discovery evidence. Local observation times do not establish native write time,
and hook-time copies require separate delayed snapshots to assess final flush.

The model is asked to state markers it actually received. A successful hook
exit or schema-valid response alone does not establish model visibility.

The controlled prompts exercise:

1. a fresh chat with one successful shell tool, one failing shell tool, and a
   final response with no following tool;
2. reopening and resuming that conversation;
3. a second chat in the same Cursor window;
4. a delayed shell result followed by a tool-free final response;
5. manual compaction, if the installed desktop exposes it;
6. disabled native Session export only if an explicit setting is found.

Pause and resume recording belong to the ox integration acceptance run and are
not claimed by this native-only probe.

## Required transformation before committing fixtures

Raw capture stays in the gitignored evidence directory. Before copying a real
fixture into `testdata/desktop/`:

- replace the controlled workspace prefix with `<WORKSPACE>`;
- replace the home directory prefix with `<HOME>`;
- replace authenticated email with `<USER_EMAIL>`;
- replace native conversation and generation UUIDs consistently with stable
  `conversation-*` and `generation-*` tokens;
- retain event order, JSON field names, null versus missing values, timestamps,
  observed tool-input shapes, and unknown record types; do not invent omitted
  tool results, errors, call IDs, or call/result links;
- document every replacement and the source snapshot sequence in this file;
- verify no credentials, unrelated conversation content, or absolute personal
  paths remain.

## Documentation consulted

- [Cursor Hooks](https://prod.cursor.com/docs/hooks), checked 2026-09-14. It
  documents the common identity/path envelope and per-event request/response
  schemas used to construct the controlled probe.
- [Cursor Downloads](https://prod.cursor.com/en-US/download), checked
  2026-09-14. It listed Cursor `3.20` as the current desktop release family at
  capture preparation time; the installed cask supplied patch version `3.20.17`.

Remaining qualification questions include post-stop late-write and drain timing,
recording pause/resume, shared skill activation, and context delivery after
summarization. The documented-but-unobserved `sessionEnd`, `workspaceOpen`, and
`preCompact` paths remain unqualified for this Cursor Agents Window / This Mac
version. Source-field omissions are accepted for the first release; the JSONL
and hook observations above do not establish broader lifecycle guarantees.
