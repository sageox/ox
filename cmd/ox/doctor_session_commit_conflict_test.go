package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const conflictedSession = "sessions/2026-09-24T10-00-alice-OxAAAA"

// newDoctorLedgerProject wires a project whose ledger resolves the same way for
// getLedgerPath and the auto-stage check, and chdirs into it.
func newDoctorLedgerProject(t *testing.T) string {
	t.Helper()
	skipIntegration(t)
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmp, "cache"))
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	project := filepath.Join(tmp, "project")
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".sageox"), 0o755))
	mustRunGit(t, project, "init", "--initial-branch=main")
	require.NoError(t, os.WriteFile(filepath.Join(project, ".sageox", "config.json"),
		[]byte(`{"config_version":"2","repo_id":"repo_1055"}`), 0o644))
	ledgerPath := config.DefaultLedgerPath("repo_1055", endpoint.GetForProject(project))
	require.NoError(t, config.SaveLocalConfig(project, &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: ledgerPath},
	}))

	require.NoError(t, os.MkdirAll(ledgerPath, 0o755))
	mustRunGit(t, ledgerPath, "init", "--initial-branch=main")
	mustRunGit(t, ledgerPath, "config", "user.name", "Test")
	mustRunGit(t, ledgerPath, "config", "user.email", "test@example.com")
	mustRunGit(t, ledgerPath, "config", "commit.gpgsign", "false")
	writeSessionFile(t, ledgerPath, conflictedSession+"/meta.json", "{\n  \"title\": \"base\"\n}\n")
	mustRunGit(t, ledgerPath, "add", "--sparse", "sessions/")
	mustRunGit(t, ledgerPath, "commit", "-m", "base")

	t.Chdir(project)
	require.Equal(t, ledgerPath, getLedgerPath())
	return ledgerPath
}

func writeSessionFile(t *testing.T, ledgerPath, rel, content string) {
	t.Helper()
	path := filepath.Join(ledgerPath, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// createAutostashConflict leaves meta.json unmerged with no merge/rebase in
// progress (the stash-pop shape from #956), plus a new session written behind it.
func createAutostashConflict(t *testing.T, ledgerPath string) {
	t.Helper()
	meta := conflictedSession + "/meta.json"
	writeSessionFile(t, ledgerPath, meta, "{\n  \"title\": \"local title\"\n}\n")
	mustRunGit(t, ledgerPath, "stash", "push", "-m", "autostash")
	writeSessionFile(t, ledgerPath, meta, "{\n  \"title\": \"upstream title\"\n}\n")
	mustRunGit(t, ledgerPath, "commit", "-am", "upstream")
	out, err := runIsolatedGit(t, ledgerPath, "stash", "pop")
	require.Error(t, err, "stash pop must conflict: %s", out)

	writeSessionFile(t, ledgerPath, "sessions/2026-09-24T11-00-bob-OxBBBB/meta.json", `{"title":"b"}`+"\n")
	writeSessionFile(t, ledgerPath, "sessions/2026-09-24T11-00-bob-OxBBBB/summary.json", `{"summary":"b"}`+"\n")
	status, err := runIsolatedGit(t, ledgerPath, "status", "--porcelain=v1")
	require.NoError(t, err)
	require.Contains(t, status, "UU "+meta)
}

func headLog(t *testing.T, ledgerPath string) string {
	t.Helper()
	log, err := runIsolatedGit(t, ledgerPath, "log", "--oneline")
	require.NoError(t, err)
	return log
}

// TestDoctorFix_AutostashConflictNeverCommittedAsSessionUpdate runs the Sessions
// checks in `ox doctor --fix` order against a live stash-pop conflict.
// Without it, auto-stage marks the conflict resolved and the session commit
// bakes the markers into ledger history as invalid meta.json (#1055).
func TestDoctorFix_AutostashConflictNeverCommittedAsSessionUpdate(t *testing.T) {
	ledgerPath := newDoctorLedgerProject(t)
	createAutostashConflict(t, ledgerPath)
	before := headLog(t, ledgerPath)

	doctor.NewSessionAutoStageCheck(findGitRoot()).Run(context.Background(), false)
	r := checkSessionCommit(true)

	assert.False(t, r.passed, "must refuse: %+v", r)
	assert.Contains(t, r.message, "refusing to auto-commit")
	assert.Equal(t, before, headLog(t, ledgerPath), "no commit may be created")
	unmerged, err := runIsolatedGit(t, ledgerPath, "ls-files", "-u")
	require.NoError(t, err)
	assert.NotEmpty(t, unmerged, "the conflict must stay unmerged for manual or automatic resolution")
	head, err := runIsolatedGit(t, ledgerPath, "show", "HEAD:"+conflictedSession+"/meta.json")
	require.NoError(t, err)
	assert.True(t, json.Valid([]byte(head)), "HEAD meta.json must stay valid JSON: %s", head)
}

// TestSessionCommit_RefusesSeveralUnmergedPaths covers a stash pop that leaves more than one session unmerged.
// Without it, the refusal could under-report how many files need manual resolution.
func TestSessionCommit_RefusesSeveralUnmergedPaths(t *testing.T) {
	ledgerPath := newDoctorLedgerProject(t)
	second := "sessions/2026-09-24T12-00-carol-OxCCCC/meta.json"
	writeSessionFile(t, ledgerPath, second, "{\n  \"title\": \"base\"\n}\n")
	mustRunGit(t, ledgerPath, "add", "--sparse", "sessions/")
	mustRunGit(t, ledgerPath, "commit", "-m", "second session")
	metas := []string{conflictedSession + "/meta.json", second}
	for _, rel := range metas {
		writeSessionFile(t, ledgerPath, rel, "{\n  \"title\": \"local\"\n}\n")
	}
	mustRunGit(t, ledgerPath, "stash", "push", "-m", "autostash")
	for _, rel := range metas {
		writeSessionFile(t, ledgerPath, rel, "{\n  \"title\": \"upstream\"\n}\n")
	}
	mustRunGit(t, ledgerPath, "commit", "-am", "upstream")
	out, err := runIsolatedGit(t, ledgerPath, "stash", "pop")
	require.Error(t, err, "stash pop must conflict: %s", out)
	before := headLog(t, ledgerPath)

	r := checkSessionCommit(true)

	assert.False(t, r.passed, "must refuse: %+v", r)
	assert.Contains(t, r.detail, "2 unmerged file(s)")
	assert.Contains(t, r.detail, "(+1 more)")
	assert.Equal(t, before, headLog(t, ledgerPath), "no commit may be created")
}

// TestSessionCommit_RefusesStagedConflictMarkers covers markers already staged
// with no U-state left (e.g. a human `git add` on the conflict).
// Without it, the bare commit publishes the markers.
func TestSessionCommit_RefusesStagedConflictMarkers(t *testing.T) {
	ledgerPath := newDoctorLedgerProject(t)
	writeSessionFile(t, ledgerPath, conflictedSession+"/meta.json",
		"{\n<<<<<<< Updated upstream\n  \"title\": \"a\"\n=======\n  \"title\": \"b\"\n>>>>>>> Stashed changes\n}\n")
	mustRunGit(t, ledgerPath, "add", "--sparse", "sessions/")
	before := headLog(t, ledgerPath)

	r := checkSessionCommit(true)

	assert.False(t, r.passed, "must refuse: %+v", r)
	assert.Contains(t, r.detail, conflictedSession+"/meta.json")
	assert.Equal(t, before, headLog(t, ledgerPath), "no commit may be created")
}

// TestSessionCommit_CommitsCleanStagedSession guards the normal path and the sessions/ scope.
// Without it, doctor could stop committing healthy sessions or sweep unrelated staged files into them.
func TestSessionCommit_CommitsCleanStagedSession(t *testing.T) {
	ledgerPath := newDoctorLedgerProject(t)
	writeSessionFile(t, ledgerPath, "sessions/2026-09-24T11-00-bob-OxBBBB/meta.json", `{"title":"b"}`+"\n")
	writeSessionFile(t, ledgerPath, "sessions/2026-09-24T11-00-bob-OxBBBB/summary.json", `{"summary":"b"}`+"\n")
	writeSessionFile(t, ledgerPath, "notes.txt", "not a session\n")
	mustRunGit(t, ledgerPath, "add", "notes.txt")
	ar := doctor.NewSessionAutoStageCheck(findGitRoot()).Run(context.Background(), false)
	require.Equal(t, doctor.StatusPass, ar.Status)

	r := checkSessionCommit(true)

	assert.True(t, r.passed, "%+v", r)
	status, err := runIsolatedGit(t, ledgerPath, "status", "--porcelain=v1")
	require.NoError(t, err)
	assert.Equal(t, "A  notes.txt", status, "sessions must be committed and non-session files left staged")
	subject, err := runIsolatedGit(t, ledgerPath, "log", "-1", "--format=%s")
	require.NoError(t, err)
	assert.Contains(t, subject, "Update sessions")
}
