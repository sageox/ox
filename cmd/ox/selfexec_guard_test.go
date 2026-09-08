package main

import (
	"context"
	"testing"

	"github.com/sageox/ox/internal/selfexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every function below re-executes the ox binary as a subprocess. Under
// `go test` os.Executable() is the compiled TEST binary, and Go's flag package
// drops the subcommand, so `ox.test <subcommand>` silently re-runs this entire
// suite — with no -test.timeout, spawning further generations each time. That
// is what left 30 orphaned `ox.test daemon start` processes on a workstation.
//
// selfexec.Path refuses under testing.Testing(). These tests pin that refusal
// to each individual call site, so re-introducing a bare os.Executable() here
// fails loudly instead of quietly rebuilding the fork bomb.

func TestAutoStartDaemon_RefusesToSpawnUnderTest(t *testing.T) {
	t.Setenv("OX_NO_DAEMON", "") // exercise the guard, not the kill switch above it

	err := autoStartDaemon()

	require.Error(t, err)
	assert.ErrorIs(t, err, selfexec.ErrUnderTest)
}

func TestStartDaemonBackground_RefusesToSpawnUnderTest(t *testing.T) {
	err := startDaemonBackground("")

	require.Error(t, err)
	assert.ErrorIs(t, err, selfexec.ErrUnderTest)
}

func TestRunPrimeForHook_RefusesToSpawnUnderTest(t *testing.T) {
	err := runPrimeForHook("claude-code", &HookContext{Phase: "session-start"})

	require.Error(t, err)
	assert.ErrorIs(t, err, selfexec.ErrUnderTest)
}

func TestRunPlanSubprocess_RefusesToSpawnUnderTest(t *testing.T) {
	out, ok := runPlanSubprocess("# a plan", planEnrichArgs()...)

	assert.False(t, ok, "plan-exit nudge must fail open rather than re-exec the test binary")
	assert.Nil(t, out)
}

func TestShellLocalQueryRunner_RefusesToSpawnUnderTest(t *testing.T) {
	results, err := (&shellLocalQueryRunner{}).Query(context.Background(), "anything", 5)

	require.Error(t, err)
	assert.ErrorIs(t, err, selfexec.ErrUnderTest)
	assert.Nil(t, results)
}
