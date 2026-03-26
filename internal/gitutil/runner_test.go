package gitutil

import (
	"context"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultRunner(t *testing.T) {
	runner := DefaultRunner()
	require.NotNil(t, runner)
	assert.IsType(t, &RealRunner{}, runner)
}

func TestRealRunner_RunGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	runner := &RealRunner{}
	output, err := runner.RunGit(context.Background(), "", "version")
	require.NoError(t, err)
	assert.Contains(t, output, "git version")
}
