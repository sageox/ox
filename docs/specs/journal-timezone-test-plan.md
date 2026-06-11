# Timezone Removal — E2E Test Plan

Status: plan
Owner: test-architect
Scope: end-to-end test plan for the **team timezone removal** feature
landing in its own PR. The journal read surface (`ox journal list` /
`show` / `since`) is tested in a sibling plan
(`journal-read-test-plan.md`) so each PR can land independently.

The feature under test: `OX_TIMEZONE`, `internal/config/timezone.go`,
project + team config `timezone` keys, and every `.In(tz)` call in
`cmd/ox/distill*.go` are deleted. Date computation becomes hardcoded
UTC. A new `ox doctor` rule (Unit 7) detects and scrubs dead
`timezone:` keys from existing config files. See
`team-memory-journal.md` §6.4 and `journal-timezone-plan.md`.

**Explicit boundary:** this plan covers only what the tz PR changes.
Reader-side behavior (how new `ox journal list` / `show` / `since`
commands handle legacy filenames) is owned by the sibling read PR's
test plan.

"End-to-end" here means: build the real `ox` binary from source, invoke
it as a subprocess with a staged tempdir + env, assert on stdout /
stderr / exit code. No in-process command-object tests. Unit tests
(deletion of `timezone.go`, `parseFactDate` signature changes, helper
refactors) are owned by the implementation plan (`journal-timezone-plan.md`);
they are explicitly **out of scope** for this document.

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
in-tree conflict with the sibling journal-read plan.

### 1.2 Staged repo + team context

Each test creates:

- **Workspace root** `W` (git-init + one commit + `.sageox/config.json`)
  via the existing `setupE2EWorkspace` pattern at
  `cmd/ox/incremental_e2e_test.go:399`. The config carries
  `team_id=team_tz_e2e` and `endpoint=https://test.sageox.ai` (testguard
  allows `test.sageox.ai` and `localhost` through the production-host
  guard; see `internal/testguard/testguard.go:43`).
- **Team context root** `T` — a bare directory tree that impersonates a
  cloned team context. We do **not** run `ox init` end to end; instead
  we write a minimal `config.local.toml` containing a
  `[[team_contexts]]` row pointing `team_id=team_tz_e2e` at `T`, and we
  pre-create `T/memory/daily/`, `T/memory/weekly/`, `T/memory/monthly/`,
  `T/memory/.github-facts/`, `T/memory/.session-facts/`,
  `T/memory/.discussion-facts/`, `T/memory/.observations/`. This is the
  shape `FindRepoTeamContext` + `LocalConfig.TeamContexts` already reads
  from (`internal/config/local_config_findrepo_test.go:110`).
- **XDG reroute** — every subprocess inherits `XDG_DATA_HOME`,
  `XDG_CONFIG_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME`,
  `XDG_RUNTIME_DIR` pointing into `t.TempDir()` subfolders, plus
  `HOME=<tmp>` and `OX_XDG_ENABLE=1`. Identical to the pattern at
  `cmd/ox/doctor_e2e_helpers_test.go:111`. This is load-bearing: it
  guarantees nothing the test does can touch the developer's real
  `~/.sageox/` / `~/.local/share/sageox/`.
- **Auth** — pre-written fake `auth.json` under
  `XDG_CONFIG_HOME/sageox/auth.json` with token keyed by
  `test.sageox.ai`. The TZ cases never hit the network; we need auth
  only so endpoint resolution does not refuse to run.
- **Daemon** — **never started**. `OX_NO_DAEMON=1` is injected by
  `testguard.MinimalEnv`. All TZ cases exercise local-file code paths
  only; the spec requires no IPC. Any test that spawns a daemon is a
  defect.

### 1.3 Invocation pattern

Every test command uses this exact shape:

```go
// conceptual — actual helper lives in the test file, not in this plan
out, exit, _ := testguard.RunOx(t, oxBin, W, env,
    "journal", "summarize", "--since=48h", "--format=json", "--dry-run")
```

The command is the real `ox` subprocess, `W` is the workspace cwd,
`env` carries the XDG reroute + test endpoint. Combined output is
captured for assertions. Exit code is checked against the matrix
below.

### 1.4 Config files are staged in Go, not via `ox`

Stray `timezone:` keys (TZ-01, TZ-03, TZ-04, TZ-05) are staged by
writing `.sageox/config.json` and the team `config.toml` directly
with `os.WriteFile`. The test controls the exact bytes — both the
dead `timezone` key and the unrelated keys that TZ-05 asserts are
preserved through `--fix`. Using `ox config set` to stage the dead
key would be impossible after Unit 4 lands (the command will refuse
unknown settings), so file-level staging is the only workable
approach.

Observations (TZ-01) are landed via the real `ox memory put`
command, since that is the supported write path and it exercises the
same `memory/.observations/` layout that `ox distill --dry-run` will
read.

### 1.5 Bucketing is observed via `ox distill --dry-run`

The bucketing case (TZ-01 below) needs to prove that a new
observation buckets on the correct UTC day. The vehicle is
`ox distill --dry-run --json`: it already exists in the tz worktree,
it exercises the exact functions that had `.In(tz)` removed
(`groupObservationsByDay`, `determineLayers`, `inferDailyHighWater`),
and the `--dry-run` flag short-circuits the LLM invocation while
still running the date math. Observations are landed via
`ox memory put` and the assertion is on the JSON envelope's
would-write block, not on any file written to disk.

Actual on-disk filename shape is covered by the existing slow distill
tests (`cmd/ox/distill_test.go`, `distill_write_test.go`) which
continue to run unchanged after the revert.

### 1.6 Gitea digital twin applicability

**Two twins exist in this repo:**

1. **Docker-backed Gitea container** at `internal/daemon/twin_gitea_test.go`
   (build tag `slow`, requires Docker, port `13719`, lazily started via
   `sync.Once`). Exercises the daemon's git + LFS + credential flows.
   Touches only daemon code paths.
2. **Bare-git "twin" repos** at `cmd/ox/distill_github_twin_test.go`
   (`setupFactsTwinRepos`, `runTwinGit`). Creates a local bare repo plus
   two clones in `t.TempDir()` and exercises two "nodes" writing fact
   files concurrently. No Gitea, no Docker, no LFS.

**Verdict for this plan: neither twin is applicable.** The revert
affects date bucketing (`groupObservationsByDay`, `determineLayers`)
and filename prefixing on the write path. The filename matters, the
network does not. The bare-git twin in `distill_github_twin_test.go`
is already the right home for any follow-up e2e that wants to observe
a real two-node extraction under UTC-only naming, but that is a
**write-path** test and is covered by the existing slow distill tests,
not by this plan. The Docker Gitea twin has no bearing on date math.

Plumbing Gitea into this plan today would add container startup cost,
Docker as a CI dependency, and zero behavioral coverage over what
filesystem-staged fixtures give us.

---

## 2. Test matrix

Four cases. Every command exists in the tz PR's worktree — no
dependency on the journal read surface. Each case directly verifies
one of the changes in the tz plan.

| ID | What's verified | Fixture + env | Command | Observable outcome | Failure mode caught |
|---|---|---|---|---|---|
| TZ-01 | **UTC bucketing; all three tz inputs silently ignored.** | Project config `.sageox/config.json` carries a stray `"timezone": "Asia/Tokyo"` key; team `config.toml` carries a stray `timezone = "Europe/Berlin"` key; env carries `OX_TIMEZONE=America/Los_Angeles`; one observation landed via `ox memory put` with RFC3339 timestamp `2026-04-12T23:30:00-07:00` (= `2026-04-13T06:30:00Z`). | `ox distill --dry-run --json` | Exit 0. JSON envelope's would-write block shows the observation bucketed on `2026-04-13` (the UTC day of the instant), NOT `2026-04-12`. Stderr carries no "using timezone ..." line and no "invalid timezone" warning. | Any single surviving tz input — env var, project config key, team config key, or a `.In(tz)` call in `groupObservationsByDay` / `determineLayers` — would bucket on `2026-04-12` (LA/Tokyo/Berlin wall-clock day) and the test fails. This one case catches every incomplete-revert failure mode on the decision path. |
| TZ-02 | **`ox config set timezone` is rejected at the command surface.** | Fresh workspace. | `ox config set timezone UTC` | Non-zero exit. Stderr contains `unknown setting` (or whatever the registry-miss message is). No config file is modified. | The `timezone` entry in the `ConfigSetting` registry survived Unit 4, so the command succeeds. |
| TZ-03 | **`ox config get timezone` does not leak the stray key.** | Project config `.sageox/config.json` carries a stray `"timezone": "Asia/Tokyo"` key. | `ox config get timezone` | Non-zero exit. Stderr contains `unknown setting`. Stdout does NOT echo `Asia/Tokyo`. | `getResolvedConfigValue`'s `case "timezone"` survived, or the `ProjectConfig.Timezone` field is still unmarshalled into a resolvable path, letting the dead key leak out as a first-class "resolved" value. |
| TZ-04 | **`ox doctor` auto-scrubs dead `timezone` keys from both configs without collateral damage, and is idempotent.** | Project config `.sageox/config.json` carries a stray `"timezone": "Asia/Tokyo"` AND an unrelated `"team_id": "team_abc"` key; team `config.toml` carries `timezone = "Europe/Berlin"` AND an `[owners]` section with at least one member. | `ox doctor --fix --yes`, run twice back-to-back | **First run:** exit 0; both config files on disk no longer contain the top-level `timezone` key; `team_id` and `[owners]` section + TOML comments are byte-preserved. **Second run:** exit 0; both files byte-identical to their post-first-run state (zero mutation idempotency). File-state assertions are stronger than finding-count assertions here because they directly prove the scrub happened on disk — `ox doctor` has no JSON output mode, so finding-count cannot be asserted via stdout parsing. | Unit 7's autofix rule is missing; scrubs more than the `timezone` key (collateral damage); corrupts TOML structure or comments; leaves the key in place; is not idempotent (re-fires on an already-clean file); or emits a `fail`-level finding that would flip exit code non-zero. |

**Count: 4 cases.** TZ-01 covers the write-path revert (Units 1, 2,
3, 5). TZ-02/03 cover the config surface deletion (Unit 4). TZ-04
covers the doctor autofix rule (Unit 7).

> **Cross-PR dependency:** None. Every command is in the tz PR's
> worktree. No `t.Skip` guards, no probes, no coordination with the
> journal-read PR.

---

## 3. Fixture recipes

Each recipe is a named function in the test file that returns the
staged team-context root path. Recipes accept a `now time.Time`
argument so tests can stage files at deterministic offsets from the
test's reference instant (see §4 on flakiness).

| Recipe | Stages | Used by |
|---|---|---|
| **StrayKeysWithObservation** | Fresh workspace. `.sageox/config.json` carries a stray `"timezone": "Asia/Tokyo"` key. Team `config.toml` carries a stray `timezone = "Europe/Berlin"` key. One observation landed via `ox memory put` at RFC3339 `2026-04-12T23:30:00-07:00` (= `2026-04-13T06:30:00Z`). | TZ-01 |
| **FreshWorkspace** | A standard initialized project with no stray keys and no observations. | TZ-02 |
| **StrayProjectKey** | Fresh workspace + `.sageox/config.json` carrying `"timezone": "Asia/Tokyo"`. | TZ-03 |
| **StrayKeysWithUnrelatedContent** | Fresh workspace. `.sageox/config.json` carries `"timezone": "Asia/Tokyo"` AND an unrelated `"team_id": "team_abc"` key. Team `config.toml` carries `timezone = "Europe/Berlin"` AND an `[owners]` section with at least one member (plus surrounding TOML comments, so TZ-04 can verify comment preservation). | TZ-04 |

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

All TZ tests can run concurrently: `t.Parallel()` is safe because each
test owns its tempdir and no subprocess writes outside it. There is
no port binding. The daemon is never started.

**`OX_TIMEZONE` caveat.** `OX_TIMEZONE` is a process env var and would
collide if we used `t.Setenv`. We do **not** use `t.Setenv`; we pass
the env to the subprocess via `testguard.RunOx`'s `envVars` argument,
which is per-invocation. The parent test process's environment is
never touched.

**Serialized tests.** None. Zero.

### 4.3 Flakiness controls

Any test that depends on "now" is a latent flake if the test uses
`time.Now()` naïvely and the clock crosses a day boundary mid-run.
Mitigations:

1. Fixture recipes take a `now time.Time` argument and **write files
   relative to that instant** (mtime via `os.Chtimes`). The test reads
   `now := time.Now().UTC()` once at the top, passes it to the recipe,
   and does not reference `time.Now()` again.
2. Window boundary assertions use comfortable margins (6 h, not 30 s)
   so a slow CI run that straddles a day boundary still passes.
3. No test asserts on a literal `YYYY-MM-DD` date string derived from
   `time.Now()`. Date strings are always computed from the test's own
   `now` variable and substituted into expected JSON values.
4. **Explicitly not using `t.Setenv("TZ", ...)`.** Go's `time` package
   caches the local location on first use; mutating `TZ` mid-test is
   unreliable. All tests run as if the parent process is UTC —
   irrelevant because everything we care about is in the subprocess's
   environment, not the parent's.

### 4.4 Cleanup

`t.TempDir` collects everything. No test writes outside its tempdir.
No `t.Cleanup` is required for file system. No daemon means no
`StopDaemonCleanup` is needed.

---

## 5. Coverage gaps (deliberately not covered)

1. **On-disk filename shape from a real summarize run** — i.e.,
   proving that a live `ox distill` run (not `--dry-run`) produces a
   file named with a UTC prefix under
   `OX_TIMEZONE=America/Los_Angeles`. Out of scope: requires either a
   real LLM or a test stub for `OX_AGENT_CLI`. The existing slow
   distill tests (`cmd/ox/distill_test.go`, `distill_write_test.go`)
   cover the filename-shape assertion directly; they continue to run
   unchanged after the revert and serve as the write-path safety net.
2. **Reader-side behavior** (legacy-file handling, empty directory,
   `ox journal list` / `show` / `since`, legacy-prefix trust-rule).
   Owned by the read PR's test plan. Not a gap in this plan's
   scope — an explicit boundary.
3. **IPC / daemon paths.** TZ revert is a CLI-local concern; the
   daemon never participates.
4. **Wall-clock / DST transitions.** Every test is hardcoded to UTC
   and date math is UTC, so DST never appears. A fixture that spans
   a spring-forward or fall-back boundary cannot behave differently
   from a fixture that does not.

---

## 6. Summary

- **4 end-to-end cases** directly verifying the tz changes:
  - TZ-01: UTC bucketing with env, project config, and team config
    tz inputs all present and all silently ignored.
  - TZ-02: `ox config set timezone` is rejected.
  - TZ-03: `ox config get timezone` does not leak a stray key.
  - TZ-04: `ox doctor` auto-scrubs dead `timezone` keys without
    collateral damage and is idempotent across runs.
- All parallelizable, all hermetic via `testguard.MinimalEnv` +
  `t.TempDir`, all gated behind the existing `//go:build slow` tag
  so `make test` stays fast.
- **Neither Gitea twin is applicable.** The revert is a date /
  config-surface concern.
- **No cross-PR dependency.** Every command lives in the tz PR's
  worktree. The test file runs to completion regardless of the
  read-surface PR's state.
