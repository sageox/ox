//go:build !short

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newDoctorProjectWithLedger(t *testing.T, ledger string) {
	t.Helper()
	project := testGitRepo(t)
	originalWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(originalWd) })
	require.NoError(t, os.Chdir(project))
	createFreshSageoxStructure(t, project)
	require.NoError(t, config.SaveLocalConfig(project, &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: ledger},
	}))
}

// Registered Sessions checks must actually run under `ox doctor`. Failure
// prevented: session-draft-orphan and session-uncommitted passed --fix-slug
// validation and then did nothing (GH #1214).
func TestDoctorRun_InvokesRegisteredDraftOrphanAndUncommittedChecks(t *testing.T) {
	sandboxDoctorEnv(t)
	ledger := newMarkedLedger(t, false)
	newDoctorProjectWithLedger(t, ledger)

	calls := map[string]int{}
	for _, slug := range []string{CheckSlugSessionDraftOrphan, CheckSlugSessionUncommitted} {
		registered := GetDoctorCheck(slug)
		require.NotNil(t, registered)
		original := registered.Run
		registered.Run = func(bool) checkResult {
			calls[slug]++
			return PassedCheck("stub", "stub")
		}
		t.Cleanup(func() { registered.Run = original })
	}

	_, err := runDoctorChecks(context.Background(), doctorOptions{})
	require.NoError(t, err)

	assert.Equal(t, 1, calls[CheckSlugSessionDraftOrphan], "session-draft-orphan was never run")
	assert.Equal(t, 1, calls[CheckSlugSessionUncommitted], "session-uncommitted was never run")
}

// A bare `ox doctor --fix` repairs conflict markers BEFORE the Ledger
// branch-status auto-fix pushes. Failure prevented: doctor published a commit
// holding conflict markers it was about to fix, to every teammate.
func TestDoctorFix_RepairsConflictMarkersBeforeBranchStatusPush(t *testing.T) {
	sandboxDoctorEnv(t)
	ledger := newMarkedLedger(t, false)
	newDoctorProjectWithLedger(t, ledger)
	remote := filepath.Join(filepath.Dir(ledger), "remote.git")

	var statusSawMarkers *bool
	branchStatus := GetDoctorCheck(CheckSlugLedgerBranchStatus)
	require.NotNil(t, branchStatus)
	original := branchStatus.Run
	branchStatus.Run = func(fix bool) checkResult {
		tip, _ := runIsolatedGit(t, ledger, "show", "HEAD:"+markedUpstreamMeta)
		sawMarkers := strings.Contains(tip, "<<<<<<<")
		statusSawMarkers = &sawMarkers
		return original(fix)
	}
	t.Cleanup(func() { branchStatus.Run = original })

	_, err := runDoctorChecks(context.Background(), doctorOptions{fix: true, forceYes: true})
	require.NoError(t, err)

	require.NotNil(t, statusSawMarkers, "branch-status never ran")
	assert.False(t, *statusSawMarkers, "branch-status ran while the unpushed commit still carried conflict markers")
	publishedTip, _ := runIsolatedGit(t, remote, "show", "main:"+markedUpstreamMeta)
	assert.NotContains(t, publishedTip, "<<<<<<<", "the remote tip carries conflict markers")
}
