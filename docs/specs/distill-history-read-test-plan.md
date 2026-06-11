# `ox distill history` Read Surface — E2E Test Plan

Status: plan
Owner: test-architect
Scope: end-to-end test plan for the **journal read surface** feature
landing in its own PR. The team timezone removal is tested in a
sibling plan (`journal-timezone-test-plan.md`) so each PR can land
independently.

The feature under test: new read-only commands `ox distill history list`,
`ox distill history show`, `ox distill history since`. See `team-memory-journal.md`
§3.4–§3.6 and §4. These commands read `memory/daily/*.md` under a
team context directory. No writes, no IPC, no network. The
facts-layer commands (`ox journal facts list`, `ox journal facts
show`) are out of scope for this PR and are owned by a separate
plan.

"End-to-end" here means: build the real `ox` binary from source,
invoke it as a subprocess with a staged tempdir + env, assert on
stdout / stderr / exit code. No in-process command-object tests.
Unit tests (reader library internals, frontmatter parsing, ID
resolution) are owned by the implementation plan
(`distill-history-read-plan.md`); they are explicitly **out of scope** for
this document.

---

## 1. Test infrastructure

### 1.1 Binary build

All cases compile the ox binary once per test via
`testguard.BuildOxBinary(t, projectRoot)`
(`internal/testguard/testguard.go:217`), which `go build -o <tmp>/ox
./cmd/ox` with `CGO_ENABLED=0`. Every test that needs the binary calls
this directly — we do **not** introduce a `TestMain` or a package-level
shared binary. Rationale: `t.TempDir()` collects the binary
automatically, tests can run with `-run` selection without a
shared-state gotcha, and the 1–2 s build cost is bounded by Go's own
build cache.

This PR's test file reuses the existing `slow` gate, matching
`cmd/ox/incremental_e2e_test.go`:

```
//go:build slow
```

Runs under `make test-slow` with no new tag required. Each PR lives
in its own worktree and runs its own test file, so there is no
in-tree conflict with the sibling tz-removal plan.

### 1.2 Staged repo + team context

Each test creates:

- **Workspace root** `W` (git-init + one commit + `.sageox/config.json`)
  via the existing `setupE2EWorkspace` pattern at
  `cmd/ox/incremental_e2e_test.go:399`. The config carries
  `team_id=team_read_e2e` and `endpoint=https://test.sageox.ai`
  (testguard allows `test.sageox.ai` and `localhost` through the
  production-host guard; see `internal/testguard/testguard.go:43`).
- **Team context root** `T` — a bare directory tree that impersonates
  a cloned team context. We do **not** run `ox init` end to end;
  instead we write a minimal `config.local.toml` containing a
  `[[team_contexts]]` row pointing `team_id=team_read_e2e` at `T`, and
  we pre-create `T/memory/daily/`, `T/memory/weekly/`,
  `T/memory/monthly/`, `T/memory/.github-facts/`,
  `T/memory/.session-facts/`, `T/memory/.discussion-facts/`. This is
  the shape `FindRepoTeamContext` + `LocalConfig.TeamContexts` already
  reads from
  (`internal/config/local_config_findrepo_test.go:110`). The git
  metadata on `T` is initialized but never pushed — read-only commands
  never need a remote.
- **XDG reroute** — every subprocess inherits `XDG_DATA_HOME`,
  `XDG_CONFIG_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME`,
  `XDG_RUNTIME_DIR` pointing into `t.TempDir()` subfolders, plus
  `HOME=<tmp>` and `OX_XDG_ENABLE=1`. Identical to the pattern at
  `cmd/ox/doctor_e2e_helpers_test.go:111`. This is load-bearing: it
  guarantees nothing the test does can touch the developer's real
  `~/.sageox/` / `~/.local/share/sageox/`.
- **Auth** — pre-written fake `auth.json` under
  `XDG_CONFIG_HOME/sageox/auth.json` with token keyed by
  `test.sageox.ai`. Read commands never hit the network; we need auth
  only so `FindRepoTeamContext` / endpoint resolution does not refuse
  to run.
- **Daemon** — **never started**. `OX_NO_DAEMON=1` is injected by
  `testguard.MinimalEnv`. All journal-read commands are local-file
  readers; the spec requires no IPC. Any test that spawns a daemon is
  a defect.

### 1.3 Invocation pattern

Every test command uses this exact shape:

```go
// conceptual — actual helper lives in the test file, not in this plan
out, exit, _ := testguard.RunOx(t, oxBin, W, env,
    "journal", "list", "--since=24h", "--format=json")
```

The command is the real `ox` subprocess, `W` is the workspace cwd,
`env` carries the XDG reroute + test endpoint. Combined output is
captured for assertions. Exit code is checked against the matrix
below.

### 1.4 Fixture writes are done in Go, not via `ox`

We stage `memory/daily/*.md`, `memory/.*-facts/*.jsonl`, and any other
on-disk input by writing files directly from the test using
`os.WriteFile`, because:

1. The journal-read surface is read-only and **must** work against
   files that were written by some other process (another node, an
   older CLI version, a human editor). If we stage via `ox journal
   summarize` we would co-test the write path and the read path
   simultaneously — a broken writer would mask a broken reader.
2. Staging as files lets us exercise edge cases (legacy prefixes,
   malformed filenames, empty-marker files) that a real writer would
   refuse to produce.

Fact file bytes are written with the real `internal/facts` package so
we cannot drift from the on-disk schema; daily `.md` bodies are
hand-written literal strings with the frontmatter shape documented in
the spec §4.3.

### 1.5 Gitea digital twin applicability

**Two twins exist in this repo:**

1. **Docker-backed Gitea container** at `internal/daemon/twin_gitea_test.go`
   (build tag `slow`, requires Docker, port `13719`, lazily started
   via `sync.Once`). Exercises the daemon's git + LFS + credential
   flows against a real Gitea server. Touches only daemon code paths.
2. **Bare-git "twin" repos** at `cmd/ox/distill_github_twin_test.go`
   (`setupFactsTwinRepos`, `runTwinGit`). Creates a local bare repo
   plus two clones in `t.TempDir()` and exercises two "nodes" writing
   fact files concurrently. No Gitea, no Docker, no LFS.

**Verdict for this plan: neither twin is applicable.** The read
surface is strictly local-file enumeration under `T/memory/daily/`
and `T/memory/.*-facts/`. There is no clone, no push, no LFS, no
GitHub fact extraction, no remote network call. Plumbing Gitea would
only let us observe a round-trip that the read path does not depend
on.

If a future feature needs cross-node merge semantics under UTC
filenames, the pattern is already in place at
`cmd/ox/distill_github_twin_test.go:180`. Wiring it into this plan
today would add container startup cost, Docker as a CI dependency,
and zero behavioral coverage over what filesystem-staged fixtures
give us.

---

## 2. Test matrix

All cases are hermetic: fresh workspace, fresh XDG reroute, fresh
staged team context. Each row is one test function (or subtest) in
the journal-read-e2e test file. All fixtures are files staged
directly under `T/`.

| ID | Fixture | Command | Observable outcome | Failure mode caught |
|---|---|---|---|---|
| JR-01 | **MinimalDaily**: one valid daily file `T/memory/daily/2026-04-12-019c8a3f.md` with frontmatter `sources: [memory/.github-facts/2026-04-12-019c8a0e-pr-487.jsonl]`, body `# Daily Memory — 2026-04-12\n\n...`. File mtime is `now - 1h`. | `ox distill history list --since=24h --format=json` | Exit 0. `data.entries` contains exactly one row with `id=2026-04-12-019c8a3f`, `layer=daily`, `date=2026-04-12`, `path` set, `fact_count`, `citation_count`, `source_files`. `data.window.since` and `data.window.until` are UTC `Z`-suffixed. `data.window.layer_resolved=daily`. | Reader cannot locate `memory/daily/` under the staged team context at all. |
| JR-02 | **EmptyWindow**: One valid daily file whose **filename date prefix** is yesterday's UTC day (`<yesterday>-<uuid7>.md`); mtime is irrelevant. Command run at a fixed `now`. | `ox distill history list --since=1h --format=json` | Exit 0. `success: true`. `data.entries: []`. `data.truncated: false`. `data.window.since=today 00:00:00Z`, `data.window.until=tomorrow 00:00:00Z` (the day-rounded window only contains today, yesterday's file is out of range). No error, no warning. | Reader raises `error.code=not_found` on empty windows instead of returning structured empty, OR the day-rounding rule is wrong and yesterday's file incorrectly appears in a 1-hour window. |
| JR-03 | **MultiSnapshotSameDay**: Two daily files staged for the same date: `2026-04-12-019c8a3f.md` (mtime `now - 4h`) and `2026-04-12-019c87aa.md` (mtime `now - 2h`). Both in-window. | `ox distill history list --since=24h --format=json` | Two entries returned, ordered by `date` asc then `created_at` asc — so `019c8a3f` **before** `019c87aa` (older mtime first). Both share `date=2026-04-12`. | Reader dedupes by `date` and hides one of the two files — breaks spec §5.c "multiple summary files per day is a first-class state". Or ordering is wrong. |
| JR-04 | MultiSnapshotSameDay. | `ox distill history show 2026-04-12 --format=json` | Returns **both** entries in `data.entries`, ordered by `created_at` ascending. Exit 0. There is no `--latest` shortcut — a bare-date always expands to the day's full union of snapshots because facts can arrive out of order and a day's summary can grow after the fact. | Reader picks "newest snapshot" as the default; or a bare-date match returns only one entry when multiple exist. |
| JR-05 | MultiSnapshotSameDay. | `ox distill history show 2026-04-12 --latest --format=json` | Exit 2. JSON error envelope. `error.code="usage_error"`. Cobra rejects `--latest` at parse time because the flag does not exist on `show` — the "latest snapshot" concept was explicitly rejected. | `--latest` was accidentally implemented; or it's silently ignored (drops to JR-04 behavior) instead of erroring. |
| JR-06 | MultiSnapshotSameDay. | `ox distill history show 019c8a3f --format=json` (short UUID7 prefix per spec §4.4) | Exactly one entry matching that file. Exit 0. | Prefix matcher only accepts full IDs or only accepts date-prefixed IDs, rejecting the 6-char UUID7 short form. |
| JR-07 | MultiSnapshotSameDay plus a third file `2026-04-12-019c8a3f-dup.md` staged to force an ambiguous `019c8a3f` match. | `ox distill history show 019c8a3f --format=json` | Exit 1. `success: false`, `error.code="id_ambiguous"`, `retryable: false`. Stderr carries the full list of matches. | Ambiguous-prefix logic returns the first match instead of failing, hiding a bug where agents silently grab wrong content. |
| JR-08 | MinimalDaily. | `ox distill history show 2026-04-12-019c8a3f --format=content` | Stdout is **only** the markdown body (frontmatter stripped, no JSON envelope). First non-empty line is `# Daily Memory — 2026-04-12`. Stderr may carry progress/warnings but stdout is parseable as markdown. Exit 0. | Frontmatter not stripped, or the JSON envelope leaks into stdout, or stderr content lands on stdout and corrupts the downstream pipe to `claude`. This is the case `factory/distill/summary.ts` will depend on. |
| JR-09 | **ThreeDays**: Three staged daily files across three consecutive UTC days, each with distinct UUID7s; the oldest is 48 h old, middle is 24 h old, newest is 6 h old. | `ox distill history since 24h --format=content` | Stdout contains the middle and newest files' bodies in chronological order (oldest in-window first), separated by `\n---\n`, each preceded by `<!-- entry: <id> \| layer: daily \| date: <date> -->`. The 48 h file is **excluded** (out of window). Exit 0. | Ordering is reversed, or the window boundary is off by one day, or the entry marker is wrong shape (breaks spec §3.6). |
| JR-10 | ThreeDays, staged relative to the test's captured `now`. The subprocess uses the real clock; assertions compute expected values from the test's own `now`. | `ox distill history since 24h --format=json` | `data.entries` and `data.bodies` are parallel arrays of length 2 (the in-window middle and newest files). `data.window.since = day-floor(now - 24h)`, `data.window.until = day-ceil(now)`, both UTC `Z`. Note the effective window is usually 48 h wide (or wider) even though `--since=24h` was requested — the envelope reflects the day-rounded boundaries. Test tolerates ±few-seconds drift between the captured `now` and the subprocess's `time.Now()` because day-floor/day-ceil are stable across such drift unless `now` is within seconds of a UTC midnight (guarded by §4.3 item 2). | `bodies` array is not populated when `--format=json`; OR the envelope reports the raw `now - 24h` instead of the day-floored value; OR the window is reported as 24 h wide instead of the rounded N×24 h. |
| JR-11 | **MalformedFilename**: MinimalDaily plus a file `T/memory/daily/not-a-date.md`. | `ox distill history list --since=24h --format=json` | Exit 0. `data.entries` has exactly one row (the valid file). Stderr carries a warning naming the skipped file. No panic, no `error` envelope. | Reader crashes or fails the whole call on a single malformed filename, making the journal unusable if any stray file lands under `memory/daily/`. |
| JR-12 | **EmptyMarkerOnly**: No daily files. Staged fact file `T/memory/.github-facts/2026-04-12-019c8a0e-pr-487.jsonl` with a valid `_meta` header and **zero** body lines (the empty-marker case, spec §5.h item 1). | `ox distill history list --since=24h --format=json` | Exit 0. `data.entries: []`. The fact file is NOT surfaced as a daily entry — the daily reader only looks under `memory/daily/`. | The daily reader accidentally walks into `memory/.github-facts/` (or any sibling directory) and treats fact files as daily entries, polluting the envelope with non-daily content. |
| JR-13 | **MixedTzAndUtcLegacy**: One legacy daily (`2026-04-12-019c8a3f-legacy.md`, UUID7 encodes `2026-04-13T04:00:00Z`, prefix is `2026-04-12`) and one new-style daily (`2026-04-13-019c8b00.md`, prefix and UUID7 both UTC). Both in-window. | `ox distill history list --since=48h --format=json` | Both entries returned. Legacy has `date=2026-04-12`, new has `date=2026-04-13`. Ordering by `date` asc → legacy first, new second. Stderr silent (silent legacy policy). | Legacy file's `date` comes back as `2026-04-13` (reader re-derived from UUID7 instead of trusting the prefix), which breaks the `id == filename stem` contract and confuses agents that grep filenames. Or stderr carries a spurious warning despite the silent policy. |
| JR-14 | **MultiTeamList**: Two staged team contexts `T1` and `T2`, both registered in `config.local.toml`. `T1` has one daily for `2026-04-12`, `T2` has one daily for `2026-04-12` with a different UUID7. Project config's `active` team is `T1`. | `ox distill history list --since=24h --format=json` (default team) | Returns only `T1`'s entry. Each entry has `team="team_t1"` (always present, per spec §4.3). Exit 0. | Reader defaults to all-teams and leaks `T2`'s entry into the default call. Or `team` field is missing. |
| JR-15 | MultiTeamList. | `ox distill history list --since=24h --all-teams --format=json` | Returns both entries. Ordering is `date` asc, then `created_at` asc, then team slug asc as tiebreaker (spec §5.g). Each row carries its own `team` field. | Cross-team merge fails or ordering collapses to insertion order, breaking agent expectations when they iterate `data.entries`. |
| JR-16 | MultiTeamList. | `ox distill history since 24h --all-teams --format=content` | Stdout concatenates both bodies in chronological-then-team order. Each body preceded by the `<!-- entry: <id> \| layer: daily \| date: ... -->` marker. (Whether a per-team marker is also included is an open question §6; this test asserts only the `entry` marker that the spec commits to.) | Team-merge concatenation is missing the per-entry marker, or orders purely by team rather than date. |
| JR-17 | MinimalDaily. | `ox distill history show 2026-04-12-019c8a3f --format=json` | Response includes `body_md` (full markdown, frontmatter stripped), `citations` array, `source_files` array. `elapsed_ms` present. `success: true`. | `show` returns only metadata (forgetting `body_md`), or leaves frontmatter in `body_md`. |
| JR-18 | MinimalDaily plus an invalid `.md` file sharing a prefix (`2026-04-12-badfile.md`) that fails frontmatter parsing. | `ox distill history show 2026-04-12-019c8a3f --format=json` | Still returns the good entry. Stderr warns about the bad file. Exit 0. | One malformed neighbor breaks `show` for unrelated IDs. |
| JR-19 | Fresh workspace; no team context registered at all (`config.local.toml` empty, no fallback dir). | `ox distill history list --since=24h --format=json` | Exit 1, `success: false`, `error.code="team_not_found"`, `retryable: false`. | Reader panics on missing team context, or returns empty-success (which would hide the misconfiguration from agents). |
| JR-20 | **UsageError**: invalid `--since` value. | `ox distill history list --since=not-a-duration --format=json` | Exit 2 (usage error per spec §3). Stdout is the JSON error envelope. `error.code="usage_error"` or similar. | Usage errors land on exit code 1 (runtime error) and agents cannot distinguish "bad flag" from "real failure". |
| JR-21 | **AbsoluteTzRoundTrip**: Two daily files dated `2026-04-12` and `2026-04-13` (both valid, both in a clean UTC fixture). | `ox distill history list --since=2026-04-12T00:00 --until=2026-04-12T23:59 --tz=America/Los_Angeles --format=json` | Exit 0. Both files returned in `data.entries`. The LA-local `2026-04-12` 24-hour window translates to UTC `[2026-04-12T07:00:00Z, 2026-04-13T06:59:00Z]`; day-rounded outward this becomes `[2026-04-12T00:00:00Z, 2026-04-14T00:00:00Z)`, which contains both file dates. `data.window.since=2026-04-12T00:00:00Z`, `data.window.until=2026-04-14T00:00:00Z`. | `--tz` is not honored (naked timestamps interpreted as UTC → only `2026-04-12` returned); offset conversion applied to the wrong direction (`2026-04-12` excluded instead of `2026-04-13`); rounding computed before conversion (rounds UTC midnight, not LA midnight, giving a 24 h window instead of 48 h). |
| JR-22 | **TzConflictAndInvalid** (one test, two subtests). | **A:** `ox distill history list --since=2026-04-12T09:00-07:00 --tz=America/Los_Angeles --format=json`. **B:** `ox distill history list --since=2026-04-12T09:00 --tz=Not/A/Real/Zone --format=json`. | Both exit 2. Both emit JSON error envelope with `error.code="usage_error"`. A's `error.message` mentions "conflicting timezone" (or equivalent); B's mentions "invalid timezone" (or equivalent). No stdout pollution (the envelope is the only stdout). | A silently honors one input and hides the conflict (caller's intent is ambiguous and we hid it); B panics, defaults silently to UTC, or uses a zero `time.Location`. |
| JR-23 | **EffectiveWindowRounding**: MinimalDaily with filename prefix equal to today's UTC day (derived from the test's captured `now`). | `ox distill history list --since=6h --format=json` | Exit 0. `data.entries` contains the one entry. `data.window.since = day-floor(now - 6h).Format(RFC3339)` (computed from the test's `now` and compared as a string), `data.window.until = day-ceil(now).Format(RFC3339)`. **Not** `now - 6h` and **not** `now` — the envelope reports rounded boundaries, not raw input. | The CLI reports the raw `--since` / `--until` instants in the envelope instead of the day-floored/ceilinged values, silently defeating the caller's ability to detect rounding. |
| JR-24 | **StaleFilenameRecentMtime**: One daily file whose filename prefix is 90 days before the test's captured `now` (e.g., `<now-90d>-<uuid7>.md`) but whose mtime is `now - 1h` (file written recently, about an event 90 days ago). Fresh team context, nothing else staged. | `ox distill history list --since=24h --format=json` | Exit 0. `data.entries: []`. `data.window.since = day-floor(now - 24h)`, `data.window.until = day-ceil(now)`. The file is excluded because its **filename prefix** (90 days ago) falls outside the 24 h window — the reader uses the prefix for window filtering, not the file mtime or the UUID7 instant. | Reader uses `mtime` or UUID7 for window filtering instead of the filename prefix, which would incorrectly return the stale-but-recently-written file in a 24 h query and break the event-day-vs-write-time contract. This is the single most important semantic lock-in for §2.1 "Event day, not write time." |

**Count: 24 cases.**

> **Cross-PR dependency note.** JR-13 exercises a legacy-prefixed
> file and asserts the `date` trust-prefix rule plus silent stderr.
> No warning plumbing, no pre-merge-conditional assertions, no
> coordination with the tz-removal PR required. Same hard assertion
> in every merge order.

---

## 3. Fixture recipes

Each recipe is a named function in the test file that returns the
staged team-context root path. Recipes compose (e.g.,
`MultiSnapshotSameDay` is built on top of `MinimalDaily` by adding a
second file). All recipes accept a `now time.Time` argument so tests
can stage files at deterministic offsets from the test's reference
instant (see §4 on flakiness).

| Recipe | Stages | Purpose |
|---|---|---|
| **MinimalDaily** | One valid `memory/daily/YYYY-MM-DD-<uuid7>.md` file with valid YAML frontmatter (`sources:` list with one entry), a `# Daily Memory — <date>` heading, and a `## Sources` footer. File mtime explicitly set via `os.Chtimes` to a deterministic offset from `time.Now().UTC()`. | Baseline: every "happy path" case inherits this. |
| **MultiSnapshotSameDay** | MinimalDaily + a second daily for the same date, different UUID7, different mtime. | Exercises spec §5.c "multiple summary files per day." Sensitizes ordering, prefix-match, and the no-`--latest` contract. |
| **ThreeDays** | Three daily files across three consecutive UTC days (now-48h, now-24h, now-6h). | Window boundary + `since` + ordering. |
| **MixedTzAndUtcLegacy** | One "legacy" daily where the filename prefix is `2026-04-12` but the UUID7 encodes `2026-04-13T04:00:00Z` (the non-UTC team-tz case); plus one "new" daily where prefix and UUID7 agree. | Exercises spec §5.b recommendation (a) — trust the prefix. |
| **MalformedFilename** | MinimalDaily + a sibling file whose name does not match `YYYY-MM-DD-*.md` nor the legacy `YYYY-MM-DD.md` pattern. | Must-warn-not-crash behavior. |
| **EmptyMarkerOnly** | No daily files. One fact file with a populated `_meta` header and zero body lines. | Exercises spec §5.h item 1 (empty marker surfacing). |
| **MultiTeamList** | Two staged team-context roots, both listed in `config.local.toml`. Each has one daily file. | `--all-teams` merge + per-entry `team` field. |
| **AmbiguousPrefix** | MultiSnapshotSameDay + a third file sharing the same UUID7 short prefix. | JR-07 id_ambiguous path. |
| **BadFrontmatterNeighbor** | MinimalDaily + a sibling `.md` file with broken YAML frontmatter. | JR-18 graceful-neighbor-skip. |
| **TwoConsecutiveUtcDays** | Two valid daily files with filename prefixes on two consecutive UTC days (configurable via the `now` argument so tests can pick which days). | JR-21 — lets the absolute-timezone case exercise a window that spans a day boundary when converted from LA to UTC. |
| **StaleFilenameRecentMtime** | One daily file whose filename prefix is ~90 days before `now` (e.g., `2026-01-15-<uuid7>.md` when `now` is in April) but whose mtime is `now - 1h`. Valid frontmatter, valid body. | JR-24 — proves the reader filters by filename prefix, not mtime. |
| **YesterdayOnly** | One valid daily file whose filename prefix is `now - 24h` in UTC day terms (i.e., yesterday). | JR-02 — empty-window case under day-rounded rules. |

---

## 4. Ordering, isolation, and parallel safety

### 4.1 Hermeticity

- Every test acquires a fresh `t.TempDir()` for workspace, team
  context, XDG directories, and ox binary. No file is shared across
  tests.
- Every subprocess runs under `testguard.MinimalEnv` which allowlists
  `PATH`, `HOME`, `TMPDIR`, `USER`, `LANG`, `LC_ALL`, `GOPATH`,
  `GOROOT`, `GIT_*`, and injects `OX_NO_DAEMON=1`, `DO_NOT_TRACK=1`.
  Everything else is blocked — no leakage from the developer's shell,
  no accidental `SAGEOX_ENDPOINT` pickup.
- `testguard.validateEnv` fails loudly if any env value contains
  `sageox.ai` unless it is `test.sageox.ai` or `localhost`. This
  prevents a test from accidentally hitting production.

### 4.2 Parallel execution

All JR tests can run concurrently: `t.Parallel()` is safe because
each test owns its tempdir and no subprocess writes outside it. There
is no port binding. The daemon is never started.

**Serialized tests.** None. Zero.

### 4.3 Flakiness controls

Any test that depends on "now" is a latent flake if the test uses
`time.Now()` naïvely and the clock crosses a day boundary mid-run.
Mitigations:

1. Fixture recipes take a `now time.Time` argument and **write files
   relative to that instant** (mtime via `os.Chtimes`). The test reads
   `now := time.Now().UTC()` once at the top, passes it to the recipe,
   and does not reference `time.Now()` again.
2. Window boundary assertions (JR-02, JR-09, JR-10, JR-13, JR-23,
   JR-24) use comfortable margins (6 h or more from any UTC midnight,
   not 30 s) so a slow CI run that straddles a day boundary still
   passes. Tests that capture `now := time.Now().UTC()` at the top
   and assert on day-floor / day-ceil derivations are stable across
   the ±few-seconds drift between the parent `now` and the
   subprocess's `time.Now()` unless the test happens to run within
   seconds of a UTC midnight — guard by skipping the window-rounding
   assertions when `now.Hour() == 23 && now.Minute() >= 59` or
   similar, and re-run.
3. No test asserts on a literal `YYYY-MM-DD` date string derived from
   `time.Now()`. Date strings are always computed from the test's own
   `now` variable and substituted into expected JSON values.

### 4.4 Cleanup

`t.TempDir` collects everything. No test writes outside its tempdir.
No `t.Cleanup` is required for file system. No daemon means no
`StopDaemonCleanup` is needed.

---

## 5. Coverage gaps (deliberately not covered)

1. **`journal facts sweep` / `journal facts extract` / `journal
   summarize`** write paths. Out of scope: read surface only. The
   full write-path matrix is a separate plan owned by a later phase.
2. **IPC / daemon paths.** Journal read is CLI-local; the daemon
   never participates. If the implementer threads any journal read
   through the daemon, that is a bug and this plan does not cover it.
3. **`ox distill` alias** (spec §6.1). The hidden alias is tested by
   existing `distill_test.go`. We assert only that `ox distill history list`
   / `show` / `since` exist; we do not re-test `ox distill`.
4. **Concurrent-reader race conditions.** Two `ox distill history list`
   invocations against the same team context while a writer is
   running — out of scope; the spec §5.h item 10 covers the
   write-side commit mutex and existing `distill_github_twin_test.go`
   covers the UUID7 conflict case.
5. **Facts-layer commands** (`ox journal facts list|show`). Out of
   scope for this PR — owned by the facts PR and its own test plan.
6. **Legacy-warning assertion.** JR-13 asserts the reader emits no
   legacy warning and trusts the filename prefix. The sibling
   tz-removal test plan covers the same silent expectation.

---

## 6. Summary

- **24 end-to-end cases** covering `list`, `show` (including prefix
  match, ambiguity, and the explicit `--latest`-is-rejected
  contract), `since` (both JSON and content formats), empty windows,
  malformed filenames, multi-snapshot same-day ordering, mixed
  legacy/UTC files, `--all-teams` merge, the `team_not_found` error
  path, `--tz` absolute-time round-trip, `--tz` conflict and
  invalid-zone usage errors, effective-window rounding, and the
  event-day vs. write-time filtering contract. Facts-layer commands
  (`ox journal facts list|show`) are out of scope for this PR and
  have no test coverage here — they belong to the facts PR.
- All parallelizable, all hermetic via `testguard.MinimalEnv` +
  `t.TempDir`, all gated behind the existing `//go:build slow` tag so
  `make test` stays fast.
- **Neither Gitea twin is applicable.** The read surface is strictly
  local-file enumeration; no clone, push, LFS, or remote is in the
  loop.
- **Cross-PR dependency:** None. JR-13 asserts the `date`
  trust-prefix rule and silent stderr; both hold regardless of
  merge order.
