# Review past sessions before import

`ox session import` opens a terminal picker for an interactive coworker.
All ready sessions start selected. Arrow keys move between sessions, Space
toggles the focused session, `a` selects all ready sessions, and `x` clears
selection. Page Up/Down scroll the excerpts. Enter reviews the chosen set;
the final upload confirmation appears in the terminal. `q` or Escape cancels.

`b` opens the same selection in a local browser. `--browse` starts there
directly. The browser shows opening requests, human prompt navigation, the last
AI reply, and the retained conversation with tool activity collapsed. Returning
a selection stops the local server and asks for confirmation in the terminal.
Clearing every checkbox imports nothing. Deselection applies only to this run.

`--preview --session <id>` prints one session's excerpts, with `--json` available
for the full normalized retained entries. Previewing uses the same redaction as
import, creates no staging files, and calls no summarizer. A changed source file
requires a fresh review. Content actually previewed also receives a local
SHA-256 pin, so restoring the original size and timestamp cannot bypass the
check. Excerpts help choose a session; importing retains the
redacted conversation and available tool activity and generates its summary
after confirmation. The last reply does not imply the work is complete.

Unattended, AI coworker, JSON, and `--dry-run` invocations retain their structural
preview behavior. `--yes` continues to import the explicitly selected sessions.
Browser review cannot be combined with `--yes` or JSON output. If terminal input
is disabled, `--browse` falls back to a structural preview without launching.

## Manual testing against Tilt

Use a disposable private repository in a local team. The cleaned Math Blitz
fixture contains eight native recordings and cannot read your personal session
stores when `--from-test-data` is supplied.

Find the running monorepo stack:

```sh
ox_import_monorepo=/path/to/sageox-monorepo
cd "$ox_import_monorepo"
make dev-ls
# If needed: make dev-shared-up, make dev, then make dev-wait.
```

The CLI endpoint is the edge router, `http://localhost:<3000 + stack offset>`.
The checked stack had offset `100`, so its endpoint was `http://localhost:3100`.
Use your stack's offset; the API and web application ports are different.

Build the PR checkout and seed a fresh fixture repository:

```sh
ox_import_worktree=/path/to/ox-pr-checkout
ox_import_stack=http://localhost:3100
ox_import_team='YOUR LOCAL TEAM SLUG'
cd "$ox_import_worktree"
make build
ox_import_bin="$ox_import_worktree/bin/ox"
ox_import_parent=$(mktemp -d)
ox_import_repo="$ox_import_parent/math-blitz"
python3 "$ox_import_worktree/scripts/session_import_testbed.py" create "$ox_import_repo"
"$ox_import_bin" login --endpoint "$ox_import_stack"
cd "$ox_import_repo"
"$ox_import_bin" init --endpoint "$ox_import_stack" --team "$ox_import_team"
"$ox_import_bin" config set session_recording auto --repo
"$ox_import_bin" sync
"$ox_import_bin" status
```

The local account must belong to that team, and Ledger provisioning/cloning must
finish. An actual import also needs an installed, authenticated Claude Code or
Codex CLI to summarize. `--summarizer codex` or `--summarizer claude` can choose
one usable CLI for both types. Previews make no summarization calls.

```sh
"$ox_import_bin" session import --from-test-data .git/ox-test-data --dry-run --json
"$ox_import_bin" session import --from-test-data .git/ox-test-data --preview --session 21bb267b
"$ox_import_bin" session import --from-test-data .git/ox-test-data
"$ox_import_bin" session import --from-test-data .git/ox-test-data --browse
```

Check these paths:

- Browse with arrows and Space; confirm that all ready sessions start selected.
- Press `b`; confirm that terminal deselections carry into the browser.
- Search an opening request; hidden rows keep their selection.
- Jump between human requests and expand retained conversation/tool activity.
- Clear all, return to terminal, and verify that no upload confirmation appears.
- Select one, return, and answer `n`; nothing uploads or gets summarized.
- Select one and answer `y`; only that session enters the import pipeline.
- Repeat `--dry-run --json`; imported sessions are recognized and skipped.
- During review, append a valid message to a selected fixture's native file;
  returning or confirming must refuse or hold the changed session.
- Try `--browse --no-input`; it prints a structural preview and does not wait
  for the browser. Try `--preview --session 21BB267B`; prefixes are case tolerant.

The local browser binds an ephemeral port on `127.0.0.1`, independently of Tilt.
The browser returns native session IDs; it never calls Tilt or uploads sessions.
The CLI verifies the destination, confirms, and then uses the existing Ledger
upload pipeline. Authentication for the preview API is a per-run token in the
opened URL fragment, carried in a header on subsequent local API requests.

## Automated checks

```sh
make test-preflight # lint, full and slow tiers, generated docs, and guards
go test -race ./cmd/ox ./internal/session/nativeimport \
  -run 'TestImport|TestPreviewEntries|TestWriteRaw' -count=1
go test -tags browser ./cmd/ox -run '^TestImportBrowser' -count=1
node --check cmd/ox/session_import_browser.js
python3 scripts/session_import_testbed_test.py
```

Browser tests require Chrome/Chromium. To capture desktop, mobile, and dark-mode
QA images, add `-args -import-browser-screenshots=/private/tmp/ox-import-qa` to
the browser test command. Real-adapter fixture tests build the adapter binaries
from this checkout; fast `-short` tests omit that build.

To keep the complete gate independent of saved machine credentials and shared
data, scope fresh XDG directories to that command:

```sh
ox_import_checks=$(mktemp -d)
XDG_CONFIG_HOME="$ox_import_checks/config" \
XDG_DATA_HOME="$ox_import_checks/data" \
XDG_CACHE_HOME="$ox_import_checks/cache" make test-preflight
```
