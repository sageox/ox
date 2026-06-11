# Implementation Plan — Team Timezone Removal

**Companion to:** [`team-memory-journal.md`](./team-memory-journal.md) §5.b, §6.4

**Scope:** The team-timezone feature revert only. All journal
read-surface work (`ox journal list/show/since`, `extract`, `sweep`,
`summarize`) is out of scope and is being planned in parallel.

**Audience:** Go implementer working on the ox CLI + daemon. This plan
describes **how** to break the work into compile-clean, shippable,
independently-testable units. It intentionally does not re-enumerate
the file-level diff — the spec's §6.4 is the source of truth for that.

---

## 1. Summary

- **Goal:** Delete the configurable team-timezone feature in full. All
  date bucketing (daily / weekly / monthly) in the distill pipeline
  becomes hardcoded UTC. Legacy `memory/daily/YYYY-MM-DD-*.md` files
  with team-tz-derived prefixes are trusted as-is (§5.b option (a)).
- **Non-goal:** No changes to the new `ox journal` command surface,
  `extract`, `sweep`, `summarize`, fact-file schema, or any LLM prompt.
- **Non-goal:** No data migration, no on-disk rewrite, no fact-file
  reformat. Old daily files are read by the new pipeline unchanged.
- **Non-goal:** No loud warning on users with `OX_TIMEZONE` set or a
  `timezone:` config key — those are silently dropped per §6.4.
  A dead-key autofix rule is in scope (Unit 7): `ox doctor` always
  scrubs stray `timezone:` keys on every run, reports the scrub as
  an informational finding, and never fails.
- **Non-goal:** No rewrite of `parseFactDate`'s parsing logic — only
  its signature and its one `In(loc)` branch change.
- **Non-goal:** No change to the cache-index schema, the
  `distill-state.json` format, or `ox memory distill` (the API-backed
  path).
- **End state:** `grep -r Timezone cmd/ internal/` on the repo returns
  only unrelated RFC3339 test literals ("timestamp with timezone
  offset"). Zero production-code references remain.
- **End state:** `make lint && make test && make test-slow` pass at
  every unit boundary. No unit leaves `main` uncompilable.
- **End state:** `ox distill` continues to work on existing team
  contexts with no user action required.

---

## 2. Unit breakdown

The revert is ordered **inside-out**: the distill callsites are changed
to UTC first (while the resolver still exists and is trivially
callable), then the resolver is deleted, then the config surface and
schema. This ordering is deliberate — it keeps every intermediate
state compile-clean and reversible.

| # | Unit | LOC feel | Can land alone? |
|---|------|----------|-----------------|
| 1 | Distill call-sites convert to hardcoded UTC (parameter threading still present) | ~150 lines changed | Yes |
| 2 | Strip `tz` parameters from distill helpers + `parseFactDate` | ~80 lines changed | Yes (after #1) |
| 3 | Delete `internal/config/timezone.go` + resolver callsite | ~90 lines deleted | Yes (after #2) |
| 4 | Delete `timezone` from `config get/set` surface | ~60 lines deleted | Yes (after #3) |
| 5 | Delete `Timezone` fields from `ProjectConfig` / `TeamConfig` + `EnvTimezone` | ~20 lines deleted | Yes (after #4) |
| 6 | Docs + cosmetic test comment sweep | ~30 lines changed | Yes (after #5) |

Each unit below specifies **what** changes at the architectural level,
**why** those changes are grouped, the **test strategy**, and the
**compile-safety invariant** that makes the unit landable without
breaking `main`.

---

### Unit 1 — Distill callsites use UTC (behavior flip)

**What changes.** Inside `cmd/ox/distill.go`, `distill_discussions.go`,
`distill_github.go`, and `distill_sessions.go`, every place that today
reads the resolved `*time.Location` and calls `.In(tz)` is replaced
with a hardcoded `time.UTC` / `.UTC()`. The resolver
(`config.ResolveTimezone`) still exists after this unit — its return
value is simply ignored or bypassed. Parameter threading (the
`tz *time.Location` / `tz ...*time.Location` arguments) **is preserved
in this unit**. Callers still pass `time.UTC` explicitly.

**Why grouped this way.** This is the only unit that changes runtime
behavior. Splitting the behavior flip from the signature cleanup keeps
the signature churn (Unit 2) purely mechanical and easy to review. It
also means a bisect that blames a date-regression lands squarely on
this commit, not on a multi-package rename.

**Unit test strategy.** The existing
`TestScanPendingSessionsStartedAt` / `TestSessionSummaryToFactsStartedAt`
tests at `cmd/ox/distill_sessions_test.go:741-818` already assert UTC
behavior for session facts — they stay green. Add / repurpose tests
for `groupObservationsByDay`, `determineLayers`, `enumerateWeeks`, and
`enumerateMonths` that feed observations and `now` values where a
non-UTC team-tz would have produced a different answer from UTC.
Verify the new code returns the UTC answer in all cases. A single
regression table case per helper suffices — do not rebuild the full
matrix.

**Integration test strategy.** Run `ox distill` against a fixture
team-context that contains (a) an observation recorded at
`2026-04-12T23:30:00-07:00` (which is `2026-04-13` in UTC but
`2026-04-12` in `America/Los_Angeles`). Verify the daily file lands
under `2026-04-13-*.md` regardless of whether `OX_TIMEZONE` /
`timezone:` is set. Reuse existing distill integration fixtures where
possible.

**Compile-safety rationale.** The resolver is untouched. Its return
value is still a valid `*time.Location`. Helpers still take `tz`
parameters — callers just pass `time.UTC` or ignore the resolver's
return. Every file still compiles and every existing test that passes
a `*time.Location` still type-checks.

**Dependencies.** None. This is Unit 1 because it is the only unit
with a behavior change; all subsequent units are mechanical.

---

### Unit 2 — Strip `tz` parameters from distill helpers and `parseFactDate`

**What changes.** The `tz` / `tz ...*time.Location` parameter is
removed from every signature that has one: `endOfDay`, `endOfMonth`,
`isoWeekRange`, `groupObservationsByDay`, `enumerateWeeks`,
`enumerateMonths`, `determineLayers`, `distillDaily`, `distillWeekly`,
`distillMonthly`, `readWeeklyFilesForMonth`, `readPendingDiscussionFacts`,
`readPendingGitHubFacts`, `readPendingSessionFacts`, and
`parseFactDate`. The function bodies stop receiving `tz` and the one
`t.In(loc)` branch inside `parseFactDate` at `distill_discussions.go:471`
is deleted — it always returns a UTC day now.

All callers inside `cmd/ox/` stop passing `tz`.

**Why grouped this way.** This is a single, mechanical, signature-wide
rename. Splitting it per-file would force temporary adapter shims
between files in the same `cmd/ox/` package. Keeping it as one unit
lets the compiler verify atomically that every caller has been
updated.

**Unit test strategy.** The existing `parseFactDate` test table at
`cmd/ox/distill_test.go:831` stays green — it already calls the
function without `tz`. The existing `readPendingGitHubFacts` /
`readPendingDiscussionFacts` / `readPendingSessionFacts` test files
already call these functions with `tz` omitted (it is variadic), so
they remain compile-safe when the variadic is dropped — just delete
the trailing `...` at each call site. Add one regression case to
`parseFactDate`'s table where the `_meta.recorded_at` is
`2026-03-10T23:30:00-07:00` and the expected result is `2026-03-11`
(the UTC day), locking in the §5.b semantics.

**Integration test strategy.** No new integration tests needed —
Unit 1 already exercised the behavior. This unit is a pure
signature cleanup; the integration test from Unit 1 is the
regression guard.

**Compile-safety rationale.** The Go compiler enforces this entirely.
`go build ./...` either passes (every caller updated) or fails loudly
(a caller was missed). There is no hidden runtime change.

**Dependencies.** Unit 1 must land first so that callsites already
pass `time.UTC` explicitly and the stripping is mechanical.

---

### Unit 3 — Delete `internal/config/timezone.go` and its tests

**What changes.** Delete `internal/config/timezone.go` in full
(including `ResolveTimezone`, `loadTeamTimezone`, `IsValidTimezone`).
Delete `internal/config/timezone_test.go`. Delete the
`tz := config.ResolveTimezone(projectRoot)` line at
`cmd/ox/distill.go:665` and the surrounding `now := time.Now().In(tz)`,
replacing with `now := time.Now().UTC()`.

The `config_settings.go` reference to `config.IsValidTimezone` at
`:520` is resolved by Unit 4. Unit 3 and Unit 4 must land together
in the same tree state — the resolver is gone and its last consumer
is gone — otherwise the build breaks. How the implementer partitions
that into commits is their call.

**Why grouped this way.** The resolver and its test are a coherent
module. Deleting one without the other is nonsense.

**Unit test strategy.** No new tests. Deleted code cannot have tests.
Verify `grep -r ResolveTimezone internal/ cmd/` returns zero matches
after the unit lands.

**Integration test strategy.** Re-run the Unit 1 integration fixture.
Result must be identical. Also run `ox distill` with
`OX_TIMEZONE=America/Los_Angeles` set in the environment and verify
it is silently ignored (no error, no warning, no behavior change from
the unset case).

**Compile-safety rationale.** The `IsValidTimezone` consumer in
`config_settings.go` is the only non-test consumer of the resolver
package after Unit 2. Unit 3 must land in the same tree state as
Unit 4. Nothing else imports `internal/config/timezone.go`.

**Dependencies.** Unit 2. Tree state must also contain Unit 4.

---

### Unit 4 — Remove `timezone` from `ox config get/set`

**What changes.** Delete the entire `timezone` entry from the
`ConfigSetting` registry (`config_settings.go:176-200`). Delete the
`case "timezone"` arms in `getResolvedConfigValue`'s switch
(`:399-405`), in `SetConfigValue`'s custom validation (`:519-522`),
and in `setRepoConfig` / `setTeamConfig` (`:670-671`, `:730-731`).
Delete the `if key != "timezone"` exception in the lowercasing logic
at `cmd/ox/config_get.go:155`.

After this unit, the `config_settings.go` file no longer references
`config.IsValidTimezone` — which is what lets Unit 3's deletion of
the resolver remain compile-clean.

**Why grouped this way.** The config surface is a coherent sub-system:
one switch statement per level, one registry entry, one custom
validator, one lowercasing exception.

**Unit test strategy.** Update or delete any `config_settings_test.go`
/ `config_get_test.go` case that asserts `timezone` is a known key.
Add one new test: `SetConfigValue("timezone", "America/New_York", ...)`
must now return `unknown setting: timezone` at every level. Add
another: loading a `.sageox/config.json` or team `config.toml` that
still contains a `timezone:` field must succeed with no error — the
field is silently dropped on unmarshal because Go's `encoding/json`
and `BurntSushi/toml` ignore unknown fields. Lock this in as a
regression test because it is the §6.4 behavioral guarantee for
existing users.

**Integration test strategy.** Run `ox config set timezone UTC`
against an initialized fixture project. Expect non-zero exit code with
`unknown setting` error on stderr. Run `ox config get timezone` —
same. Run `ox distill` against a fixture whose `config.toml` still has
`timezone = "America/Los_Angeles"` and verify it completes without
error and buckets observations in UTC.

**Compile-safety rationale.** This unit only deletes switch-arms and
registry entries. No new imports, no new types, no new functions.
`go build ./cmd/ox` either passes or a missed deletion is caught by
the compiler.

**Dependencies.** Unit 2. Tree state must also contain Unit 3.

---

### Unit 5 — Delete `Timezone` fields and `EnvTimezone` constant

**What changes.** Delete the `Timezone string` field from
`ProjectConfig` at `internal/config/project_config.go:142-145`. Delete
the `Timezone string` field from `TeamConfig` at
`internal/config/team_config.go:27-30`. Delete the `EnvTimezone`
constant from `internal/config/env.go:30-32` (and its doc comment).

**Why grouped this way.** These are the three remaining
leaf-references to the team-timezone feature. They are in three
different files but form a single conceptual deletion: "the struct
fields and env var the feature read from no longer exist." Deleting
one without the other two leaves an obviously half-reverted state.
The compiler will not object (struct fields and constants are
unreferenced after Units 3+4), but the reviewer will.

**Unit test strategy.** Grep. After the unit, `grep -rn
'Timezone\s*string' internal/ cmd/` must return zero production-code
matches. The regression test added in Unit 4 — loading a config file
with a still-present `timezone:` key succeeds — remains the guard.

**Integration test strategy.** None beyond the Unit 4 integration
test.

**Compile-safety rationale.** `Timezone` fields are unexported
consumers as of Unit 4 (nothing references them). `EnvTimezone` is
unexported consumer as of Unit 3 (nothing references it). Go will
emit a "declared and not used" error for local vars but **not** for
unused struct fields or package-level constants — so the tree compiles
both before and after this unit lands. The deletion is cosmetic-but-
mandatory.

**Dependencies.** Units 3 and 4.

---

### Unit 7 — Doctor autofix rule for dead `timezone` config keys

**What changes.** Add a new `ox doctor` rule that **always
auto-scrubs** stray `timezone:` fields from `.sageox/config.json`
and the team `config.toml`. The rule:

- **Always fix.** On every `ox doctor` run — with or without
  `--fix` — inspect both config files as raw JSON / TOML (not via
  the now-deleted struct fields), and if a `timezone` key is
  present, rewrite the file without it. Preserve all other keys,
  TOML comments, and file formatting to the extent the existing
  config-writing helpers do. This is consistent with the silent-drop
  read-side policy: the key is dead, and cleaning it up requires no
  user decision.
- **Idempotent.** Once the keys are scrubbed, subsequent runs are a
  no-op — the rule exits silently because there is nothing to remove.
- **Reported, not a failure.** The check emits an informational
  finding (`type: info`) describing the scrub. Whether that is a
  single bundled finding (`"removed dead timezone key from N
  file(s)"`) or one finding per scrubbed file is an implementation
  detail — both are acceptable. The finding is NOT a `fail`, so
  exit code stays `0` regardless. `ox doctor` has no JSON output
  mode at the time of this revert; the finding surfaces through
  the normal human-readable output.

**Why grouped this way.** The doctor rule is conceptually
independent from the config-surface deletion in Unit 4. Unit 4
removes the feature; Unit 7 cleans up residue on existing
installations. Unit 7 is pure addition — a reviewer can approve it
independently.

**Unit test strategy.** Add table-driven tests for the new rule:
(a) config with no `timezone` key → no finding, no mutation;
(b) config with a `timezone` key only → key removed, info finding
emitted; (c) config with `timezone` + unrelated keys → `timezone`
removed, unrelated keys preserved byte-for-byte; (d) TOML with
comments around the `timezone` line → comments preserved, only the
`timezone` line removed; (e) second run on an already-scrubbed file →
no finding, no mutation (idempotency).

**Integration test strategy.** Run `ox doctor --fix --yes` against
a fixture project that has both a project `timezone:` key and a team
`timezone:` key plus unrelated legitimate keys. Assert on on-disk
state: both files no longer contain the `timezone` key after the
run; unrelated keys and TOML comments are preserved byte-for-byte;
exit code `0`. Run `ox doctor --fix --yes` a second time; assert
both files are byte-identical to their post-first-run state (zero
mutation idempotency). File-state assertions are stronger than
finding-count assertions for this rule because they directly prove
the scrub happened on disk, not just that the check reported it.

**Compile-safety rationale.** Additive only — the new doctor rule is
a new function registered with the existing doctor check registry. No
existing code changes.

**Dependencies.** Units 3 and 4. Cannot land earlier because the
check's whole point is to scrub keys that the feature-deletion PR
made dead. Landing before Unit 4 would produce a doctor rule that
removes a still-valid configuration.

---

### Unit 6 — Documentation and cosmetic test sweep

**What changes.**

1. Audit `docs/specs/codedb-temporal-distillation.md` and
   `docs/coes/2026-04-07-multi-node-write-conflicts.md` for
   "team timezone" references. Rewrite or delete the affected
   paragraphs so the surviving prose matches the UTC-only reality.
2. In `cmd/ox/distill_sessions_test.go:750`, `:805-806`, rename the
   fixture title string from `"Timezone test"` to `"UTC date test"`
   (per §6.4's explicit note). This is purely cosmetic — the test
   assertions do not change.
3. Regenerate the `ox config` reference docs (`make docs` or
   equivalent): deleting the `timezone` registry entry in Unit 4
   cascades to the generated `.mdx`. Run the generation step, commit
   the resulting diff.
4. Final sweep: `grep -rn -i 'team.timezone\|OX_TIMEZONE\|ResolveTimezone' .`
   must return zero production matches and zero non-test-comment
   documentation matches.

**Why grouped this way.** Documentation and cosmetic test-comment
changes have no runtime or compile impact. Batching them as the last
unit means reviewers can sanity-check all the cleanup in one place
without having to skim five earlier commits for stale prose.

**Unit test strategy.** None — this unit is prose and generated
artifacts.

**Integration test strategy.** None.

**Compile-safety rationale.** Zero code changes (only regenerated
`.mdx` and string-literal edits in tests).

**Dependencies.** Units 1-5. Safe to land any time after Unit 5 is
merged; does not need to be simultaneous.

---

## 3. Parallelism analysis — shared surface with the journal-read plan

The journal-read plan (new commands `ox journal list/show/since/facts
list/show`) will read the same fact files and daily files that this
plan's units touch. The shared surface is narrow but real. Each row
below is a file or symbol both plans touch, with the mitigation.

| Shared surface | This plan | Reader plan | Mitigation |
|---|---|---|---|
| `parseFactDate` signature in `cmd/ox/distill_discussions.go:456` | Unit 2 strips the `tz ...*time.Location` variadic | Reader plan calls `parseFactDate` from new `journal facts list` / `facts show` code paths | **Reader plan freezes on `parseFactDate(content, filename) string` after this plan's Unit 2 lands.** Reader plan starts coding against the new signature from day one; until Unit 2 merges, reader-plan author uses a local branch that cherry-picks Unit 2 as a WIP commit. |
| `readPendingDiscussionFacts` / `readPendingGitHubFacts` / `readPendingSessionFacts` | Unit 2 strips `tz ...*time.Location` | Reader plan's `sweep` and `summarize` helpers are likely refactored from these or call them directly | Same freeze — reader plan commits to the post-Unit-2 signatures. If the reader plan needs a parallel refactor (e.g., extracting a shared fact-reading abstraction), it **must not** accept a `tz` parameter. The reader plan's author confirms this at kickoff. |
| `cmd/ox/distill.go` (file-level) | Units 1, 2, 3 all edit it heavily | Reader plan likely adds new helpers and/or extracts code into new files | **Reader plan holds all edits to `cmd/ox/distill.go` until Unit 2 is merged.** After Unit 2, both plans can edit the file in parallel because their edits are in disjoint regions (this plan only deletes; reader plan adds new helpers). Physical merge conflicts will be textual, not semantic. |
| `config.ResolveTimezone` | Unit 3 deletes it | Reader plan MUST NOT reintroduce it | §5.b item 6 is the contract: *the CLI MUST NOT introduce a `--timezone` flag, MUST NOT read `OX_TIMEZONE`, and MUST NOT consult any project or team config for timezone information.* Reader plan acknowledges this at kickoff. |
| `ProjectConfig.Timezone` / `TeamConfig.Timezone` fields | Unit 5 deletes them | Reader plan must not read them from any new code path | Same contract. If the reader plan author writes new config-reading code, it must not reference these fields. Units 3-5 lock this in mechanically (compile error if a stray reference sneaks in). |
| `OX_TIMEZONE` env var | Unit 3 (via deleting the resolver) and Unit 5 (via deleting the constant) | Reader plan must not introduce a new reader for `OX_TIMEZONE` | Same contract. |
| `_test.go` files in `cmd/ox/` | Units 1, 2, 6 touch several test files | Reader plan will add new `_test.go` files | No conflict — new test files are additive. |
| `docs/specs/codedb-temporal-distillation.md` | Unit 6 audits for team-tz references | Reader plan may also edit this file if it describes the extract surface | Low risk — coordinate via PR review. If both plans edit, rebase and merge prose. No architectural coupling. |

**Bottom line for parallelism.** The reader plan can start work in
parallel **as long as it does not edit `cmd/ox/distill.go` or the
`readPending*Facts` helpers until this plan's Unit 2 lands.** Other
reader-plan work (new `journal_*.go` files, new cobra subcommands,
new envelope types, the `ox journal` command tree scaffolding) is
entirely conflict-free with this plan.

**Note on legacy warning coupling.** The reader emits no warning
when a legacy daily file's filename prefix disagrees with its
UUID7-embedded UTC day. This plan introduces no warning plumbing;
the reader plan has no `legacy.go` file. No cross-PR coupling on
warning behavior.

---

## 4. Risks and rollback

| Unit | Primary risk | Rollback strategy |
|---|---|---|
| 1 | A date-bucketing regression ships to a team in a non-UTC zone: observations near midnight local time land on a different day. | Revert Unit 1. It is the only unit with behavior impact, so rollback is surgical. The resolver still exists after revert; no cascading cleanup needed. |
| 2 | Mechanical signature change misses a caller and breaks compile. | Caught by `go build ./...` in CI. Revert Unit 2. |
| 3 | Deleting `timezone.go` breaks a consumer the audit missed. | Same as above — compile-time caught. Revert Unit 3 and Unit 4 together (they must land in the same tree state). |
| 4 | A registered user depends on `ox config set timezone` in an automation script and it starts erroring. | Accepted risk per §6.4 ("silently ignored" is the policy for existing *configured* values, but `ox config set timezone` is an active command and will error). Release notes call this out. Mitigation: the command returning `unknown setting` is a clean error a shell script can detect and ignore. |
| 5 | A `BurntSushi/toml` or `encoding/json` edge case rejects a config file because of the dropped field. | Low risk — both decoders drop unknown keys by default, verified in the regression test added in Unit 4. Revert Unit 5. |
| 6 | Docs regenerate produces unexpected churn in generated `.mdx`. | Commit the churn in a separate sub-commit of Unit 6 so doc-only reviewers can approve it without reading code diffs. |

**Global rollback.** Units are reversible in dependency order
(6 → 5 → 7 → 4 → 3 → 2 → 1). No manual patching required.
