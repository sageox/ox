# `ox distill history` Read Commands — Implementation Plan

Status: plan
Owner: tooling-engineer
Scope: `ox distill history list`, `ox distill history show`, `ox distill history since`
Spec: [`team-memory-journal.md`](team-memory-journal.md) §§3–6
Parallel plan: team-timezone removal (separate author, in flight)

---

## 1. Summary

**Goal.** Ship the three stable read commands on top of a purpose-built
reader library. Unblock `factory/distill/summary.ts` by giving it a single
call — `ox distill history since 24h --format=content` — that returns every
journal body in the window as one contiguous markdown stream.

**In scope.**

- New top-level `ox distill history` Cobra namespace containing `list`, `show`,
  `since` as visible subcommands.
- A new internal package `internal/distill/history/read` (the "reader library")
  that is the sole entry point into `memory/daily/`, `memory/weekly/`,
  and `memory/monthly/`. The three commands are thin Cobra wrappers.
- JSON envelopes per §4, `--format=text` renderers, and a
  `--format=content` path on `show` and `since` that emits raw bodies
  with frontmatter stripped and the `<!-- entry: ... -->` separator.
- Legacy-file tolerance per §5.b option (a): trust the filename prefix.
  No warning — silent policy.
- Multi-snapshot and multi-summary-per-day transparency per §5.a/§5.c
  (the reader returns exactly what the summarizer wrote; it does not
  dedupe or fold).
- **UTC-only date math.** All window computation runs in UTC. The CLI
  layer accepts `--tz=<zone>` on commands that take absolute time
  flags; it converts the input to UTC before constructing a
  `ReadQuery`. The reader never sees a `time.Location`.
- **Day-atomic windows with floor/ceiling rounding.** Summary files
  cover whole UTC days and cannot be sliced sub-day. A requested
  window is rounded outward to full UTC day boundaries (effective
  window = `[day-floor(since), day-ceil(until))`); the envelope's
  `window.since` / `window.until` report the rounded boundaries so
  callers see exactly what came back. A `--since=24h` query can
  therefore return up to ~48 h of content when it straddles a
  midnight.
- **Event-day filtering, not write-time.** A summary file's day
  prefix (the event day, derived from fact `RecordedAt`) is what
  places it in or out of window. The filename's UUID7 (the write
  instant) is opaque to filtering. A summary file written today
  about a month-old session is dated to last month and does not
  appear in `--since=24h`.
- **Per-day union handling.** When a day D is in window, every
  `D-*.md` file is returned in `CreatedAt`-ascending order; no file
  is treated as canonical. Days grow over time as distill runs
  multiple times and catches late-arriving facts.
- Team resolution: default active team via `config.FindRepoTeamContext`,
  `--team=<slug>` override, `--all-teams` merge for reads.
- Per-entry layer resolution for `--layer=auto` (monthly if window
  covers a full month, else weekly if full week, else daily).

**Non-goals (deferred).**

- `ox journal facts list|show`, `extract`, `sweep`, `summarize` — owned
  by a separate plan.
- Rewriting or pruning any legacy file. No filesystem mutation from
  read commands, period.
- Migration of `distill-index.json`. Reader treats it as a read-only
  performance hint.
- New prompt shapes, new LLM calls, new network I/O.
- Changing how the team timezone is removed elsewhere in the codebase —
  that is the parallel plan's job.

---

## 2. Reader library design

The reader lives at `internal/distill/history/read`. Every read command in
`cmd/ox/distill_history_*.go` is a thin translator: parse flags → build a
`ReadQuery` → call one of three entry points → marshal the result to
the envelope.

### 2.1 Responsibilities (prose)

- **Inputs:** a `ReadQuery` struct carrying the resolved time window
  (both ends UTC RFC3339), the requested layer (`daily|weekly|monthly|
  auto`), team resolution (one team path, or a slice of team paths for
  `--all-teams`), and an optional ID list for `show`.
- **Outputs:**
  - `ListEntries(ctx, q) ([]Entry, ListMeta, error)` — metadata only,
    no body. Chronological order by `date` then `created_at`.
  - `LoadEntries(ctx, q, ids []string) ([]Entry, error)` — materializes
    bodies for a specific set of IDs (supports prefix match and the
    bare-date special case). Per-ID errors are attached to the matching
    `Entry.Status` / `Entry.Error` fields rather than aborting the
    whole call.
  - `Since(ctx, q) ([]Entry, []string /*bodies*/, ListMeta, error)` —
    the composite path: calls `ListEntries`, materializes every entry's
    body, returns them in parallel slices. This is literally
    `ListEntries` + per-entry `LoadEntries` glued together, but lives
    in the reader so the three commands share one implementation and
    one set of tests.
- **Window semantics.**
  - `since`/`until` in `ReadQuery` are already UTC RFC3339 instants.
    The CLI layer is responsible for resolving user input into UTC
    before constructing the query. The reader itself never parses a
    user time string and never consults a `time.Location`.
    - Relative forms (`--since=24h`): CLI computes
      `time.Now().UTC().Add(-24h)`.
    - Absolute forms (`--since=2026-04-10T09:00
      --until=2026-04-10T15:00 --tz=America/Los_Angeles`): CLI parses
      each bound against the supplied `--tz`, then converts to UTC.
    - Offset-bearing timestamps (`2026-04-10T09:00-07:00`) are
      already absolute; passing `--tz` alongside one is a
      `usage_error` ("conflicting timezone specification").
    - Naked timestamps without `--tz` are interpreted as UTC.
  - **Atomic unit is the day.** Summary files are identified by a
    `YYYY-MM-DD` filename prefix — the UTC day they summarize. Their
    prose body is LLM-generated narrative that does NOT preserve
    per-fact timestamps, so the reader cannot slice a summary file
    sub-day. A summary file is either in window or not.
  - **Inclusion rule (floor/ceiling rounding to UTC day boundaries).**
    An entry is "in window" iff the UTC day identified by its `date`
    field overlaps `[since, until]`. Equivalently: the **effective
    window** is `[day-floor(since), day-ceil(until))`, interpreted as
    a half-open range of full UTC days, and any daily entry whose
    filename date prefix falls in `[day-floor(since),
    day-floor(until)]` (inclusive on both ends) is returned. Weekly
    and monthly entries use the spec's first-of-period `Entry.date`
    and check interval overlap against the full week/month span.
  - **The effective window is what agents see.** `ListMeta` carries
    `EffectiveSince` and `EffectiveUntil` (the rounded boundaries,
    not the CLI's raw input). The command layer surfaces these as
    the envelope's `window.since` / `window.until`. Example:
    `ox distill history since 6h` at 2026-04-12T15:00Z produces
    `window.since=2026-04-11T00:00:00Z`,
    `window.until=2026-04-13T00:00:00Z` — the half-open UTC-day
    interval containing the requested 6-hour slice. Callers detect
    rounding by comparing the reported window to what they asked for.
  - **Upper bound on content.** For a requested window of length
    `W` starting at an arbitrary UTC instant, the number of UTC days
    touched is at most `ceil(W / 24h) + 1`. Concretely,
    `--since=1h` or `--since=24h` can return up to ~48 h of facts
    (24 h from each of 2 days) when the window straddles a midnight.
    Agents treat the declared duration as a floor, not a ceiling,
    and the envelope's `window` object as the source of truth on
    what was actually returned.
  - **Per-day content is a union, not a single file.** A single UTC
    day D may have zero, one, or many `D-*.md` summary files in
    `memory/daily/`. Distill may run multiple times per day, and
    late-arriving facts (new sessions committed out-of-order, `--all`
    backfills, cross-node merges) produce additional `D-*.md` files
    for the same day after the fact — each one carrying only the
    facts that were NEW at that run (the watermark filter in
    `distill.go:1057-1074` prevents redistillation). When D is in
    window, ALL of `D-*.md` is returned, ordered by `CreatedAt`
    (file `mtime`, ascending). No single file is the canonical
    summary for D; the day's "content" is the union of all files
    whose prefix is D. Callers must not assume a day's summary is
    complete at any point — it can grow.
  - **Event day, not write time.** The day prefix reflects the
    **event day** (when the summarized facts actually occurred),
    derived from fact `RecordedAt` values — which in turn come from
    source timestamps (session `_meta.started_at`, PR `MergedAt` or
    `UpdatedAt`, discussion `CreatedAt`). The UUID7 in the filename
    encodes the write instant (when distill ran) and is NOT used by
    the reader for window filtering. Consequence: a summary file
    written today about a session that occurred last month is dated
    to last month and falls OUTSIDE a `--since=24h` query, which
    matches the primary `factory/distill/summary.ts` use case ("what
    happened in the team in the last day?"). Callers who want
    "what landed in my journal recently" regardless of event day
    would need a different query primitive — out of scope for this
    plan.
- **Entry materialization.**
  - `ID` = filename stem without `.md`.
  - `Layer` derived from the parent directory name
    (`memory/daily|weekly|monthly`).
  - `Date` resolution order: filename prefix (always, per §5.b) →
    weekly/monthly regex → empty string (signals a corrupt file;
    entry is surfaced in `ListMeta.warnings` rather than hidden).
  - `Path` is stored **relative to the team-context root** (the same
    form the spec uses in all envelope samples). The reader's exported
    shape never contains an absolute path; each call that needs one
    joins it with the team root internally.
  - `CreatedAt` is the file `mtime` (not the UUID7 instant — the spec
    explicitly decides this in §4.3).
  - `SourceFiles`, `FactCount`, `CitationCount` come from parsing the
    YAML frontmatter `sources:` block (reusing logic equivalent to
    `parseDailySources` at `cmd/ox/distill.go:1516`) and the
    `## Sources` section (via `parseSourcesSection` in
    `cmd/ox/distill_citations.go:575`). Both functions live in the
    `main` package today; see Unit 2 for the carve-out.
  - `BodyMD` is produced only for `show`/`since`: read file, strip
    frontmatter (everything between the leading `---\n` and the
    matching `\n---\n`, inclusive of both delimiters and the trailing
    newline), return the rest as-is.
- **Legacy-file handling (silent).**
  - The reader **trusts the filename prefix** as the UTC day for the
    entry. It does NOT compare the prefix to the UUID7-embedded
    timestamp and does NOT emit a warning when they disagree. There
    is no `legacyWarnOnce`, no `LegacyWarningEmitted` field on
    `ListMeta`, and no `legacy.go` file in the reader package.
  - UUID7 parsing is not in the read path at all. The reader reads
    the filename prefix for the date and uses file `mtime` for
    `CreatedAt`. The UUID7 is opaque — it participates in ID
    matching only as an ordinary string.
- **Error surfacing.** The reader distinguishes three error classes:
  1. Whole-call failures (`team_not_found`, `team_ambiguous`, I/O
     errors reading the team-context directory itself) — returned as a
     Go error to the command; the command maps them to envelope error
     codes per §3.
  2. Per-entry failures during `LoadEntries` — attached to the entry
     struct (`Status`, `Error` fields) so `show` can emit partial
     results per §3.5.
  3. Per-file corruption during `ListEntries` — appended to
     `ListMeta.Warnings` as a structured record, never aborts the
     call. The CLI layer decides whether to promote these to
     `agent_hint` text.

### 2.2 Exported interface (signatures only)

```go
package read // import "github.com/sageox/ox/internal/distill/history/read"

type Layer string // "daily" | "weekly" | "monthly" | "auto"

type ReadQuery struct {
    Since     time.Time // UTC, inclusive lower bound
    Until     time.Time // UTC, inclusive upper bound
    Layer     Layer
    Teams     []TeamRef // 1 = scoped; N = --all-teams merge
    Limit     int       // 0 = no limit
    WantBody  bool      // true for show/since
    // IDs is only consulted by LoadEntries.
}

type TeamRef struct {
    Slug string // envelope field `team`
    Path string // absolute team-context dir
}

type Entry struct {
    ID            string
    Layer         Layer
    Date          string // YYYY-MM-DD | YYYY-Www | YYYY-MM
    Team          string
    RelPath       string // relative to TeamRef.Path
    FactCount     int
    CitationCount int
    SourceFiles   []string
    Citations     []Citation // only when WantBody
    BodyMD        string     // only when WantBody
    CreatedAt     time.Time  // mtime, UTC
    Status        string     // "ok" | "not_found" | "ambiguous" | "read_error"
    Error         *EntryError
}

type ListMeta struct {
    LayerResolved  Layer
    EffectiveSince time.Time // UTC day-floor of ReadQuery.Since
    EffectiveUntil time.Time // UTC day-ceiling of ReadQuery.Until, exclusive
    Truncated      bool
    Warnings       []Warning
}

func ListEntries(ctx context.Context, q ReadQuery) ([]Entry, ListMeta, error)
func LoadEntries(ctx context.Context, q ReadQuery, ids []string) ([]Entry, error)
func Since(ctx context.Context, q ReadQuery) ([]Entry, []string, ListMeta, error)
```

(One code block, per the plan's constraints. No bodies. The CLI never
constructs `Entry` values — that's the reader's job — so the shape can
evolve without churning command code.)

### 2.3 Derived helpers (internal to `internal/distill/history/read`)

- `resolveLayer(q)` — given a `ReadQuery`, returns the concrete layer
  when `q.Layer == "auto"`. Implements the "full month / full week"
  rule from §3.4 and §3.6.
- `stripFrontmatter(md)` — extracts the body per §3.5.
- `parseEntryFile(absPath, layer, teamSlug)` — opens an `.md` file,
  parses frontmatter + citations, returns an `Entry` with `BodyMD`
  optionally filled.
- `matchID(ids, index)` — resolves prefix matches, bare-date matches
  (§4.4 special case 1), and the 6-char uuid7-short form (§4.4
  special case 2). Produces `Status=ambiguous` with a list of
  candidates when more than one entry matches.

All of the above are package-private. The three CLI commands only see
the three exported entry points above.

---

## 3. Unit breakdown

Units are ordered so each one depends only on previously shipped,
functionally-verified work. Every unit ends on a green `make test` and
a binary that compiles and runs. No unit leaves the repo in a broken
state.

### Unit 1 — `internal/distill/history/read` skeleton + `ListEntries` on daily

**What changes.** Create `internal/distill/history/read/` with:

- `read.go` — the three exported functions, stubbed so the package
  compiles. `LoadEntries` and `Since` return `errors.ErrUnsupported` /
  a sentinel "not yet implemented" for this unit.
- `types.go` — `ReadQuery`, `TeamRef`, `Entry`, `ListMeta`, `Warning`,
  `EntryError`, `Citation`, the `Layer` constants.
- `list.go` — real `ListEntries` implementation for `Layer=="daily"`
  only. Weekly and monthly return `(nil, zero, nil)` with a warning.
- `paths.go` — tiny wrapper around `memory/daily/` path join; no
  exported symbols.

Note: no `legacy.go`. The reader does not detect or report
prefix/UUID7 mismatches.

Plus: one file under `cmd/ox/` that stubs `distillHistoryCmd` and the three
child commands as hidden (`Hidden: true`) so the binary stays
compile-clean. The child commands in this unit are skeletons that
print a "not yet implemented" error — they are NOT wired to the reader
yet; that is Units 3–5.

**Why grouped this way.** `ListEntries(daily)` is the smallest shippable
vertical slice: it proves the package builds, proves file discovery
works against real `memory/daily/` layouts, and gives the later units
a fixture-heavy target. Weekly/monthly add pure regex variation on top
of a working daily path.

**Unit test strategy.**

- Table-driven tests for `resolveLayer` covering the full
  month/week/day matrix.
- Table-driven tests for `parseEntryFile` with hand-crafted daily
  fixtures: with frontmatter, without frontmatter, with citation list,
  with zero citations, with malformed YAML, with no trailing newline.
- `ListEntries` over `testdata/team1/memory/daily/` (committed
  fixtures): ordering, `Truncated` behavior at `Limit=1`, empty-dir
  clean return, non-existent-dir clean return (matches
  `discoverMemoryFiles` behavior at `cmd/ox/agent_prime.go:1677`).
- `matchID`: exercised only via a helper; full matrix lives in Unit 4.

**Integration test strategy.**

- `internal/distill/history/read/read_integration_test.go`: spin up a
  temp-dir team context with `config.CreateInitializedProject`, write
  a small set of daily `.md` files with real frontmatter, resolve a
  `ReadQuery` against it, assert the returned entries match by ID.
- Use real `config.FindRepoTeamContext` to resolve the team root so
  the reader is exercised against the canonical team-context code
  path, not a test-only shortcut.

**Compile-safety rationale.**

- No other package imports `internal/distill/history/read` yet.
- The `distillHistoryCmd` skeleton under `cmd/ox/` is registered but hidden
  and all subcommands return a stub error — no existing command is
  touched.
- No changes to `cmd/ox/distill*.go`. Parallel tz-removal work does
  not touch `internal/distill/history/read` at all.

**Additive-only constraint, with a narrow exit-code exception.**
Commits through Unit 5 stay additive: new files under
`internal/distill/history/read/`, `internal/distill/history/memoryio/`, and
`cmd/ox/distill_history_*.go`, plus test files for each. Pre-existing files
— `cmd/ox/distill*.go`, `cmd/ox/main.go`, `cmd/ox/distill_history.go`'s
`distillHistoryCmd` parent, etc. — are not modified.

The single bounded exception is exit-code propagation for the
command layer's typed errors: Unit 3 adds one `errors.As` check for
`*distillHistoryExitError` to `cmd/ox/main.go`'s `executeWithFrictionRecovery`
so that usage errors from `ox distill history list|show|since` can bypass
friction recovery and surface exit code 2 per §3. The check is:

- Scoped to a single function (`executeWithFrictionRecovery`).
- Behaviorally inert for non-journal commands — the `errors.As`
  branch misses and existing friction recovery runs unchanged.
- Verified by `TestJournalList_InProc_UsageErrorExitCode`.

No other modifications to `cmd/ox/main.go`, `cmd/ox/distill*.go`, or
any pre-existing `cmd/ox/*.go` files are permitted through Unit 5.
Units 4 and 5 must not interpret this carve-out as license to touch
`main.go` for other reasons; any such change remains a Blocker.

**Dependencies within this plan.** None.

---

### Unit 2 — Factor frontmatter + citation parsing into a shared helper

**What changes.** Move the small pure functions the reader needs out of
the `cmd/ox/` main package and into a thin shared helper:

- `internal/distill/history/memoryio/` (new package) with:
  - `ParseFrontmatterSources(md string) []string` — carved from
    `parseDailySources` at `cmd/ox/distill.go:1516`.
  - `StripFrontmatter(md string) string` — new, matches §3.5.
  - `ParseSourcesSection(md string) []Citation` — adapter over the
    existing `parseSourcesSection` at
    `cmd/ox/distill_citations.go:575`, which stays in `cmd/ox/` but
    exposes just enough via `memoryio` to feed the reader.

For the citation parser specifically: rather than relocate the full
`distill_citations.go`, introduce a delegate. `memoryio.Citation` is a
tiny struct (fields per §4.3) that the reader populates from the
existing parser's output. The delegation lives in a new file
`cmd/ox/distill_history_citations_bridge.go` that converts `citation` →
`memoryio.Citation`. This keeps the existing `distill_citations.go`
unchanged and avoids any overlap with tz-removal.

**Why grouped this way.** The reader needs frontmatter parsing
immediately; relocating the logic into a package the reader can import
avoids cycling through `cmd/ox/`. Citation parsing is heavier and has
its own regression tests (`distill_citations_test.go`), so we bridge
rather than move. This is the minimum extraction that lets later units
read entry bodies without duplicating parsing code.

**Unit test strategy.**

- Move the existing `parseDailySources` test coverage from
  `cmd/ox/distill_gh476_regression_test.go` into the new package as a
  table test (keep the old one in place as a regression guard until
  the original caller is deleted in a future plan).
- New table-driven test for `StripFrontmatter`: no frontmatter, with
  frontmatter, with frontmatter and an internal `---` horizontal rule
  (must not trip), empty file.
- Bridge shim has its own test: round-trip a synthetic daily file
  through `parseSourcesSection` → `memoryio.Citation` and assert no
  field loss.

**Integration test strategy.**

- Extend `read_integration_test.go` (Unit 1) with a fixture that
  contains both a `sources:` frontmatter and a `## Sources` section,
  then assert that both `Entry.SourceFiles` (from frontmatter) and
  `Entry.CitationCount` (from the section) are populated correctly.

**Compile-safety rationale.**

- `internal/distill/history/memoryio` is a new package with no dependents
  outside the reader + the bridge shim.
- `distill_citations.go` is untouched — the tz-removal plan will not
  collide here because `distill_citations_test.go` still runs against
  the original symbol.
- The bridge file under `cmd/ox/` is new; no existing file is
  modified.

**Dependencies within this plan.** Unit 1 (needs `Entry`, `Citation`
types to populate).

---

### Unit 3 — `ListEntries` weekly + monthly, `--layer=auto`, and `ox distill history list`

**What changes.**

- Extend `internal/distill/history/read/list.go` to handle `Layer=="weekly"`
  and `Layer=="monthly"`, using the existing `weeklyRe` / `monthlyRe`
  patterns (re-declared locally in the reader package — do NOT import
  from `cmd/ox/` to keep the reader standalone). Add a regression
  test that fixes the reader's copy to the canonical regex strings so
  if either pattern drifts in `cmd/ox/distill.go`, the test catches
  the mismatch.
- Implement `resolveLayer` for `auto`.
- Wire `cmd/ox/distill_history_list.go` to call `read.ListEntries`.
  Implement flag parsing (`--since`, `--until`, `--tz`, `--layer`,
  `--team`, `--all-teams`, `--limit`, `--format`).
- **Time-input resolution** lives in a new helper
  `cmd/ox/distill_history_time.go` shared by all three commands:
  - Relative `--since=<dur>` → `time.Now().UTC().Add(-dur)`.
  - Absolute `--since=<ts>` / `--until=<ts>`:
    - If the timestamp carries an offset, use it directly.
    - Otherwise, if `--tz=<zone>` is supplied, parse against that
      zone (via `time.LoadLocation`), then convert to UTC.
    - Otherwise, interpret as UTC.
  - Conflict: `--tz` + an offset-bearing timestamp → usage error
    (`usage_error`, "conflicting timezone specification: drop --tz
    or drop the offset").
  - Invalid `--tz` zone (`time.LoadLocation` error) → usage error.
  - `--since` without `--until` defaults `Until = time.Now().UTC()`.
  - `--until` without `--since` is a usage error (no implicit
    "beginning of time" window).
- **Window rounding** (also in `journal_time.go`):
  - `EffectiveSince = since.UTC().Truncate(24 * time.Hour)` —
    day-floor.
  - `EffectiveUntil = until.UTC().Truncate(24 * time.Hour).Add(24 *
    time.Hour)` — day-ceiling (exclusive upper bound).
  - Both are populated on `ListMeta` by the reader and surfaced to
    the JSON envelope by the command layer as `window.since` /
    `window.until`. The raw `ReadQuery.Since` / `Until` are
    NEVER reported to the caller.
- Implement the envelope types in a new file
  `cmd/ox/distill_history_envelope.go` shared by all three commands. These
  are command-local view structs — NOT exported from the reader.
- Implement the `--format=text` renderer for `list`.

**Why grouped this way.** `list` is the foundation; `show` and `since`
are both built on it (§3.6 literally says `since = list + show`). All
of `list`'s hard work — layer resolution, window handling, time-input
resolution (`--tz`, relative durations, day rounding), team
resolution — only needs to be solved once here.

**Unit test strategy.**

- Extend Unit 1's fixture directory with weekly and monthly files.
- Table tests for `resolveLayer`'s auto resolution.
- Table tests for `cmd/ox/distill_history_time.go` covering: relative
  durations, naked UTC timestamps, offset-bearing timestamps,
  `--tz=America/Los_Angeles` combined with naked timestamps,
  `--tz` + offset-bearing timestamp (usage error), invalid `--tz`
  zone (usage error), `--until` without `--since` (usage error),
  `--since` without `--until` (defaults to `now`), and
  day-floor/ceiling rounding on each result.
- Table tests for the `cmd/ox/distill_history_list.go` flag parser using a
  small `argv → ReadQuery` helper — no subprocess required.
- A dedicated test for the 48-h upper-bound case: a `--since=1h`
  query run at a fixed UTC instant 30 seconds after midnight
  asserts that `EffectiveSince` is yesterday-midnight and
  `EffectiveUntil` is tomorrow-midnight.
- Golden-file test for the `--format=text` output against a fixture
  list of entries, so the human renderer is pinned.

**Integration test strategy.**

- A new `cmd/ox/distill_history_list_test.go` that uses
  `config.CreateInitializedProject`, writes daily + weekly + monthly
  fixtures, runs `distillHistoryListCmd.Execute()` in-process with JSON
  output, decodes the envelope, asserts fields.
- A separate test runs the same scenario with `--all-teams` against
  two team-context fixtures and asserts the merged ordering.
- A test with a deliberately legacy-shaped file (filename prefix
  `2025-06-10` + embedded UUID7 whose day is `2025-06-11`) asserts
  the file is returned with `date=2025-06-10` (trust-prefix rule),
  with NO stderr warning and NO `agent_hint`.

**Compile-safety rationale.**

- Reader package is self-contained; the command file only adds new
  code under `cmd/ox/`. `distillCmd` and every other existing command
  are untouched.
- If the tz-removal plan lands first, our reader is unaffected
  because it never calls `config.ResolveTimezone` — the reader is
  built against UTC from day 1.

**Dependencies within this plan.** Units 1, 2.

---

### Unit 4 — `LoadEntries`, ID resolution, and `ox distill history show`

**What changes.**

- Implement `read.LoadEntries` in `internal/distill/history/read/show.go`.
- Implement `matchID` with the full grammar: full stem, UUID7 short
  form, and bare date (which **always** expands to every entry with
  that date prefix — there is no "latest" shortcut). `matchID`
  consults an index produced by a lightweight
  `ListEntries(..., WantBody:false, Limit:0)` call that is scoped to
  the correct layer and team. A bare-date match is not an ambiguous
  case; it is a multi-match that returns every file as a successful
  result, because late-arriving facts and multiple distill runs per
  day mean no single snapshot is canonical and the union of all
  `D-*.md` files is the day's authoritative content.
- Implement `stripFrontmatter` and body materialization (reader-side).
- Wire `cmd/ox/distill_history_show.go`:
  - positional IDs, `--team`, `--format` (`json`, `text`, `content`).
    **No `--latest` flag** — the "newest snapshot" concept is
    explicitly rejected because facts arrive out of order and a
    single day can grow more files after the fact.
  - `--format=content` emits the concatenated body form per §3.5,
    with the `\n---\n` separator and `<!-- entry: <id> -->` markers.
  - Per-ID partial success: the envelope always returns `success:
    true` with a per-entry `status` field, unless **all** IDs failed,
    in which case `success: false` + the first error.

**Why grouped this way.** `show` is the first time the reader has to
load file bodies and handle prefix ambiguity. Keeping that work in
one unit lets the tests build on a single fixture set and prevents
split-brain between Unit 3's metadata path and this unit's body path.

**Unit test strategy.**

- Table tests for `matchID`:
  - full stem exact match
  - prefix match (6+ chars, unambiguous → ok)
  - prefix match (ambiguous → `ambiguous` status + candidates list)
  - bare date (multiple entries on the day → all returned, ordered
    by `created_at` ascending)
  - bare date (zero entries on the day → `not_found`)
  - UUID7-only short form (6-char hex prefix) matches the stem
  - unknown prefix → `not_found`
  - `--latest` is NOT a valid flag (Cobra rejects it at parse time)
- Table tests for `stripFrontmatter`.
- Golden-file test for `--format=content` multi-entry concatenation
  (separator, markers, ordering).

**Integration test strategy.**

- `cmd/ox/distill_history_show_test.go` runs the command end-to-end against
  a populated fixture, asserts JSON envelope, text output, and
  content output for all three format modes.
- Partial-success scenario: pass three IDs where one is `not_found`,
  assert `success: true`, per-ID `status` fields are correct, and
  the exit code is 0.
- All-failures scenario: pass two unknown IDs, assert `success:
  false`, exit code 1.

**Compile-safety rationale.**

- Reader package gains one exported function; no other package touches.
- Command file is new; existing files are not modified.

**Dependencies within this plan.** Units 1, 2, 3.

---

### Unit 5 — `Since` composite + `ox distill history since`

**What changes.**

- Implement `read.Since` as a thin composition: `ListEntries` (bodies
  off) → `LoadEntries` with the collected IDs (bodies on) → return
  `[]Entry`, `[]string bodies`, `ListMeta`.
- Wire `cmd/ox/distill_history_since.go`:
  - positional `<dur>`, `--layer`, `--team`, `--all-teams`,
    `--format`, `--limit`.
  - Default `--format=content` (see Open questions — confirmed as the
    primary use case). JSON and text formats are also supported for
    symmetry with `list`.
  - JSON envelope shape per §3.6: `entries` (from `list`) + `bodies`
    (parallel slice).
  - Content envelope: bodies concatenated with `\n---\n` separator
    and per-entry marker (`<!-- entry: <id> | layer: <l> | date:
    <d> -->`).
  - **Time resolution** reuses `cmd/ox/distill_history_time.go` from Unit 3:
    the positional `<dur>` becomes
    `Since=Now().UTC().Add(-dur)`, `Until=Now().UTC()`, then the
    same day-floor/day-ceiling rounding applies and the effective
    window is reported in every output format. `since` does NOT
    accept `--tz` — that flag is only meaningful with absolute
    timestamps.

**Why grouped this way.** `Since` is pure composition. Making it its
own unit keeps the commit scope small and makes the tests prove the
*composition* rather than re-testing List and Load paths.

**Unit test strategy.**

- Test that `read.Since` with an empty window returns empty parallel
  slices and no error.
- Test that `read.Since` preserves the Unit 3 ordering contract and
  that `bodies[i]` corresponds to `entries[i]`.
- Table test for the CLI `--format=content` marker rendering.

**Integration test strategy.**

- `cmd/ox/distill_history_since_test.go`:
  - happy path: 3 daily entries in the last 6 h, assert JSON
    envelope, text mode, and content mode.
  - `--all-teams` with two team fixtures: assert the merged output
    orders entries correctly and the content-mode separator still
    works.
  - Regression: pin the content-mode output as a golden file that
    `factory/distill/summary.ts` can parse. The parallel
    TypeScript work is out of scope, but the golden file is its
    contract.

**Compile-safety rationale.**

- Reader package gains one more exported function. Nothing else
  changes.
- Command file is new.

**Dependencies within this plan.** Units 1, 2, 3, 4.

---

### Unit 6 — Reference docs, feature flag reveal, and polish

**What changes.**

- Flip `distillHistoryCmd.Hidden = false` and the three subcommands from
  hidden to visible.
- Regenerate `docs/reference/` via `go build -o ox-tmp ./cmd/ox &&
  ./ox-tmp docs --output docs/reference && rm ox-tmp`. Commit the
  generated `.mdx` files.
- Add a short section to `ox doctor` that confirms the reader can
  list at least one entry when a team context is present (exercises
  the integration end-to-end on user machines). Out of scope if the
  doctor plan objects — otherwise cheap.
- Hide or rename any stale `distill`-named comments that mention
  "where the journal lives" so agents discover the new surface.

**Why grouped this way.** Docs and discoverability happen after the
surface is stable and tested, not during. Making the commands visible
is a one-line flip but must be the last step.

**Unit test strategy.**

- Docs: golden-compare the generated `ox_journal_list.mdx` against
  the committed version (existing reference-docs test pattern).
- Flag-visibility test: `distillHistoryCmd.Hidden == false`.

**Integration test strategy.**

- Smoke test: `ox distill history list --format=json` on a real seeded
  fixture, assert JSON decodes cleanly and `type == "distill_history_list"`.

**Compile-safety rationale.**

- Only touches Cobra flag state, generated docs, and `ox doctor`
  guidance. No new symbols in the reader.

**Dependencies within this plan.** Units 1–5.

---

## 4. Parallelism analysis

| Shared symbol / file | This plan's touch | tz-removal plan's touch | Mitigation |
|---|---|---|---|
| `cmd/ox/distill.go:1516` `parseDailySources` | Carved into `internal/distill/history/memoryio.ParseFrontmatterSources` (Unit 2). Original is kept, wrapper-imported. | Not touched by tz plan. | No conflict. Keeping the original in place lets tz-removal rebase cleanly; Unit 2's carve-out is additive. |
| `cmd/ox/distill_citations.go:575` `parseSourcesSection` | NOT moved. Bridge file `cmd/ox/distill_history_citations_bridge.go` depends on it (Unit 2). | Not touched by tz plan. | No conflict. |
| `cmd/ox/distill.go:40` `dailyDateRe` | Reader re-declares its own copy with a pin test (Unit 3). | Not touched by tz plan. | No conflict. Reader copy exists precisely so the reader is not in the tz plan's blast radius. |
| `cmd/ox/distill.go:63` `inferDailyHighWater` | Not used by the reader at all. | tz plan deletes its `In(tz)` / adjusts. | No conflict. |
| `cmd/ox/distill_discussions.go:456` `parseFactDate` | Not used by read commands (facts are out of scope here). | tz plan drops its `tz` variadic. | No conflict: read commands do not touch `parseFactDate`. |
| `internal/config/timezone.go` | Not imported by reader. | Deleted. | No conflict. Verified: reader has zero `time.Location` references anywhere. |
| `internal/config.FindRepoTeamContext` | Used by reader's CLI wrappers. | tz plan touches `ResolveTimezone` which uses `FindRepoTeamContext`; the function itself is untouched. | No conflict. Worst case is a merge against Unit 3's test that imports it. |
| `internal/config.ProjectConfig.Timezone` field | Not read. | Deleted. | No conflict. |
| `cmd/ox/config_settings.go:176-200` timezone registry | Not touched. | Deleted. | No conflict. |
| `cmd/ox/distill.go` (top-level imports, `runDistill`) | Not touched. | Heavy touch. | Unit ordering: land Unit 1 first so the reader package exists before any `distill.go` rewrite; later units only add new files. |
| `docs/reference/` | Regenerated in Unit 6. | Not touched by tz plan. | No conflict. |
| `.sageox/cache/distill-index.json` | Read-only, fallback scan if missing (per §6.2). | Not touched. | No conflict. |

**Merge order recommendation.** Unit 1 can land before, during, or
after any tz-removal commit — it shares zero symbols. Units 2–5 should
land after tz-removal has stabilized in `distill.go` only if the
bridge file's import of `parseSourcesSection` would collide with a
tz-related move; since the tz plan is a local-only edit of time
handling inside `distill.go`, this is unlikely. If a merge conflict
does appear, it will be on pure import ordering in `distill.go` and
will not affect semantics.

---

## 5. Risks & rollback

| Unit | Risk | Rollback |
|---|---|---|
| 1 | Fixture paths drift from production (`memory/daily/...`). | Reader package is unused by anything else until Unit 3; safe to revert. |
| 2 | `ParseFrontmatterSources` drifts from `parseDailySources`. | Keep both in place until a future plan removes the original. Pin-test in the new package guards against drift. Revert by dropping the new package. |
| 3 | `--layer=auto` resolution differs from user expectations on edge cases (partial week/month). | `--layer` takes an explicit override; reverting the auto heuristic is a one-line change in `resolveLayer`. |
| 3 | Silent legacy policy hides a genuinely broken file from operators. | If reversed, the rollback is additive (add a new `legacy.go` warner behind a feature flag) and does not require touching the reader's existing code paths. |
| 4 | Prefix resolution picks the wrong entry for an ambiguous UUID7 short form. | `matchID` returns `Status=ambiguous` with candidates — the caller can always disambiguate via full stem. Rollback is limited to Unit 4. |
| 4 | `stripFrontmatter` corrupts a body that legitimately begins with `---`. | `StripFrontmatter` has a test specifically for this. If a user file is found that breaks the parser, fix `StripFrontmatter` — the reader is the only caller. |
| 5 | `Since` materializes a very large window and OOMs the process. | `Since` has no default limit by design (primary caller needs the full window); mitigation is the caller passing an explicit `--limit` when querying multi-month windows. Rollback is additive. |
| 6 | Docs regeneration picks up an unrelated command change. | Generated `.mdx` files are committed; `git diff docs/reference` shows what changed, easy to trim or revert. |

The overall rollback story for the entire plan is clean because every
unit is additive. The only files modified in `cmd/ox/` that did not
previously exist are new `journal_*.go` files; no existing test or
command file is edited until Unit 6's doc regen.

---

> Guided by SageOx
