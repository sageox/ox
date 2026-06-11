# GitHub Activity Assembly — TDD Implementation Plan

## Overview

9-step TDD implementation plan for assembling GitHub event clusters from CodeDB
for the fact extractor. Each step builds on the previous and produces a working,
testable increment.

**Package:** `internal/codedb/query/`
**Callers:** `ox code activity` (CLI) and `ox distill` (pipeline step)
**Output:** Flat JSON array of event clusters matching `github-extractor.md` input spec

### Key Design Decisions (from cross-review)

- **Flat array output**: `ActivityResult` keeps separate typed arrays for Go-side use;
  `MarshalEventClusters()` produces the flat `[{"type":"pull_request",...}, ...]` format
- **Field name alignment**: PR body → `"description"`, PR state → `"status"` in JSON
- **ReviewGroup.status dropped**: schema has no review-level status; extractor infers from text
- **`related_issue` omitted**: not populated by assembly; extractor infers from PR body text
- **`files_changed` omitted**: not available in CodeDB
- **`ReviewComment.Line` is `*int`**: nullable, serializes as JSON `null` not `0`
- **Cross-window PR commit exclusion**: commits linked to ANY PR excluded from standalone,
  regardless of whether that PR is in the query time window
- **JSON-first CLI output**: `ox code activity` defaults to compact JSON (data is inherently
  nested clusters); `--json` gives pretty-printed for debugging

### Coding Standards Applied

- **Error handling**: `fmt.Errorf("context: %w", err)` wrapping; `errors.Is()` for sentinels
- **No new interfaces**: `AssembleActivity` takes `*store.Store` directly (one implementation)
- **Logging**: `slog.Info("action", "key", val)` single-line key=value format
- **Context**: `context.Context` first parameter; `ctx.Err()` check in N+1 loop
- **Testing**: table-driven `t.Run()`, `t.Parallel()`, `require` for setup, `assert` for assertions
- **CLI errors**: semantic styles from `internal/cli/styles.go` (`StyleError`, `StyleCommand`)
- **Naming**: PascalCase exported, camelCase unexported, kebab-case flags

---

## Step 1: Define Output Types

**File:** `internal/codedb/query/types.go`

**What changes:** Create the `query` package with two layers of types.

Go-side types for query results and testing:
- `ActivityResult` — container with `PRClusters []PRCluster`, `StandaloneIssues []StandaloneIssue`,
  `StandaloneCommits []StandaloneCommit`, `Metadata ActivityMetadata`
- `PRCluster` — number, title, description (`json:"description"`), author, status (`json:"status"`),
  merged_at, url, commits `[]CommitEntry`, reviews `[]ReviewGroup`,
  discussion_comments `[]Comment`. No `related_issue`. No `files_changed`.
- `ReviewGroup` — reviewer, comments `[]ReviewComment`. No status field.
- `ReviewComment` — body, path (`*string`), line (`*int`)
- `Comment` — author, body, created_at
- `CommitEntry` — sha, message, author, timestamp
- `StandaloneIssue` — number, title, body, author, state, url, comments `[]Comment`
- `StandaloneCommit` — sha, message, author, timestamp
- `ActivityMetadata` — since, until, pr_count, issue_count, commit_count

Flat array serialization:
- `MarshalEventClusters() ([]byte, error)` method on `*ActivityResult` — iterates PR clusters
  (adding `"type":"pull_request"`), standalone issues (adding `"type":"issue"`), standalone
  commits (adding `"type":"commit"`), returns flat JSON array. Uses thin wrapper structs
  with a `Type` field that embed the cluster types.

**Tests first:**
- Flat array contract: ActivityResult with 1 PR, 1 issue, 1 commit → `MarshalEventClusters()`
  → parse as `[]map[string]interface{}` → 3 elements, each has `"type"` field
- Type discriminators: PR → `"pull_request"`, issue → `"issue"`, commit → `"commit"`
- Field name alignment: marshal PRCluster → JSON keys are `"description"`, `"status"`,
  NOT `"body"`, `"state"`
- Empty result: `MarshalEventClusters()` on empty ActivityResult → `[]`
- Nil/omitempty: no reviews → `"reviews":[]`; `Line` nil → null in JSON;
  no `related_issue` or `files_changed` keys present
- Round-trip of Go types: marshal PRCluster → unmarshal → assert equality

**Why first:** The flat array format IS the output contract. Every downstream step depends on
this being correct.

---

## Step 2: PR Query — Fetch PRs in Time Window

**File:** `internal/codedb/query/activity.go`

**What changes:** Implement `AssembleActivity(ctx context.Context, s *store.Store, since, until time.Time) (*ActivityResult, error)` starting with Query A — PRs where `updated_at`, `merged_at`, or `created_at` falls within `[since, until]` (inclusive). Returns incomplete `PRCluster` structs (no comments or commits yet). DB column `state` maps to Go `Status` (`json:"status"`); DB column `body` maps to Go `Description` (`json:"description"`).

Also create `testutil_test.go` with helper functions: `insertPR()`, `insertComment()`, `insertCommit()`, `insertIssue()`, `insertIssueComment()`, `insertPRCommit()`. Used throughout Steps 2-7.

**Tests first** (table-driven, `store.Open(t.TempDir())` pattern):
- PR within window by `updated_at` → appears
- PR within window by `merged_at` only (created before window) → appears
- PR within window by `created_at` only → appears
- PR outside window → excluded
- PR at exact boundary timestamps → included (validates `>=` and `<=`)
- Empty database → empty ActivityResult with zero counts
- Multiple PRs → ordered by `COALESCE(merged_at, updated_at, created_at) DESC`
- Field mapping: DB `state` → Go `Status` → JSON `"status"`; DB `body` → Go `Description`
  → JSON `"description"`; labels (JSON-encoded string in DB); nullable timestamps

**Why this step:** Foundation for steps 3-4 which add child data to these PRs.

---

## Step 3: Comment Splitting — Review vs. Discussion

**File:** `internal/codedb/query/activity.go`

**What changes:** Add per-PR comment fetching (Query B). For each PR from Step 2, query
`pr_comments` ordered by `created_at ASC`. Split in Go: `path IS NOT NULL` → review comments,
`path IS NULL` → discussion comments. Group review comments by author into `ReviewGroup`
structs (reviewer + comments only, no status).

**Tests first:**
- Split correctness: 2 comments with `path`, 2 without → correct classification
- Grouping by author: 3 from "alice", 2 from "bob" → 2 ReviewGroups with correct counts
- Single reviewer, multiple comments: 4 from "alice" → 1 ReviewGroup, 4 comments
- Comment ordering: varying `created_at` → ordered ASC within each group
- PR with no comments → empty slices (not nil, so JSON serializes as `[]`)
- ReviewComment fields: path, line, body all mapped correctly
- Nullable line: review comment with `path` set but `line` NULL → `ReviewComment.Line` is nil
  → serializes as JSON `null`
- No `status` field on ReviewGroup in output

**Why this step:** Most complex Go-side logic. Testing in isolation before commits ensures
the splitting algorithm is correct independently.

---

## Step 4: PR Commits via LEFT JOIN

**File:** `internal/codedb/query/activity.go`

**What changes:** Add per-PR commit fetching (Query C). LEFT JOIN `pr_commits.sha` to
`commits.hash`. When LEFT JOIN returns NULL (squash/rebase), emit sha with nil metadata fields.

**Tests first:**
- Normal: 2 `pr_commits` with matching `commits` rows → full CommitEntry structs
- Squash degradation: `pr_commits` SHAs with NO matching `commits` row → sha present,
  author/message/timestamp nil
- Mixed: 3 `pr_commits`, only 2 have matching `commits` rows → 2 full, 1 degraded
- Ordering: by `timestamp ASC`
- PR with no `pr_commits` rows → empty `[]CommitEntry`

**Why this step:** LEFT JOIN degradation is a critical design decision. Testing it explicitly
ensures the graceful fallback works before adding remaining cluster types.

---

## Step 5: Standalone Issues

**File:** `internal/codedb/query/activity.go`

**What changes:** Add Query D — fetch issues within the time window (by `updated_at` or
`created_at`). For each issue, also fetch `issue_comments`. Return as
`[]StandaloneIssue` on `ActivityResult`.

**Tests first:**
- Issue within window by `updated_at` → appears
- Issue outside window → excluded
- Issue with 3 comments → comments ordered by `created_at ASC`
- Issue with no comments → empty `[]Comment`
- Field mapping: number, title, body, author, state, url
- Multiple issues → ordered by `COALESCE(updated_at, created_at) DESC`

**Why this step:** Second cluster type. Simpler than PRs (no review splitting, no commit joins).

---

## Step 6: Standalone Commits

**File:** `internal/codedb/query/activity.go`

**What changes:** Add Query E — fetch commits within the time window that are NOT linked to
any PR via `pr_commits`. Uses `NOT IN (SELECT sha FROM pr_commits)` — excludes ALL PR-linked
commits regardless of whether that PR is in the time window.

**Tests first:**
- Standalone commit (no `pr_commits` entry) → appears
- PR-linked commit → excluded
- Cross-window exclusion: commit linked to a PR OUTSIDE the current time window → still
  excluded from standalone (explicit test documenting this design decision)
- Commit outside time window → excluded
- Field mapping: hash, author, message, timestamp
- Ordering: by `timestamp DESC`

**Why this step:** Completes all three cluster types. Depends on `pr_commits` table context
from Step 4.

---

## Step 7: ActivityMetadata, Full Integration, and Volume Test

**File:** `internal/codedb/query/activity.go`, `internal/codedb/query/activity_test.go`

**What changes:** Add `ActivityMetadata` to the result (time window, counts per cluster type).
Write comprehensive integration tests.

**Tests first:**
- Metadata counts match actual data (pr_count, issue_count, commit_count)
- Metadata time window matches input parameters
- Golden-file test (shared dataset agreed with tester):
  - 1 merged PR: 2 review comments (one reviewer, `path` set) + 1 discussion comment +
    2 commits (1 full metadata, 1 degraded/squash)
  - 1 open PR: 1 review comment, 0 commits
  - 1 closed-without-merge PR: 2 discussion comments
  - 2 standalone issues (1 with comments, 1 without)
  - 3 standalone commits
  - Validate Go struct fields AND `MarshalEventClusters()` flat JSON output
- `related_issue` absent from all PR clusters in JSON output
- `files_changed` absent from all PR clusters in JSON output
- Empty window → zero counts, empty slices, `MarshalEventClusters()` returns `[]`
- Context cancellation: already-cancelled context → `context.Canceled` error
- Mid-loop cancellation: insert 5 PRs, context with short deadline →
  `context.DeadlineExceeded` without hanging
- Volume test: insert 20 PRs each with 2-3 comments → all returned, no truncation,
  completes promptly

**Why this step:** Validates composition of Steps 2-6. The golden-file test IS the UAT
Scenario 10 dataset (complementary to tester's Test 3 which exercises the full pipeline
through the indexer).

---

## Step 8: CLI Command — `ox code activity`

**File:** `cmd/ox/code_activity.go`

**What changes:** New cobra command registered under `codeCmd` in `init()`, following the
`codeInsightsCmd` pattern. Command: `ox code activity --since 7d [--json]`.

The `--since` flag accepts durations (`7d`, `24h`) or dates (`2026-03-15`). Default: `7d`.
Steps: find repo root → resolve CodeDB dir → open DB → compute `(since, until)` → call
`query.AssembleActivity()` → call `MarshalEventClusters()` → stdout.

Output modes:
- Default (no flags): compact JSON flat array (data is inherently nested clusters)
- `--json` flag: pretty-printed JSON with indentation (for human debugging)
- Agent context: compact JSON (same as default)

Error messages use semantic styles from CLI design system:
- Missing CodeDB: `StyleError` + `StyleCommand` with remediation

CLI E2E tests (5 subtests, `//go:build integration`, `testguard.BuildOxBinary()` pattern):
- Default output: valid flat JSON array
- `--json`: pretty-printed output with indentation
- Empty window: valid JSON `[]`, exit code 0
- Invalid `--since` (`banana`): clear error message, non-zero exit
- No CodeDB: error message suggesting `ox code index`, non-zero exit

Unit tests (no build tag, direct handler invocation):
- Duration parsing: `7d` → 7 days ago, `24h` → 24 hours, `2026-03-15` → date, invalid → error
- Output is flat JSON array (not ActivityResult with separate typed arrays)
- Agent auto-detection behavior

**Why this step:** First real caller. Simpler than distill (no state management).

---

## Step 9: Distill Pipeline Integration

**File:** `cmd/ox/distill_github.go`

**What changes:** New file following the `distill_discussions.go` pattern. Adds
`extractGitHubFacts()` function called from `runDistill()` before `distillDaily()`.

Steps: open CodeDB → compute time window from distill state → call
`query.AssembleActivity()` → call `MarshalEventClusters()` → construct LLM prompt
(system prompt from `github-extractor-prompt.md`, user prompt with `<batch>` tags wrapping
the flat JSON array) → call LLM extractor → write resulting facts to
`memory/.github-facts/{date}.md` → update distill state with `LastGitHubFactsTimestamp`.

**Tests first:**
- Fact file output: mock LLM returns 2 facts → correctly formatted `.md` file in
  `memory/.github-facts/`
- Empty clusters: `AssembleActivity` returns empty → LLM NOT called
- State tracking: `LastGitHubFactsTimestamp` updated after success
- Time window from state: `since` computed from distill high-water mark
- Idempotency: same state twice → no new facts on second run
- Prompt verification (mock LLM captures input):
  - System prompt = exact content of `github-extractor-prompt.md`
  - User prompt contains `<batch>` tags wrapping flat JSON array of clusters
  - `{interval}` placeholder replaced with actual time window description

**Why last:** Depends on everything else + introduces LLM integration requiring mocks.

---

## Test Ownership

| Test | Owner | Location | Build Tag |
|------|-------|----------|-----------|
| Steps 1-7 unit tests | Developer | `internal/codedb/query/activity_test.go` | none |
| Step 8 unit tests | Developer | `cmd/ox/code_activity_test.go` | none |
| Step 8 CLI E2E | Developer | `cmd/ox/code_activity_e2e_test.go` | `integration` |
| Step 9 unit tests | Developer | `cmd/ox/distill_github_test.go` | none |
| Test 1: Index→Query boundary | Tester | `internal/codedb/query/activity_boundary_test.go` | none |
| Test 3: Full pipeline | Tester | `internal/codedb/query/activity_pipeline_test.go` | none |

### Boundary Between Unit and Integration Tests

- **Developer unit tests (Steps 1-7)**: In-memory SQLite via `store.Open(t.TempDir())`, data
  seeded via direct INSERT statements, test each query/logic layer in isolation. Fast feedback.
- **Tester Test 1 (boundary)**: Real `ledger.PRFile`/`IssueFile` → `IndexGitHubData()` →
  `AssembleActivity()`. Catches field mapping gaps between indexer and queries.
- **Tester Test 3 (pipeline)**: Full pipeline through `MarshalEventClusters()`. Validates
  flat array format, field names, type discriminators against extractor contract.
- **Developer Step 8 unit tests**: Cobra handler invoked directly with test store.
  Catches flag parsing, output formatting.
- **Developer Step 8 E2E**: Real binary via `testguard`. Catches path resolution,
  env isolation, exit codes.

Same golden dataset, different layers, different failure modes. No overlap.

### Note on `pr_commits` in Integration Tests

`IndexGitHubData()` populates `pr_commits` with `(pr_id, sha)` only. Commit metadata
(author, message, timestamp) comes from the `commits` table populated by git history indexing.
Integration tests that want full commit metadata must also insert rows into `commits`.
Tests that only run `IndexGitHubData()` will see all PR commits in the degraded state
(sha present, metadata NULL) — this is correct behavior, not a bug.
