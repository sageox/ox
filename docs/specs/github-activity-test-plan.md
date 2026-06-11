# GitHub Activity Assembly — Test Plan

Reference test plan for the GitHub activity assembly feature. Covers user acceptance
scenarios, integration tests, and simulation strategy.

Related specs:
- `github-activity-assembly.md` — design decisions
- `github-extractor.md` — extractor PRD (defines the output contract)
- `github-extractor-prompt.md` — extractor prompt (system + user template)

## Output Format Contract

Assembly output is a **flat JSON array** of event clusters with type discriminators.
This is the input to the extractor prompt's `<batch>` tags.

```json
[
  {"type": "pull_request", "number": 42, "description": "...", "status": "merged", ...},
  {"type": "issue", "number": 30, "body": "...", "state": "open", ...},
  {"type": "commit", "sha": "abc123", "message": "...", ...}
]
```

Key field rules:
- PR text field: `"description"` (not `"body"`)
- PR status field: `"status"` with values `"merged"`, `"open"`, `"closed"` (not `"state"`)
- Issue text field: `"body"`
- Issue status field: `"state"`
- Every element has a `"type"` field: `"pull_request"`, `"issue"`, or `"commit"`
- `related_issue` and `files_changed` are **absent** (not null — omitted entirely)
- ReviewGroup has `reviewer` + `comments[]` only (no `status` — not available from schema)
- ReviewComment `line` field is `*int` (nullable — serializes as `null` not `0`)
- Empty arrays serialize as `[]` not `null`

---

## UAT Scenarios

### Scenario 1: Basic PR Cluster Assembly

**Preconditions:** CodeDB indexed, contains merged PR #42 with: title, body, author,
2 review comments (one with `path` set, one without), 3 commits linked via
`pr_commits`, and 1 discussion comment.

**Action:** `ox code activity --since 7d`

**Expected outcome:**
- Flat JSON array contains a cluster with `"type": "pull_request"`, `"number": 42`
- Uses `"description"` (not `"body"`), `"status": "merged"`
- `reviews` array: review comments (those with `path` set) grouped by reviewer into
  ReviewGroups — each with `reviewer` and `comments[]` with `body`, `path`, `line`
- `discussion_comments` array: comments without `path`
- `commits` array: 3 entries with `sha`, `message`, `author`, `timestamp`
- No `related_issue` or `files_changed` keys

**Edge cases:**
- PR with only review comments → `discussion_comments` is `[]`
- PR with only discussion comments → `reviews` is `[]`
- Multiple reviewers → separate ReviewGroup per reviewer
- Review comment with `path` set but `line` NULL → `line` serializes as `null`

---

### Scenario 2: Squash Merge Degradation

**Preconditions:** CodeDB has merged PR #55 where `pr_commits` contains SHAs that
do NOT match any row in the `commits` table. This simulates squash merge — individual
branch commits were squashed into one merge commit.

**Action:** `ox code activity --since 7d`

**Expected outcome:**
- PR cluster #55 appears in output
- `commits` array contains entries with `sha` populated but `message`, `author`,
  `timestamp` are null/empty
- PR title, body, comments fully populated — cluster usable by extractor
- No error in stderr

**What this prevents:** Silently dropping squash-merged PRs from the alignment feed.

---

### Scenario 3: Rebase PR Degradation

**Preconditions:** CodeDB has merged PR #60 where `pr_commits` has zero rows.

**Action:** `ox code activity --since 7d`

**Expected outcome:**
- PR cluster #60 appears with empty `commits` array (`[]`)
- PR title, body, author, status, comments all present
- Cluster is valid and parseable by extractor

---

### Scenario 4: Standalone Issues

**Preconditions:** CodeDB has issue #30 (open bug report) with 2 comments, created
within the time window. No PR references issue #30.

**Action:** `ox code activity --since 7d`

**Expected outcome:**
- Flat array contains element with `"type": "issue"`, `"number": 30`
- Uses `"body"` (not `"description"`), `"state": "open"`
- Includes `url`, `author`, `comments[]` (with author, body, created_at)

**Edge cases:**
- Issue with zero comments → `comments` is `[]`
- Closed issue updated in window → still appears (updated_at within window)

---

### Scenario 5: Standalone Commits

**Preconditions:** CodeDB has commits `abc123` and `def456` with timestamps in window.
Neither appears in `pr_commits`.

**Action:** `ox code activity --since 7d`

**Expected outcome:**
- Flat array contains elements with `"type": "commit"`
- Each has `sha`, `message`, `author`, `timestamp`
- These commits do NOT appear in any PR cluster's `commits` array

**Edge cases:**
- Commit linked to out-of-window PR → still excluded from standalone (correct behavior:
  `NOT IN (SELECT sha FROM pr_commits)` excludes ALL PR-linked commits regardless of window)

---

### Scenario 6: Time Window Filtering

**Preconditions:**
- PR #10 merged 2 days ago (within `--since 7d`)
- PR #11 merged 30 days ago (outside window)
- Issue #20 created 3 days ago (within window)
- Issue #21 created 60 days ago but updated 1 day ago (within window via `updated_at`)
- Commit `aaa` from 5 days ago (within window)
- Commit `bbb` from 20 days ago (outside window)

**Action:** `ox code activity --since 7d`

**Expected outcome:**
- PR #10 appears, PR #11 does NOT
- Issue #20 and #21 both appear (#21 via OR on updated_at)
- Commit `aaa` appears, `bbb` does NOT
- Timestamps are inclusive on both ends (`>=` and `<=`)

**Edge cases:**
- `--since 24h` — narrow window
- `--since 30d` — wide window
- `--since 2026-03-15` — absolute date
- No `--since` flag → default 7d
- Data exactly at boundary timestamps → included (inclusive)

---

### Scenario 7: Empty Results

**Preconditions:** CodeDB exists and is indexed, but no data within requested window.

**Action:** `ox code activity --since 1h`

**Expected outcome:**
- Output is `[]` (empty flat array)
- Exit code 0
- No error messages

**What this prevents:** Pipeline crash on empty input.

---

### Scenario 8: No CodeDB Exists

**Preconditions:** Repo initialized (`ox init`) but `ox code index` never run.

**Action:** `ox code activity --since 7d`

**Expected outcome:**
- Clear error message suggesting `ox code index`
- Non-zero exit code
- No garbage output

---

### Scenario 9: Distill Pipeline Integration

**Preconditions:** Repo initialized, CodeDB indexed with recent activity, team context
exists with `memory/` directory, distill state exists, LLM backend available (or mocked).

**Action:** `ox distill` (full pipeline run)

**Expected outcome:**
- GitHub fact extraction step runs as a pipeline step
- Assembly queries CodeDB for time window since last distill
- Clusters serialized to flat JSON array matching extractor spec
- LLM called with:
  - System prompt: exact content of `github-extractor-prompt.md`
  - User prompt: contains `<batch>` tags wrapping serialized clusters, `{interval}`
    placeholder replaced with actual time window
- Facts written to `memory/.github-facts/{date}.md`
- Distill state updated with new high-water mark
- Daily distiller reads BOTH `.discussion-facts/` and `.github-facts/`

**Edge cases:**
- First distill run (no prior state) → reasonable default window
- No data in distill window → no facts, state still updated
- Extractor returns empty array → no fact file written, state updated
- Extractor returns `[Uncertain significance]` facts → included for downstream distiller

---

### Scenario 10: Output Format Validation (Extractor Contract)

**Preconditions:** CodeDB has rich dataset: 1 merged PR with review + discussion +
commits, 1 standalone issue, 2 standalone commits.

**Action:** `ox code activity --since 7d --json`

**Validation checklist:**
- Output is valid JSON, parseable by `jq .`
- Top level is a flat array (not object with typed sub-arrays)
- Every element has `"type"` field: `"pull_request"`, `"issue"`, or `"commit"`
- PR uses `"description"` and `"status"`; issue uses `"body"` and `"state"`
- No `"related_issue"` or `"files_changed"` keys anywhere
- Output is directly consumable by extractor prompt `<batch>` tags

**What this prevents:** Schema drift silently breaking the alignment feed.

---

### Scenario 11: Multiple PRs with Overlapping Activity

**Preconditions:** 3 PRs in same window:
- PR #1: merged, 5 commits, 3 review comments, 2 discussion comments
- PR #2: open, 0 commits, 1 review comment
- PR #3: closed without merge, 2 discussion comments

**Action:** `ox code activity --since 7d`

**Expected outcome:**
- All 3 PRs as separate clusters in the flat array
- PR #1 status "merged", PR #2 status "open", PR #3 status "closed"
- Ordered by most recent activity DESC

**What this prevents:** Missing closed-without-merge PRs (direction change signals).

---

### Scenario 12: Large Repo / High Volume

**Preconditions:** 50+ PRs, 30+ issues, 200+ commits in time window.

**Action:** `ox code activity --since 7d`

**Expected outcome:**
- All clusters returned (no silent truncation)
- Completes in under 5 seconds
- JSON is well-formed

---

### Scenario 13: Idempotency

**Preconditions:** CodeDB with stable data.

**Action:** Run `ox code activity --since 7d` twice.

**Expected outcome:** Both runs produce identical JSON. No side effects.

---

### Scenario 14: Distill State Tracking Across Runs

**Preconditions:**
- First `ox distill`: PRs #1-5 in window
- Between runs: PR #6 merges, issue #40 opened
- Second `ox distill`

**Expected outcome:**
- First run extracts facts from PRs #1-5
- Second run only processes PR #6 and issue #40 (high-water mark excludes prior data)
- No duplicate facts

**What this prevents:** Re-extracting same facts (wasted LLM tokens, duplicate feed entries).

---

### Scenario 15: Invalid `--since` Input

**Preconditions:** Repo initialized, CodeDB indexed.

**Action:** `ox code activity --since banana`

**Expected outcome:**
- Clear error about invalid duration/date format
- Non-zero exit code
- No JSON output (stderr only)

---

## Test Environment

When any test scenario requires running the real `ox` CLI (not just Go function calls),
the following environment rules apply:

**Endpoint:** Always use the test environment: `https://test.sageox.ai`
- Set via `--endpoint https://test.sageox.ai` or `SAGEOX_ENDPOINT=https://test.sageox.ai`

**Test credentials:** Create a disposable test account for each test run:
- Email: `galex+{hash}@sageox.ai` where `{hash}` is a short unique identifier
  (e.g., timestamp or random hex) to avoid collisions across test runs
- Use `ox login --endpoint https://test.sageox.ai` to authenticate

**Browser auth steps:** Any auth flow that requires browser interaction (e.g., device
code confirmation, OAuth callbacks) should be performed using Chrome DevTools MCP
(`chrome-devtools-mcp` with `--headless`). This allows the testing agent to complete
auth flows programmatically without a visible browser.

---

## Integration Tests

### Test 1: Index→Query Boundary

**File:** `internal/codedb/query/activity_boundary_test.go`
**Build tag:** none
**Owner:** tester

Tests the data path: ledger JSON → `IndexGitHubData()` → CodeDB → `AssembleActivity()`.
Catches mismatches between how the indexer writes data and how the query layer reads it.

**Setup:**
1. Create temp ledger dir
2. Write 3 PRs via `ledger.WriteGitHubPR()`:
   - PR #10: merged, author "alice", body "Closes #5", 2 comments (one with
     `path: "api/handler.go"` + `line: 42`, one without path), 2 commits
     (SHA "aaa111", "bbb222")
   - PR #11: open, author "bob", 1 comment (no path), 0 commits
   - PR #12: closed (not merged), author "carol", 2 discussion comments
3. Write 2 issues via `ledger.WriteGitHubIssue()`:
   - Issue #5: open, author "dave", 1 comment
   - Issue #8: open, author "eve", 0 comments
4. `store.Open(t.TempDir())` → `index.IndexGitHubData(ctx, store, ledgerPath, nil)`
5. **Critical:** `IndexGitHubData()` only populates `pr_commits` with (pr_id, sha).
   Commit metadata comes from the `commits` table (populated by git history indexing,
   NOT GitHub indexing). After indexing, manually INSERT:
   ```sql
   INSERT INTO commits (repo_id, hash, author, message, timestamp)
   VALUES (1, 'aaa111', 'sarah', 'feat: add rate limiter', <unix_ts>)
   ```
   Leave "bbb222" WITHOUT a `commits` row to test degradation.
6. Call `query.AssembleActivity(ctx, store, since, until)`

**Assertions:**
- 3 PR clusters, 2 standalone issues, N standalone commits
- PR #10:
  - `reviews`: 1 ReviewGroup (comment with path), correct `path` and `line`
  - `discussion_comments`: 1 entry (comment without path)
  - `commits[0]` (SHA "aaa111"): full metadata — author, message, timestamp populated
  - `commits[1]` (SHA "bbb222"): degraded — sha present, author/message/timestamp null
- PR #11: status "open", empty commits, 1 discussion comment
- PR #12: status "closed", 2 discussion comments
- Issue #5: 1 comment with correct author/body
- Issue #8: empty comments array `[]`

**What this catches:**
- Field name mismatches between indexer INSERTs and query SELECTs
- Timestamp format disagreements (RFC3339 string vs unix int)
- `pr_commits` population behavior — LEFT JOIN degradation when `commits` row absent
- Nullable column handling differences between write and read paths

---

### Test 2: CLI Integration — Real Binary

**File:** `cmd/ox/code_activity_e2e_test.go`
**Build tag:** `//go:build integration`
**Owner:** developer (Step 8)

Tests the full CLI path: real ox binary → CodeDB path resolution → query → JSON output.
Follows the `doctor_e2e_test.go` pattern with `testguard.BuildOxBinary()`.

**Setup:**
1. Build ox binary via `testguard.BuildOxBinary(t, projectRoot)`
2. Create temp project with `config.CreateInitializedProject(t)`
3. Create CodeDB at expected location, populate via SQL INSERTs:
   - 1 merged PR (#42) with 2 comments (1 review, 1 discussion), 2 commits
   - 1 standalone issue (#30) with 1 comment
   - 1 standalone commit
4. Close store (so binary can open it)

**Subtests:**

| Subtest | Command | Assert |
|---------|---------|--------|
| A: Default output | `--since 7d` | Exit 0, valid JSON array, 3 elements with correct types, `"description"` not `"body"`, no `related_issue`/`files_changed` |
| B: Pretty-printed | `--since 7d --json` | Output has indentation, same structure as A |
| C: Empty window | `--since 1s` (data is old) | Exit 0, output is `[]` |
| D: Invalid input | `--since banana` | Exit non-zero, stderr has error about invalid format |
| E: No CodeDB | Fresh project, no index | Exit non-zero, stderr mentions `ox code index` |

**What this catches:**
- CodeDB path resolution from project root
- CLI flag parsing in real cobra hierarchy
- JSON encoding from actual command handler
- Exit code behavior for error cases

---

### Test 3: Full Pipeline — Extractor Contract

**File:** `internal/codedb/query/activity_pipeline_test.go`
**Build tag:** none
**Owner:** tester

Tests: ledger → `IndexGitHubData()` → `AssembleActivity()` → `MarshalEventClusters()`
→ validate JSON against extractor spec. This is THE schema contract test.

**Setup — golden dataset (aligned with developer's Step 7):**
1. Create temp ledger dir
2. Write via `ledger.WriteGitHubPR()`:
   - **PR #100 (merged):** "Add rate limiting to API", body "Implements token bucket
     at 100 req/min. Closes #50.", author "sarah", merged_at "2026-03-18T14:30:00Z"
     - Comments: `{jake, "Switch to token bucket?", path: "api/rate_limit.go", line: 42}`,
       `{jake, "Approving.", path: nil}`, `{sarah, "Good point.", path: nil}`
     - Commits: SHA "aaa111", SHA "bbb222"
   - **PR #101 (open):** "WIP: Add caching", author "bob", 1 comment with path, 0 commits
   - **PR #102 (closed, not merged):** "Experiment with Redis", author "carol",
     2 discussion comments
3. Write issues: #50 (1 comment), #55 (0 comments)
4. `store.Open()` → `IndexGitHubData()` → manually INSERT commit for "aaa111"
   (leave "bbb222" unmatched for degradation) → `AssembleActivity()` →
   `MarshalEventClusters()`

**Assertions on JSON output:**
1. Valid JSON, parses as flat array
2. Count: 3 PRs + 2 issues + N standalone commits
3. Every element has `"type"` field
4. PR #100: `"type": "pull_request"`, `"number": 100`, `"description"` key (not `"body"`),
   `"status": "merged"`, `"merged_at"` is ISO 8601, `reviews` has 1 ReviewGroup for jake
   with 1 comment (the one with path), `discussion_comments` has 2 entries,
   `commits` has 2 entries (one full metadata, one degraded)
5. PR #101: `"status": "open"`, commits `[]`, reviews has 1 group
6. PR #102: `"status": "closed"`, no `"merged_at"`, 2 discussion comments
7. Issue #50: `"type": "issue"`, `"body"` key, `"state": "open"`, 1 comment
8. Issue #55: comments is `[]`
9. Standalone commits: `"type": "commit"`, `sha`, `message`, `author`, `timestamp`
10. Raw JSON does NOT contain `"related_issue"` or `"files_changed"`
11. Valid UTF-8, no unescaped control characters

**What this catches:**
- Serialization bugs (`omitempty` producing `null` instead of `[]`)
- Wrong JSON field names across index→query→serialize pipeline
- Extractor contract violation (if this passes, extractor gets valid input)

---

## Simulation Strategy

### Tier Summary

| Tier | Scenarios | Method | Build Tag | Catches |
|------|-----------|--------|-----------|---------|
| 1: Query layer | 1-7, 10-13 | SQL INSERTs → `AssembleActivity()` | none | Query logic, filtering, splitting, degradation |
| 2: Index→query | Test 1 | `WriteGitHubPR()` → `IndexGitHubData()` → `AssembleActivity()` | none | Indexer/query field mismatches |
| 3: CLI E2E | 8, 15 | Real binary via `testguard` | `integration` | Path resolution, flag parsing, exit codes |
| 4: Distill pipeline | 9, 14 | Mock `agentcli.Backend` + real filesystem | none (mock) / `integration` (real LLM) | Prompt correctness, fact output, state |
| 5: Full pipeline | Test 3 | Ledger → Index → Assembly → Serialize | none | Serialization fidelity, extractor contract |

### Tier 1: Query-Layer Tests

Direct SQL INSERTs into in-memory SQLite via `store.Open(t.TempDir())`. Matches the
established pattern in `code_insights_test.go` (`setupInsightsDB`). Tests the query
layer in isolation — how data got into the store is not the query layer's concern.

Developer's `testutil_test.go` provides helpers: `insertPR()`, `insertComment()`,
`insertCommit()`, `insertIssue()`.

### Tier 2: Index→Query Boundary (Integration Test 1)

Feeds ledger JSON through `IndexGitHubData()` then queries with `AssembleActivity()`.

**Critical detail:** `IndexGitHubData()` only populates `pr_commits` with (pr_id, sha).
The `commits` table metadata comes from git history indexing (a separate process). After
`IndexGitHubData()`, manually INSERT into `commits` for SHAs that should have full
metadata. Leave others unmatched to verify LEFT JOIN degradation.

### Tier 3: CLI Tests

Real ox binary built with `testguard.BuildOxBinary()`. Temp project via
`config.CreateInitializedProject(t)`. CodeDB populated via SQL INSERTs then closed
before binary invocation. Uses `testguard.RunOx()` for env isolation.

For simple flag parsing (Scenario 15), direct cobra handler invocation is acceptable
as a lighter alternative.

### Tier 4: Distill Pipeline Tests

Mock `agentcli.Backend` that captures prompts and returns canned extractor responses.
Real filesystem for team context `memory/.github-facts/` directory. Mock assertions
verify:
- System prompt matches `github-extractor-prompt.md` exactly
- User prompt contains `<batch>` tags with serialized clusters
- `{interval}` placeholder replaced with actual time window

E2E with real LLM gated behind `integration` tag + `ANTHROPIC_API_KEY`.

### Tier 5: Full Pipeline (Integration Test 3)

Extends Tier 2 through `MarshalEventClusters()` serialization. Validates the JSON
output is valid extractor input. This is the authoritative contract test.

---

## Ownership

| Artifact | Owner | Status |
|----------|-------|--------|
| UAT scenarios 1-15 | tester | complete |
| Unit tests (Steps 1-7) | developer | pending |
| CLI command tests (Step 8) | developer | pending |
| CLI E2E test (Test 2) | developer | pending |
| Distill tests (Step 9) | developer | pending |
| Index→query test (Test 1) | tester | blocked on Steps 1-7 |
| Pipeline contract test (Test 3) | tester | blocked on Steps 1-7 |
