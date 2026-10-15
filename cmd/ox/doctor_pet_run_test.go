//go:build !short

package main

import (
	"context"
	"testing"

	"github.com/sageox/ox/internal/pet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoctorRun_RefreshesStaleOxy drives the real doctor runner, the way a
// coworker's plain `ox doctor` does. Doctor executes an explicit list, so a
// check that is only registered never runs.
// Failure prevented: the refresh exists and passes its unit test, yet no
// `ox doctor` run ever reaches it.
func TestDoctorRun_RefreshesStaleOxy(t *testing.T) {
	if testing.Short() {
		t.Skip("short: full doctor run plus rendering every Oxy frame")
	}
	sandboxDoctorEnv(t)
	project := testGitRepo(t)
	t.Chdir(project)
	writePetMod("0.0.1")(t, pet.Dir())

	categories, err := runDoctorChecks(context.Background(), doctorOptions{})
	require.NoError(t, err)

	var found bool
	for _, cat := range categories {
		for _, check := range cat.checks {
			if check.name == "Oxy pet" {
				found = true
				assert.True(t, check.passed, check.message)
			}
		}
	}
	require.True(t, found, "doctor output has no Oxy pet line")
	got, err := pet.WrittenVersion(pet.Dir())
	require.NoError(t, err)
	assert.NotEqual(t, "0.0.1", got, "plain `ox doctor` must refresh a stale Oxy")
}
