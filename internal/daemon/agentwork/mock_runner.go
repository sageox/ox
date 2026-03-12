package agentwork

import (
	"context"
)

// MockRunner is a test double for the Runner interface.
type MockRunner struct {
	RunFunc   func(ctx context.Context, req RunRequest) (*RunResult, error)
	available bool
}

// NewMockRunner creates a MockRunner that reports itself as available.
func NewMockRunner(available bool) *MockRunner {
	return &MockRunner{available: available}
}

func (m *MockRunner) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	if m.RunFunc != nil {
		return m.RunFunc(ctx, req)
	}
	return &RunResult{}, nil
}

func (m *MockRunner) Available() bool {
	return m.available
}
