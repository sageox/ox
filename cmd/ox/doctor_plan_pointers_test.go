package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ledgerWithUpstream is a Ledger clone tracking a bare remote.
func ledgerWithUpstream(t *testing.T) string {
	t.Helper()
	skipIntegration(t)
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	ledger := filepath.Join(base, "ledger")
	initBareRemote(t, remote, "--initial-branch=main")
	mustRunGit(t, base, "clone", "-q", remote, ledger)
	mustRunGit(t, ledger, "config", "user.name", "Person A")
	mustRunGit(t, ledger, "config", "user.email", "person@test.sageox.ai")
	mustRunGit(t, ledger, "config", "commit.gpgsign", "false")
	mustRunGit(t, ledger, "symbolic-ref", "HEAD", "refs/heads/main")
	require.NoError(t, os.WriteFile(filepath.Join(ledger, ".gitkeep"), nil, 0o644))
	mustRunGit(t, ledger, "add", ".gitkeep")
	mustRunGit(t, ledger, "commit", "-q", "-m", "init")
	mustRunGit(t, ledger, "push", "-q", "-u", "origin", "main")
	return ledger
}

// fakePlanStore stands in for the content store: what the finder reports
// before and after a repair, and what the repair returns.
type fakePlanStore struct {
	before, after []lfs.MissingPlanRender
	findErr       error
	repairErr     error
	repairs       int
}

func (f *fakePlanStore) install(t *testing.T) {
	t.Helper()
	prevFind, prevRepair := findMissingPlanRenders, reconcileLedgerPush
	t.Cleanup(func() { findMissingPlanRenders, reconcileLedgerPush = prevFind, prevRepair })
	findMissingPlanRenders = func(context.Context, string, string) ([]lfs.MissingPlanRender, error) {
		if f.repairs > 0 {
			return f.after, nil
		}
		return f.before, f.findErr
	}
	reconcileLedgerPush = func(context.Context, string, string, *slog.Logger) (*lfs.ReconcileResult, error) {
		f.repairs++
		return &lfs.ReconcileResult{}, f.repairErr
	}
}

var lostLaunchMockup = lfs.MissingPlanRender{
	Path: "data/plans/2026-10-24-launch-mockup/plan.html",
	OID:  "sha256:" + strings.Repeat("ab", 32),
	Size: 2196945,
}

// TestPlanPointersMissing_Outcomes: the check reports a wedging plan page
// without touching anything, repairs only under fix, and passes only when the
// pages are verified present afterwards. Failure prevented: a check that says
// "fixed" while the push stays refused, or repairs on a bare `ox doctor`.
func TestPlanPointersMissing_Outcomes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		store       fakePlanStore
		noUpstream  bool
		fix         bool
		wantStatus  string // passed, failed, warning, skipped
		wantRepairs int
		wantText    []string
	}{
		{name: "no upstream", noUpstream: true, wantStatus: "skipped"},
		{name: "store unreachable", store: fakePlanStore{findErr: errors.New("lfs client: no git credentials found")}, wantStatus: "warning", wantText: []string{"no git credentials"}},
		{name: "nothing missing", wantStatus: "passed"},
		{
			name:       "lost page, report only",
			store:      fakePlanStore{before: []lfs.MissingPlanRender{lostLaunchMockup}},
			wantStatus: "failed",
			wantText:   []string{"refuses every push", "2026-10-24-launch-mockup: no copy anywhere", "--fix-slug=" + CheckSlugPlanPointersMissing},
		},
		{
			name:        "lost page, fixed",
			store:       fakePlanStore{before: []lfs.MissingPlanRender{lostLaunchMockup}},
			fix:         true,
			wantStatus:  "passed",
			wantRepairs: 1,
			wantText:    []string{"replaced 1 lost page(s) with a tombstone"},
		},
		{
			name:        "repair fails",
			store:       fakePlanStore{before: []lfs.MissingPlanRender{lostLaunchMockup}, repairErr: errors.New("validate unpushed Ledger")},
			fix:         true,
			wantStatus:  "failed",
			wantRepairs: 1,
			wantText:    []string{"did not finish", "validate unpushed Ledger"},
		},
		{
			name:        "still missing after the repair",
			store:       fakePlanStore{before: []lfs.MissingPlanRender{lostLaunchMockup}, after: []lfs.MissingPlanRender{lostLaunchMockup}},
			fix:         true,
			wantStatus:  "failed",
			wantRepairs: 1,
			wantText:    []string{"still missing"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger := ledgerWithUpstream(t)
			if tc.noUpstream {
				mustRunGit(t, ledger, "branch", "--unset-upstream")
			}
			store := tc.store
			store.install(t)

			got := runPlanPointersMissing(context.Background(), ledger, "https://sageox.ai", tc.fix)

			assert.Equal(t, tc.wantStatus, checkStatus(got), "%s: %s", got.message, got.detail)
			assert.Equal(t, tc.wantRepairs, store.repairs)
			for _, text := range tc.wantText {
				assert.Contains(t, got.message+"\n"+got.detail, text)
			}
		})
	}
}

func checkStatus(r checkResult) string {
	switch {
	case r.skipped:
		return "skipped"
	case r.warning: // WarningCheck also sets passed
		return "warning"
	case r.passed:
		return "passed"
	default:
		return "failed"
	}
}

// TestDoctorRun_PlanPointersMissingIsInvoked drives the Ledger Git Health phase
// `ox doctor` runs. The check this restores was registered in #815 and never
// called by any phase, so `--fix-slug=plan-pointers-missing` did nothing.
func TestDoctorRun_PlanPointersMissingIsInvoked(t *testing.T) {
	sandboxDoctorEnv(t)
	ledger := ledgerWithUpstream(t)
	project := testGitRepo(t)
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	require.NoError(t, os.Chdir(project))
	createFreshSageoxStructure(t, project)
	require.NoError(t, config.SaveLocalConfig(project, &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: ledger},
	}))
	store := fakePlanStore{before: []lfs.MissingPlanRender{lostLaunchMockup}}
	store.install(t)

	results := checkLedgerGitHealth(context.Background(), doctorOptions{
		fix:      true,
		fixSlugs: []string{CheckSlugPlanPointersMissing},
	})

	var found *checkResult
	for i := range results {
		if results[i].name == planPointersCheckName {
			found = &results[i]
		}
	}
	require.NotNil(t, found, "the Ledger Git Health phase never ran %q", CheckSlugPlanPointersMissing)
	assert.Equal(t, "passed", checkStatus(*found), "%s: %s", found.message, found.detail)
	assert.Equal(t, 1, store.repairs, "--fix-slug must reach the repair")
}
