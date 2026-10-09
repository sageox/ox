package cursorpaths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeSourceRejectsInvalidIdentityAndWorkspaceLocations(t *testing.T) {
	for _, test := range []struct {
		name, field, value string
	}{
		{"noncanonical identity", "identity", "../not-a-conversation"},
		{"empty home", "home", ""},
		{"relative home", "home", "relative-home"},
		{"file home", "home", "file"},
		{"empty repository", "repo", ""},
		{"relative repository", "repo", "relative-repository"},
		{"file repository", "repo", "file"},
		{"missing repository", "repo", "missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home, repo := cursorFixture(t)
			id := conversationA
			value := test.value
			switch value {
			case "file":
				value = filepath.Join(t.TempDir(), "personal-file-name")
				require.NoError(t, os.WriteFile(value, []byte("preserve me"), 0o600))
			case "missing":
				value = filepath.Join(t.TempDir(), "unavailable-repository")
			}
			switch test.field {
			case "identity":
				id = value
			case "home":
				home = value
			case "repo":
				repo = value
			}
			path, err := ValidateSource(home, repo, id, "")
			require.Empty(t, path, "invalid native claims must not produce a usable source path")
			if test.field == "identity" {
				require.ErrorIs(t, err, ErrInvalidConversationID)
			} else {
				require.ErrorIs(t, err, ErrInvalidSource)
			}
			if value != "" {
				require.NotContains(t, err.Error(), value, "diagnostics must not expose native paths or identities")
			}
		})
	}
}

func TestNativeSourceRejectsNonDirectoryAncestor(t *testing.T) {
	home, repo := cursorFixture(t)
	path, err := SessionPath(home, repo, conversationA)
	require.NoError(t, err)
	parent := filepath.Dir(path)
	require.NoError(t, os.MkdirAll(filepath.Dir(parent), 0o755))
	require.NoError(t, os.WriteFile(parent, []byte("ordinary file"), 0o600))
	got, err := ValidateSource(home, repo, conversationA, "")
	require.Empty(t, got)
	require.ErrorIs(t, err, ErrInvalidSource)
	require.ErrorContains(t, err, "native source component is not a directory")
	data, err := os.ReadFile(parent)
	require.NoError(t, err)
	require.Equal(t, "ordinary file", string(data))
}

func TestNativeSourceRetainsPermissionErrorWithoutLeakingPath(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix file permissions without root privileges")
	}
	home, repo := cursorFixture(t)
	path := writeNativeTranscript(t, home, repo, conversationA)
	parent := filepath.Dir(path)
	require.NoError(t, os.Chmod(parent, 0))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	got, err := ValidateSource(home, repo, conversationA, "")
	require.Empty(t, got)
	require.ErrorIs(t, err, ErrInvalidSource)
	require.ErrorIs(t, err, os.ErrPermission)
	require.NotContains(t, err.Error(), path)
	require.NotContains(t, err.Error(), home)
	require.NotContains(t, err.Error(), conversationA)
}
