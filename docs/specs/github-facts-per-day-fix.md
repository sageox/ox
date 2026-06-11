# Fix: GitHub Facts Per-Day Bucketing

## Problem

`extractGitHubFacts()` has the same class of bugs that issue #211 identified for
observations/discussions, fixed in PR #212:

1. **Intra-day overwrites**: Writes to `memory/.github-facts/{date}.md` — running
   distill twice in a day overwrites the first run's facts
2. **Multi-day accumulation**: If distill hasn't run for 3 days, all GitHub activity
   gets dumped into one fact file for today instead of bucketed per day
3. **Multi-clone collision**: Same filename from different clones = overwrite
4. **No high-water inference**: Fresh clone with no state file reprocesses everything

## Fix (follow PR #212 pattern)

### 1. Group GitHub activity by day

Instead of one `AssembleActivity(since, until)` call for the whole window, partition
results by day. Two approaches:

- **Option A**: Call `AssembleActivity()` once for the full window, then partition the
  returned clusters by their primary timestamp (merged_at for PRs, updated_at for issues,
  timestamp for commits) into per-day buckets
- **Option B**: Loop day-by-day calling `AssembleActivity(dayStart, dayEnd)` for each day

Option A is better — one DB round-trip, partition in Go.

### 2. UUID7 filenames

Change from `memory/.github-facts/{date}.md` to `memory/.github-facts/{date}-{uuid7}.md`.
This matches the daily distill pattern and prevents both intra-day and multi-clone collisions.

### 3. Per-day LLM extraction

One extractor call per day's clusters. If a day has no meaningful clusters, skip it.
This ensures each fact file corresponds to one day's activity.

### 4. Infer high-water from existing files

When `state.LastGitHubFacts` is empty (fresh clone), scan existing
`memory/.github-facts/` files and infer the high-water mark from the most recent
file's date. Follow the same `inferDailyHighWater` pattern from PR #212.

### 5. readPendingGitHubFacts compatibility

`readPendingGitHubFacts()` already handles any `.md` files in the directory by parsing
dates from content. The UUID7 suffix in filenames won't break it, but verify this.

## Files to modify

- `cmd/ox/distill_github.go` — Main changes: per-day bucketing, UUID7 filenames,
  high-water inference
- `cmd/ox/distill_github_test.go` — Update tests for new behavior
- `internal/codedb/query/types.go` — May need a timestamp accessor on cluster types
  for day-bucketing
- `internal/codedb/query/activity.go` — No changes needed if using Option A

## Test changes

- Integration tests need to verify per-day output
- Idempotency test needs to verify UUID7 prevents overwrites
- Add intra-day re-run test
- Add multi-day catch-up test (3 days of activity → 3 fact files)
- Verify `readPendingGitHubFacts` works with UUID7 filenames
