package doctor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRunner(t *testing.T) {
	runner := NewRunner()
	require.NotNil(t, runner)
	assert.NotNil(t, runner.checks)
}

func TestRunnerRegister(t *testing.T) {
	runner := NewRunner()

	check1 := &mockCheck{
		name: "check1",
		result: CheckResult{
			Name:   "check1",
			Status: StatusPass,
		},
	}
	check2 := &mockCheck{
		name: "check2",
		result: CheckResult{
			Name:   "check2",
			Status: StatusFail,
		},
	}

	runner.Register(check1)
	runner.Register(check2)

	assert.Len(t, runner.checks, 2)
}

func TestRunnerRunAll(t *testing.T) {
	runner := NewRunner()

	checks := []Check{
		&mockCheck{
			name: "pass check",
			result: CheckResult{
				Name:    "pass check",
				Status:  StatusPass,
				Message: "ok",
			},
		},
		&mockCheck{
			name: "warn check",
			result: CheckResult{
				Name:    "warn check",
				Status:  StatusWarn,
				Message: "warning",
			},
		},
		&mockCheck{
			name: "fail check",
			result: CheckResult{
				Name:    "fail check",
				Status:  StatusFail,
				Message: "error",
				Fix:     "fix it",
			},
		},
		&mockCheck{
			name: "skip check",
			result: CheckResult{
				Name:   "skip check",
				Status: StatusSkip,
			},
		},
	}

	for _, c := range checks {
		runner.Register(c)
	}

	ctx := context.Background()
	results := runner.RunAll(ctx, false)

	require.Len(t, results, 4)

	// verify each result matches expected
	assert.Equal(t, StatusPass, results[0].Status)
	assert.Equal(t, StatusWarn, results[1].Status)
	assert.Equal(t, StatusFail, results[2].Status)
	assert.Equal(t, StatusSkip, results[3].Status)

	// verify fix hint is preserved
	assert.Equal(t, "fix it", results[2].Fix)
}

func TestRunnerEmptyChecks(t *testing.T) {
	runner := NewRunner()

	ctx := context.Background()
	results := runner.RunAll(ctx, false)

	assert.Empty(t, results)
}
