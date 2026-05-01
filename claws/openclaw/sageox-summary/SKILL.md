---
name: sageox-summary
description: "Generate an overall team summary covering the last 24 hours across all SageOx-enabled teams. Reads distilled daily entries via `ox distill history` and produces a structured, Slack-ready overview."
version: 0.3.1
metadata:
  {
    "openclaw":
      {
        "emoji": "📰",
        "os": ["macos", "linux"],
        "requires": { "bins": ["ox", "claude", "jq"] },
        "install":
          [
            {
              "id": "node-claude",
              "kind": "node",
              "package": "@anthropic-ai/claude-code",
              "bins": ["claude"],
              "label": "Install Claude Code CLI (npm)",
            },
            {
              "id": "brew-jq",
              "kind": "brew",
              "formula": "jq",
              "bins": ["jq"],
              "label": "Install jq (brew)",
            },
          ],
        "homepage": "https://sageox.ai",
      },
  }
---

# SageOx Summary

You are an agent that generates a cross-team summary of the last 24 hours of
SageOx distilled activity. You enumerate each team's recent distilled entries
via `ox distill history`, inline their content into a prompt for `claude -p`,
and return a structured Slack-formatted summary.

This skill pairs with the [`sageox-distill`](https://clawhub.ai/skills/sageox-distill)
skill — distill writes the source material, this skill synthesizes it.

**ox version requirement:** this skill uses `ox distill history list` and
`ox distill history show`, which landed in [PR #507](https://github.com/sageox/ox/pull/507).
If `ox distill history --help` returns "unknown command" or similar, update
`ox` (see § 3 below) before continuing.

## Prerequisites

Before doing anything else, verify the user's environment. Run every check
in order. If any required check fails, explain precisely what's missing
and stop. Do not proceed until the user has fixed it.

### 1. Required binaries

`ox`, `claude`, and `jq` are declared in the front matter's
`requires.bins`, so OpenClaw checks them before running the skill.
`claude` (npm) and `jq` (brew) have declarative installs in the front
matter; `ox` does not. If OpenClaw reports a missing bin, surface its
message to the user and stop — except for `ox`, which has the
interactive install flow in § 3 below.

`claude -p` will use whatever credentials `claude` already has — either
an OAuth session from `claude login` (Pro/Max subscription) or
`ANTHROPIC_API_KEY` exported in the shell that launched OpenClaw. The
skill no longer accepts a per-skill `apiKey`.

### 2. Path validation rules

Several steps below read a path from a JSON state file. Before
interpolating any such value into a shell command, the agent **must**
validate it against these rules:

1. **Absolute path required.** Must start with `/` or `~`. Reject relative
   paths and bare names.
2. **No `..` segments.** Reject anything containing `..`.
3. **No shell metacharacters.** Reject anything containing any of these
   characters: `;` `$` `` ` `` `|` `&` `<` `>` `(` `)` `{` `}` `*` `?`
   `[` `]` `!` `\` newline.

On any validation failure: print a clear error to the user explaining
which rule failed and ask them to provide a different path. **Do not
attempt to "fix up" or sanitize the input** — reject and re-prompt.

Treat values read from `~/.openclaw/memory/*.json` files as untrusted
even though this skill writes them: the user (or a process running as
the user) may have edited the file by hand or by another tool between
runs. Re-validate every read.

### 3. Installing `ox`

The `ox` CLI install state is recorded in
`~/.openclaw/memory/sageox-ox-install.json`. On every run of this
skill, invoke the bundled readiness gate:

```bash
bash scripts/update-ox.sh
```

Contract:

- **Stdout:** nothing on success
- **Stderr:** on any failure, a two-line message — an `error:` line
  describing what's wrong, followed by a `fix:` line with the
  remediation. Surface both verbatim to the user.
- **Exit:** `0` ox is pinned, installed, and reports the expected
  version (continue to § 4); `2` ox is not usable — one of: state file
  missing, binary missing at `$HOME/.local/bin/ox`, `ox` on PATH
  resolves to a different binary, binary fails to run, or binary
  reports a version other than the one recorded in
  `sageox-ox-install.json`. On exit `2`, STOP, read
  [`references/INSTALL.md`](references/INSTALL.md), follow the install
  flow, then re-run this script to confirm.

There is no per-run auto-update. The curl install pins a specific `ox`
release by tag and sha256; users pick up newer releases by re-running
`clawhub install` for this skill after a new skill version publishes.
The user can say **"reinstall ox"** at any time to re-enter the flow in
[`references/INSTALL.md`](references/INSTALL.md).

**Do not install `ox` via Homebrew or any package manager** (e.g.
`brew install sageox/tap/ox`, `apt`, `dnf`, `pacman`). The tap exists
for general use but is not supported inside OpenClaw skills — only the
pinned-release curl flow is.

### 4. Authentication

1. `ox status` — confirm ox is authenticated. If not, tell the user to
   run `ox login` and try again.
2. Smoke-test `claude -p`:

   ```bash
   claude -p "say hi" --model claude-sonnet-4-6
   ```

   If it fails with an auth error, `claude` has no usable credentials.
   Tell the user to either run `claude login` (Pro/Max OAuth) or
   export `ANTHROPIC_API_KEY` in the shell that launches OpenClaw, then
   re-run the skill. The skill cannot inject the key itself.

## Configuration

The skill uses two pieces of state:

1. **Repo manifest** — `~/.openclaw/memory/sageox-distill-repos.json`
   (shared with the `sageox-distill` skill). Format:

   ```json
   {
     "repos": [
       { "path": "/home/user/repos/my-project", "team_id": "my-team" }
     ]
   }
   ```

   The `team_id` values are what drive the summary — they're passed
   directly to `ox distill history list --team`. The `path` entries
   are not used by this skill, but stay in the manifest so
   `sageox-distill` keeps working.

2. **Summary state** — `~/.openclaw/memory/sageox-summary-state.json`.
   Tracks which distilled daily entry ids have already been included in
   a prior summary run so the skill never re-summarizes the same
   content. Shape:
   `{updated_at, teams: {<team_id>: {included_ids: [<id>, ...]}}}`.
   Missing file → empty state (first run). Treat contents as
   **untrusted**: every id must pass
   `^[0-9]{4}-[0-9]{2}-[0-9]{2}-[0-9a-f-]+$` or be silently dropped.
   Both bundled scripts handle this already — the rule is stated here
   for anyone reading the state file directly.

   Pre-0.2.0 versions of this skill stored entries under
   `included_files` (with a `.md` suffix). That field is ignored by the
   current scripts; users upgrading from 0.1.x will re-summarize the
   most recent window once and the state file will converge to the new
   schema on the next successful run.

If the manifest does not exist, tell the user to run the `sageox-distill`
skill first to set up the repos, or ask for paths directly and populate
it.

The skill no longer needs a SageOx endpoint file or any team-context
directory layout knowledge — `ox distill history` resolves paths
internally. Earlier versions read `~/.openclaw/memory/sageox-endpoint.txt`;
that file is ignored now and can be left alone or removed.

## Summary Pipeline

When the user asks for a summary, run the steps in order. Steps 2 and
5 delegate their mechanics to `scripts/select-new-entries.sh` and
`scripts/update-state.sh` (invoke via `bash scripts/<name>`).

### Step 1: Load the Manifest and Summary State

1. Read `~/.openclaw/memory/sageox-distill-repos.json` with `jq`.
   **Re-validate every `path` entry** against the Path validation rules
   in Prerequisites § 2 before using it — the manifest is user-writable
   and may have been hand-edited between runs. (The summary pipeline
   itself does not dereference the paths, but if the manifest looks
   corrupt we stop rather than guess at intent.)
2. Read `~/.openclaw/memory/sageox-summary-state.json` with `jq` if it
   exists. If it is **missing**, proceed silently as if the `teams` map
   were empty — first runs are normal. If it exists but is **malformed
   or unreadable**, proceed as if empty AND emit exactly one warning to
   **stderr**:

   ```text
   warning: sageox-summary-state.json was unreadable, starting from empty state
   ```

   Never route this warning to stdout — stdout is the final summary
   (Step 6), and mixing decorative output into it breaks downstream
   consumers. This file is rewritten at the end of every successful
   run (see Step 5).
3. Collect the unique `team_id` values from the manifest. These drive
   every subsequent `ox distill history` call.

### Step 2: Select New Entries per Team

The window is the last 24h, resolved server-side by `ox distill history
list` against entry `created_at` timestamps. `ox` auto-expands the
window around the UTC day boundary so runs don't miss yesterday's late
entries — this skill passes `24h` verbatim and lets ox decide how wide
to actually look. The state file then subtracts anything already
summarized, so re-runs within the window are idempotent. First run (no
state file) = every candidate is new, not an error.

For each unique `team_id`, invoke `scripts/select-new-entries.sh`. It
runs `ox distill history list --team <tid> --since 24h --layer daily
--format json`, extracts the entry ids, and subtracts the team's
`included_ids` set from the state file. Contract:

- **Usage:** `select-new-entries.sh <team_id> <since> <state_file>`
- **Stdout:** one entry id per line, sorted; empty if nothing new
- **Stderr:** one-line warnings (malformed state, failed ox call)
- **Exit:** `0` success (empty is not a failure; a failed ox call is
  treated as "no new entries" so the remaining teams still summarize),
  `2` usage, `3` internal (`jq` or `ox` missing)

```bash
STATE_FILE=~/.openclaw/memory/sageox-summary-state.json
NEW_IDS="$(bash scripts/select-new-entries.sh \
  "$TEAM_ID" 24h "$STATE_FILE")"
```

Then:

1. If `NEW_IDS` is empty for a team, skip it this run — log
   `<team_id>: no new entries since last summary` to **stderr** and
   continue with the remaining teams.
2. If **every** team ends up with zero new entries, stop the pipeline
   before invoking Claude. Print one line to stdout —
   `No new distilled content since last summary.` — and exit. Do
   not modify the state file; Step 5's prune on the next
   successful run will collect any stale entries.

### Step 3: Fetch Entry Content and Build the Prompt

For each team that survived Step 2, fetch the content of every new
entry in a single `ox distill history show` call. `show` accepts
multiple ids and emits each entry's markdown separated by its own
`<!-- entry: <id> -->` header:

```bash
# shellcheck disable=SC2086
TEAM_BLOCK="$(ox distill history show \
  --team "$TEAM_ID" \
  --format content \
  $NEW_IDS)"
```

(Intentionally unquoted expansion: `NEW_IDS` is a sorted set of ids,
each already matched against `^[0-9]{4}-[0-9]{2}-[0-9]{2}-[0-9a-f-]+$`
by Step 2, so word-splitting is safe and desired here.)

If the `show` call fails for a team, surface the stderr and **stop the
whole run** — partial summaries misrepresent the team's activity, and
Step 5 stays untouched so the next run retries the same set.

Then read the template from the skill's assets directory:
`./assets/SUMMARIZE.md` (relative to this SKILL.md file). The file path
on disk depends on where OpenClaw loaded the skill from — typically
one of:

- `~/.openclaw/skills/sageox-summary/assets/SUMMARIZE.md`
- `./skills/sageox-summary/assets/SUMMARIZE.md` (workspace skill)

Substitute template placeholders:

- `{{ENTRIES}}` — one section per team that has a non-empty `NEW_IDS`
  set, in this format:

  ```text
  ### Team "<team_id>"

  <TEAM_BLOCK for that team>
  ```

  Teams with zero new entries were already dropped in Step 2 and must
  not appear in `{{ENTRIES}}`. Do not re-number, re-wrap, or otherwise
  mutate the markdown inside `TEAM_BLOCK` — it is authored by
  `ox distill` and preserves the citation anchors Claude references in
  the final summary.

- `{{MULTI_TEAM_RULES}}` — if **two or more** teams survived Step 2
  with non-empty entry sets, replace with:

  ```text
  - Organize the summary by team, using each team ID as a section header
  - Attribute insights to the correct team
  ```

  Otherwise, replace with an empty string.

### Step 4: Run Claude

Invoke `claude -p` with the substituted prompt. The prompt already
contains every entry's full text, so Claude does not need filesystem
access — do **not** pass `--add-dir` and do **not** grant read tools.

- `--model claude-sonnet-4-6`
- No `--add-dir` (content is inline)
- No `--allowedTools` (prompt is self-contained)
- The substituted prompt passed via stdin

`claude -p` will use whatever credentials `claude` already has — either
an OAuth session from `claude login` or `ANTHROPIC_API_KEY` from the
shell that launched OpenClaw (see Prerequisites § 1). Wrap the
invocation in `timeout 600` (10 minutes) — this matches the
timeout used by `pkg/sessionsummary/claude.go` in the `ox` repo for
comparable Claude synthesis work and gives the model enough headroom for
cross-team summaries that inline many daily entries:

```bash
timeout 600 claude -p \
  --model claude-sonnet-4-6 <<< "$PROMPT"
```

If the invocation fails (non-zero exit, timeout exit 124, network
error), surface the error to the user and **do not** proceed to Step 5
— leaving the state file untouched lets the next run retry exactly
the same candidate set.

### Step 5: Update Summary State

Only run this step if Claude exited successfully. On any failure
(non-zero exit, timeout, network error), skip it entirely — leaving
the state file untouched lets the next run retry the same candidate set.

For each team that had non-empty `NEW_IDS` in Step 2, pipe that team's
ids into `scripts/update-state.sh`. The script merges them into the
team's `included_ids`, prunes entries whose date prefix is strictly
older than yesterday UTC (the candidate window, after ox's auto-expansion,
spans at most today + yesterday), and writes the result atomically via
a sibling temp file + `mv -f`. See the script header for full details;
the short form:

- **Usage:** `update-state.sh <state_file> <team_id>`
- **Stdin:** one entry id per line (regex-filtered on read)
- **Stdout:** nothing on success
- **Stderr:** one-line warnings (e.g. malformed prior state)
- **Exit:** `0` success, `2` usage, `3` internal

```bash
printf '%s\n' "$NEW_IDS" \
  | bash scripts/update-state.sh "$STATE_FILE" "$TEAM_ID"
```

Teams skipped in Step 2 are **not** invoked here — their prior state
passes through unchanged because `update-state.sh` only rewrites the
team_id it was given. Teams with no prior entry AND no new ids are
correctly never added to the state file.

### Step 6: Return the Summary

Return Claude's stdout to the user directly. It is already formatted for
Slack mrkdwn. Do not reformat or annotate — just show it.

## Output

The primary output is the Claude-generated summary. Prefix it with a
brief one-line header showing:

- How many teams were summarized
- Any teams that were skipped (and why — typically "no new entries
  since last summary" from Step 2)

Keep any preamble or postamble minimal. The summary itself is the value.

If Step 2 short-circuited because every team had zero new entries, the
only output is the single line `No new distilled content since last
summary.` — do not invoke Claude and do not print a header.
