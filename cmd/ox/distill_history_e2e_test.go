//go:build slow

// distill_history_e2e_test.go — harness sanity tests for the ox distill
// history read surface. This file holds the three "does the whole thing wire
// up?" tests that exercise setupDistillHistoryE2E + every fixture recipe +
// addSecondaryTeam. The harness types/setup live in
// distill_history_e2e_harness_test.go; the stagers and 12 recipes live in
// distill_history_e2e_fixtures_test.go; the per-case JR-* skeletons live
// in the list/show/since files.
//
// Every test here runs in parallel, captures `now := time.Now().UTC()`
// exactly once at the top, and passes that instant through to the
// recipe so on-disk state cannot drift across a day boundary mid-test
// (plan §4.3).
//
// See docs/specs/distill-history-read-test-plan.md §1 and §3.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/facts"
)

// ---------------------------------------------------------------------------
// Sanity tests — prove the infra + every recipe stages deterministically
// ---------------------------------------------------------------------------

// TestDistillHistoryE2E_Recipes_Stage exercises every fixture recipe end-to-end
// through setupDistillHistoryE2E and asserts that:
//  1. The harness builds a fresh ox binary, workspace, team context root,
//     XDG reroute, fake auth, and config.local.toml pointing at the team.
//  2. Every recipe produces the daily/fact files it claims to, at the
//     exact absolute paths reported in its stagedFixture receipt, with
//     the exact mtime requested via os.Chtimes.
//  3. All dailies fall under memory/daily/ and parse the YYYY-MM-DD
//     filename prefix cleanly (or, for MalformedFilename, the recipe has
//     an ExtraFiles entry naming the non-conforming sibling — the
//     reader's response to that file is JR-11's job, not this sanity
//     test's).
//
// Failure prevented: a recipe silently fails to stage, or stages to the
// wrong path, or sets the wrong mtime — any of which would make the
// downstream JR-* assertions vacuously pass.
func TestDistillHistoryE2E_Recipes_Stage(t *testing.T) {
	t.Parallel()

	type recipeCase struct {
		name string
		// run invokes the recipe on a fresh harness and returns the staged
		// fixture plus the harness so subsequent assertions can reach into
		// the team root paths.
		run func(t *testing.T, e2e *distillHistoryE2E, now time.Time) stagedFixture
		// wantDailies / wantFacts / wantExtras are soft-count assertions.
		// A recipe that claims N of each must produce exactly N.
		wantDailies int
		wantFacts   int
		wantExtras  int
	}

	cases := []recipeCase{
		{
			name: "MinimalDaily",
			run: func(t *testing.T, e *distillHistoryE2E, now time.Time) stagedFixture {
				return recipeMinimalDaily(t, e.primaryTeam, now)
			},
			wantDailies: 1,
		},
		{
			name: "MultiSnapshotSameDay",
			run: func(t *testing.T, e *distillHistoryE2E, now time.Time) stagedFixture {
				return recipeMultiSnapshotSameDay(t, e.primaryTeam, now)
			},
			wantDailies: 2,
		},
		{
			name: "ThreeDays",
			run: func(t *testing.T, e *distillHistoryE2E, now time.Time) stagedFixture {
				return recipeThreeDays(t, e.primaryTeam, now)
			},
			wantDailies: 3,
		},
		{
			name: "MixedTzAndUtcLegacy",
			run: func(t *testing.T, e *distillHistoryE2E, now time.Time) stagedFixture {
				return recipeMixedTzAndUtcLegacy(t, e.primaryTeam, now)
			},
			wantDailies: 2,
		},
		{
			name: "MalformedFilename",
			run: func(t *testing.T, e *distillHistoryE2E, now time.Time) stagedFixture {
				return recipeMalformedFilename(t, e.primaryTeam, now)
			},
			wantDailies: 1,
			wantExtras:  1,
		},
		{
			name: "EmptyMarkerOnly",
			run: func(t *testing.T, e *distillHistoryE2E, now time.Time) stagedFixture {
				return recipeEmptyMarkerOnly(t, e.primaryTeam, now)
			},
			wantFacts: 1,
		},
		{
			name: "MultiTeamList",
			run: func(t *testing.T, e *distillHistoryE2E, now time.Time) stagedFixture {
				secondary := addSecondaryTeam(t, e, "team_read_e2e_t2", "Journal Read E2E T2", "journal-read-e2e-t2")
				return recipeMultiTeamList(t, e.primaryTeam, secondary, now)
			},
			wantDailies: 2,
		},
		{
			name: "AmbiguousPrefix",
			run: func(t *testing.T, e *distillHistoryE2E, now time.Time) stagedFixture {
				return recipeAmbiguousPrefix(t, e.primaryTeam, now)
			},
			wantDailies: 3,
		},
		{
			name: "BadFrontmatterNeighbor",
			run: func(t *testing.T, e *distillHistoryE2E, now time.Time) stagedFixture {
				return recipeBadFrontmatterNeighbor(t, e.primaryTeam, now)
			},
			wantDailies: 1,
			wantExtras:  1,
		},
		{
			name: "TwoConsecutiveUtcDays",
			run: func(t *testing.T, e *distillHistoryE2E, now time.Time) stagedFixture {
				return recipeTwoConsecutiveUtcDays(t, e.primaryTeam, now)
			},
			wantDailies: 2,
		},
		{
			name: "StaleFilenameRecentMtime",
			run: func(t *testing.T, e *distillHistoryE2E, now time.Time) stagedFixture {
				return recipeStaleFilenameRecentMtime(t, e.primaryTeam, now)
			},
			wantDailies: 1,
		},
		{
			name: "YesterdayOnly",
			run: func(t *testing.T, e *distillHistoryE2E, now time.Time) stagedFixture {
				return recipeYesterdayOnly(t, e.primaryTeam, now)
			},
			wantDailies: 1,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Capture now once at the top of the test. Recipes use this
			// for every time-relative computation so the fixture state
			// cannot drift across a day boundary mid-test.
			now := time.Now().UTC()

			e := setupDistillHistoryE2E(t, now)
			fx := tc.run(t, e, now)

			if got, want := len(fx.Dailies), tc.wantDailies; got != want {
				t.Fatalf("%s: dailies count = %d, want %d", tc.name, got, want)
			}
			if got, want := len(fx.Facts), tc.wantFacts; got != want {
				t.Fatalf("%s: facts count = %d, want %d", tc.name, got, want)
			}
			if got, want := len(fx.ExtraFiles), tc.wantExtras; got != want {
				t.Fatalf("%s: extra files count = %d, want %d", tc.name, got, want)
			}

			for _, d := range fx.Dailies {
				info, err := os.Stat(d.Path)
				if err != nil {
					t.Fatalf("%s: expected daily %s to exist: %v", tc.name, d.Path, err)
				}
				if !d.Mtime.IsZero() {
					// Modern filesystems (APFS, ext4, xfs) preserve sub-second
					// mtimes — 100 ms is loose enough to absorb os.Chtimes +
					// os.Stat round-tripping without hiding a second-rounding bug.
					const tol = 100 * time.Millisecond
					if delta := info.ModTime().Sub(d.Mtime); delta > tol || delta < -tol {
						t.Fatalf("%s: daily %s mtime = %v, want %v (delta %v)", tc.name, d.Path, info.ModTime(), d.Mtime, delta)
					}
				}
				// RelPath must land under memory/daily.
				if !strings.HasPrefix(d.Rel, "memory/daily/") {
					t.Fatalf("%s: daily rel %q not under memory/daily/", tc.name, d.Rel)
				}
				// The date prefix in the filename must match Date.
				name := filepath.Base(d.Path)
				if !strings.HasPrefix(name, d.Date+"-") {
					t.Fatalf("%s: daily %s does not start with date %s", tc.name, name, d.Date)
				}
				// Reading the file back must yield parseable frontmatter.
				data, err := os.ReadFile(d.Path)
				if err != nil {
					t.Fatalf("%s: read back daily %s: %v", tc.name, d.Path, err)
				}
				body := string(data)
				if !strings.HasPrefix(body, "---\n") {
					t.Fatalf("%s: daily %s missing frontmatter opener", tc.name, name)
				}
				if !strings.Contains(body, "\n---\n") {
					t.Fatalf("%s: daily %s missing frontmatter closer", tc.name, name)
				}
				if !strings.Contains(body, "# Daily Memory — "+d.Date) {
					t.Fatalf("%s: daily %s missing heading for date %s", tc.name, name, d.Date)
				}
			}

			for _, f := range fx.Facts {
				info, err := os.Stat(f.Path)
				if err != nil {
					t.Fatalf("%s: expected fact file %s to exist: %v", tc.name, f.Path, err)
				}
				if info.Size() == 0 {
					t.Fatalf("%s: fact file %s is zero bytes", tc.name, f.Path)
				}
				header, parsed, err := facts.ReadFacts(f.Path)
				if err != nil {
					t.Fatalf("%s: read back fact file %s: %v", tc.name, f.Path, err)
				}
				if header.Meta.SchemaVersion == "" {
					t.Fatalf("%s: fact file %s has no _meta header", tc.name, f.Path)
				}
				if len(parsed) != f.Facts {
					t.Fatalf("%s: fact file %s has %d body lines, want %d", tc.name, f.Path, len(parsed), f.Facts)
				}
			}

			for _, extra := range fx.ExtraFiles {
				if _, err := os.Stat(extra); err != nil {
					t.Fatalf("%s: expected extra file %s to exist: %v", tc.name, extra, err)
				}
			}
		})
	}
}

// TestDistillHistoryE2E_HarnessShape proves that setupDistillHistoryE2E itself
// produces a self-consistent harness before any recipe runs: workspace
// config files exist, the team context tree is populated, fake auth is
// in place, and the env slice carries the XDG reroute plus the test
// endpoint.
//
// Failure prevented: the harness silently stops wiring up one of its
// components (e.g. forgets to create memory/daily), which would make
// every recipe test vacuously pass because the subprocess never even
// starts.
func TestDistillHistoryE2E_HarnessShape(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	e := setupDistillHistoryE2E(t, now)

	// Binary exists and is executable.
	if info, err := os.Stat(e.oxBin); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("oxBin %s missing or not executable: err=%v", e.oxBin, err)
	}

	// Workspace .sageox files.
	mustExist(t, filepath.Join(e.workspace, ".sageox", "config.json"))
	mustExist(t, filepath.Join(e.workspace, ".sageox", "config.local.toml"))
	mustExist(t, filepath.Join(e.workspace, ".git"))
	mustExist(t, filepath.Join(e.workspace, "README.md"))

	// Team context subtree.
	for _, sub := range []string{"memory/daily", "memory/weekly", "memory/monthly", "memory/.github-facts", "memory/.session-facts", "memory/.discussion-facts"} {
		mustExist(t, filepath.Join(e.primaryTeam.path, sub))
	}

	// Fake auth.
	mustExist(t, filepath.Join(e.configHome, "sageox", "auth.json"))

	// config.local.toml references the primary team's path.
	data, err := os.ReadFile(filepath.Join(e.workspace, ".sageox", "config.local.toml"))
	if err != nil {
		t.Fatalf("read config.local.toml: %v", err)
	}
	if !strings.Contains(string(data), e.primaryTeam.path) {
		t.Fatalf("config.local.toml missing primary team path %s\n---\n%s", e.primaryTeam.path, string(data))
	}
	if !strings.Contains(string(data), e.primaryTeam.id) {
		t.Fatalf("config.local.toml missing primary team id %s\n---\n%s", e.primaryTeam.id, string(data))
	}

	// Env slice carries the reroute. We do not call testguard.MinimalEnv
	// here — it is applied inside testguard.RunOx — but we do assert the
	// raw slice contents so a regression that drops XDG_DATA_HOME fails
	// this test rather than silently pointing at the real home dir.
	wantEnvKeys := []string{
		"HOME=",
		"XDG_CONFIG_HOME=",
		"XDG_DATA_HOME=",
		"XDG_STATE_HOME=",
		"XDG_CACHE_HOME=",
		"XDG_RUNTIME_DIR=",
		"OX_XDG_ENABLE=1",
		"SAGEOX_ENDPOINT=https://test.sageox.ai",
	}
	for _, prefix := range wantEnvKeys {
		if !envHasPrefix(e.env, prefix) {
			t.Fatalf("env slice missing %q; have:\n%s", prefix, strings.Join(e.env, "\n"))
		}
	}
}

// TestDistillHistoryE2E_SecondaryTeamIsolated proves that addSecondaryTeam
// appends a second [[team_contexts]] row without clobbering the primary,
// lands the secondary team context under the same parent tempdir, and
// stays under the hermetic root. Guards against a regression where the
// helper overwrites config.local.toml instead of appending, or where it
// re-uses the primary team's directory and corrupts its fixtures.
func TestDistillHistoryE2E_SecondaryTeamIsolated(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	e := setupDistillHistoryE2E(t, now)
	secondary := addSecondaryTeam(t, e, "team_read_e2e_t2", "Journal Read E2E T2", "journal-read-e2e-t2")

	if secondary.path == e.primaryTeam.path {
		t.Fatalf("secondary reused primary path %s", secondary.path)
	}
	if filepath.Dir(secondary.path) != filepath.Dir(e.primaryTeam.path) {
		t.Fatalf("secondary %s not under shared teams root %s", secondary.path, filepath.Dir(e.primaryTeam.path))
	}
	mustExist(t, filepath.Join(secondary.path, "memory", "daily"))

	data, err := os.ReadFile(filepath.Join(e.workspace, ".sageox", "config.local.toml"))
	if err != nil {
		t.Fatalf("read config.local.toml: %v", err)
	}
	toml := string(data)
	if !strings.Contains(toml, e.primaryTeam.id) {
		t.Fatalf("config.local.toml lost primary team id\n%s", toml)
	}
	if !strings.Contains(toml, secondary.id) {
		t.Fatalf("config.local.toml missing secondary team id\n%s", toml)
	}
	if strings.Count(toml, "[[team_contexts]]") != 2 {
		t.Fatalf("config.local.toml should have 2 [[team_contexts]] rows, got:\n%s", toml)
	}
}
