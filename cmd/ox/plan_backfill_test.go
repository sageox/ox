package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// plan_backfill_test.go covers the cmd/ox layer of `ox plan backfill-titles`:
// dry-run output/no-op contract and the real-run apply+rename+commit
// orchestration. The retitle/rename/collision/idempotency computation itself
// is proven directly against internal/plan.ComputeBackfill/ApplyBackfillMeta
// in internal/plan/backfill_test.go — this file only proves the CLI plumbing
// wires those primitives together correctly, per the test pyramid split
// plan_lifecycle_test.go already documents for this command family.

// writeBackfillPlanFixture hand-writes a plan directory (plan.md, meta.json,
// events.jsonl) directly under ledgerPath/data/plans — the on-disk shape
// runPlanBackfillTitlesOnLedger reads — without going through plan.Save,
// since Save's ledger resolution goes through a package-private resolver
// tests outside internal/plan cannot override. buggyTopic simulates exactly
// what the pre-ox-1tjj.8 planTopic wrote: the first H2's text, not the H1's.
func writeBackfillPlanFixture(t *testing.T, ledgerPath, dirName, raw, buggyTopic string, createdAt time.Time) string {
	t.Helper()
	dir := filepath.Join(ledgerPath, "data", "plans", dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := plan.Meta{Topic: buggyTopic, Slug: plan.Slugify(buggyTopic), CreatedAt: createdAt}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), metaBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	ev := plan.Event{PlanID: "pln_backfilltest00000001", Kind: plan.EventCreated, Status: plan.PlanStatusDraft, Topic: buggyTopic}
	if err := plan.AppendEvent(context.Background(), dir, ev); err != nil {
		t.Fatalf("seed created event: %v", err)
	}
	return dir
}

// runBackfillForTest wires an isolated cobra.Command (own buffer, own
// context) and calls runPlanBackfillTitlesOnLedger directly against
// ledgerPath — the testable core, bypassing findGitRoot/config resolution.
func runBackfillForTest(t *testing.T, ledgerPath string, dryRun bool) (string, error) {
	t.Helper()
	return runBackfillForTestWithFlags(t, ledgerPath, dryRun, false)
}

// runBackfillForTestWithFlags is runBackfillForTest plus the --allow-diverged
// escape hatch, for the preflight-guard tests that need to exercise it.
func runBackfillForTestWithFlags(t *testing.T, ledgerPath string, dryRun, allowDiverged bool) (string, error) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := runPlanBackfillTitlesOnLedger(cmd, ledgerPath, dryRun, allowDiverged)
	return out.String(), err
}

const backfillTestRaw = "# Conversation model update — the execution plan\n\n" +
	"## 1. Context — Why Now\n\nframing prose.\n\n## Approach\n\ndo the thing.\n"

func TestRunPlanBackfillTitlesOnLedger_DryRunTouchesNothing(t *testing.T) {
	ledger := t.TempDir()
	dir := writeBackfillPlanFixture(t, ledger, "2026-05-01-1-context-why-now", backfillTestRaw, "1. Context — Why Now", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))

	metaBefore, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}

	out, err := runBackfillForTest(t, ledger, true)
	if err != nil {
		t.Fatalf("runPlanBackfillTitlesOnLedger dry-run: %v", err)
	}

	if !strings.Contains(out, "Dry run — nothing written.") {
		t.Errorf("output missing dry-run banner:\n%s", out)
	}
	if !strings.Contains(out, "total=1 retitled=1 renamed=1 skipped=0") {
		t.Errorf("output missing expected counts:\n%s", out)
	}
	if !strings.Contains(out, "Conversation model update") {
		t.Errorf("output missing the corrected title:\n%s", out)
	}
	if !strings.Contains(out, "retitled + renamed") {
		t.Errorf("output missing the retitled+renamed action:\n%s", out)
	}

	// Nothing on disk changed: same dir, same meta.json bytes, no new dir.
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("original plan dir was removed/renamed during a dry-run: %v", err)
	}
	metaAfter, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(metaBefore) != string(metaAfter) {
		t.Errorf("meta.json changed during a dry-run")
	}
	entries, err := os.ReadDir(filepath.Join(ledger, "data", "plans"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("plans dir has %d entries after dry-run, want 1 (no rename applied)", len(entries))
	}
}

func TestRunPlanBackfillTitlesOnLedger_DryRunNoPlansIsCleanNoOp(t *testing.T) {
	ledger := t.TempDir()
	out, err := runBackfillForTest(t, ledger, true)
	if err != nil {
		t.Fatalf("runPlanBackfillTitlesOnLedger on an empty ledger: %v", err)
	}
	if !strings.Contains(out, "total=0") {
		t.Errorf("output = %q, want total=0", out)
	}
}

// initGitLedger turns ledgerPath into a minimal git repo with one commit —
// the real-run path's git mv/add/commit needs a working git repository.
// Pushing is best-effort (see runPlanBackfillTitlesOnLedger's contract
// mirroring commitPlanToLedger/savePlanArtifacts) and is expected to fail
// here (no remote configured) without failing the command.
func initGitLedger(t *testing.T, ledgerPath string) {
	t.Helper()
	if testing.Short() {
		t.Skip("short: initializes and commits a real ledger repository")
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = ledgerPath
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(ledgerPath, ".gitignore"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "init")
}

// TestRunPlanBackfillTitlesOnLedger_RealRunRenamesAndCommits exercises the
// real (non-dry-run) path end to end against an actual git repo: the plan
// directory is renamed on disk, its meta.json is corrected, and a batch
// commit lands — all without a network push succeeding (best-effort, must
// not fail the command).
func TestRunPlanBackfillTitlesOnLedger_RealRunRenamesAndCommits(t *testing.T) {
	ledger := t.TempDir()
	oldDir := writeBackfillPlanFixture(t, ledger, "2026-05-01-1-context-why-now", backfillTestRaw, "1. Context — Why Now", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	sessionDir := writeBackfillSessionFixture(t, ledger, "2026-05-03-sess-Ox0001", []string{"1-context-why-now"})
	initGitLedger(t, ledger)

	out, err := runBackfillForTest(t, ledger, false)
	if err != nil {
		t.Fatalf("runPlanBackfillTitlesOnLedger real run: %v", err)
	}
	if !strings.Contains(out, "Backfilled: total=1 retitled=1 renamed=1 skipped=0") {
		t.Errorf("output missing expected summary:\n%s", out)
	}

	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Errorf("old plan dir still exists after a real run: stat err=%v", err)
	}
	newDir := filepath.Join(ledger, "data", "plans", "2026-05-01-conversation-model-update-the-execution-plan")
	metaBytes, err := os.ReadFile(filepath.Join(newDir, "meta.json"))
	if err != nil {
		t.Fatalf("renamed plan dir missing meta.json: %v", err)
	}
	var meta plan.Meta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Topic != "Conversation model update — the execution plan" {
		t.Errorf("meta.Topic = %q, want the corrected H1 title", meta.Topic)
	}
	// A failed push retains the coherent local commit, including its references.
	sessionMeta, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	require.Equal(t, []string{"conversation-model-update-the-execution-plan"}, sessionMeta.ProducedPlans)
	require.Contains(t, runGitInDir(t, ledger, "show", "HEAD:sessions/"+filepath.Base(sessionDir)+"/meta.json"), `"conversation-model-update-the-execution-plan"`)

	// The rename + edit landed in a real git commit (whether or not the
	// best-effort push to a nonexistent remote succeeded).
	logCmd := exec.Command("git", "log", "--oneline")
	logCmd.Dir = ledger
	logOut, err := logCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, logOut)
	}
	if !strings.Contains(string(logOut), "plan: backfill 1 title") {
		t.Errorf("git log missing the batch commit:\n%s", logOut)
	}
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = ledger
	statusOut, err := statusCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, statusOut)
	}
	if strings.TrimSpace(string(statusOut)) != "" {
		t.Errorf("working tree not clean after the batch commit:\n%s", statusOut)
	}
}

// TestRunPlanBackfillTitlesOnLedger_RealRunIdempotent proves a second real
// run on an already-backfilled ledger is a clean no-op: no further rename,
// no new commit.
func TestRunPlanBackfillTitlesOnLedger_RealRunIdempotent(t *testing.T) {
	ledger := t.TempDir()
	writeBackfillPlanFixture(t, ledger, "2026-05-01-1-context-why-now", backfillTestRaw, "1. Context — Why Now", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	initGitLedger(t, ledger)

	if _, err := runBackfillForTest(t, ledger, false); err != nil {
		t.Fatalf("first real run: %v", err)
	}
	// exec.Cmd runs exactly once — a fresh Command is needed for each git log
	// call, not a reused one.
	gitLog := func() string {
		t.Helper()
		cmd := exec.Command("git", "log", "--oneline")
		cmd.Dir = ledger
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git log: %v\n%s", err, out)
		}
		return string(out)
	}
	before := gitLog()

	out, err := runBackfillForTest(t, ledger, false)
	if err != nil {
		t.Fatalf("second real run: %v", err)
	}
	if !strings.Contains(out, "Nothing to backfill") {
		t.Errorf("second run output = %q, want the no-op message", out)
	}

	after := gitLog()
	if before != after {
		t.Errorf("a no-op second run created a new commit:\nbefore: %s\nafter:  %s", before, after)
	}
}

// writeBackfillSessionFixture hand-writes a sessions/<name>/meta.json with
// the given produced_plans, the on-disk shape ComputeProducedPlansRewrite /
// ApplyProducedPlansRewrite read and write.
func writeBackfillSessionFixture(t *testing.T, ledgerPath, name string, producedPlans []string) string {
	t.Helper()
	dir := filepath.Join(ledgerPath, "sessions", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := lfs.NewSessionMeta(name, "Person A", "Ox0001", "claude-code", time.Now().UTC()).
		ProducedPlans(producedPlans).
		Build()
	if err := lfs.WriteSessionMetaOnly(dir, meta); err != nil {
		t.Fatalf("write session meta: %v", err)
	}
	return dir
}

// corruptEventsLog appends a malformed line to dir/events.jsonl so a
// subsequent LoadEvents call fails to parse it — the trigger
// appendBackfillRevisedEvent's finding-5 fix reacts to.
func corruptEventsLog(t *testing.T, dir string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("not valid json\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestRunPlanBackfillTitlesOnLedger_FailedApplyExcludesSlugFromSessionRewrite
// is the finding-1 regression: when ApplyBackfillMeta fails for one
// candidate in a batch (here: a corrupted events.jsonl makes
// appendBackfillRevisedEvent fail), that plan is never renamed on disk — and
// a session's produced_plans entry naming its OLD slug must NOT be rewritten
// to the NewSlug it was never actually renamed to. A sibling candidate in
// the same batch that DOES apply cleanly must still get its own session
// reference fixed, proving the exclusion is targeted, not a batch-wide bail.
func TestRunPlanBackfillTitlesOnLedger_FailedApplyExcludesSlugFromSessionRewrite(t *testing.T) {
	ledger := t.TempDir()

	goodDir := writeBackfillPlanFixture(t, ledger, "2026-05-01-1-context-why-now", backfillTestRaw, "1. Context — Why Now", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	const goodNewSlug = "conversation-model-update-the-execution-plan"

	brokenRaw := "# Add Login Support\n\n## TL;DR\n\nShip it.\n\n## Approach\n\ndo it.\n"
	brokenDir := writeBackfillPlanFixture(t, ledger, "2026-05-02-tl-dr", brokenRaw, "TL;DR", time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC))
	corruptEventsLog(t, brokenDir)

	sessionDir := writeBackfillSessionFixture(t, ledger, "2026-05-03-sess-Ox0001", []string{"1-context-why-now", "tl-dr"})

	initGitLedger(t, ledger)

	if _, err := runBackfillForTest(t, ledger, false); err != nil {
		t.Fatalf("real run: %v", err)
	}

	// goodDir got renamed; brokenDir's apply failed, so it must be left at
	// its ORIGINAL path — never renamed, despite ComputeBackfill proposing
	// a NewDir for it same as any other slug-changing candidate.
	if _, statErr := os.Stat(goodDir); !os.IsNotExist(statErr) {
		t.Errorf("goodDir still exists at its old path after a successful apply: stat err=%v", statErr)
	}
	if _, statErr := os.Stat(brokenDir); statErr != nil {
		t.Errorf("brokenDir was renamed/removed despite its apply failing: %v", statErr)
	}

	got, err := lfs.ReadSessionMeta(sessionDir)
	if err != nil {
		t.Fatalf("ReadSessionMeta: %v", err)
	}
	if len(got.ProducedPlans) != 2 {
		t.Fatalf("ProducedPlans = %v, want 2 entries", got.ProducedPlans)
	}
	want := map[string]bool{goodNewSlug: false, "tl-dr": false}
	for _, slug := range got.ProducedPlans {
		if _, ok := want[slug]; !ok {
			t.Errorf("unexpected produced_plans entry %q (got %v)", slug, got.ProducedPlans)
			continue
		}
		want[slug] = true
	}
	if !want[goodNewSlug] {
		t.Errorf("produced_plans missing the rewritten slug %q for the successfully applied plan: got %v", goodNewSlug, got.ProducedPlans)
	}
	if !want["tl-dr"] {
		t.Errorf("produced_plans no longer contains the stale slug %q — it must stay untouched since that plan's apply failed: got %v", "tl-dr", got.ProducedPlans)
	}
}

// TestRunPlanBackfillTitlesOnLedger_PreCommitFailureFailsCommandAndSkipsSummary
// is the finding-3/6 regression: a failure at or before `git commit` (here:
// every git operation is blocked by a stale .git/index.lock, so `git mv` —
// the FIRST git call the real run makes — fails before anything is
// committed) must make the command return a non-nil error and must NOT
// print the "Backfilled: ..." success summary. Contrast with
// TestRunPlanBackfillTitlesOnLedger_RealRunRenamesAndCommits, where a
// push-only failure (no remote configured) still returns nil and prints the
// summary — this is the case that must NOT be treated the same way.
func TestRunPlanBackfillTitlesOnLedger_PreCommitFailureFailsCommandAndSkipsSummary(t *testing.T) {
	ledger := t.TempDir()
	writeBackfillPlanFixture(t, ledger, "2026-05-01-1-context-why-now", backfillTestRaw, "1. Context — Why Now", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	initGitLedger(t, ledger)

	lockPath := filepath.Join(ledger, ".git", "index.lock")
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runBackfillForTest(t, ledger, false)
	if rmErr := os.Remove(lockPath); rmErr != nil && !os.IsNotExist(rmErr) {
		t.Fatalf("cleanup index.lock: %v", rmErr)
	}

	if err == nil {
		t.Fatalf("expected a non-nil error when every git operation is blocked; output:\n%s", out)
	}
	if strings.Contains(out, "Backfilled:") {
		t.Errorf("output printed the success summary despite the commit never landing:\n%s", out)
	}

	logOut := runGitInDir(t, ledger, "log", "--oneline")
	if strings.Count(strings.TrimSpace(logOut), "\n") != 0 {
		t.Errorf("expected only the init commit (nothing landed), got:\n%s", logOut)
	}
}

// Failed repairs must not leave references that an ordinary session upload
// can publish without the plan rename. Exercise failures before and after staging.
func TestPlanBackfillFailureKeepsPublishedReferencesValid(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real ledger commits and pushes")
	}
	for _, failure := range []string{"second rename", "commit"} {
		t.Run(failure, func(t *testing.T) {
			root := newPlanCaptureTestRepo(t)
			ledger, remote := newSharedLedger(t, root, true)
			oldName := "2026-05-01-1-context-why-now"
			newName := "2026-05-01-conversation-model-update-the-execution-plan"
			created := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
			writeBackfillPlanFixture(t, ledger, oldName, backfillTestRaw, "1. Context — Why Now", created)
			sessionName := "2026-05-03-sess-Ox0001"
			sessionDir := writeBackfillSessionFixture(t, ledger, sessionName, []string{"1-context-why-now"})
			secondSessionDir := writeBackfillSessionFixture(t, ledger, "2026-05-03-sess-Ox0003", []string{"1-context-why-now"})
			require.NoError(t, os.WriteFile(filepath.Join(ledger, "sessions", ".gitignore"), []byte("*.needs-summary\n"), 0o644))
			runGitInDir(t, ledger, "add", "--", "data", "sessions")
			runGitInDir(t, ledger, "commit", "-m", "saved plan and session")
			runGitInDir(t, ledger, "push", "origin", "main")

			meta, err := lfs.ReadSessionMeta(sessionDir)
			require.NoError(t, err)
			meta.Title = "Staged title"
			require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, meta))
			rel := "sessions/" + sessionName + "/meta.json"
			runGitInDir(t, ledger, "add", "--", rel)
			staged := runGitInDir(t, ledger, "rev-parse", ":"+rel)
			meta.Title = "Working title"
			require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, meta))
			working, err := os.ReadFile(filepath.Join(sessionDir, "meta.json"))
			require.NoError(t, err)
			secondWorking, err := os.ReadFile(filepath.Join(secondSessionDir, "meta.json"))
			require.NoError(t, err)
			secondRel := "sessions/" + filepath.Base(secondSessionDir) + "/meta.json"
			secondStaged := runGitInDir(t, ledger, "rev-parse", ":"+secondRel)

			lockPath := filepath.Join(ledger, ".git", "refs", "heads", "main.lock")
			if failure == "second rename" {
				// A save that failed before staging leaves a real, untracked plan.
				writeBackfillPlanFixture(t, ledger, "2026-05-02-tl-dr", "# Second Plan\n\n## TL;DR\n\nBody.\n", "TL;DR", created.Add(24*time.Hour))
			} else {
				// The ref lock allows staging but prevents the final commit.
				require.NoError(t, os.WriteFile(lockPath, nil, 0o644))
			}
			_, err = runBackfillForTest(t, ledger, false)
			require.ErrorIs(t, err, errBackfillCommitFailed)
			if failure == "second rename" {
				require.ErrorContains(t, err, "git mv")
			} else {
				require.ErrorContains(t, err, "main.lock")
				require.NoError(t, os.Remove(lockPath))
			}
			after, err := os.ReadFile(filepath.Join(sessionDir, "meta.json"))
			require.NoError(t, err)
			require.Equal(t, string(working), string(after), "working metadata must survive the failed repair")
			require.Equal(t, staged, runGitInDir(t, ledger, "rev-parse", ":"+rel), "restore the staged version, not HEAD")
			secondAfter, err := os.ReadFile(filepath.Join(secondSessionDir, "meta.json"))
			require.NoError(t, err)
			require.Equal(t, string(secondWorking), string(secondAfter))
			require.Equal(t, secondStaged, runGitInDir(t, ledger, "rev-parse", ":"+secondRel))
			for _, dir := range []string{sessionDir, secondSessionDir} {
				require.NoError(t, lfs.MutateSessionMeta(context.Background(), dir, func(*lfs.SessionMeta) (*lfs.SessionMeta, error) {
					return nil, nil // the failure must release every metadata lock
				}))
			}

			otherName := "2026-05-03-other-Ox0002"
			writeBackfillSessionFixture(t, ledger, otherName, nil)
			require.NoError(t, commitAndPushLedger(ledger, otherName))
			published := runGitInDir(t, remote, "show", "main:"+rel)
			require.Contains(t, published, `"1-context-why-now"`)
			require.Contains(t, published, "Staged title")
			require.NotEmpty(t, runGitInDir(t, remote, "ls-tree", "--name-only", "main", "--", "data/plans/"+oldName))
			require.Empty(t, runGitInDir(t, remote, "ls-tree", "--name-only", "main", "--", "data/plans/"+newName))
			require.NotEmpty(t, runGitInDir(t, remote, "show", "main:sessions/"+otherName+"/meta.json"))
			require.Contains(t, runGitInDir(t, remote, "show", "main:"+secondRel), `"1-context-why-now"`)
		})
	}
}

// A process error after Git advances HEAD must not undo the committed references.
func TestPlanBackfillKeepsReferencesWhenCommitLandsBeforeInterruption(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real ledger commits and pushes")
	}
	if runtime.GOOS == "windows" {
		t.Skip("post-commit interruption fixture requires POSIX signals")
	}
	root := newPlanCaptureTestRepo(t)
	ledger, remote := newSharedLedger(t, root, true)
	writeBackfillPlanFixture(t, ledger, "2026-05-01-1-context-why-now", backfillTestRaw, "1. Context — Why Now", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	name := "2026-05-03-sess-Ox0001"
	dir := writeBackfillSessionFixture(t, ledger, name, []string{"1-context-why-now"})
	require.NoError(t, os.WriteFile(filepath.Join(ledger, "sessions", ".gitignore"), []byte("*.needs-summary\n"), 0o644))
	runGitInDir(t, ledger, "add", "--", "data", "sessions")
	runGitInDir(t, ledger, "commit", "-m", "saved plan and session")
	runGitInDir(t, ledger, "push", "origin", "main")
	hook := filepath.Join(ledger, ".git", "hooks", "post-commit")
	marker := filepath.Join(ledger, ".git", "interrupted-backfill")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\ncase \"$(git log -1 --format=%s)\" in\n  'plan: backfill '*) touch .git/interrupted-backfill; kill -TERM \"$PPID\";;\nesac\n"), 0o755))
	_, err := runBackfillForTest(t, ledger, false)
	require.NoError(t, err)
	require.FileExists(t, marker, "the interruption must actually fire")
	require.NoError(t, os.Remove(hook))
	meta, err := lfs.ReadSessionMeta(dir)
	require.NoError(t, err)
	require.Equal(t, []string{"conversation-model-update-the-execution-plan"}, meta.ProducedPlans)
	require.NoError(t, commitAndPushLedger(ledger, name))
	require.Contains(t, runGitInDir(t, remote, "show", "main:sessions/"+name+"/meta.json"), `"conversation-model-update-the-execution-plan"`)
	require.NotEmpty(t, runGitInDir(t, remote, "ls-tree", "--name-only", "main", "--", "data/plans/2026-05-01-conversation-model-update-the-execution-plan"))
	require.Empty(t, runGitInDir(t, ledger, "status", "--porcelain"))
}
