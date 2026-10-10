---
component: session-import-screen
package: cmd/ox/session_import_tui.go
since: 0.20.0
family: screen
renderer: freeze
exports: [runImportTerminal]
---

# Session-import screen

> Choose native Claude Code and Codex sessions using their opening requests and retained conversation excerpts before uploading them to the Ledger.

Live: `ox session import`. Catalog reference: `ox dev catalog --component=session-import-screen`.

## When this is the right reference

A selection screen where a coworker needs to inspect content before choosing. The list identifies each session by its first meaningful human request, with date, tool and eligibility beneath it. Focusing a row shows the opening request, ordered human prompts and last AI reply. Content loads only for visible rows, with at most two reads running concurrently.

This is a composed command screen. Its session classification, preview loading and browser handoff belong to the import workflow.

## When NOT to copy this

For a small set without content previews, use [Multi-select](multi-select.md). For sessions already in the Ledger, use [Session-list screen](session-list-screen.md). For generic tabular output, use [Columns](columns.md).

If another command needs a reusable part of this screen, extract that part with its own catalog entry rather than copying the import workflow.

## Anatomy

The destination and its visibility appear above the selection. Ready sessions start selected unless `--session` narrows the initial choice. Skipped sessions remain readable and cannot be selected. The last AI reply is an excerpt from the recording, without an inferred completion claim.

At 92 columns or wider, the session list and excerpts appear side by side. At 80 columns, the list sits above the excerpts. Both layouts keep output within the terminal width and height; page keys scroll the excerpts.

```text
Choose sessions to import · 2 selected
Math Blitz · repo_math_blitz · private
Preview only. Nothing uploads until you confirm in this terminal.

> [x] Add a persistent high-score table
      Oct 01 10:30 · claude · ready
  [x] Make difficulty adapt to the player
      Oct 02 09:15 · codex · ready

────────────────────────────────────────────────────────────────────────────────
claude · 21bb267b-05af-43ce-b42f-f850b70d2ef4

Opening request
Add a persistent high-score table.

Human prompts (1)
1. Add a persistent high-score table.

Last AI reply
The high-score table is implemented and tests pass.
↑/↓ move · space toggle · a all · x clear · enter review · b browser · q cancel
pgup/pgdown scroll · home/end first/last excerpt
```

## Composition

Uses the [Multi-select](multi-select.md) checkbox convention, quiet supporting metadata and a help footer. The live screen uses semantic CLI primary and dim colors. Its cursor and `[x]` / `[ ]` / `[-]` markers carry meaning without color. Borders are limited to the divider between stacked regions.

The catalog renders a static freeze reference. Keyboard interaction and loading happen in the live command; the reference does not pretend to animate them.

## Keyboard and fallbacks

| Key | Action |
| --- | --- |
| Up / Down, `k` / `j` | Focus another session |
| Space | Toggle the focused ready session |
| `a` / `x` | Select all ready sessions / clear the selection |
| Page Up / Page Down | Scroll excerpts |
| Home / End | Move to the top / bottom of the excerpts |
| Enter | Return the chosen sessions for terminal confirmation |
| `b` | Open the local browser with the current selection |
| `q`, Escape, Control-C | Cancel |

Noninteractive runs retain the structural text or JSON preview. `--preview --session <id>` reads one session without importing it. The browser returns selected IDs to the terminal; its callback does not authorize an upload.

## Content and safety

Previewing creates no staging directory, generates no AI summary and uploads nothing. Preview entries pass through the same redaction pipeline as imported recordings. Terminal escape sequences and control characters are removed before native content is displayed. Loading or unavailable content appears explicitly, and a late response cannot replace the newly focused session.

Clearing every checkbox means nothing is uploaded. Native files that change after review must be reviewed again before publication. The preview excerpts help a coworker choose; the eventual import retains the redacted conversation and available tool activity.

## Source and tests

[`cmd/ox/session_import_tui.go`](../../../cmd/ox/session_import_tui.go) — `runImportTerminal` and the Bubble Tea model.

[`cmd/ox/session_import_tui_test.go`](../../../cmd/ox/session_import_tui_test.go) covers keyboard selection, cancellation, late responses, bounded loading, unavailable content, safe text and narrow layouts. [`cmd/ox/session_import_content_test.go`](../../../cmd/ox/session_import_content_test.go) covers selection confirmation and source changes.
