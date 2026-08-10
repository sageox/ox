# Fixture provenance

## `session-2026-04-03T22-06-4b1c7e02.json`

| Field | Value |
|-------|-------|
| Producer | Gemini CLI **0.36.0** (`@google/gemini-cli`, `/opt/homebrew/lib/node_modules/@google/gemini-cli`) |
| Source path | `~/.gemini/tmp/<project_hash>/chats/session-2026-04-03T22-06-5d09fc36.json` |
| Written by gemini | 2026-04-03 |
| Copied into this repo | 2026-08-09 |
| Model in transcript | `gemini-3-flash-preview` |

This is **real Gemini CLI output**, not a hand-authored approximation. It was
copied out of the session directory that the installed `gemini` binary writes to
on its own — see `docs/cli/session-management.md` inside the installed package,
which documents `~/.gemini/tmp/<project_hash>/chats/` as the automatic session
store.

### What was changed

Leaf **string values** only, by direct substring replacement on the raw bytes.
No re-serialization: key order, indentation, whitespace, and every timestamp are
byte-identical to what gemini wrote. A structural diff (keys + JSON types, at
every level) against the original passes.

| Replaced | With |
|----------|------|
| `sessionId` UUID | `4b1c7e02-a3d9-4f18-9c26-7e5b0a1d2f34` |
| `projectHash` (64 hex) | all-zero 64 hex |
| 5 per-message UUIDs | `aaaaaaaa-0000-4000-8000-00000000000N` |
| 3 tool-call ids | `toolaaa1` / `toolbbb2` / `toolccc3` (same 8-char lowercase-alnum shape gemini uses) |
| Absolute capture path under `/private/var/folders/.../T/...` | `/tmp/example-project` |

Nothing else was touched. The conversation text, the `thoughts` entries, the
`tokens` block, the tool `args`, `status`, `displayName`, `resultDisplay` and
`renderOutputAsMarkdown` fields are exactly as gemini emitted them.

The filename encodes the new id: gemini names session files
`session-<YYYY-MM-DD>T<HH-MM>-<first 8 chars of sessionId>.json`, so renaming
the id required renaming the file to match. That naming rule was verified
against all 68 real session files on the capture machine.

### What this fixture does NOT contain

Named as `Want.Unproven` in `conformance_test.go`, so the gap stays visible:

- **A failed tool call.** Every `toolCalls[].status` in all 68 real sessions on
  the capture machine is `"success"`. The `is_error` path is therefore exercised
  only by the unit test in `session_test.go`, against the status values
  (`error`, `cancelled`) and the `response.error` key read out of the installed
  0.36.0 bundle and `docs/hooks/reference.md` — not against a captured failure.
- **An agent version.** Gemini does not record its own version anywhere in the
  session file, so `SessionMetadata.AgentVersion` is always empty for this
  adapter.
- **Multiple user turns.** No real session on the capture machine has more than
  one; these were single-prompt runs.
