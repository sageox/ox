package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckSlugOxInPath_Registered(t *testing.T) {
	check := GetDoctorCheck(CheckSlugOxInPath)
	require.NotNil(t, check, "ox-in-path check should be registered")
	assert.Equal(t, "ox in PATH", check.Name)
	assert.Equal(t, "Ecosystem", check.Category)
	assert.Equal(t, FixLevelSuggested, check.FixLevel,
		"writing to a shell rc file is opt-in-only; this check must never be FixLevelAuto")
}

func TestCheckSlugOxInPath_RunDoesNotPanic(t *testing.T) {
	check := GetDoctorCheck(CheckSlugOxInPath)
	require.NotNil(t, check)

	// exact state depends on the real environment running `go test`; the
	// state machine itself is covered exhaustively in
	// internal/doctor/checks/ox_in_path_test.go. This only proves the
	// registered closure wires through end to end without panicking.
	result := check.Run(false)
	assert.Equal(t, "ox in PATH", result.name)
	assert.False(t, !result.passed && !result.skipped,
		"should never be a hard failure -- an inconclusive probe must skip, not fail")
}

func TestCheckSlugAdapterSiblings_Registered(t *testing.T) {
	check := GetDoctorCheck(CheckSlugAdapterSiblings)
	require.NotNil(t, check, "adapter-siblings check should be registered")
	assert.Equal(t, "Adapter binaries", check.Name)
	assert.Equal(t, "Ecosystem", check.Category)
	assert.Equal(t, FixLevelCheckOnly, check.FixLevel)
}
