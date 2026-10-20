//go:build !short

package main

// doctor_ledger_plans_test.go: a plain `ox doctor` never commits a plan file.
// Its automatic cleanup commits leave data/plans/ to the plan's own writers
// (#1409).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/plan"
)

const (
	editedPlanDir  = "data/plans/2026-10-01-edited"  // committed, then edited and not saved
	pendingPlanDir = "data/plans/2026-10-02-pending" // a save whose plan.html upload failed
)

// commitLedgerFiles writes files (slash path to body) in ledger, commits them
// and pushes.
func commitLedgerFiles(t *testing.T, ledger string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		writeLedgerFile(t, ledger, rel, body)
		runGitInDir(t, ledger, "add", rel)
	}
	runGitInDir(t, ledger, "commit", "-q", "-m", "seed")
	runGitInDir(t, ledger, "push", "-q")
}

// plansLedger is a Ledger with a bare origin holding the plan at editedPlanDir.
func plansLedger(t *testing.T) (ledger, origin string) {
	t.Helper()
	ledger = reviewTestLedger(t)
	origin = strings.TrimSpace(runGitInDir(t, ledger, "remote", "get-url", "origin"))
	commitLedgerFiles(t, ledger, map[string]string{
		editedPlanDir + "/plan.md":   "# Edited\n",
		editedPlanDir + "/meta.json": `{"topic":"Edited"}` + "\n",
	})
	return ledger, origin
}

// leavePlanEdits leaves what doctor must not commit: an unsaved plan.md edit
// and a meta.json edit someone already staged in editedPlanDir, in
// pendingPlanDir a large plan.html left plain, as a failed upload leaves it,
// with the save's meta.json, and a Finder .DS_Store directly under
// data/plans/, which belongs to no plan. It returns each file's bytes.
func leavePlanEdits(t *testing.T, ledger string) map[string]string {
	t.Helper()
	files := map[string]string{
		editedPlanDir + "/plan.md":    "# Edited\n\nan unsaved edit\n",
		editedPlanDir + "/meta.json":  `{"topic":"Edited","staged":true}` + "\n",
		pendingPlanDir + "/plan.html": strings.Repeat("<p>large render</p>\n", 60000), // past the 1 MiB LFS threshold
		pendingPlanDir + "/meta.json": `{"topic":"Pending"}` + "\n",
		"data/plans/.DS_Store":        "finder\n",
	}
	for rel, body := range files {
		writeLedgerFile(t, ledger, rel, body)
	}
	runGitInDir(t, ledger, "add", editedPlanDir+"/meta.json")
	return files
}

// assertPlanEditsKept checks that the files leavePlanEdits left keep their
// bytes, only the staged edit is staged, and the remote holds neither edit nor
// the page.
func assertPlanEditsKept(t *testing.T, ledger, origin string, files map[string]string) {
	t.Helper()
	for rel, want := range files {
		if got, err := os.ReadFile(filepath.Join(ledger, filepath.FromSlash(rel))); err != nil || string(got) != want {
			t.Errorf("%s lost its bytes (err %v)", rel, err)
		}
	}
	if got := runGitInDir(t, ledger, "show", ":"+editedPlanDir+"/meta.json"); got != strings.TrimSpace(files[editedPlanDir+"/meta.json"]) {
		t.Errorf("the staged meta.json edit must stay staged, the index holds %q", got)
	}
	if got := runGitInDir(t, ledger, "diff", "--cached", "--name-only", "--", "data/plans"); got != editedPlanDir+"/meta.json" {
		t.Errorf("only the staged meta.json edit may be staged under data/plans/, staged now: %q", got)
	}
	if got := runGitInDir(t, origin, "show", "main:"+editedPlanDir+"/plan.md"); got != "# Edited" {
		t.Errorf("the remote's plan.md changed to %q", got)
	}
	if got := runGitInDir(t, origin, "show", "main:"+editedPlanDir+"/meta.json"); got != `{"topic":"Edited"}` {
		t.Errorf("the remote's meta.json changed to %q", got)
	}
	if strings.Contains(remoteFiles(t, origin), pendingPlanDir+"/plan.html") {
		t.Error("the remote holds the page that was never uploaded")
	}
}

// ledgerCommit is one commit's subject and the paths it changes.
type ledgerCommit struct {
	subject string
	paths   []string
}

// commitsSince lists the commits HEAD has after base.
func commitsSince(t *testing.T, ledger, base string) []ledgerCommit {
	t.Helper()
	var commits []ledgerCommit
	for _, rec := range strings.Split(runGitInDir(t, ledger, "log", "--format=%x1e%s", "--name-only", base+"..HEAD"), "\x1e") {
		lines := strings.Split(strings.TrimSpace(rec), "\n")
		if lines[0] == "" {
			continue
		}
		c := ledgerCommit{subject: lines[0]}
		for _, p := range lines[1:] {
			if p != "" {
				c.paths = append(c.paths, p)
			}
		}
		commits = append(commits, c)
	}
	return commits
}

// plainDoctor is a plain `ox doctor`: no fix flags, so every automatic fix
// applies and nothing else does.
var plainDoctor = doctorOptions{forceYes: true}

// runDoctor runs `ox doctor` with opts in a fresh project whose Ledger is
// ledger.
func runDoctor(t *testing.T, ledger string, opts doctorOptions) []checkCategory {
	t.Helper()
	sandboxDoctorEnv(t)
	t.Setenv(config.EnvProjectRoot, "")
	project := testGitRepo(t)
	t.Chdir(project)
	createFreshSageoxStructure(t, project)
	if err := config.SaveLocalConfig(project, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: ledger}}); err != nil {
		t.Fatal(err)
	}
	categories, err := runDoctorChecks(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return categories
}

// doctorLine is the result `ox doctor` reported under name.
func doctorLine(t *testing.T, categories []checkCategory, name string) checkResult {
	t.Helper()
	check, found := findCheckByName(categories, name)
	if !found {
		t.Fatalf("ox doctor has no %q line", name)
	}
	return check
}

// TestPlainDoctor_CleanupCommitsLeavePlanFilesAlone runs a plain `ox doctor`
// on a Ledger holding plan edits (leavePlanEdits) beside each thing that makes
// doctor commit on its own. Failure prevented: a cleanup commit publishes a
// plan edit nobody saved, or a multi-megabyte page meant to reach the Ledger
// as an LFS pointer, or plan files alone make doctor commit.
func TestPlainDoctor_CleanupCommitsLeavePlanFilesAlone(t *testing.T) {
	for _, tc := range []struct {
		name    string
		seed    map[string]string // committed and pushed before the plan edits
		dirty   string            // left uncommitted beside them
		subject string            // the cleanup commit doctor makes; "" for none
		carries string            // a path that commit changes
		workdir string            // in the Ledger clean workdir result
	}{
		{
			name:    "unrelated dirty file",
			dirty:   "audit/unrelated.md",
			subject: "ox doctor: auto-commit ledger changes",
			carries: "audit/unrelated.md",
			workdir: "committed 1 file(s)",
		},
		{
			name:    "tracked .rej artifact",
			seed:    map[string]string{"data/github/x.json.rej": "@@ rejected hunk\n"},
			subject: "chore: untrack and ignore .rej patch-reject artifacts",
			carries: "data/github/x.json.rej",
			workdir: "clean",
		},
		{
			name:    "tracked cache file",
			seed:    map[string]string{".sageox/cache/sync-state.json": "{}\n"},
			subject: "chore: untrack local-only cache files",
			carries: ".sageox/cache/sync-state.json",
			workdir: "clean",
		},
		{
			name:    "plan files only",
			workdir: "clean",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger, origin := plansLedger(t)
			if tc.seed != nil {
				commitLedgerFiles(t, ledger, tc.seed)
			}
			if tc.dirty != "" {
				writeLedgerFile(t, ledger, tc.dirty, "unrelated\n")
			}
			base := runGitInDir(t, ledger, "rev-parse", "HEAD")
			files := leavePlanEdits(t, ledger)

			categories := runDoctor(t, ledger, plainDoctor)

			commits := commitsSince(t, ledger, base)
			made := false
			for _, c := range commits {
				for _, p := range c.paths {
					if strings.HasPrefix(p, "data/plans/") {
						t.Errorf("commit %q carries %s", c.subject, p)
					}
					made = made || c.subject == tc.subject && p == tc.carries
				}
			}
			if tc.subject == "" && len(commits) > 0 {
				t.Errorf("plan files alone made doctor commit: %+v", commits)
			}
			if tc.subject != "" && !made {
				t.Errorf("doctor made no %q commit changing %s, so this row tests nothing: %+v", tc.subject, tc.carries, commits)
			}
			if got := doctorLine(t, categories, "Ledger clean workdir").message; !strings.Contains(got, tc.workdir) {
				t.Errorf("Ledger clean workdir says %q, want it to say %q", got, tc.workdir)
			}
			if edits := doctorLine(t, categories, "Ledger plan edits"); !edits.warning || edits.message != "2 plan(s) have unsaved edits: 2026-10-01-edited, 2026-10-02-pending" {
				t.Errorf("plain ox doctor must show both plans as unsaved edits, got %q", edits.message)
			}
			assertPlanEditsKept(t, ledger, origin, files)
		})
	}
}

// TestPlainDoctor_LeavesAFailedSaveToItsRetry: doctor's sweep runs before its
// plan retry, and the retry still publishes what the save owes, from its own
// commit, without the page no store took, and keeps the save's retry marker.
// Failure prevented: refusing the sweep costs a saved plan its recovery.
func TestPlainDoctor_LeavesAFailedSaveToItsRetry(t *testing.T) {
	ledger, origin := plansLedger(t)
	writeLedgerFile(t, ledger, "audit/unrelated.md", "unrelated\n")
	base := runGitInDir(t, ledger, "rev-parse", "HEAD")
	files := leavePlanEdits(t, ledger)
	recordPlanPushOutcome(context.Background(), ledger, filepath.Join(ledger, filepath.FromSlash(pendingPlanDir)),
		time.Now(), errors.New("plan.html upload failed"))
	useLFSStore(t, nil) // still no store, so the retry cannot store the page either

	categories := runDoctor(t, ledger, plainDoctor)

	if got := doctorLine(t, categories, "Ledger plan edits").message; got != "1 plan(s) have unsaved edits: 2026-10-01-edited" {
		t.Errorf("a save its retry owns is not an unsaved edit, got %q", got)
	}
	swept, retried := false, false
	for _, c := range commitsSince(t, ledger, base) {
		for _, p := range c.paths {
			mine := c.subject == "plan: 2026-10-02-pending" && p == pendingPlanDir+"/meta.json"
			if strings.HasPrefix(p, "data/plans/") && !mine {
				t.Errorf("commit %q carries %s", c.subject, p)
			}
			swept = swept || c.subject == "ox doctor: auto-commit ledger changes" && p == "audit/unrelated.md"
			retried = retried || mine
		}
	}
	if !swept {
		t.Error("doctor's sweep made no commit of the unrelated file, so this test checks nothing about it")
	}
	if !retried || !strings.Contains(remoteFiles(t, origin), pendingPlanDir+"/meta.json") {
		t.Error("the plan's retry must publish the save's meta.json from its own commit")
	}
	if _, err := os.Stat(planPushMarkerPath(ledger, pendingPlanDir)); err != nil {
		t.Errorf("the page is still not stored, so its retry marker must stay: %v", err)
	}
	assertPlanEditsKept(t, ledger, origin, files)
}

// TestPlainDoctor_QueuesAStrandedSave: a plan whose `ox plan save` ran but
// never committed, with no retry marker (a crash, an older ox), is queued and
// published by the same plain `ox doctor`. Failure prevented: saved work that
// nothing retries waits for a re-save that may never come.
func TestPlainDoctor_QueuesAStrandedSave(t *testing.T) {
	event := func(kind plan.EventKind) string {
		b, err := json.Marshal(plan.Event{Timestamp: time.Now().UTC(), PlanID: "pln_stranded", Kind: kind})
		if err != nil {
			t.Fatal(err)
		}
		return string(b) + "\n"
	}
	created, revised := event(plan.EventCreated), event(plan.EventRevised)
	const dir = "data/plans/2026-10-03-stranded"
	for _, tc := range []struct {
		name             string
		committed, saved string // plan.md and events.jsonl as HEAD has them, and as the save wrote them
		committedEvents  string
		savedEvents      string
	}{
		{name: "first save", saved: "# Stranded\n", savedEvents: created},
		{name: "revision", committed: "# Stranded\n", committedEvents: created, saved: "# Stranded\n\nrevised\n", savedEvents: created + revised},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger, origin := plansLedger(t)
			if tc.committed != "" {
				commitLedgerFiles(t, ledger, map[string]string{dir + "/plan.md": tc.committed, dir + "/events.jsonl": tc.committedEvents})
			}
			writeLedgerFile(t, ledger, dir+"/plan.md", tc.saved)
			writeLedgerFile(t, ledger, dir+"/events.jsonl", tc.savedEvents)

			categories := runDoctor(t, ledger, plainDoctor)

			if got := doctorLine(t, categories, "Ledger plan saves").message; got != "queued 1 saved plan(s) for the plan retry: 2026-10-03-stranded" {
				t.Errorf("Ledger plan saves says %q", got)
			}
			if got := runGitInDir(t, origin, "show", "main:"+dir+"/plan.md"); got != strings.TrimSpace(tc.saved) {
				t.Errorf("the remote's plan.md is %q, want what the save wrote", got)
			}
			if planRetryPending(ledger, dir) || planDirDirty(ledger, dir) {
				t.Error("the queued save must be published, leaving no retry and no uncommitted change")
			}
		})
	}
}

// TestDoctorFix_PublishesUnsavedPlanEditsOnlyWithConsent drives
// `ox doctor --fix-slug=plan-unsaved-edits`: with nobody to confirm it
// publishes nothing; confirmed, it publishes each plan as it is from the
// plan's own commit, and a large page no store can take stays queued, never
// committed raw, until a later `ox doctor` lands it as an LFS pointer.
// Failure prevented: a draft goes out without a person's yes, or the publish
// commits a raw page.
func TestDoctorFix_PublishesUnsavedPlanEditsOnlyWithConsent(t *testing.T) {
	ledger, origin := plansLedger(t)
	base := runGitInDir(t, ledger, "rev-parse", "HEAD")
	files := leavePlanEdits(t, ledger)
	useLFSStore(t, nil) // no store reachable yet
	prevNoInput, prevYes := cli.NoInput(), cli.AssumeYes()
	t.Cleanup(func() { cli.SetNoInput(prevNoInput); cli.SetAssumeYes(prevYes) })
	fixSlug := doctorOptions{fix: true, fixSlugs: []string{CheckSlugPlanUnsavedEdits}}

	cli.SetNoInput(true)
	edits := doctorLine(t, runDoctor(t, ledger, fixSlug), "Ledger plan edits")
	if !strings.Contains(edits.detail, "Nothing was published") || len(commitsSince(t, ledger, base)) > 0 {
		t.Fatalf("with nobody to confirm, nothing may be published: %q, %q", edits.message, edits.detail)
	}
	assertPlanEditsKept(t, ledger, origin, files)

	cli.SetNoInput(false)
	cli.SetAssumeYes(true)
	edits = doctorLine(t, runDoctor(t, ledger, fixSlug), "Ledger plan edits")
	if edits.message != "1 of 2 plan(s) not published yet: 2026-10-02-pending" {
		t.Fatalf("Ledger plan edits says %q: %s", edits.message, edits.detail)
	}
	for _, c := range commitsSince(t, ledger, base) {
		if !strings.HasPrefix(c.subject, "plan: ") {
			t.Errorf("commit %q is not the plan's own", c.subject)
		}
	}
	if got := runGitInDir(t, origin, "show", "main:"+editedPlanDir+"/plan.md"); got != strings.TrimSpace(files[editedPlanDir+"/plan.md"]) {
		t.Errorf("the remote's plan.md is %q, want the edit as it was", got)
	}
	if strings.Contains(remoteFiles(t, origin), pendingPlanDir+"/plan.html") || !planRetryPending(ledger, pendingPlanDir) {
		t.Fatal("a page no store took must stay queued, never committed raw")
	}

	useLFSStore(t, newPlanLFSStore(t, false).client)
	runDoctor(t, ledger, plainDoctor)
	if _, _, err := lfs.ParsePointer(runGitInDir(t, origin, "show", "main:"+pendingPlanDir+"/plan.html") + "\n"); err != nil {
		t.Errorf("once a store takes it, the queued page must reach the remote as an LFS pointer: %v", err)
	}
}

// TestHasUncommittedSave_IgnoresLineEndings: a clone with core.autocrlf checks
// the committed events.jsonl out with CRLF endings, which must not read as a
// save that never committed, or ox doctor would publish the draft beside it.
func TestHasUncommittedSave_IgnoresLineEndings(t *testing.T) {
	ledger, _ := plansLedger(t)
	const dir = "data/plans/2026-10-03-crlf"
	created := `{"ts":"2026-10-03T00:00:00Z","plan_id":"pln_crlf","event":"created"}`
	commitLedgerFiles(t, ledger, map[string]string{dir + "/events.jsonl": created + "\n"})

	writeLedgerFile(t, ledger, dir+"/events.jsonl", created+"\r\n")
	if hasUncommittedSave(context.Background(), ledger, dir) {
		t.Error("the committed save, checked out with CRLF, read as one that never committed")
	}
	writeLedgerFile(t, ledger, dir+"/events.jsonl", created+"\r\n"+`{"ts":"2026-10-04T00:00:00Z","plan_id":"pln_crlf","event":"revised"}`+"\r\n")
	if !hasUncommittedSave(context.Background(), ledger, dir) {
		t.Error("a revised event HEAD does not hold must read as a save that never committed")
	}
}
