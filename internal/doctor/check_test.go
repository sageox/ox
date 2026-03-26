package doctor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// mockCheck implements Check for testing.
type mockCheck struct {
	name     string
	category string
	result   CheckResult
}

func (m *mockCheck) Name() string {
	return m.name
}

func (m *mockCheck) Category() string {
	if m.category != "" {
		return m.category
	}
	return "test"
}

func (m *mockCheck) Run(_ context.Context, _ bool) CheckResult {
	return m.result
}

func TestCheckResult(t *testing.T) {
	tests := []struct {
		name   string
		result CheckResult
		want   Status
	}{
		{
			name: "pass result",
			result: CheckResult{
				Name:    "test check",
				Status:  StatusPass,
				Message: "all good",
			},
			want: StatusPass,
		},
		{
			name: "fail result",
			result: CheckResult{
				Name:    "test check",
				Status:  StatusFail,
				Message: "something wrong",
				Fix:     "run fix command",
			},
			want: StatusFail,
		},
		{
			name: "warn result",
			result: CheckResult{
				Name:    "test check",
				Status:  StatusWarn,
				Message: "potential issue",
			},
			want: StatusWarn,
		},
		{
			name: "skip result",
			result: CheckResult{
				Name:   "test check",
				Status: StatusSkip,
			},
			want: StatusSkip,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.result.Status)
		})
	}
}

func TestCheckInterface(t *testing.T) {
	check := &mockCheck{
		name:     "mock check",
		category: "test",
		result: CheckResult{
			Name:    "mock check",
			Status:  StatusPass,
			Message: "passed",
		},
	}

	assert.Equal(t, "mock check", check.Name())
	assert.Equal(t, "test", check.Category())

	ctx := context.Background()
	result := check.Run(ctx, false)

	assert.Equal(t, StatusPass, result.Status)
	assert.Equal(t, "passed", result.Message)
}
