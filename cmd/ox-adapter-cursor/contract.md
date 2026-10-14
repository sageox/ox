# Cursor Agents Window JSONL implementation contract

Revision 1, 2026-09-14, frozen by ox-178e.6 for the user-approved JSONL-first
scope. This supersedes the original plan wherever it required database reads,
complete tool results, failure correlation, call pairing, or persisted call IDs.
Beads owns execution status. A frozen implementation contract is not a release
or live-compatibility claim; .16/.17/.19 retain those gates.

## Scope and evidence

Initial qualification target: Cursor 3.20.21 stable, commit
f09fca384ceca23f7bf21f9c23655b162641d740, Agents Window / This Mac,
macOS 26.4.1 arm64, one initialized Git worktree, default local profile.
Editor chat, CLI, cloud/remote, multi-root, custom profiles, historical import,
capture-prior and controlled workers are not advertised.

The canonical evidence is testdata/PROVENANCE.md and testdata/desktop/. The
native JSONL snapshots demonstrate ordered user/assistant text, tool-use
inputs, and turn-ended records. They have no structured timestamps, tool IDs,
results, or tool-failure flags. Preserve those omissions; do not infer them from
hook timing, command strings, assistant descriptions, or a native database.
Native database exploration remains ignored research and is not a dependency.

The selected source is the native JSONL export. It preserved its existing bytes
across ordinary turns, reopening and UI-confirmed summarization in this capture.
Both startup/pre-prompt orders occurred, including overlapping hook processes.
Missing startup path hints are normal. The final response and turn_ended were
present by afterAgentResponse and unchanged at stop and two delayed samples.
These observations do not establish a universal write-flush deadline.

## Composition and public producer signatures

Use current pkg/adapterprotocol types and pkg/adapterruntime.Config. There is no
new protocol field, opaque database handle, parser-state cache, or capability
for source proofs. main.go owns adapterName, adapterDisplay, adapterVersion,
adapterConfig, handleInfo and dispatch. Producers return (response pointer,
error), with these names:

| Owner | Handler | Input | Response |
| --- | --- | --- | --- |
| .8 | handleDetect | none | DetectResponse |
| .8 | handleFindSession | FindSessionParams | FindSessionResult |
| .7 | handleRead | ReadParams | ReadResult |
| .7 | handleReadMetadata | ReadParams | ReadMetadataResult |
| .7 | handleReadFromOffset | ReadFromOffsetParams | ReadFromOffsetResult |
| .10 | handleInstallHooks | HookParams | InstallHooksResponse |
| .10 | handleCheckHooks | HookParams | CheckHooksResponse |
| .10 | handleUninstallHooks | HookParams | UninstallHooksResponse |
| .12 | handleDiagnose | DiagnoseParams | DiagnoseResult |

.9 exports runHook(event string, stdin io.Reader, stdout, stderr io.Writer)
error. .13 dispatches hook <event> before ordinary runtime dispatch, implements
handleServe(*adapterruntime.Server), and uses the same readers in one-shot and
serve mode. Parser correctness must not depend on previous process state.

Initial wired capabilities: session_reader, incremental_reader, hook_installer,
serve_mode, diagnose if that capability exists in the current protocol. Do not
advertise file_watcher; the host owns polling and drain requests. Do not
advertise import, capture_prior, workers or compatibility skill RPCs.
.10 exposes cursorSkillTargets() []adapterprotocol.SkillTarget: key
agents-project, root .agents/skills, format agent-skills/v1, project scope,
reject-symlink. The host's existing inventory owns materialization.

## Faithful parser and byte offsets (.7)

The supported conversation row is role user or assistant with message.content
as an array of blocks. Walk the array in order; emit one entry per text or
supported tool-use block, including mixed text/tool arrays. Text is preserved
verbatim, including user_query/timestamp wrappers. Do not deduplicate equal
content. Text blocks require string text. Tool blocks require type tool_use,
nonempty string name and an input JSON value; encode input compactly as JSON
without converting numbers through float64. Emit role tool, ToolName and
ToolInput only. Missing tool output, error flag and call ID stay empty/false.

The observed control row {"type":"turn_ended","status":"success"} is known
metadata and emits no entry. Accept a nonempty string status as turn metadata;
it describes the turn, not the preceding tool. Extra object keys are tolerated;
unknown row roles, block types and malformed required fields fail visibly.
Empty content arrays and empty text are known empty content, not parse errors.

ReadMetadata returns empty metadata for the observed source. Do not invent
model/version from installed app or displayed timestamp text. The host may
retain model/version supplied by a validated hook in existing metadata fields.

Use TailJSONLWithStats with a strict wrapper: negative, past-EOF and non-LF
boundary offsets are errors, never reset to zero. Any ParseErrors in complete
records rejects the batch with the original offset. A metadata-only read is
successful and advances. An unterminated last line remains unread. Full and
incremental reads share the parser. ReadResult.Skipped counts recognized rows
that emit no entry. Do not treat AllLinesFailedToParse alone as an error when
all rows were recognized metadata.

Bounds: the shared 10 MiB complete-line limit applies; cap a source read at
64 MiB and return an actionable source-too-large error rather than silently
truncate. Verify the source remains the same regular file and has unchanged
size/mtime across a read; concurrent writes yield a retryable source-changed
error with no acknowledged offset. This is deliberately conservative.

Parser RPCs accept an absolute regular JSONL path supplied by the validated
host (matching other readers and the fixture/conformance harness). Exact
workspace/conversation authorization belongs to discovery, startup and host
admission, not a guessed workspace in ReadParams, which has no RepoRoot field.

## Exact identity and source validation (.8/.11)

The hook must have a canonical UUID conversation_id. A nonempty session_id
must agree; normalize it to conversation_id. generation_id identifies a turn
and must never replace conversation identity. Preserve unknown JSON fields
when forwarding; replace session_id and add session_file_hint when the native
transcript_path is nonempty. Reject wrong field types and conflicting IDs.
Require one workspace_roots entry whose canonical directory is the selected
initialized Git worktree. Reject background/cloud and multi-root payloads.

The verified source layout is:

    ~/.cursor/projects/<project-key>/agent-transcripts/<UUID>/<UUID>.jsonl

For the observed macOS encoding, remove the leading slash from the canonical
workspace path and replace each run of '/' or '.' with '-'. Preserve case and
all other characters. A root encoding collision does not authorize another
conversation: both nested path IDs must equal the exact native UUID. No newest
file fallback or project-wide conversation scan is allowed. Flat layout is
deferred; it was not observed. No arbitrary database/source override is added.

.8 owns shared internal/session/cursorpaths path helpers, with explicit home
and workspace arguments so host validation and adapter discovery use the same
rules. Export ValidateConversationID(id string) error,
SessionPath(homeDir, repoRoot, conversationID string) (string, error), and
ValidateSource(homeDir, repoRoot, conversationID, hint string) (string, error).
ValidateSource returns the canonical expected path, including when pending;
walk to the nearest existing ancestor to prove containment for missing parents,
reject symlink escapes/dangling links, nonregular existing leaves and alternate
layout/identity. It must not create native directories or files.
The executable keeps validateCursorSource(repoRoot, conversationID, hint string)
(string, error) as a wrapper using os.UserHomeDir().

handleFindSession requires RepoRoot and AgentSessionID. It looks up only the
validated exact source and returns Offset 0 (a reader coordinate, not an
initial recording-policy decision). A pending source returns session-not-found,
never a different chat. Since/mtime are not identity or boundary fallbacks.
Detection may use explicit AGENT_ENV=cursor, known desktop installation or
native project-root presence, but must not enumerate conversation contents.

.8 adds only .cursor/projects to KnownSessionRoots; .11/.14 enforce the exact
workspace/UUID path on top of SafeSessionFilePath before persistence, daemon
admission, rediscovery and final reads. Ryan reviews the concrete native-root
and data-access change before the delivery gate.

## First-message boundary and startup (.11, then .14)

Before starting a potentially slow prime subprocess, persist a validated source
boundary under the native identity using the existing SessionMarker store and
a bounded per-marker lock. Preserve it across marker updates. A pending-only
marker with no AgentID/zero PrimedAt is not an already-primed Session.

At beforeSubmitPrompt: if a recording is already active, reuse its checkpoint.
Otherwise snapshot the complete exported EOF for this generation; fresh missing
source is a known zero. Repeated same-generation hooks reuse the first saved
boundary. sessionStart must preserve an earlier pre-prompt boundary; when it
arrives first, it may establish zero for a missing source or the end of the last
completed turn for a source with a current partial turn. Do not skip a user row
merely because it appeared before sessionStart. Serialize overlapping hooks.

Source-boundary state carries workspace, source path/pending identity, byte
offset, generation identity, and known-zero provenance. Store it in existing
marker/recording state, not another sidecar. .11 prepares the boundary and native
identity helpers, uses explicit cursor selection and disables cross-chat PID
fallback. Add StartOffsetKnown bool to RecordingState/StartRecordingOptions;
SourcePrefixSHA256 string retains the hash at SourceOffset. A pending source
has SessionFile empty and the known zero/hash; exact identity/workspace allow
later lookup. .14 transfers the selected boundary into RecordingState atomically
with StartOffset/SourceOffset before starting capture. A known-zero flag must
survive restart; unknown-time entries selected by a known byte boundary are not
filtered by StartedAt. A later source discovery reuses that boundary, never a
new EOF. FindSessionResult.Offset does not define the recording start policy.

If no startup/pre-prompt boundary or existing recording can be recovered, do
not guess at a completed old conversation: report boundary-unavailable and
wait for the next pre-prompt/manual-start boundary. Manual start explicitly
starts at complete exported EOF and cannot promise to exclude older text still
buffered inside Cursor. Session controls use the recorded native identity or an
explicit AI coworker ID; never choose a chat by PID alone.

.11 may add only startup boundary fields/helpers in recording.go after .5;
that file then transfers to .14 along with the other startup files. This is a
bounded amendment to the original ownership table, not shared concurrent edit
permission. Full capture-loop integration remains .14.

## Hook bridge and context (.9/.10/.14)

Install version-1 project .cursor/hooks.json entries for sessionStart,
beforeSubmitPrompt, postToolUse, postToolUseFailure, afterAgentResponse, stop,
sessionEnd and preCompact. Use the compiled adapter hook command with safely
quoted absolute executable path and a 10-second native timeout. Only exact
ox-owned commands may be replaced/removed. Preserve unrelated hooks/keys,
refuse malformed/unsupported config and symlink escapes, and use locked atomic
updates. Installing twice is idempotent; check is read-only. User scope is an
explicit unsupported scope for this first release.

The bridge validates input before invoking ox agent hook <event>, sets
AGENT_ENV=cursor, uses the canonical workspace cwd, and preserves normalized raw
JSON into the prime path. Prefer a runnable sibling ox; controlled PATH lookup
is the fallback. No shell evaluates hook payload data. Child lifetime is at
most 8 seconds, stdin at most 1 MiB, stdout at most 1 MiB, stderr at most 64 KiB.
Timeout/overflow/malformed input fails open with a short diagnostic code on
stderr and the normal empty native response. Never echo raw payloads/errors.

| Native event | Response / host behavior |
| --- | --- |
| sessionStart | additional_context from successful prime/context stdout; bind/start idempotently |
| beforeSubmitPrompt | continue:true; preserve boundary/identity, no consumption of pending model context |
| postToolUse / postToolUseFailure | additional_context from successful host context stdout; best-effort drain |
| afterAgentResponse | {}; best-effort drain, no context consumption or turn-count increment |
| stop | {}; turn completion/drain only; no followup_message and no conversation finalization |
| sessionEnd | {}; bounded final drain then durable pending finalization |
| preCompact | {}; drain/preserve identity; no premature re-prime or conversation stop |

Only sessionStart and the two post-tool channels are proven model-visible.
At nondeliverable events, host handlers must not dequeue whispers/tasks or mark
prime context delivered. A pending-only marker is completed on a deliverable
startup/post-tool event; capture can use the saved boundary when delayed startup
recovers. Preserve the existing AGENTS.md context floor. Automatic refresh in a
tool-free turn after manual summarization is not claimed. No sessionEnd or
preCompact event was captured in the tested UI scenario, so they are defensive
handlers, not established always-fired lifecycle signals. An absent native
sessionEnd must not be the only finalization/liveness mechanism.

## Capture ownership, recovery and final drain (.14)

Cursor always uses host tail mode. Daemon owns capture. Hooks deliver context
independently and send best-effort drain requests; unavailable IPC does not
establish ownership. A fallback uses bounded raw-file TryLock, reloads state
after acquiring it, and skips if a live owner holds it. Use existing RawWriter
for every saved entry and .5 append/redact/sync-before-checkpoint recovery.
Advance across metadata-only batches. Preserve concurrent lifecycle updates.

Source replacement/shrink must not reset the offset or fall back to full replay.
The host retains a SHA-256 of the acknowledged file prefix in existing
RecordingState and compares it before/after reads; this is host file validation,
not a new adapter protocol. Before append, require the same regular source and
stable read observation. Preserve the old checkpoint and report source-changed
on failure. The captured append-only build is the supported behavior; arbitrary
source rewriting has no automatic repair/import guarantee.

Keep pause/resume upload sequence boundaries synchronized with append/checkpoint.
Respect manual/disabled modes and sticky explicit stop. Compaction alone cannot
stop/restart an active recording. Never enrich the last tool from a failure
hook for Cursor. Session log/coworker-history paths must not write into Cursor's
native source or corrupt the capture checkpoint; guard unsupported source writes
rather than pretending they are native messages. Historical import remains off.

A stop hook requests a drain and leaves the watcher alive for later writes.
Actual end/manual stop drains complete lines and retains pending finalization
while the source has an unterminated line or lacks a completed turn ending.
Use bounded retries/recovery rather than making context wait; source absence
or missing exports must remain distinguishable from a completed empty Session.
A terminal turn record is a current source observation, not a promise that no
future turn or write can occur.

## Error taxonomy and acceptance

Stable diagnostic categories: missing-native-identity, identity-conflict,
workspace-mismatch, unsupported-scope, source-not-found/pending,
source-unreadable, invalid-source-path, invalid-offset, source-changed,
unsupported-source-format, source-too-large, boundary-unavailable,
hooks-missing/invalid, adapter-missing, and hook-timeout/output-limit.
Diagnostics carry no source content, arbitrary native paths or personal IDs.
Safe repairs are confined to installing missing ox-owned integration entries;
no native history reset/deletion or undocumented settings mutation.

.7 proves real fixtures at all selected LF splits, including metadata-only and
partial-line suffixes, invalid offsets and mixed unknown/valid rows. .8 proves
exact per-chat/per-worktree lookup and symlink/path refusals. .9/.10 prove native
JSON envelopes, command quoting, deadlines, config preservation and repeated
install/uninstall. .11/.14 prove overlapping startup hooks, known-zero/reopen
boundaries, missing-start recovery, owner contention, unavailable IPC, crash
reconciliation, sticky controls, delayed final writes and source replacement.
.20's oracle compares actual saved text/tool inputs and absence of invented
results. .16 runs compiled conformance and repository gates; .17 qualifies the
assembled binaries through human-operated Agents Window scenarios. Shared
skills activation, controls and context delivery are still live release checks.

## Exclusive files and pinned tests

Paths below are relative to this repository unless explicitly qualified.
Existing files stay with their owner until the stated handoff. Workers may read
any source; additions outside their listed files require coordinator assignment.

| Bead | Production ownership | Test/evidence ownership |
| --- | --- | --- |
| .2 → .13/.6 | `main.go` → .13; `contract.md` → .6 | scaffold build and `info` smoke |
| .3 | no production source | `testdata/desktop/*`, `testdata/PROVENANCE.md` |
| .4 → .20 | external `sageox/ox-test-harness` scaffold | exact paths pinned in .4 before edits; never embedded in ox |
| .5 → .14 | `internal/session/recording.go`; `internal/daemon/agentwork/{session_watcher,session_finalize}.go` | `internal/session/recording_checkpoint_test.go`; `internal/daemon/agentwork/session_checkpoint_recovery_test.go` |
| .7 | `session_parse.go`, `session_read.go` | `session_parse_test.go`, `session_read_test.go` |
| .8 | `detect.go`, `session_lookup.go`; `internal/session/cursorpaths/paths.go`; `internal/session/adapters/session_roots.go` | `detect_test.go`, `session_lookup_test.go`; `internal/session/cursorpaths/paths_test.go`; `internal/session/adapters/cursor_session_roots_test.go` |
| .9 | `hook_bridge.go` | `hook_bridge_test.go` |
| .10 | `hooks.go`, `skills.go` | `hooks_test.go`, `skills_test.go` |
| .11 → .14 | `cmd/ox/{agent_prime,agent_session,prime_session_marker,agent_cursor_input}.go` | `cmd/ox/agent_cursor_input_test.go`, `cmd/ox/agent_cursor_identity_test.go` |
| .12 | `diagnose.go`; assigned `cmd/ox/doctor_adapters*.go` changes | `diagnose_test.go`, `cmd/ox/doctor_adapters_cursor_test.go` |
| .13 | `main.go`, `serve.go` | `main_test.go`, `serve_test.go`, `conformance_test.go`; `tests/adapters/cursor_capability_contract_test.go` |
| .14 | .5 and .11 handoffs; `cmd/ox/agent_hook.go`, `cmd/ox/agent_session_incremental.go`; required IPC drain seam assigned before edits | `cmd/ox/agent_cursor_capture_test.go`, `internal/daemon/agentwork/session_cursor_capture_test.go` |
| .15 | `Makefile`, `scripts/install.sh`, `.config/goreleaser.yml`, `internal/adapter/registry.yaml`, `internal/session/adapters/adapter.go` | `internal/session/adapters/cursor_distribution_test.go`; packaging scratch under `.context/cursor-adapter/packaging/` |
| .16 | integration changes assigned back to owning bead | deterministic gate evidence under `.context/cursor-adapter/acceptance/` |
| .17 | desktop qualification | desktop fixture/result namespace only |
| .18 | `docs/guides/cursor.md`, `CHANGELOG.md`, affected guide links | verified claims and rollback |
| .19 | final evidence and review package | no publishing without user authorization |

Unqualified production filenames in this table live in `cmd/ox-adapter-cursor/`.
`ox-z2u7` owns only separate CLI fixture/result paths and
`docs/guides/cursor-cli-qualification.md`. It cannot block desktop qualification.
