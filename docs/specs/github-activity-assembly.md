# GitHub Activity Assembly — Discussion Summary & Decisions

## Goal

Construct preprocessed GitHub event clusters from CodeDB that feed into the
fact extractor (see `github-extractor.md` and `github-extractor-prompt.md`).
This is the missing piece between raw GitHub data sync and the alignment feed
(daily standup replacement for agentic teams).

## Context

- `ox distill` already distills observations and discussion transcript facts
- GitHub data is already synced from the API → ledger JSON → CodeDB (SQLite)
- Commit d2cad06 added PR→commit indexing to support this work
- The alignment feed composer lives in ox-bot (out of scope here)
- See `alignment-feed-prfaq.md` for high-level direction

## Decisions

### Data source: CodeDB (SQL), not ledger JSON

Query the SQLite database directly. No filesystem walks. The relevant tables:

- `pull_requests` — PR metadata (number, title, body, author, state, labels, timestamps, merge_commit, url)
- `pr_comments` — flat list of review + discussion comments (author, body, path, line, created_at)
- `pr_commits` — join table: (pr_id, sha) only, no metadata
- `commits` — git commits with full metadata (hash, author, message, timestamp)
- `issues` — issue metadata
- `issue_comments` — issue comments

PR commit metadata is obtained via `pr_commits.sha` LEFT JOIN `commits.hash`.
This works for merge and fast-forward PRs. Squash merges lose individual branch
commit detail (only the squash commit is on main). Rebase PRs return no matches.
Both cases degrade gracefully — the PR itself still has title/body/comments.

### Data scope: CodeDB as-is, no schema changes

No new tables, no new API calls. The LLM extractor can:
- Infer PR→issue links from PR body text (`Closes #N`, `Fixes #N`)
- Distinguish review comments from discussion comments via `path` field presence

### Three cluster types

1. **PR cluster** — PR + comments (split into review vs discussion) + commits (via join). Related issues inferred by extractor from body text.
2. **Standalone issue** — Issues not referenced by any PR in the window.
3. **Standalone commit** — Commits in the window not linked to any PR via `pr_commits`.

### Output shape: match the spec

Nested JSON matching `github-extractor.md` cluster format. Go-side logic splits
comments into review vs discussion and groups review comments by author.

### Package: `internal/codedb/query/`

New subpackage under codedb, alongside `store/`, `index/`, and `search/`.
Read-only query layer over the same database.

### Time window: parameter-driven

Assembly function accepts `(since, until)` as parameters. Callers decide the window:
- `ox distill` infers window from distill state, passes it in
- `ox code activity` accepts explicit `--since` flag

### Two callers

1. **`ox code activity --since <duration|date>`** — CLI subcommand for explicit inspection/debugging
2. **`ox distill`** — calls assembly as a pipeline step (like discussion fact extraction)
