package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/plan"
	"github.com/stretchr/testify/require"
)

// plan_commit_test.go covers commitPlanBackfillToLedger's failure routing:
// a failure at or before `git commit` must be distinguishable from a
// push-only failure (see errBackfillCommitFailed in plan_commit.go), since
// the caller (runPlanBackfillTitlesOnLedger) treats the two very
// differently — one is a hard command failure, the other a best-effort
// warning. Reuses initGitLedger from plan_backfill_test.go (same package).

// TestCommitPlanBackfillToLedger_PreCommitGitFailureIsErrBackfillCommitFailed
// forces `git mv` to fail (source path was never created) — a stand-in for
// any pre-commit git failure — and verifies the returned error satisfies
// errors.Is(err, errBackfillCommitFailed).
func TestCommitPlanBackfillToLedger_PreCommitGitFailureIsErrBackfillCommitFailed(t *testing.T) {
	ledger := t.TempDir()
	initGitLedger(t, ledger)

	bogusOld := filepath.Join(ledger, "data", "plans", "2026-01-01-does-not-exist")
	bogusNew := filepath.Join(ledger, "data", "plans", "2026-01-01-renamed")

	err := commitPlanBackfillToLedger(ledger, [][2]string{{bogusOld, bogusNew}}, nil, nil)
	if err == nil {
		t.Fatal("expected an error: git mv on a path that was never created")
	}
	if !errors.Is(err, errBackfillCommitFailed) {
		t.Errorf("error = %v, want errors.Is(err, errBackfillCommitFailed)", err)
	}

	// Nothing must have been committed — HEAD is still just the init commit.
	out := runGitInDir(t, ledger, "log", "--oneline")
	if strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Errorf("expected only the init commit after a pre-commit failure, got:\n%s", out)
	}
}

// TestCommitPlanBackfillToLedger_PushOnlyFailureIsNotErrBackfillCommitFailed
// is the negative case finding 3 explicitly requires: a real, valid commit
// (mv/add/commit all succeed) followed by a push failure (no remote
// configured) must return an error that does NOT match
// errBackfillCommitFailed, and the commit must have actually landed.
func TestCommitPlanBackfillToLedger_PushOnlyFailureIsNotErrBackfillCommitFailed(t *testing.T) {
	ledger := t.TempDir()
	initGitLedger(t, ledger)

	dir := filepath.Join(ledger, "data", "plans", "2026-01-01-real")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"topic":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	err := commitPlanBackfillToLedger(ledger, nil, []string{dir}, nil)
	if err == nil {
		t.Fatal("expected a push failure: no remote is configured for this ledger")
	}
	if errors.Is(err, errBackfillCommitFailed) {
		t.Errorf("push-only failure incorrectly matched errBackfillCommitFailed: %v", err)
	}

	out := runGitInDir(t, ledger, "log", "--oneline")
	if !strings.Contains(out, "plan: backfill 1 title") {
		t.Errorf("commit did not land locally despite this being a push-only failure:\n%s", out)
	}
	status := runGitInDir(t, ledger, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Errorf("working tree not clean after a successful commit (push-only failure): %q", status)
	}
}

// Backfill owns its rename and edits, even when another operation staged work first.
func TestPlanBackfillLeavesUnrelatedChangesStaged(t *testing.T) {
	ledger := t.TempDir()
	oldDir := writeBackfillPlanFixture(t, ledger, "2026-05-01-old", backfillTestRaw, "Old", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	initGitLedger(t, ledger)
	newDir := filepath.Join(filepath.Dir(oldDir), "2026-05-01-new")
	if err := os.WriteFile(filepath.Join(ledger, "unrelated.md"), []byte("staged for another operation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, ledger, "add", "--", "unrelated.md")
	err := commitPlanBackfillToLedger(ledger, [][2]string{{oldDir, newDir}}, nil, nil)
	if errors.Is(err, errBackfillCommitFailed) {
		t.Fatal(err)
	}
	changed := runGitInDir(t, ledger, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	if strings.Contains(changed, "unrelated.md") {
		t.Errorf("backfill committed unrelated staged work:\n%s", changed)
	}
	if !strings.Contains(changed, "data/plans/2026-05-01-old/plan.md") || !strings.Contains(changed, "data/plans/2026-05-01-new/plan.md") {
		t.Errorf("backfill commit omitted old or new rename paths:\n%s", changed)
	}
	if got := strings.TrimSpace(runGitInDir(t, ledger, "diff", "--cached", "--name-only")); got != "unrelated.md" {
		t.Errorf("remaining staged changes = %q, want unrelated.md", got)
	}
}

// A migration may rename more than five sacred files without losing their originals.
func TestPlanBackfillPreservesOriginalsForLargeRenameBatch(t *testing.T) {
	ledger := t.TempDir()
	var renames [][2]string
	pointer := []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("a", 64) + "\nsize 12345\n")
	for _, name := range []string{"first", "second"} {
		old := writeBackfillPlanFixture(t, ledger, "2026-05-01-"+name, backfillTestRaw, "Old", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
		if err := os.WriteFile(filepath.Join(old, "plan.html"), pointer, 0o644); err != nil {
			t.Fatal(err)
		}
		renames = append(renames, [2]string{old, old + "-renamed"})
	}
	initGitLedger(t, ledger)
	var originals []string
	for _, pair := range renames {
		originals = append(originals, runGitInDir(t, ledger, "show", "HEAD:data/plans/"+filepath.Base(pair[0])+"/meta.json"))
		cacheDir := filepath.Join(ledger, ".sageox", "cache", "data", "plans", filepath.Base(pair[0]))
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cacheDir, "plan.html"), []byte("hydrated plan page"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := plan.ApplyBackfillMeta(context.Background(), pair[0], "Corrected title", "corrected-title"); err != nil {
			t.Fatal(err)
		}
	}
	if err := commitPlanBackfillToLedger(ledger, renames, nil, nil); errors.Is(err, errBackfillCommitFailed) {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runGitInDir(t, ledger, "diff-tree", "--no-commit-id", "--name-only", "-r", "--diff-filter=D", "HEAD")); strings.Count(got, "\n")+1 != 8 {
		t.Fatalf("expected eight old paths removed by migration, got:\n%s", got)
	}
	for i, pair := range renames {
		cached, err := os.ReadFile(filepath.Join(ledger, ".sageox", "cache", "data", "plans", filepath.Base(pair[0]), "meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		if string(cached) != originals[i] {
			t.Error("cache must preserve original HEAD metadata, not the already-edited worktree")
		}
		hydrated, err := os.ReadFile(filepath.Join(ledger, ".sageox", "cache", "data", "plans", filepath.Base(pair[0]), "plan.html"))
		if err != nil || string(hydrated) != "hydrated plan page" {
			t.Errorf("unchanged pointer's hydrated cache must survive migration: %q, %v", hydrated, err)
		}
		if got := runGitInDir(t, ledger, "show", "HEAD:data/plans/"+filepath.Base(pair[1])+"/meta.json"); !strings.Contains(got, "Corrected title") {
			t.Errorf("renamed plan metadata was not committed:\n%s", got)
		}
	}
}

// A conflicting preservation copy must stop the batch before any plan moves.
func TestPlanBackfillCacheMismatchLeavesSourceInPlace(t *testing.T) {
	ledger := t.TempDir()
	old := writeBackfillPlanFixture(t, ledger, "2026-05-01-old", backfillTestRaw, "Old", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	initGitLedger(t, ledger)
	before := runGitInDir(t, ledger, "rev-parse", "HEAD")
	cacheDir := filepath.Join(ledger, ".sageox", "cache", "data", "plans", filepath.Base(old))
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "meta.json"), []byte("different saved content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := plan.ApplyBackfillMeta(context.Background(), old, "Corrected title", "corrected-title"); err != nil {
		t.Fatal(err)
	}
	err := commitPlanBackfillToLedger(ledger, [][2]string{{old, old + "-renamed"}}, nil, nil)
	if !errors.Is(err, errBackfillCommitFailed) || !strings.Contains(err.Error(), "cache copy") {
		t.Fatalf("expected preservation refusal, got %v", err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("source must remain in place after preservation refusal: %v", err)
	}
	if got := runGitInDir(t, ledger, "rev-parse", "HEAD"); got != before {
		t.Error("preservation refusal unexpectedly committed")
	}
}

// In-place title/reference repairs own metadata, not other edits in those directories.
func TestPlanBackfillInPlaceLeavesOtherFilesStaged(t *testing.T) {
	ledger := t.TempDir()
	planDir := writeBackfillPlanFixture(t, ledger, "2026-05-01-in-place", backfillTestRaw, "Old", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	sessionDir := writeBackfillSessionFixture(t, ledger, "example", []string{"old"})
	initGitLedger(t, ledger)
	if err := os.WriteFile(filepath.Join(planDir, "plan.md"), []byte("author's unsaved revision\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "notes.md"), []byte("another operation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, ledger, "add", "--", "data/plans/2026-05-01-in-place/plan.md", "sessions/example/notes.md")
	if err := plan.ApplyBackfillMeta(context.Background(), planDir, "Corrected title", "in-place"); err != nil {
		t.Fatal(err)
	}
	if err := plan.ApplyProducedPlansRewrite(context.Background(), sessionDir, map[string]string{"old": "new"}); err != nil {
		t.Fatal(err)
	}
	if err := commitPlanBackfillToLedger(ledger, nil, []string{planDir}, []string{sessionDir}); errors.Is(err, errBackfillCommitFailed) {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runGitInDir(t, ledger, "diff", "--cached", "--name-only")); got != "data/plans/2026-05-01-in-place/plan.md\nsessions/example/notes.md" {
		t.Errorf("unrelated changes must stay staged:\n%s", got)
	}
}

func TestPlanBackfillLeavesLargePlainHTMLLocal(t *testing.T) {
	for _, rename := range []bool{false, true} {
		for _, tracked := range []bool{false, true} {
			for _, staged := range []bool{false, true} {
				t.Run(fmt.Sprintf("rename=%t/tracked=%t/staged=%t", rename, tracked, staged), func(t *testing.T) {
					ledger := t.TempDir()
					old := writeBackfillPlanFixture(t, ledger, "2026-05-01-old", backfillTestRaw, "Old", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
					pointer := "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("a", 64) + "\nsize 2000000\n"
					if tracked {
						require.NoError(t, os.WriteFile(filepath.Join(old, planHTMLFileName), []byte(pointer), 0o644))
					}
					initGitLedger(t, ledger)
					pending := strings.Repeat("pending HTML\n", 100000)
					require.NoError(t, os.WriteFile(filepath.Join(old, planHTMLFileName), []byte(pending), 0o644))
					if staged {
						runGitInDir(t, ledger, "add", "--", "data/plans/2026-05-01-old/plan.html")
					}
					// Hydration caches need not contain the pointer's literal bytes.
					cache := filepath.Join(ledger, ".sageox", "cache", "data", "plans", filepath.Base(old), planHTMLFileName)
					require.NoError(t, os.MkdirAll(filepath.Dir(cache), 0o700))
					require.NoError(t, os.WriteFile(cache, []byte("hydrated old HTML"), 0o600))
					require.NoError(t, plan.ApplyBackfillMeta(context.Background(), old, "Corrected title", "new"))
					dest := old
					var renames [][2]string
					touched := []string{old}
					if rename {
						dest = old + "-renamed"
						renames = [][2]string{{old, dest}}
						touched = nil
					}
					err := commitPlanBackfillToLedger(ledger, renames, touched, nil)
					require.NotErrorIs(t, err, errBackfillCommitFailed, "only the missing remote may fail")
					rel := "data/plans/" + filepath.Base(dest)
					require.Contains(t, runGitInDir(t, ledger, "show", "HEAD:"+rel+"/meta.json"), "Corrected title")
					if tracked {
						require.Equal(t, strings.TrimSpace(pointer), runGitInDir(t, ledger, "show", "HEAD:"+rel+"/plan.html"))
					} else {
						require.Empty(t, runGitInDir(t, ledger, "ls-tree", "--name-only", "HEAD", "--", rel+"/plan.html"))
					}
					working, err := os.ReadFile(filepath.Join(dest, planHTMLFileName))
					require.NoError(t, err)
					require.Equal(t, pending, string(working), "pending HTML must survive the backfill")
					cached, err := os.ReadFile(cache)
					require.NoError(t, err)
					require.Equal(t, "hydrated old HTML", string(cached))
				})
			}
		}
	}
}

// A refused migration must preserve user files and staged work, including
// when a later candidate fails after an earlier candidate passed preflight.
func TestPlanBackfillFailuresContainUserChanges(t *testing.T) {
	for _, tc := range []struct {
		name     string
		variants []string
		wantErr  string
	}{
		{"outside source", []string{"absolute", "symlink"}, "not inside ledger"},
		{"outside destination", []string{"absolute", "symlink"}, "not inside ledger"},
		{"outside touched directory", []string{"plan", "session", "plan symlink", "session symlink"}, "not inside ledger"},
		{"missing destination parent", []string{"later candidate"}, "resolve rename destination"},
		{"missing tracked source artifact", []string{"later candidate"}, "read updated plan"},
		{"untracked source", []string{"git mv refusal"}, "git mv"},
	} {
		for _, variant := range tc.variants {
			t.Run(tc.name+"/"+variant, func(t *testing.T) {
				ledger := t.TempDir()
				created := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
				oldDir := writeBackfillPlanFixture(t, ledger, "2026-05-01-old", backfillTestRaw, "Old", created)
				secondDir := writeBackfillPlanFixture(t, ledger, "2026-05-01-second", backfillTestRaw, "Old", created)
				initGitLedger(t, ledger)
				head := runGitInDir(t, ledger, "rev-parse", "HEAD")
				const unrelated = "another operation's staged bytes\n"
				if err := os.WriteFile(filepath.Join(ledger, "unrelated.md"), []byte(unrelated), 0o644); err != nil {
					t.Fatal(err)
				}
				runGitInDir(t, ledger, "add", "--", "unrelated.md")
				stagedUnrelated := runGitInDir(t, ledger, "rev-parse", ":unrelated.md")
				outsideRoot := t.TempDir()
				outsideDir := writeBackfillPlanFixture(t, outsideRoot, "outside", backfillTestRaw, "Outside", created)
				outsidePath := outsideDir
				if strings.Contains(variant, "symlink") {
					outsidePath = filepath.Join(ledger, "data", "plans", "outside-link")
					if err := os.Symlink(outsideDir, outsidePath); err != nil {
						t.Skipf("symlink containment requires symlink support: %v", err)
					}
				}
				newDir := oldDir + "-renamed"
				renames := [][2]string{{oldDir, newDir}}
				var touchedPlans, touchedSessions []string
				sourceToPreserve := oldDir
				switch tc.name {
				case "outside source":
					renames[0][0] = outsidePath
				case "outside destination":
					renames[0][1] = filepath.Join(outsidePath, "renamed")
				case "outside touched directory":
					if strings.HasPrefix(variant, "plan") {
						touchedPlans = []string{outsidePath}
					} else {
						touchedSessions = []string{outsidePath}
					}
				case "missing destination parent":
					renames = append(renames, [2]string{secondDir, filepath.Join(ledger, "missing-parent", "renamed")})
				case "missing tracked source artifact":
					if err := os.Remove(filepath.Join(secondDir, "plan.md")); err != nil {
						t.Fatal(err)
					}
					renames = append(renames, [2]string{secondDir, secondDir + "-renamed"})
				case "untracked source":
					sourceToPreserve = writeBackfillPlanFixture(t, ledger, "2026-05-02-untracked", backfillTestRaw, "Untracked", created)
					renames[0] = [2]string{sourceToPreserve, sourceToPreserve + "-renamed"}
				}
				protected := []string{
					filepath.Join(sourceToPreserve, "meta.json"),
					filepath.Join(outsideDir, "meta.json"),
					filepath.Join(outsideDir, "events.jsonl"),
					filepath.Join(outsideDir, "plan.md"),
				}
				before := make([][]byte, len(protected))
				for i, path := range protected {
					var err error
					before[i], err = os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
				}
				err := commitPlanBackfillToLedger(ledger, renames, touchedPlans, touchedSessions)
				if !errors.Is(err, errBackfillCommitFailed) || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected contained %s failure, got %v", tc.name, err)
				}
				if got := runGitInDir(t, ledger, "rev-parse", "HEAD"); got != head {
					t.Error("refused migration changed HEAD")
				}
				if got := runGitInDir(t, ledger, "rev-parse", ":unrelated.md"); got != stagedUnrelated {
					t.Errorf("unrelated staged blob was changed: %s", got)
				}
				if got := runGitInDir(t, ledger, "diff", "--cached", "--name-only", "--", "data/plans", "sessions"); got != "" {
					t.Errorf("refused migration staged user files: %s", got)
				}
				for i, path := range protected {
					after, err := os.ReadFile(path)
					if err != nil || string(after) != string(before[i]) {
						t.Errorf("refused migration changed protected file %s: %v", path, err)
					}
				}
				if _, err := os.Stat(oldDir); err != nil {
					t.Errorf("original plan directory must survive: %v", err)
				}
				if _, err := os.Stat(newDir); !os.IsNotExist(err) {
					t.Errorf("a refused migration must not move the first plan: %v", err)
				}
				if tc.name == "missing tracked source artifact" {
					if got := runGitInDir(t, ledger, "show", "HEAD:data/plans/2026-05-01-second/plan.md"); got != strings.TrimSpace(backfillTestRaw) {
						t.Error("missing source artifact must remain recoverable from HEAD")
					}
				}
			})
		}
	}
}
