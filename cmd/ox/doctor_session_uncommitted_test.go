package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uses initTestGitRepo from memory_put_write_test.go

func TestSessionUncommitted_CleanLedger(t *testing.T) {
	ledger := t.TempDir()
	initTestGitRepo(t, ledger)

	require.NoError(t, os.MkdirAll(filepath.Join(ledger, "sessions"), 0755))

	output, err := exec.Command("git", "-C", ledger, "status", "--porcelain", "sessions/").Output()
	require.NoError(t, err)
	assert.Empty(t, string(output))
}

func TestSessionUncommitted_DetectsUncommittedFiles(t *testing.T) {
	ledger := t.TempDir()
	initTestGitRepo(t, ledger)

	sessionDir := filepath.Join(ledger, "sessions", "test-session-001")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "meta.json"), []byte(`{"session":"test"}`), 0644))

	output, err := exec.Command("git", "-C", ledger, "status", "--porcelain", "sessions/").Output()
	require.NoError(t, err)
	assert.NotEmpty(t, string(output))
	assert.Contains(t, string(output), "meta.json")
}
