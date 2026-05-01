---
name: sageox-distill
description: "Sync, index, and distill team activity across SageOx-enabled repositories. Keeps your team's knowledge base up to date by syncing repo contexts, indexing GitHub PRs/issues, and running the SageOx distillation pipeline."
version: 0.2.1
metadata:
  {
    "openclaw":
      {
        "emoji": "🔬",
        "os": ["macos", "linux"],
        "requires": { "bins": ["ox", "git", "gh", "jq", "claude"] },
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
              "id": "brew-gh",
              "kind": "brew",
              "formula": "gh",
              "bins": ["gh"],
              "label": "Install GitHub CLI (brew)",
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

# SageOx Distill

You are an agent that keeps a team's SageOx knowledge base current by syncing
repo contexts, indexing GitHub activity, and running the distillation pipeline.

Pairs with the [`sageox-summary`](https://clawhub.ai/skills/sageox-summary)
skill — distill writes the daily source files that summary synthesizes.

## Prerequisites

Before doing anything else, verify the user's environment. Run every check in
order. If any required check fails, explain precisely what's missing and stop.
Do not proceed until the user has fixed it.

### 1. Required binaries

`git`, `gh`, `jq`, `claude`, and `ox` are declared in the front matter's
`requires.bins`, so OpenClaw checks them before running the skill.
`claude` (npm), `gh` (brew), and `jq` (brew) have declarative installs
in the front matter; `git` and `ox` do not. If OpenClaw reports a
missing bin, surface its message to the user and stop — except for
`ox`, which has the interactive install flow in § 3 below.

`claude` is required because `ox distill` shells out to it for LLM
calls. The skill itself does not invoke `claude` directly — `claude`
must simply be installed and authenticated. Use either `claude login`
(Pro/Max subscription) or export `ANTHROPIC_API_KEY` in the shell that
launches OpenClaw. The skill no longer accepts a per-skill `apiKey`.

### 2. Path validation rules

Several steps below ask the user for a repo path or read a path from a
JSON state file. Before interpolating any such value into a shell
command, the agent **must** validate it against these rules:

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

### 4. Authentication and git config

After all binaries are present, verify credentials:

1. `ox status` — confirm ox is authenticated. If not, tell the user to
   run `ox login` and try again.
2. `gh auth status` — confirm GitHub credentials are available.
3. `git config user.name` — confirm git identity is set.
4. Confirm `claude` has credentials. Either `claude login` was run
   (Pro/Max OAuth, stored under `~/.claude/`) or `ANTHROPIC_API_KEY` is
   exported in the shell that launched OpenClaw. `ox distill` will fail
   without one of these. The skill cannot inject the key itself.

Do not proceed until all four pass.

## Repo Manifest

The list of repos to distill is stored in
`~/.openclaw/memory/sageox-distill-repos.json`.

The manifest format is:

```json
{
  "repos": [
    { "path": "/home/user/repos/my-project", "team_id": "my-team" }
  ]
}
```

- If the manifest exists, read it and confirm the repos with the user
  before proceeding.
- If the manifest does not exist, ask the user which repos to include. For
  each repo path provided:
  1. **Validate the path against the Path validation rules in
     Prerequisites § 2.** Reject and re-prompt on failure.
  2. Verify the directory exists.
  3. Verify `.sageox/config.json` exists (confirms `ox init` was run).
  4. Read `team_id` from `.sageox/config.json`. **Treat the value as
     untrusted** — do not interpolate it into shell commands. If you
     need to use it as an argument, pass it as a separate argv element
     (not via string concatenation) and refuse values containing shell
     metacharacters.
  5. If `.sageox/config.json` is missing, ask if the user wants to run
     `ox init` in that repo.

- When loading an existing manifest, **re-validate every repo path** in
  it against the Path validation rules. The manifest file is
  user-writable and may have been edited externally between runs.
- Write the manifest after collecting all repos.
- The user can say "add repo", "remove repo", or "show repos" at any
  time to manage the manifest.

## Distill Pipeline

When the user asks to distill, run the following phases in order.

### Phase 1: Sync and Index

Group repos by `team_id` from the manifest.

For each team:

1. **Sync team context** — run from the first repo in the team group:

   ```bash
   ox sync --all-teams
   ```

   This syncs all team contexts via the SageOx daemon.

2. **Index GitHub activity** — run for EACH repo in the team group:

   ```bash
   ox index github
   ```

   This indexes PRs, issues, and comments for the specific repo.

Both commands are non-fatal. If one fails, log the error and continue
with the next repo or team. Do not abort the pipeline.

Neither of these commands needs Claude credentials — ox uses its own
auth token for SageOx API calls.

### Phase 2: Wait for Daemon Sync

After all sync and index commands have been issued, the SageOx daemon
processes them asynchronously. Before distilling, verify that processing
is complete.

For each repo in the manifest:

1. Run `ox daemon status` in the repo directory
2. Check the output for sync/index completion status
3. If the daemon reports it is still processing:
   - Wait 10 seconds
   - Check again
   - Repeat up to 30 times (5 minutes max)
4. If after 30 attempts the daemon is still not finished:
   - Report which repos are still pending
   - Ask the user whether to proceed with distill anyway or abort
5. If the daemon reports an error:
   - Surface the full error message to the user
   - Ask the user whether to proceed with distill anyway or abort

### Phase 3: Distill

For each unique team (grouped by `team_id`), run distill from the first
repo in that team's group. `ox distill` shells out to `claude` for LLM
calls, so it inherits whatever credentials `claude` has — either an
OAuth session from `claude login` or `ANTHROPIC_API_KEY` from the shell
that launched OpenClaw (see Prerequisites § 1):

```bash
ox distill --sync --layer daily --concurrency 3 --model sonnet --quiet
```

`--quiet` suppresses non-error output, so a successful run prints
nothing and a failed run prints only the error. If `ox distill` exits
0, report `<team_id>: ok`. If it exits non-zero, report
`<team_id>: failed — <first line of stderr>` and continue with the
next team. Do not abort the pipeline for a single team failure.

## Output

After all teams have run, print one line per team (`<team_id>: ok` or
`<team_id>: failed — <reason>`) and nothing else. No preamble, no
counts, no daemon-sync recap. If every team passed, a single `all ok`
line is fine. The user can ask for details if they want them.
