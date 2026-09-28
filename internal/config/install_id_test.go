package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolateInstallID points the user config directory at a fresh temp dir and
// clears the override, returning where the ID file will live.
func isolateInstallID(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv(envInstallID, "")
	return filepath.Join(dir, "sageox", installIDFile)
}

// Failure prevented: every ox run is counted as a new install.
func TestInstallID_SameIDOnEveryCall(t *testing.T) {
	path := isolateInstallID(t)

	first, err := InstallID()
	require.NoError(t, err)
	second, err := InstallID()
	require.NoError(t, err)

	assert.Equal(t, first, second)
	_, err = uuid.Parse(first)
	assert.NoError(t, err, "the ID is a random UUID, not derived from the machine or user")
	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, first+"\n", string(saved))
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, entries, 1, "the temporary file the ID was written through is gone")
	assert.Equal(t, installIDFile, entries[0].Name())
}

// Failure prevented: upgrading ox gives an install that already has a
// daemon-written ID a new one.
func TestInstallID_KeepsTheIDTheDaemonSaved(t *testing.T) {
	path := isolateInstallID(t)
	existing := uuid.NewString()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(existing+"\n"), 0o600))

	id, err := InstallID()

	require.NoError(t, err)
	assert.Equal(t, existing, id)
}

// Failure prevented: a SessionStart hook and the daemon it starts run for the
// first time together, and one install ends up with two IDs.
func TestInstallID_ConcurrentFirstUseAgreesOnOneID(t *testing.T) {
	isolateInstallID(t)

	const callers = 16
	ids := make([]string, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := InstallID()
			assert.NoError(t, err)
			ids[i] = id
		}()
	}
	wg.Wait()

	for _, id := range ids {
		assert.Equal(t, ids[0], id)
	}
}

// Failure prevented: a truncated or hand-edited file is sent as an ID, or is
// replaced on every run.
func TestInstallID_ReplacesADamagedFileOnce(t *testing.T) {
	path := isolateInstallID(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("3f2a9c"), 0o600))

	replaced, err := InstallID()
	require.NoError(t, err)
	again, err := InstallID()
	require.NoError(t, err)

	assert.NotEqual(t, "3f2a9c", replaced)
	assert.Equal(t, replaced, again)
}

func TestInstallID_EnvOverrideWinsAndSavesNothing(t *testing.T) {
	path := isolateInstallID(t)
	t.Setenv(envInstallID, "fleet-assigned-id")

	id, err := InstallID()

	require.NoError(t, err)
	assert.Equal(t, "fleet-assigned-id", id)
	assert.NoFileExists(t, path)
}

// Failure prevented: an ID that could not be saved, and so changes every run,
// is reported as stable and inflates the install count.
func TestInstallID_UnsavableIDIsReportedAsAnError(t *testing.T) {
	isolateInstallID(t)
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))
	t.Setenv("XDG_CONFIG_HOME", blocker)

	id, err := InstallID()

	assert.Error(t, err)
	assert.NotEmpty(t, id, "callers that only label one process can still use it")
}

// Failure prevented: with no home directory the config directory resolves to a
// relative path, and every command writes sageox/client_id into whatever
// directory it ran in, such as a CI job's repository checkout.
func TestInstallID_NothingWrittenWithoutAnAbsoluteConfigDirectory(t *testing.T) {
	isolateInstallID(t)
	workDir := t.TempDir()
	t.Chdir(workDir)
	t.Setenv("XDG_CONFIG_HOME", "relative-config")

	id, err := InstallID()

	assert.Error(t, err)
	assert.NotEmpty(t, id)
	assert.NoDirExists(t, filepath.Join(workDir, "relative-config"))
}
