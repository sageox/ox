package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/agentx"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type importContextTestEnvironment struct {
	agentx.Environment
	vars map[string]string
	root string
}

func (e importContextTestEnvironment) GetEnv(key string) string { return e.vars[key] }
func (e importContextTestEnvironment) LookupEnv(key string) (string, bool) {
	v, ok := e.vars[key]
	return v, ok
}
func (e importContextTestEnvironment) IsDir(path string) bool {
	if !filepath.IsAbs(path) {
		path = filepath.Join(e.root, path)
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
func (importContextTestEnvironment) ProcessAncestry() ([]string, error) { return nil, nil }

// A SageOx-initialized repo installs .codex/, which must not deny a coworker
// the picker while genuine coding-agent invocations retain unattended behavior.
func TestImportProjectIntegrationsDoNotImplyAgentInvocation(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".codex"), 0o700))
	for _, tc := range []struct {
		name string
		vars map[string]string
		want bool
	}{
		{"human terminal", nil, false},
		{"human terminal with PWD", map[string]string{"PWD": root}, false},
		{"codex runtime", map[string]string{"CODEX_THREAD_ID": "test-session"}, true},
		{"codex sandbox", map[string]string{"CODEX_SANDBOX": "seatbelt"}, true},
		{"claude runtime", map[string]string{"CLAUDECODE": "1"}, true},
		{"explicit agent", map[string]string{"AGENT_ENV": "codex"}, true},
		{"unknown override", map[string]string{"AGENT_ENV": "unknown"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := importContextTestEnvironment{agentx.NewSystemEnvironment(), tc.vars, root}
			assert.Equal(t, tc.want, importAgentContext(context.Background(), env))
		})
	}
	// Pin the original failure to the real detector, so this cannot be a fixture
	// that would have passed before the correction.
	env := importContextTestEnvironment{agentx.NewSystemEnvironment(), nil, root}
	detected, err := agentx.NewDetectorWithEnv(env).Detect(context.Background())
	require.NoError(t, err)
	require.NotNil(t, detected)
	assert.True(t, importAgentContext((&cobra.Command{}).Context(), importContextTestEnvironment{
		agentx.NewSystemEnvironment(), map[string]string{"CLAUDE_CODE_ENTRYPOINT": "cli"}, root,
	}))
}
