package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

func TestCursorDiagnoseRejectsUnavailableProjectAndAdapter(t *testing.T) {
	for _, state := range []string{"missing workspace", "file workspace", "unreadable workspace", "missing adapter"} {
		t.Run(state, func(t *testing.T) {
			_, repo, deps := cursorDiagnoseFixture(t)
			privateError := errors.New("private-user-native-path")
			slug := "workspace-mismatch"
			switch state {
			case "missing workspace":
				repo = filepath.Join(repo, "missing")
			case "file workspace":
				repo = filepath.Join(repo, "file")
				require.NoError(t, os.WriteFile(repo, []byte("ordinary file"), 0o600))
			case "unreadable workspace":
				deps.stat = func(string) (os.FileInfo, error) { return nil, privateError }
			case "missing adapter":
				slug = "adapter-missing"
				deps.currentExecutable = func() (string, error) { return "", privateError }
			}
			result := diagnoseCursor(adapterprotocol.DiagnoseParams{RepoRoot: repo, Scope: "project"}, deps)
			require.False(t, result.OK)
			issue := cursorIssue(result.Issues, slug)
			require.NotNil(t, issue)
			require.False(t, issue.FixSafe, "unavailable resources must not be advertised as safely repairable hooks")
			encoded, err := json.Marshal(result)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), privateError.Error())
			require.NotContains(t, string(encoded), repo)
		})
	}
}

func TestCursorDiagnoseRefusesRepairWhenHookInspectionFails(t *testing.T) {
	_, repo, deps := cursorDiagnoseFixture(t)
	hooks := filepath.Join(repo, ".cursor", "hooks.json")
	deps.checkHooks = func(string, string) (string, bool, error) { return hooks, false, nil }
	deps.lstat = func(string) (os.FileInfo, error) {
		return nil, &os.PathError{Op: "lstat", Path: hooks, Err: os.ErrPermission}
	}
	result := diagnoseCursor(adapterprotocol.DiagnoseParams{RepoRoot: repo, Scope: "project"}, deps)
	issue := cursorIssue(result.Issues, "hooks-invalid")
	require.NotNil(t, issue)
	require.Equal(t, "error", issue.Severity)
	require.False(t, issue.FixSafe)
	require.Empty(t, issue.FixArgv)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), hooks)
}

func TestCursorDiagnoseRechecksNativeRootAfterPathValidation(t *testing.T) {
	for _, state := range []string{"replaced with file", "permission lost"} {
		t.Run(state, func(t *testing.T) {
			if state == "permission lost" && (runtime.GOOS == "windows" || os.Geteuid() == 0) {
				t.Skip("requires Unix file permissions without root privileges")
			}
			home, repo, deps := cursorDiagnoseFixture(t)
			root := createCursorTranscriptRoot(t, home, repo, deps)
			probe, err := deps.sessionPath(home, repo, cursorDiagnosticProbeConversation)
			require.NoError(t, err)
			// Model the native root changing after its exact source path was
			// validated. Later filesystem checks must still reject the root.
			deps.sessionPath = func(string, string, string) (string, error) { return probe, nil }
			slug := "invalid-source-path"
			if state == "replaced with file" {
				require.NoError(t, os.Remove(root))
				require.NoError(t, os.WriteFile(root, []byte("not a directory"), 0o600))
			} else {
				slug = "source-unreadable"
				require.NoError(t, os.Chmod(root, 0))
				t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
			}
			result := diagnoseCursor(adapterprotocol.DiagnoseParams{RepoRoot: repo, Scope: "project"}, deps)
			require.False(t, result.OK)
			issue := cursorIssue(result.Issues, slug)
			require.NotNil(t, issue)
			require.False(t, issue.FixSafe)
			encoded, err := json.Marshal(result)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), root)
		})
	}
}
