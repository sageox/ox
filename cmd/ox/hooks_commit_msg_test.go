package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunHooksCommitMsg_SkipsMergeSource(t *testing.T) {
	hooksCommitMsgSource = "merge"
	hooksCommitMsgFile = "/tmp/nonexistent"

	err := runHooksCommitMsg(nil, nil)
	assert.NoError(t, err)
}

func TestRunHooksCommitMsg_NoopWhenNotInitialized(t *testing.T) {
	dir := t.TempDir()
	msgFile := filepath.Join(dir, "COMMIT_EDITMSG")
	require.NoError(t, os.WriteFile(msgFile, []byte("test commit\n"), 0644))

	// save and restore cwd
	origDir, _ := os.Getwd()
	defer func() { _ = os.Chdir(origDir) }()
	require.NoError(t, os.Chdir(dir))

	hooksCommitMsgSource = ""
	hooksCommitMsgFile = msgFile

	err := runHooksCommitMsg(nil, nil)
	assert.NoError(t, err)

	// message should be unchanged
	content, _ := os.ReadFile(msgFile)
	assert.Equal(t, "test commit\n", string(content))
}

func TestBuildSessionURL(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *config.ProjectConfig
		sessionName string
		expected    string
	}{
		{
			name:        "valid URL",
			cfg:         &config.ProjectConfig{RepoID: "repo_01abc", Endpoint: "https://sageox.ai"},
			sessionName: "2026-01-06T14-32-ryan-Ox7f3a",
			expected:    "https://sageox.ai/repo/repo_01abc/sessions/2026-01-06T14-32-ryan-Ox7f3a/view",
		},
		{
			name:        "empty repo ID",
			cfg:         &config.ProjectConfig{RepoID: "", Endpoint: "https://sageox.ai"},
			sessionName: "session-1",
			expected:    "",
		},
		{
			name:        "empty session name",
			cfg:         &config.ProjectConfig{RepoID: "repo_01abc", Endpoint: "https://sageox.ai"},
			sessionName: "",
			expected:    "",
		},
		{
			name:        "nil config",
			cfg:         nil,
			sessionName: "test",
			expected:    "",
		},
		{
			name:        "normalizes www prefix",
			cfg:         &config.ProjectConfig{RepoID: "repo_01abc", Endpoint: "https://www.sageox.ai"},
			sessionName: "session-1",
			expected:    "https://sageox.ai/repo/repo_01abc/sessions/session-1/view",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildSessionURL(tt.cfg, tt.sessionName)
			assert.Equal(t, tt.expected, result)
		})
	}
}
