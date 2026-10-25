//go:build agents

package agentwork

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClaudeRunner_Run_SuccessfulInvocation is an opt-in compatibility check
// that spends a real Claude request. Keep it out of the hermetic full tier.
func TestClaudeRunner_Run_SuccessfulInvocation(t *testing.T) {
	r := NewClaudeRunner(slog.Default())
	if !r.Available() {
		t.Skip("claude binary not installed")
	}

	result, err := r.Run(context.Background(), RunRequest{
		Prompt:          "respond with exactly one word: hello",
		WorkDir:         t.TempDir(),
		TimeoutOverride: 30 * time.Second,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)
	assert.NotEmpty(t, result.Output, "expected non-empty output from real claude")
	assert.Greater(t, result.TokensIn, 0)
	assert.Greater(t, result.TokensOut, 0)
	assert.True(t, result.Duration > 0)
}
