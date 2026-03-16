<!-- doc-audience: ai -->
# codedb: Temporal Index & Comment Distillation

How to leverage codedb's indexed data — especially comments — to extract what's notable and important from recent changes. Designed for external processes (distillation pipelines, team context generators) that consume the index programmatically.

## Core Insight

**Recent changes reveal current team direction.** A codebase's git history is a timeline of decisions. Comments on recently-changed files carry the highest-signal "why" context — architectural rationale, gotchas, invariants — that may have drifted from earlier work. Extracting and distilling this gives actionable insight into where the team is heading *now*, not where it was 6 months ago.

## What's Indexed Today

| Entity | Table | Bleve Index | Key Fields |
|--------|-------|-------------|------------|
| Commits | `commits` | — | hash, author, message, timestamp |
| Blobs | `blobs` | `CodeIndex` | content_hash, language |
| Diffs | `diffs` | `DiffIndex` | path, patch text |
| Symbols | `symbols` | — | name, kind, line |
| Comments | `comments` | `CommentIndex` | text, kind (line/block/doc), line, end_line |
| File revisions | `file_revs` | — | commit_id → blob_id → path |

### Relationships

```
repos → commits → file_revs → blobs → symbols
                                    → comments
       commits → diffs
       repos → refs → commits (branch tips)
```

## The Temporal Gap

Today, the index knows **what exists** but not **when each piece was authored**. Comments link to blobs, blobs link to commits via `file_revs`, but this gives "which commit included this file version" — not "when was this specific comment written."

The index can be rebuilt from scratch (blown away and recreated). Any timestamp based on parse time is ephemeral and unreliable.

**Git blame is the solution.** Blame maps each line to the commit that last modified it. It's deterministic — same git history always produces the same output, regardless of when you index.

## Blame-Based Temporal Attribution

### Schema Addition

```sql
ALTER TABLE comments ADD COLUMN authored_commit TEXT;  -- short hash of blame commit
ALTER TABLE comments ADD COLUMN authored_at INTEGER;   -- unix timestamp from blame commit
CREATE INDEX idx_comments_authored ON comments(authored_at);
```

### How It Works

1. After `ParseComments`, a `BlameComments` phase runs
2. For each file with comments, run `git blame --porcelain <path>`
3. For each comment's start line, extract the commit hash + author date
4. Store on the comment row
5. Mark blob as `comments_blamed = 1`

### Why Git Blame (Not Parse Timestamps)

| Approach | Survives Rebuild | Precise | Cost |
|----------|-----------------|---------|------|
| Parse timestamp (`NOW()`) | No | No (when parsed, not authored) | Free |
| File commit timestamp (from `file_revs`) | Yes | No (when file was committed, not when comment was added) | Free (join) |
| `git blame` per file | **Yes** | **Yes** (exact commit per line) | Moderate |

## Cost Analysis

### Native `git blame` vs go-git `Blame()`

**Use native git, not go-git.** go-git's Blame() is pure Go and walks commit history in-process — O(commits x lines) per file. Native git blame is 10-100x faster due to C implementation, pack file optimizations, and delta caching.

The codebase already shells out to native git for `resolveDefaultBranchGit()` (`indexer.go:608`), so this pattern is established.

### Cost Model: Initial Index

**Per-file cost:** `git blame --porcelain <path>` takes ~2-20ms for small files, ~50-200ms for large files with deep history.

| Repo | Files | Commits | Estimated Blame Time | Notes |
|------|-------|---------|---------------------|-------|
| sageox/ox | ~1,700 | 118 | **15-30s** | Shallow history, fast |
| tokio-rs/tokio | ~18,700 | 4,415 | **3-8 min** | Deep history, many files |
| linux kernel | ~75,000 | 1.2M | **hours** | Not a target use case |

**Observation:** Almost every file has at least one comment, so "filter by files with comments" doesn't meaningfully reduce work. The full file set must be blamed.

### Cost Model: Incremental Re-Index

On re-index, only **changed blobs** need re-blaming. The `comments_blamed` flag resets when a blob's content changes (new `content_hash`). For typical incremental updates:

| Scenario | Changed Files | Blame Cost |
|----------|--------------|-----------|
| Daily re-index (active repo) | 5-50 files | **< 2s** |
| Weekly re-index | 20-200 files | **< 10s** |
| Full rebuild (index destroyed) | All files | Same as initial |

### Optimization Strategies

#### 1. Shell Out to Native Git (Required)

```go
cmd := exec.Command("git", "blame", "--porcelain", path)
cmd.Dir = repoPath
```

Native git blame is 10-100x faster than go-git's pure Go implementation. Non-negotiable for bulk blaming.

#### 2. Parallel File Blaming

Blame per file is independent — run N goroutines concurrently:

```go
sem := make(chan struct{}, runtime.NumCPU())
for _, path := range filePaths {
    sem <- struct{}{}
    go func(p string) {
        defer func() { <-sem }()
        blameFile(repoPath, p)
    }(path)
}
```

Expected speedup: 4-8x on typical developer machines.

#### 3. Blame Cache Keyed by (path, HEAD commit)

If the file hasn't changed since last blame, skip it. The `comments_blamed` flag on blobs already handles this for incremental re-index. For full rebuilds, an external cache file could persist blame results:

```
blame_cache/{repo_name}/{blob_content_hash}.blame → serialized blame map
```

**Trade-off:** Cache adds complexity and disk usage. For most repos (< 20K files), full re-blame with parallelism is fast enough that caching isn't worth it.

#### 4. Blame Only Comment Lines (Partial Blame)

`git blame -L <start>,<end>` blames a line range. If a file has comments on lines 5-8 and 42-45, two range calls may be faster than full-file blame for large files.

**Trade-off:** Adds per-comment subprocess overhead. Only worthwhile for files with >1000 lines and sparse comments. For most files, full blame is simpler and fast enough.

#### 5. Incremental Blame via Commit Range

For re-index after `git fetch`, only files touched in new commits need re-blaming:

```bash
git diff --name-only <last_indexed_commit>..HEAD
```

This is the **highest-impact optimization** for the common case (incremental re-index). Combined with the `comments_blamed` flag reset on blob change, this ensures only truly-changed files are re-blamed.

#### 6. Batch Blame with `git blame --incremental`

`--incremental` outputs blame results as they're computed, suitable for streaming. Can be combined with a timeout to blame what's possible within a time budget, deferring the rest.

### Recommended Implementation Priority

1. **Native git blame + parallel** (covers 90% of cases well)
2. **Incremental via changed-file detection** (makes re-index near-instant)
3. **Line-range blame** (only if profiling shows large files are a bottleneck)
4. **Blame cache** (only if full rebuilds of large repos are frequent)

## Query Patterns for Distillation

### Recent Doc Comments (Highest Signal)

```sql
-- doc comments authored in the last 7 days
SELECT cm.text, cm.kind, fr.path, b.language,
       cm.authored_commit, cm.authored_at
FROM comments cm
JOIN blobs b ON b.id = cm.blob_id
JOIN file_revs fr ON fr.blob_id = b.id
JOIN refs r ON r.commit_id = fr.commit_id
WHERE cm.authored_at > strftime('%s', 'now', '-7 days')
  AND cm.kind = 'doc'
ORDER BY cm.authored_at DESC
```

### Comments on Recently-Changed Files (Before Blame Exists)

Even without blame data, you can approximate recency via commit timestamps:

```sql
-- comments on files touched in recent commits
SELECT cm.text, cm.kind, fr.path, c.timestamp, c.author
FROM comments cm
JOIN blobs b ON b.id = cm.blob_id
JOIN file_revs fr ON fr.blob_id = b.id
JOIN commits c ON c.id = fr.commit_id
WHERE c.timestamp > strftime('%s', 'now', '-7 days')
ORDER BY c.timestamp DESC
```

This gives "comments in files that were recently committed" — less precise than blame but zero additional cost.

### Tech Debt Signals

```sql
-- TODO/FIXME/HACK density by module
SELECT
    substr(fr.path, 1, instr(fr.path || '/', '/')) AS module,
    COUNT(*) AS debt_comments,
    GROUP_CONCAT(DISTINCT cm.text) AS samples
FROM comments cm
JOIN blobs b ON b.id = cm.blob_id
JOIN file_revs fr ON fr.blob_id = b.id
WHERE cm.text LIKE '%TODO%'
   OR cm.text LIKE '%FIXME%'
   OR cm.text LIKE '%HACK%'
   OR cm.text LIKE '%XXX%'
GROUP BY module
ORDER BY debt_comments DESC
```

### Comment Density Change (Delta Between Index Runs)

Track comment counts per module over time to detect:
- **Increasing doc comments** → team investing in documentation
- **Increasing TODO/FIXME** → tech debt accumulating
- **Decreasing comments** → code being simplified or comments rotting

```sql
-- snapshot for comparison (store externally between runs)
SELECT b.language, cm.kind, COUNT(*) as count
FROM comments cm
JOIN blobs b ON b.id = cm.blob_id
GROUP BY b.language, cm.kind
```

### New Symbols with Doc Comments (Architectural Decisions)

```sql
-- recently-authored doc comments near symbol definitions
SELECT s.name, s.kind, cm.text, fr.path, cm.authored_at
FROM comments cm
JOIN blobs b ON b.id = cm.blob_id
JOIN symbols s ON s.blob_id = b.id
    AND s.line BETWEEN cm.end_line AND cm.end_line + 3
JOIN file_revs fr ON fr.blob_id = b.id
WHERE cm.kind = 'doc'
  AND cm.authored_at > strftime('%s', 'now', '-14 days')
ORDER BY cm.authored_at DESC
```

This leverages proximity (doc comment immediately before symbol) to associate comments with the functions/types they describe.

## Distillation Pipeline Design

### Input: codedb Index + Git History

### Output: Structured Learnings for Team Context

```
┌─────────────────┐     ┌──────────────────┐     ┌─────────────────┐
│  codedb index    │────▶│  Distillation    │────▶│  Team Context   │
│  (SQLite+Bleve)  │     │  Pipeline        │     │  (memory/)      │
│                  │     │                  │     │                 │
│  - comments      │     │  1. Query recent │     │  - decisions    │
│  - symbols       │     │  2. Cluster      │     │  - patterns     │
│  - commits       │     │  3. Rank signal  │     │  - warnings     │
│  - diffs         │     │  4. Summarize    │     │  - conventions  │
└─────────────────┘     └──────────────────┘     └─────────────────┘
```

### Step 1: Query Recent Changes

Use temporal queries (above) to extract:
- Doc comments authored in last N days
- Commit messages from the same period
- New/changed symbol names
- Diff context around changed comments

### Step 2: Cluster by Topic

Group related changes:
- Same file/module → likely related
- Same author in same time window → likely one initiative
- Shared keywords across comments → thematic cluster

### Step 3: Rank Signal

Not all comments are equally valuable for distillation:

| Signal | Weight | Why |
|--------|--------|-----|
| Doc comment on new public function | High | Architectural decision |
| Doc comment on changed function | High | Intent evolution |
| TODO/FIXME with context | Medium | Known debt with rationale |
| Inline comment with "because"/"why"/"note" | Medium | Explains non-obvious choice |
| Block comment at file top | Medium | Module-level context |
| Simple TODO without context | Low | Actionable but not insightful |
| Comment restating code | Low | No new information |

### Step 4: Summarize into Learnings

Transform high-signal clusters into team context entries:

```markdown
## Recent Decision: Retry with Exponential Backoff (2026-03-08)
- **Where:** internal/client/retry.go
- **What:** Replaced fixed 3-retry with exponential backoff + jitter
- **Why:** (from doc comment) "Fixed retry storms under load — 3 concurrent
  callers with fixed delay amplified failures instead of absorbing them"
- **Impact:** All HTTP client callers inherit new behavior
```

## Tracking Team Direction Drift

The most interesting application: detecting when recent work **diverges from earlier patterns**.

### Approach: Compare Comment Themes Over Time Windows

```
Window 1 (30-60 days ago): extract doc comment themes
Window 2 (last 30 days):   extract doc comment themes
Delta: new themes, disappeared themes, shifted emphasis
```

**Example signals:**
- 60 days ago: comments mention "microservices", "event-driven"
- Last 30 days: comments mention "monolith", "simplify", "consolidate"
- **Drift detected:** Team is moving from distributed to consolidated architecture

This requires an LLM for theme extraction (not just keyword matching), but the *input* to that LLM is precisely what the codedb comment index provides — structured, temporal, language-aware comment data.

## Implementation Phases

### Phase 1: Blame Attribution (Follow-On to Comment Indexing PR)

- Add `authored_commit`, `authored_at` to comments table
- Implement `BlameComments()` phase using native `git blame --porcelain`
- Parallel file blaming with `GOMAXPROCS` goroutines
- Incremental via `comments_blamed` flag on blobs
- Search filter: `type:comment after:2026-03-01`

### Phase 2: Temporal Query API

- Expose temporal filters in `ox code search` (`after:`, `before:`, `since:`)
- Add `ox code recent-comments` convenience command
- JSON output for programmatic consumption by distillation pipelines

### Phase 3: Distillation Integration

- External process queries index via `ox code sql` or direct SQLite access
- Clusters, ranks, and summarizes into proposed team context entries
- Human reviews and approves before merging into team context

## Appendix: Blame Output Format

`git blame --porcelain` output (machine-parseable):

```
<40-char sha> <orig_line> <final_line> <num_lines>
author <name>
author-mail <email>
author-time <unix_timestamp>
author-tz <timezone>
committer <name>
committer-mail <email>
committer-time <unix_timestamp>
committer-tz <timezone>
summary <commit message first line>
filename <path>
	<line content>
```

Parse `author-time` for `authored_at`. Parse first 10 chars of SHA for `authored_commit`.
