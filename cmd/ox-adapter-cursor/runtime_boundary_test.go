package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorRuntimeDiagnoseUsesIsolatedNativeRoot(t *testing.T) {
	home, repo, deps := cursorDiagnoseFixture(t)
	t.Setenv("HOME", home)
	bin := t.TempDir()
	makeCursorTestExecutable(t, bin, "ox")
	t.Setenv("PATH", bin)
	root := createCursorTranscriptRoot(t, home, repo, deps)
	result, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: repo, Scope: "project"})
	require.NoError(t, err)
	require.NotNil(t, result)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), home)
	assert.Contains(t, string(encoded), "hooks-missing")
	assert.NotContains(t, string(encoded), "source-not-found")
	assert.NotContains(t, string(encoded), "adapter-missing")

	// The public handler's default dependencies must reject an actual native
	// project-root link, not just a simulated error from a test dependency.
	require.NoError(t, os.Remove(root))
	require.NoError(t, os.Symlink(t.TempDir(), root))
	result, err = handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: repo, Scope: "project"})
	require.NoError(t, err)
	encoded, err = json.Marshal(result)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "invalid-source-path")
	assert.NotContains(t, string(encoded), home)
}

func TestCursorRuntimeOxLookupRequiresExecutable(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	path := makeCursorTestExecutable(t, bin, "ox")
	found, err := findCursorOx()
	require.NoError(t, err)
	canonical, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	resolved, err := filepath.EvalSymlinks(found)
	require.NoError(t, err)
	assert.Equal(t, canonical, resolved)
	require.NoError(t, os.Chmod(path, 0o600))
	_, err = findCursorOx()
	require.ErrorContains(t, err, "ox-not-found")
}

func TestCursorRuntimeSourceErrorsRemovePrivatePath(t *testing.T) {
	for _, cause := range []error{os.ErrNotExist, os.ErrPermission} {
		err := cursorSourceError(&os.PathError{Op: "open", Path: "/private/conversation.jsonl", Err: cause})
		assert.ErrorIs(t, err, cause)
		assert.False(t, strings.Contains(err.Error(), "/private/conversation.jsonl"))
	}
	plain := errors.New("source-unreadable")
	assert.ErrorIs(t, cursorSourceError(plain), plain)
}

func TestCursorRuntimeMissingHomeDoesNotDiscoverAnotherProfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix HOME contract")
	}
	t.Setenv("HOME", "")
	t.Setenv("AGENT_ENV", "")
	t.Setenv("PATH", t.TempDir())
	result, err := handleDetect()
	require.NoError(t, err)
	assert.False(t, result.Detected)
	assert.Contains(t, result.Reason, "home directory")
	_, err = handleFindSession(adapterprotocol.FindSessionParams{
		RepoRoot: t.TempDir(), AgentSessionID: cursorDiagnosticProbeConversation,
	})
	require.ErrorContains(t, err, "home directory")
	diagnosis, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: t.TempDir(), Scope: "project"})
	require.NoError(t, err)
	encoded, err := json.Marshal(diagnosis)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "source-unreadable")
	var stdout, stderr bytes.Buffer
	require.NoError(t, runCursorAdapter([]string{"hook", "beforeSubmitPrompt"}, strings.NewReader(`{"private":"do not expose"}`), &stdout, &stderr))
	assert.JSONEq(t, `{"continue":true}`, stdout.String())
	assert.Contains(t, stderr.String(), "workspace-unavailable")
	assert.NotContains(t, stderr.String(), "do not expose")
}
