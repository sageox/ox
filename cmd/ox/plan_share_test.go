package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/plan"
)

// --- A. Verdict classification (pure) ---

// TestClassifyPlanShare maps every sharing failure to shared=false with a
// reason and a fix, and only a clean push to shared=true.
// Failure prevented: the 18-ahead/3538-behind incident — saved plans staged and
// unpushed while the save printed only "Saved plan to ledger".
func TestClassifyPlanShare(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name          string
		facts         planShareFacts
		wantShared    bool
		wantCommitted bool
		wantReason    string // substring
		wantWarning   bool
	}{
		{"clean push", planShareFacts{DaemonRunning: true, UpstreamKnown: true}, true, true, "", false},
		{"no ledger", planShareFacts{NoLedger: true, DaemonRunning: true}, false, false, "no ledger", false},
		{"mid-rebase", planShareFacts{UnsafeErr: boom, DaemonRunning: true}, false, false, "mid-rebase", false},
		{"commit failed", planShareFacts{CommitErr: boom, DaemonRunning: true}, false, false, "commit failed", false},
		{"push failed", planShareFacts{PushErr: boom, DaemonRunning: true}, false, true, "push failed", false},
		{"push failed on a diverged ledger", planShareFacts{PushErr: boom, Ahead: 18, Behind: 3538, UpstreamKnown: true, DaemonRunning: true}, false, true, "18 ahead / 3538 behind", false},
		{"pushed but files still dirty", planShareFacts{Dirty: true, DaemonRunning: true}, false, true, "uncommitted", false},
		{"push exit 0 but still ahead", planShareFacts{Ahead: 2, UpstreamKnown: true, DaemonRunning: true}, false, true, "not on origin", false},
		{"shared, daemon down", planShareFacts{UpstreamKnown: true}, true, true, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyPlanShare(tt.facts)
			if got.Shared != tt.wantShared || got.Committed != tt.wantCommitted {
				t.Fatalf("shared=%v committed=%v, want %v/%v (%+v)", got.Shared, got.Committed, tt.wantShared, tt.wantCommitted, got)
			}
			if !tt.wantShared {
				if !strings.Contains(got.Reason, tt.wantReason) {
					t.Errorf("reason %q missing %q", got.Reason, tt.wantReason)
				}
				if got.Fix == "" {
					t.Error("a not-shared verdict must name the fix command")
				}
			}
			if (len(got.Warnings) > 0) != tt.wantWarning {
				t.Errorf("warnings = %v, want present=%v", got.Warnings, tt.wantWarning)
			}
		})
	}
}

// --- B. End to end against a real (local) remote ---

// newSharedLedger turns the capture test repo's ledger path into a real clone
// of a bare remote. remoteOK=false points origin at a path that does not
// exist, so every push fails.
func newSharedLedger(t *testing.T, root string, remoteOK bool) (ledger, remote string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx, err := config.LoadProjectContext(root)
	if err != nil || ctx == nil || ctx.DefaultLedgerPath() == "" {
		t.Fatalf("no ledger path for test repo: %v", err)
	}
	ledger = ctx.DefaultLedgerPath()
	remote = filepath.Join(t.TempDir(), "remote.git")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(filepath.Dir(remote), "init", "-q", "--bare", "-b", "main", remote)
	if err := os.MkdirAll(filepath.Dir(ledger), 0o755); err != nil {
		t.Fatal(err)
	}
	run(filepath.Dir(ledger), "clone", "-q", remote, ledger)
	gitConfig(t, ledger, "user.name", "Person A")
	gitConfig(t, ledger, "user.email", "person@test.sageox.ai")
	gitConfig(t, ledger, "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(ledger, "README.md"), []byte("ledger\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(ledger, "add", "README.md")
	run(ledger, "commit", "-q", "-m", "init")
	run(ledger, "push", "-q", "-u", "origin", "main")
	if !remoteOK {
		run(ledger, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	}
	return ledger, remote
}

const shareTestPage = `<!doctype html><html><head><title>Share Test</title><meta name="ox-plan-slug" content="share-test"></head><body><h1>Share Test</h1><h2>Plan</h2><p>Do it.</p></body></html>`

// TestSavePlanArtifacts_ShareReport verifies the report a save returns against
// a real remote: pushed → shared with a /plan/<pln_id> link; push failure →
// not shared, but committed locally, with the fix.
// Failure prevented: a save that never reached teammates reporting success.
func TestSavePlanArtifacts_ShareReport(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone + push")
	}
	tests := []struct {
		name       string
		remoteOK   bool
		wantShared bool
	}{
		{"push succeeds", true, true},
		{"push fails", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newPlanCaptureTestRepo(t)
			ledger, remote := newSharedLedger(t, root, tt.remoteOK)

			var r planSaveReport
			in := plan.Parse(plan.ExtractMarkdown([]byte(shareTestPage)))
			dir := savePlanArtifacts(root, in, plan.Result{}, []byte(shareTestPage), plan.PrimaryHTML, withReport(&r))
			if dir == "" {
				t.Fatalf("save failed: %v", r.Err)
			}
			if r.Share.Shared != tt.wantShared {
				t.Fatalf("shared = %v, want %v (%+v)", r.Share.Shared, tt.wantShared, r.Share)
			}
			if !r.Share.Committed {
				t.Error("the plan must be committed locally either way")
			}
			if !strings.HasPrefix(r.PlanID, "pln_") || !strings.HasSuffix(r.URL, "/plan/"+r.PlanID) {
				t.Errorf("url = %q, plan_id = %q; want .../plan/<pln_id>", r.URL, r.PlanID)
			}
			if r.Event != plan.EventCreated || r.Revision != 1 {
				t.Errorf("event=%s revision=%d, want created/1", r.Event, r.Revision)
			}
			rel, _ := filepath.Rel(ledger, dir)
			out, err := exec.Command("git", "-C", remote, "log", "--oneline", "main", "--", filepath.ToSlash(rel)).CombinedOutput()
			onRemote := err == nil && strings.TrimSpace(string(out)) != ""
			if onRemote != tt.wantShared {
				t.Errorf("plan on remote = %v, want %v (%s)", onRemote, tt.wantShared, out)
			}
			if !tt.wantShared && (r.Share.Fix == "" || !strings.Contains(r.Share.Reason, "push failed")) {
				t.Errorf("not-shared verdict must carry reason+fix: %+v", r.Share)
			}
		})
	}
}

// TestSavePlanArtifacts_RevisionKeepsUncommittedPriorInHistory verifies a
// revision never overwrites a prior revision that exists only in the working
// tree: it is committed first.
// Failure prevented: Sacred-tier loss — a save whose commit failed, followed by
// a re-save of the same slug, erased the only copy of the earlier page.
func TestSavePlanArtifacts_RevisionKeepsUncommittedPriorInHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone + push")
	}
	root := newPlanCaptureTestRepo(t)
	ledger, _ := newSharedLedger(t, root, true)

	// the prior revision: written by Save but never committed (as when a
	// commit failed), so it exists nowhere but the working tree.
	v1 := strings.Replace(shareTestPage, "Do it.", "FIRST-REVISION-BODY", 1)
	in1 := plan.Parse(plan.ExtractMarkdown([]byte(v1)))
	dir, _, err := plan.Save(root, in1, plan.Result{}, []byte(v1), plan.Meta{Topic: "Share Test", Slug: "share-test", Primary: plan.PrimaryHTML})
	if err != nil {
		t.Fatalf("seed prior revision: %v", err)
	}

	var r planSaveReport
	in2 := plan.Parse(plan.ExtractMarkdown([]byte(shareTestPage)))
	got := savePlanArtifacts(root, in2, plan.Result{}, []byte(shareTestPage), plan.PrimaryHTML, withReport(&r))
	if got != dir {
		t.Fatalf("revision landed in %s, want %s", got, dir)
	}
	if r.Event != plan.EventRevised || r.Revision != 2 {
		t.Errorf("event=%s revision=%d, want revised/2", r.Event, r.Revision)
	}
	rel, _ := filepath.Rel(ledger, filepath.Join(dir, "plan.md"))
	out, err := exec.Command("git", "-C", ledger, "log", "-p", "--", filepath.ToSlash(rel)).CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "FIRST-REVISION-BODY") {
		t.Error("the uncommitted prior revision is not recoverable from ledger history")
	}
}

// --- C. Command surface ---

// TestPlanSaveCmd_VisibleWithShareFlags verifies `ox plan save` shows in
// `ox plan --help` and carries --json/--slug.
// Failure prevented: AGENTS.md telling coworkers to run a command help hides.
func TestPlanSaveCmd_VisibleWithShareFlags(t *testing.T) {
	if planSaveCmd.Hidden {
		t.Error("ox plan save must be visible in `ox plan --help`")
	}
	for _, f := range []string{"json", "slug", "file", "kind"} {
		if planSaveCmd.Flags().Lookup(f) == nil {
			t.Errorf("ox plan save is missing --%s", f)
		}
	}
}

// TestWritePlanSaveReport_NotSharedIsLoudOnBothChannels verifies the JSON
// stays one parseable document and the warning still reaches stderr.
// Failure prevented: an AI coworker parsing --json never seeing shared=false,
// or the warning corrupting stdout.
func TestWritePlanSaveReport_NotSharedIsLoudOnBothChannels(t *testing.T) {
	r := planSaveReport{Dir: "/l/data/plans/x", Slug: "x", PlanID: "pln_1", URL: "https://sageox.ai/plan/pln_1",
		Event: plan.EventCreated, Revision: 1,
		Share: planShareStatus{Committed: true, Reason: "committed locally but push failed: boom", Fix: planDoctorFix}}
	for _, jsonOut := range []bool{true, false} {
		cmd := planSaveCmd
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		if err := writePlanSaveReport(cmd, r, "plan", jsonOut); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(errOut.String(), "NOT SHARED") || !strings.Contains(errOut.String(), planDoctorFix) {
			t.Errorf("json=%v: stderr missing loud warning: %q", jsonOut, errOut.String())
		}
		if jsonOut {
			var got planSaveReport
			dec := json.NewDecoder(&out)
			dec.DisallowUnknownFields()
			if err := dec.Decode(&got); err != nil {
				t.Fatalf("stdout is not one planSaveReport document: %v", err)
			}
			if dec.More() {
				t.Error("stdout carries more than one JSON document")
			}
			if got.Share.Shared || got.Share.Fix != planDoctorFix || got.URL != r.URL || got.PlanID != r.PlanID {
				t.Errorf("json = %+v", got)
			}
		} else if !strings.Contains(out.String(), "pln_1") {
			t.Errorf("human output missing link: %q", out.String())
		}
	}
	planSaveCmd.SetOut(nil)
	planSaveCmd.SetErr(nil)
}

// TestLifecycleVerb_CommitsAndPushes verifies `ox plan approve` (and every
// lifecycle verb, which share runPlanLifecycleVerbOnDir) commits + pushes the
// event like the browser /approve path, and reports the verdict in --json.
// Failure prevented: a CLI approval living only in one laptop's working tree.
func TestLifecycleVerb_CommitsAndPushes(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone + push")
	}
	root := newPlanCaptureTestRepo(t)
	ledger, remote := newSharedLedger(t, root, true)
	in := plan.Parse(plan.ExtractMarkdown([]byte(shareTestPage)))
	dir := savePlanArtifacts(root, in, plan.Result{}, []byte(shareTestPage), plan.PrimaryHTML)
	if dir == "" {
		t.Fatal("save failed")
	}

	cmd := planApproveCmd
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Flags().Set("json", "false"); cmd.SetOut(nil); cmd.SetErr(nil) })
	if err := runPlanLifecycleVerbOnDir(cmd, root, dir, "share-test", plan.EventApproved, lifecycleVerbFlags{}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	var res planLifecycleResult
	dec := json.NewDecoder(&out)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.Changed || res.Share == nil || !res.Share.Shared {
		t.Fatalf("approve result = %+v share=%+v, want changed + shared", res, res.Share)
	}
	rel, _ := filepath.Rel(ledger, filepath.Join(dir, "events.jsonl"))
	show, err := exec.Command("git", "-C", remote, "show", "main:"+filepath.ToSlash(rel)).CombinedOutput()
	if err != nil || !strings.Contains(string(show), `"approved"`) {
		t.Errorf("approved event not on the remote: %v\n%s", err, show)
	}
}

// TestPlanShareHelpers_NeverGuess verifies the share helpers report "unknown"
// rather than inventing a link, id, or revision. Failure prevented: a printed
// share link pointing at a plan that does not exist.
func TestPlanShareHelpers_NeverGuess(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "no-such-plan")
	if got := planShareURL("", ""); got != "" {
		t.Errorf("planShareURL with no id = %q, want empty", got)
	}
	if got := planIDForDir(missing); got != "" {
		t.Errorf("planIDForDir(missing) = %q, want empty", got)
	}
	if got := planRevisionCount(missing); got != 0 {
		t.Errorf("planRevisionCount(missing) = %d, want 0", got)
	}
}

// TestSharePlanDir_NoLedgerIsNotShared verifies a repo with no ledger reports
// NOT SHARED with the fix. Failure prevented: a save in an unconfigured repo
// claiming teammates can see it.
func TestSharePlanDir_NoLedgerIsNotShared(t *testing.T) {
	prev := daemonRunningFn
	daemonRunningFn = func() bool { return true }
	t.Cleanup(func() { daemonRunningFn = prev })

	st := sharePlanDir(t.TempDir(), filepath.Join(t.TempDir(), "plan"))
	if st.Shared || st.Committed || st.Fix != planDoctorFix || !strings.Contains(st.Reason, "no ledger") {
		t.Errorf("verdict = %+v, want not shared / no ledger / doctor fix", st)
	}
}
