package claudemod

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failReadFS lists a MapFS but refuses to open one file, so the walk sees the
// file and then cannot read it.
type failReadFS struct {
	m   fstest.MapFS
	bad string
}

func (f failReadFS) Open(name string) (fs.File, error) {
	if name == f.bad {
		return nil, errors.New("disk read failed")
	}
	return f.m.Open(name)
}

func (f failReadFS) ReadDir(name string) ([]fs.DirEntry, error) { return f.m.ReadDir(name) }

// skipIfPermsIgnored skips tests that rely on a read-only directory blocking
// writes, which root and Windows do not honor.
func skipIfPermsIgnored(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced here")
	}
}

// lockDir makes dir read-only for the rest of the test and restores it so
// t.TempDir cleanup can remove it.
func lockDir(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

// TestEnable_WriteFailureInstallsNothing prevents enable from registering a
// mod with Claude Code when the mod folder could not be written.
func TestEnable_WriteFailureInstallsNothing(t *testing.T) {
	c := &fakeClaude{}
	in := newTestInstaller(t, c)
	in.FS = nil
	res, err := in.Enable(context.Background())
	assert.Error(t, err)
	assert.Nil(t, res)
	assert.Empty(t, c.calls, "claude is never asked to install a mod that was not written")
}

// TestWriteMod_UnreadableOrUnwritableModIsAnError prevents a partial mod from
// being swapped in when one of its files cannot be read or staged.
func TestWriteMod_UnreadableOrUnwritableModIsAnError(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Installer)
		want   string
	}{
		{
			name: "static file cannot be read",
			mutate: func(in *Installer) {
				in.FS = failReadFS{m: testMod(), bad: "mod/hooks/register.tsx"}
			},
			want: "write mod",
		},
		{
			name: "generated file collides with a static file",
			mutate: func(in *Installer) {
				in.Generated = func() (map[string][]byte, error) {
					return map[string][]byte{"hooks/register.tsx/inner.ts": []byte("x")}, nil
				}
			},
			want: "write generated files",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := newTestInstaller(t, nil)
			tt.mutate(&in)
			assert.ErrorContains(t, in.WriteMod(), tt.want)
			assert.NoDirExists(t, in.Dir, "nothing is swapped in")
		})
	}
}

// TestWriteMod_EmptyRootWritesTheWholeFS prevents a mod whose files sit at
// the FS root from being written under a stray prefix.
func TestWriteMod_EmptyRootWritesTheWholeFS(t *testing.T) {
	in := newTestInstaller(t, nil)
	in.Root = ""
	in.FS = fstest.MapFS{
		".claude-plugin/plugin.json": {Data: []byte(`{"version":"{{VERSION}}"}`)},
	}
	require.NoError(t, in.WriteMod())
	v, err := WrittenVersion(in.Dir)
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", v)
}

// TestWriteMod_FilesystemRefusalsAreReported prevents a refresh on a locked
// or obstructed data directory from silently claiming success.
func TestWriteMod_FilesystemRefusalsAreReported(t *testing.T) {
	skipIfPermsIgnored(t)
	tests := []struct {
		name  string
		setup func(t *testing.T, in Installer)
		want  string
	}{
		{
			name: "interrupted swap cannot be restored",
			setup: func(t *testing.T, in Installer) {
				require.NoError(t, in.WriteMod())
				require.NoError(t, os.Rename(in.Dir, in.Dir+".old"))
				lockDir(t, filepath.Dir(in.Dir))
			},
			want: "restore",
		},
		{
			name: "staging folder cannot be created",
			setup: func(t *testing.T, in Installer) {
				lockDir(t, filepath.Dir(in.Dir))
			},
			want: "stage mod",
		},
		{
			name: "live folder cannot be moved aside",
			setup: func(t *testing.T, in Installer) {
				require.NoError(t, in.WriteMod())
				// a backup that cannot be cleared blocks the first rename
				locked := filepath.Join(in.Dir+".old", "locked")
				require.NoError(t, os.MkdirAll(locked, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(locked, "f"), []byte("x"), 0o644))
				lockDir(t, locked)
			},
			want: "replace",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := newTestInstaller(t, nil)
			tt.setup(t, in)
			assert.ErrorContains(t, in.WriteMod(), tt.want)
		})
	}
}

// TestDisable_RemoveFailureIsReported prevents disable from reporting the mod
// gone when its folder is still on disk.
func TestDisable_RemoveFailureIsReported(t *testing.T) {
	skipIfPermsIgnored(t)
	in := newTestInstaller(t, nil)
	require.NoError(t, in.WriteMod())
	lockDir(t, filepath.Join(in.Dir, "hooks"))
	res, err := in.Disable(context.Background())
	assert.ErrorContains(t, err, "remove")
	assert.Nil(t, res)
}
