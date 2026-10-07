package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session/nativeimport"
)

// --from-test-data lets a test run import sessions seeded into a directory
// that stands in for this machine's stores: <dir>/claude for ~/.claude and
// <dir>/codex for ~/.codex.

// Failure prevented: a test run importing the coworker's real sessions, or
// sessions seeded for a test reaching the coworker's own Claude and Codex.
func TestImportE2E_FromTestDataReadsOnlyThatDirectory(t *testing.T) {
	f := newImportFixture(t)
	start := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt, reply: "Fixed the cookie."})
	testData := t.TempDir()
	f.claudeProjects = filepath.Join(testData, "claude", "projects")
	f.codexHome = filepath.Join(testData, "codex")
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: start.Add(time.Hour), prompt: pushPrompt, reply: "Re-uploaded the object."})

	seeded := f.run(t, importOptions{jsonOut: true, testData: testData})
	require.NoError(t, seeded.err, seeded.out)
	require.Len(t, seeded.report.Sessions, 1, seeded.out)
	assert.Equal(t, e2eCodexA, seeded.report.Sessions[0].NativeID, "only the test data is read")

	machine := f.run(t, importOptions{jsonOut: true})
	require.NoError(t, machine.err, machine.out)
	require.Len(t, machine.report.Sessions, 1, machine.out)
	assert.Equal(t, e2eClaudeA, machine.report.Sessions[0].NativeID, "without the flag, only this machine's stores")
}

// The upload command a preview prints keeps reading the same test data, and
// running it as printed uploads the sessions the preview showed.
//
// Failure prevented: the printed command reading this machine's stores, so an
// AI coworker running it finds none of the previewed sessions.
func TestImportE2E_FromTestDataSurvivesThePrintedCommand(t *testing.T) {
	f := newImportFixture(t)
	testData := t.TempDir()
	f.claudeProjects = filepath.Join(testData, "claude", "projects")
	f.codexHome = filepath.Join(testData, "codex")
	start := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: start, prompt: pushPrompt, reply: "Re-uploaded the object."})

	preview := f.run(t, importOptions{jsonOut: true, agentCtx: true, testData: testData})
	require.NoError(t, preview.err, preview.out)
	assert.Contains(t, preview.report.NextCommand, "--from-test-data '"+testData+"'")

	upload := f.run(t, optionsFromCommand(t, preview.report.NextCommand))
	require.NoError(t, upload.err, upload.out)
	assert.Equal(t, "uploaded", upload.session(t, e2eCodexA).Outcome)
	assert.Contains(t, remoteSessionDirs(t, f.barePath), upload.session(t, e2eCodexA).SessionName)
}

// Real Claude Code and Codex Desktop sessions, captured and cleaned with
// scripts/session_import_testbed.py, replayed through production discovery and
// classification: every one is readable and in scope, and the Codex review
// thread is recognized as internal.
//
// Failure prevented: a change in either app's file format, or in ox's reading
// of it, silently dropping real sessions from an import.
func TestImportE2E_CapturedDesktopSessions(t *testing.T) {
	f := newImportFixture(t)
	testData := seedCapturedSessions(t, repoPath("testdata", "session-import", "math-blitz"), f.projectRoot)

	r := f.run(t, importOptions{jsonOut: true, testData: testData})
	require.NoError(t, r.err, r.out)
	states := map[string]string{}
	for _, s := range r.report.Sessions {
		states[s.NativeID] = s.State
	}
	for _, id := range []string{
		"01a0f957-40ec-7192-b67b-71c990b28f23", // Codex Desktop: architecture and engine
		"01a0f968-60ef-7500-aaa4-13cbb34e5557", // Codex Desktop: adaptive difficulty
		"21bb267b-4585-4b4c-b7ca-062f545259f0", // Claude Desktop: high scores
		"8fa7dffa-45e9-4298-91be-05a4ea0df37b", // Claude Desktop: terminal UI
		"077ecc79-bdf7-4708-b558-7ff5dd82fe5b", // Claude Desktop: first game
		"01a0f8f3-d483-7643-b43d-e3fa4d8cee96", // Codex Desktop: hello world
		"185a01e8-9b38-46b6-a686-b6a4e3509c3d", // Claude CLI: a one-line login check
	} {
		assert.Equal(t, string(stateReady), states[id], id)
	}
	assert.Len(t, r.report.Sessions, 7, r.out)
	assert.Equal(t, 1, r.report.Ignored.InternalThreads, "the Codex review thread is internal")
	assert.Zero(t, r.report.Ignored.Unreadable, "every captured file parses")
}

// seedCapturedSessions lays a captured fixture out the way the testbed script's
// create command does: the native layout under a test-data directory, with the
// templated repo and home paths filled in and every file quiet for a day.
func seedCapturedSessions(t *testing.T, fixture, repo string) string {
	t.Helper()
	testData := t.TempDir()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	quiet := time.Now().Add(-24 * time.Hour)
	for agent, dir := range map[string]string{
		"claude": filepath.Join(testData, "claude", "projects", "-captured"),
		"codex":  filepath.Join(testData, "codex", "sessions", "2026", "10", "01"),
	} {
		files, err := filepath.Glob(filepath.Join(fixture, agent, "*.jsonl"))
		require.NoError(t, err)
		require.NotEmpty(t, files, "fixture %s has no %s sessions", fixture, agent)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		for _, src := range files {
			data, err := os.ReadFile(src)
			require.NoError(t, err)
			text := strings.NewReplacer("__REPO__", repo, "__HOME__", home).Replace(string(data))
			dest := filepath.Join(dir, filepath.Base(src))
			require.NoError(t, os.WriteFile(dest, []byte(text), 0o600))
			require.NoError(t, os.Chtimes(dest, quiet, quiet))
		}
	}
	return testData
}

// The flag as a coworker types it: a relative path is resolved to an absolute
// one, and the run says on stderr which directory it reads instead of this
// machine's stores.
//
// Failure prevented: a relative --from-test-data that names another directory
// once a printed command is rerun from elsewhere, or a test run that reads
// test data without saying so.
func TestImportCommand_FromTestDataIsResolvedAndAnnounced(t *testing.T) {
	p := newImportCmdProject(t)
	require.NoError(t, os.MkdirAll(filepath.Join(p.root, "td"), 0o755))
	root, out := importCommand("--json", "--from-test-data", "td", "--dry-run")
	stderr := &bytes.Buffer{}
	root.SetErr(stderr)

	err := root.Execute()

	assert.ErrorIs(t, err, cli.ErrSilent)
	assert.Equal(t, importErrNativeUnreadable, decodeRefusal(t, out)["error"], "an empty test-data directory is refused")
	announced := strings.TrimSpace(strings.TrimPrefix(stderr.String(), "Reading sessions from test data in "))
	require.NotEqual(t, strings.TrimSpace(stderr.String()), announced, stderr.String())
	assert.True(t, filepath.IsAbs(announced), "resolved to an absolute path: %q", announced)
	assert.Equal(t, "td", filepath.Base(announced))
}

// Every command an import prints carries the test-data directory, the retry of
// a failed session included.
//
// Failure prevented: a retry that reads this machine's stores and reports the
// failed session as gone.
func TestImportCommands_CarryTheTestDataDirectory(t *testing.T) {
	c := &importCandidate{Session: nativeimport.Session{NativeID: e2eCodexA}}
	for name, cmd := range map[string]string{
		"upload": importUploadCommand(importOptions{testData: "/tmp/td"}, []*importCandidate{c}),
		"retry":  importRetryCommand(importOptions{testData: "/tmp/td"}, c),
	} {
		assert.Contains(t, cmd, "--from-test-data '/tmp/td'", name)
		assert.Equal(t, "/tmp/td", optionsFromCommand(t, cmd).testData, name)
	}
	assert.NotContains(t, importRetryCommand(importOptions{}, c), "--from-test-data", "absent unless set")
}

// A test-data directory that holds no sessions is refused rather than read as
// "nothing to import".
//
// Failure prevented: a mistyped path reporting a clean, empty preview.
func TestImportE2E_FromTestDataRefusesADirectoryWithoutSessions(t *testing.T) {
	f := newImportFixture(t)
	r := f.run(t, importOptions{jsonOut: true, testData: t.TempDir()})
	assert.ErrorIs(t, r.err, cli.ErrSilent)
	var refusal struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(r.out), &refusal), r.out)
	assert.Equal(t, "refused", refusal.Status)
	assert.Equal(t, importErrNativeUnreadable, refusal.Error)
}
