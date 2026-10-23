//go:build !short

package main

// doctor_ledger_plans_test.go: a plain `ox doctor` never commits a plan file.
// Its automatic cleanup commits leave data/plans/ to the plan's own writers
// (#1409).

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
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
	project := testGitRepo(t)
	createFreshSageoxStructure(t, project)
	return runDoctorIn(t, project, ledger, opts)
}

// runDoctorIn runs `ox doctor` with opts in project, its Ledger set to
// ledger, away from any running daemon's socket.
func runDoctorIn(t *testing.T, project, ledger string, opts doctorOptions) []checkCategory {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv(config.EnvProjectRoot, "")
	t.Chdir(project)
	if err := config.SaveLocalConfig(project, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: ledger}}); err != nil {
		t.Fatal(err)
	}
	categories, err := runDoctorChecks(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return categories
}

// savePlanPage authors a page titled title in project and saves it with the
// real `ox plan save`, returning its plan dir relative to ledger. large pads
// the page past the LFS threshold, so the save uploads it.
func savePlanPage(t *testing.T, project, ledger, title string, large bool) string {
	t.Helper()
	page := filepath.Join(project, ".context", strings.ReplaceAll(strings.ToLower(title), " ", "-")+".html")
	if large {
		writeLargeAuthoredPage(t, page, title)
	} else {
		writeAuthoredPage(t, page, title)
	}
	if _, err := runPlanSaveCLI(t, "--file", page); err != nil {
		t.Fatalf("ox plan save: %v", err)
	}
	plans, err := plan.List(project)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plans {
		if p.Topic == title {
			rel, err := ledgerRelPath(ledger, p.Dir)
			if err != nil {
				t.Fatal(err)
			}
			return rel
		}
	}
	t.Fatalf("no saved plan titled %q among %+v", title, plans)
	return ""
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

// TestPlainDoctor_LeavesARealFailedSaveToItsRetry runs real `ox plan save`s,
// then a plain `ox doctor`: a shared plan someone edited by hand and never
// saved, and a save whose large page no store took (its retry owed). Only the
// hand edit is listed as unsaved, and the failed save is left to its retry,
// which never commits the raw page. Failure prevented: a save waiting on its
// retry is offered for publishing as an unsaved edit, or its raw page goes
// out.
func TestPlainDoctor_LeavesARealFailedSaveToItsRetry(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	ledger, remote := newSharedLedger(t, root, true)
	t.Setenv("SAGEOX_AGENT_ID", "")
	useLFSStore(t, nil) // the small page needs no store; the large one's upload fails
	edited := savePlanPage(t, root, ledger, "Edited By Hand", false)
	writeLedgerFile(t, ledger, edited+"/plan.md", "# Edited By Hand\n\nan edit nobody saved\n")
	failed := savePlanPage(t, root, ledger, "Upload Failed", true)

	categories := runDoctorIn(t, root, ledger, plainDoctor)

	if got := doctorLine(t, categories, "Ledger plan edits").message; got != "1 plan(s) have unsaved edits: "+path.Base(edited) {
		t.Errorf("only the hand edit is unsaved; the failed save is its retry's, got %q", got)
	}
	if strings.Contains(remoteFiles(t, remote), failed+"/plan.html") || !planRetryPending(ledger, failed) {
		t.Error("a page no store took must stay queued, never committed raw")
	}
}

// TestPlainDoctor_RecoversARealSaveThatNeverCommitted: a real `ox plan save`
// (a first save, and a revision of a shared plan) whose commit never happened
// and whose retry marker is gone, as a crash, an older ox or a re-clone
// leaves it, is queued and published by one plain `ox doctor`, from what the
// save wrote. Failure prevented: saved work that nothing retries waits for a
// re-save that may never come.
func TestPlainDoctor_RecoversARealSaveThatNeverCommitted(t *testing.T) {
	for _, revision := range []bool{false, true} {
		t.Run(map[bool]string{false: "first save", true: "revision"}[revision], func(t *testing.T) {
			root := newPlanCaptureTestRepo(t)
			ledger, remote := newSharedLedger(t, root, true)
			t.Setenv("SAGEOX_AGENT_ID", "")
			useLFSStore(t, nil)
			const title = "Never Committed"
			if revision {
				savePlanPage(t, root, ledger, title, false)
				page := filepath.Join(root, ".context", "never-committed.html")
				f, err := os.OpenFile(page, os.O_APPEND|os.O_WRONLY, 0o644)
				if err != nil {
					t.Fatal(err)
				}
				_, werr := f.WriteString("<p>revised</p>\n")
				if err := errors.Join(werr, f.Close()); err != nil {
					t.Fatal(err)
				}
			}
			prev := commitPlanSnapshot
			commitPlanSnapshot = func(context.Context, string, string, ...string) (bool, error) {
				return false, errors.New("ox was interrupted")
			}
			t.Cleanup(func() { commitPlanSnapshot = prev })
			dir := savePlanPage(t, root, ledger, title, false)
			commitPlanSnapshot = prev
			if err := os.RemoveAll(filepath.Join(ledger, filepath.FromSlash(planPushPendingDir))); err != nil {
				t.Fatal(err)
			}

			categories := runDoctorIn(t, root, ledger, plainDoctor)

			if got := doctorLine(t, categories, "Ledger plan saves").message; got != "queued 1 saved plan(s) for the plan retry: "+path.Base(dir) {
				t.Errorf("Ledger plan saves says %q", got)
			}
			saved, err := os.ReadFile(filepath.Join(ledger, filepath.FromSlash(dir), "plan.md"))
			if err != nil {
				t.Fatal(err)
			}
			if got := runGitInDir(t, remote, "show", "main:"+dir+"/plan.md"); got != strings.TrimSpace(string(saved)) {
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
// plan's own commit, and a large page no store can take stays queued for the
// plan retry, never committed raw. Failure prevented: a draft goes out without
// a person's yes, or the publish commits a raw page.
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
		t.Error("a page no store took must stay queued, never committed raw")
	}
}

// TestHasUncommittedSave_CommittedSaveIsNotStranded: a committed save must not
// read as one that never committed when a clone with core.autocrlf checks
// events.jsonl out with CRLF endings, or when HEAD cannot be read; either way
// ox doctor would publish the edits beside it unasked.
func TestHasUncommittedSave_CommittedSaveIsNotStranded(t *testing.T) {
	ledger, _ := plansLedger(t)
	const dir = "data/plans/2026-10-03-crlf"
	created := `{"ts":"2026-10-03T00:00:00Z","plan_id":"pln_crlf","event":"created"}`
	commitLedgerFiles(t, ledger, map[string]string{dir + "/events.jsonl": created + "\n"})

	writeLedgerFile(t, ledger, dir+"/events.jsonl", created+"\r\n"+`{"ts":"2026-10-04T00:00:00Z","plan_id":"pln_crlf","event":"revised"}`+"\r\n")
	if !hasUncommittedSave(context.Background(), ledger, dir) {
		t.Error("a revised event HEAD does not hold must read as a save that never committed")
	}
	writeLedgerFile(t, ledger, dir+"/events.jsonl", created+"\r\n")
	if hasUncommittedSave(context.Background(), ledger, dir) {
		t.Error("the committed save, checked out with CRLF, read as one that never committed")
	}
	// the plan dir's tree object, corrupt, leaves HEAD's events.jsonl unreadable
	tree := runGitInDir(t, ledger, "rev-parse", "HEAD:"+dir)
	obj := filepath.Join(ledger, ".git", "objects", tree[:2], tree[2:])
	if err := errors.Join(os.Chmod(obj, 0o644), os.WriteFile(obj, []byte("corrupt"), 0o644)); err != nil {
		t.Fatal(err)
	}
	if hasUncommittedSave(context.Background(), ledger, dir) {
		t.Error("a HEAD that could not be read read as a save that never committed")
	}
}

// TestDoctorFix_RepairsLeaveAStagedPlanEditStaged runs each --fix-only Ledger
// repair beside plan edits (leavePlanEdits). Failure prevented: the repair's
// commit took the whole index and published a staged plan edit (#1442), or
// left what it staged uncommitted.
func TestDoctorFix_RepairsLeaveAStagedPlanEditStaged(t *testing.T) {
	const legacySession = "2026-01-01T00-00-test-Ox0001"
	for _, tc := range []struct {
		slug    string
		seed    func(t *testing.T, ledger string) // what the repair changes
		subject string                            // the repair's commit
		carries string                            // a path prefix that commit changes
	}{
		{
			slug: CheckSlugSessionIDsBackfilled,
			seed: func(t *testing.T, ledger string) {
				writeTestSessionMeta(t, filepath.Join(ledger, "sessions"), legacySession, "")
			},
			subject: "session: backfill stable session_id for 1 legacy session(s)",
			carries: "sessions/" + legacySession + "/meta.json",
		},
		{
			slug: CheckSlugGitHubDataMigration,
			seed: func(t *testing.T, ledger string) {
				writeLedgerFile(t, ledger, "data/github/2026/10/01/pr/42.json", `{"number":42}`+"\n")
			},
			subject: "ox doctor: migrate 1 legacy GitHub data file(s)",
			carries: "data/github/2026/10/01/pr/42",
		},
	} {
		t.Run(tc.slug, func(t *testing.T) {
			ledger, origin := plansLedger(t)
			tc.seed(t, ledger)
			runGitInDir(t, ledger, "add", "-A")
			runGitInDir(t, ledger, "commit", "-q", "-m", "seed")
			runGitInDir(t, ledger, "push", "-q")
			base := runGitInDir(t, ledger, "rev-parse", "HEAD")
			files := leavePlanEdits(t, ledger)

			runDoctor(t, ledger, doctorOptions{fix: true, fixSlugs: []string{tc.slug}})

			made := false
			for _, c := range commitsSince(t, ledger, base) {
				for _, p := range c.paths {
					if strings.HasPrefix(p, "data/plans/") {
						t.Errorf("commit %q carries %s", c.subject, p)
					}
					made = made || c.subject == tc.subject && strings.HasPrefix(p, tc.carries)
				}
			}
			if !made {
				t.Errorf("no %q commit changing %s", tc.subject, tc.carries)
			}
			if staged := runGitInDir(t, ledger, append([]string{"diff", "--cached", "--name-only", "--"}, allButPlansPathspec...)...); staged != "" {
				t.Errorf("left %q staged", staged)
			}
			assertPlanEditsKept(t, ledger, origin, files)
		})
	}
}
