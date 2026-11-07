//go:build !short

package main

import (
	"context"
	"os"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
