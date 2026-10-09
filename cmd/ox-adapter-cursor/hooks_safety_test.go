package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

func TestCursorHookOperationsRejectInvalidLocations(t *testing.T) {
	executable := makeCursorTestExecutable(t, t.TempDir(), "ox-adapter-cursor")
	operations := []struct {
		name string
		run  func(string, string) error
	}{
		{"install", func(root, exe string) error { _, _, err := installCursorHooks(root, exe); return err }},
		{"check", func(root, exe string) error { _, _, err := checkCursorHooks(root, exe); return err }},
		{"uninstall", func(root, exe string) error { _, _, err := uninstallCursorHooks(root, exe); return err }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, "ordinary-file")
			require.NoError(t, os.WriteFile(file, []byte("preserve me"), 0o600))
			for _, test := range []struct {
				name, root, executable, want string
			}{
				{"relative workspace", "relative-repository", executable, "repo_root must be absolute"},
				{"missing workspace", filepath.Join(root, "missing"), executable, "resolve repo_root"},
				{"file workspace", file, executable, "repo_root is not a directory"},
				{"relative executable", root, "ox-adapter-cursor", "executable must be absolute"},
				{"directory executable", root, root, "executable is not a regular file"},
			} {
				t.Run(test.name, func(t *testing.T) {
					require.ErrorContains(t, operation.run(test.root, test.executable), test.want)
				})
			}
			data, err := os.ReadFile(file)
			require.NoError(t, err)
			require.Equal(t, "preserve me", string(data))
			_, err = os.Stat(filepath.Join(root, ".cursor"))
			require.ErrorIs(t, err, os.ErrNotExist, "rejected locations must not create configuration")
		})
	}
}

func TestCursorHookHandlersRequireUsableProject(t *testing.T) {
	for _, params := range []adapterprotocol.HookParams{
		{Scope: "project"},
		{Scope: "project", RepoRoot: filepath.Join(t.TempDir(), "missing")},
	} {
		installed, err := handleInstallHooks(params)
		require.Error(t, err)
		require.Nil(t, installed)
		checked, err := handleCheckHooks(params)
		require.Error(t, err)
		require.Nil(t, checked)
		uninstalled, err := handleUninstallHooks(params)
		require.Error(t, err)
		require.Nil(t, uninstalled)
	}
}

func TestCursorHookUninstallMissingConfigurationIsNoOp(t *testing.T) {
	executable := makeCursorTestExecutable(t, t.TempDir(), "ox-adapter-cursor")
	for _, directoryExists := range []bool{false, true} {
		root := t.TempDir()
		directory := filepath.Join(root, ".cursor")
		if directoryExists {
			require.NoError(t, os.Mkdir(directory, 0o755))
		}
		_, changed, err := uninstallCursorHooks(root, executable)
		require.NoError(t, err)
		require.False(t, changed)
		_, installed, owned, err := inspectCursorHooks(root, executable)
		require.NoError(t, err)
		require.False(t, installed)
		require.False(t, owned)
		_, err = os.Stat(filepath.Join(directory, "hooks.json"))
		require.ErrorIs(t, err, os.ErrNotExist)
		if !directoryExists {
			_, err = os.Stat(directory)
			require.ErrorIs(t, err, os.ErrNotExist)
		}
	}
}

func TestCursorHookUpgradeDeduplicatesQuotedStaleCommands(t *testing.T) {
	root := t.TempDir()
	executable := makeCursorTestExecutable(t, t.TempDir(), "ox-adapter-cursor")
	staleCommand := cursorHookCommand(filepath.Join(root, "old ' location", "ox-adapter-cursor"), "sessionStart")
	foreignCommands := []string{"'/unfinished", "'", `'"'"'`, "'relative/ox-adapter-cursor' hook sessionStart"}
	entries := []map[string]any{
		{"command": staleCommand, "timeout": 2, "firstOwnedMetadata": "keep"},
		{"command": cursorHookCommand(executable, "sessionStart"), "timeout": 10},
	}
	for _, command := range foreignCommands {
		entries = append(entries, map[string]any{"command": command})
	}
	path := writeCursorHooksFixture(t, root, mustJSON(t, map[string]any{
		"version": 1, "hooks": map[string]any{"sessionStart": entries},
	}), 0o600)
	_, installed, owned, err := inspectCursorHooks(root, executable)
	require.NoError(t, err)
	require.False(t, installed)
	require.True(t, owned, "partial/stale installations still need uninstall support")
	_, changed, err := installCursorHooks(root, executable)
	require.NoError(t, err)
	require.True(t, changed)
	got := cursorFixtureEntries(t, decodeCursorHooksFixture(t, path), "sessionStart")
	require.Len(t, got, 1+len(foreignCommands))
	require.Equal(t, map[string]any{
		"command": cursorHookCommand(executable, "sessionStart"), "timeout": float64(10), "firstOwnedMetadata": "keep",
	}, got[0])
	_, changed, err = uninstallCursorHooks(root, executable)
	require.NoError(t, err)
	require.True(t, changed)
	got = cursorFixtureEntries(t, decodeCursorHooksFixture(t, path), "sessionStart")
	require.Len(t, got, len(foreignCommands))
	for i, command := range foreignCommands {
		require.Equal(t, command, got[i].(map[string]any)["command"])
	}
}

// The read and atomic write boundaries revalidate paths after the outer
// install/check/uninstall checks. Replacing a checked path must never redirect
// a subsequent read or write outside this repository.
func TestCursorHookIORefusesReplacedConfigurationPaths(t *testing.T) {
	for _, target := range []string{"missing root", "missing parent", "file parent", "symlink parent", "directory leaf", "symlink leaf"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			path := writeCursorHooksFixture(t, root, `{"version":1,"hooks":{}}`, 0o600)
			document, exists, err := readCursorHooksDocument(path)
			require.NoError(t, err)
			require.True(t, exists)
			outside := t.TempDir()
			externalPath := filepath.Join(outside, "hooks.json")
			preserved := []byte(`{"version":1,"hooks":{},"external":"must survive"}`)
			require.NoError(t, os.WriteFile(externalPath, preserved, 0o600))
			switch target {
			case "missing root":
				require.NoError(t, os.RemoveAll(root))
			case "missing parent":
				require.NoError(t, os.RemoveAll(filepath.Dir(path)))
			case "file parent":
				require.NoError(t, os.RemoveAll(filepath.Dir(path)))
				require.NoError(t, os.WriteFile(filepath.Dir(path), preserved, 0o600))
			case "symlink parent":
				require.NoError(t, os.RemoveAll(filepath.Dir(path)))
				if err := os.Symlink(outside, filepath.Dir(path)); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "directory leaf":
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Mkdir(path, 0o755))
			case "symlink leaf":
				require.NoError(t, os.Remove(path))
				if err := os.Symlink(externalPath, path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			_, _, err = readCursorHooksDocument(path)
			require.Error(t, err, "reader must revalidate the checked configuration path")
			require.Error(t, writeCursorHooksDocument(path, document), "writer must revalidate the checked configuration path")
			data, err := os.ReadFile(externalPath)
			require.NoError(t, err)
			require.Equal(t, preserved, data)
			if target == "file parent" {
				data, err = os.ReadFile(filepath.Dir(path))
				require.NoError(t, err)
				require.Equal(t, preserved, data)
			}
		})
	}
}

func TestCursorHookOperationsRejectNonDirectoryConfig(t *testing.T) {
	executable := makeCursorTestExecutable(t, t.TempDir(), "ox-adapter-cursor")
	for _, leaf := range []bool{false, true} {
		root := t.TempDir()
		path := filepath.Join(root, ".cursor")
		if leaf {
			require.NoError(t, os.Mkdir(path, 0o755))
			require.NoError(t, os.Mkdir(filepath.Join(path, "hooks.json"), 0o755))
		} else {
			require.NoError(t, os.WriteFile(path, []byte("keep"), 0o600))
		}
		_, _, err := installCursorHooks(root, executable)
		require.Error(t, err)
		_, _, err = checkCursorHooks(root, executable)
		require.Error(t, err)
		_, _, err = uninstallCursorHooks(root, executable)
		require.Error(t, err)
		if !leaf {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, "keep", string(data))
		}
	}
}

func TestCursorHookIOReportsPermissionLossWithoutMutation(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix file permissions without root privileges")
	}
	root := t.TempDir()
	path := writeCursorHooksFixture(t, root, `{"version":1,"hooks":{}}`, 0o600)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	document, _, err := readCursorHooksDocument(path)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(path, 0))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	_, _, err = readCursorHooksDocument(path)
	require.ErrorContains(t, err, "read hooks.json")
	require.NoError(t, os.Chmod(path, 0o600))
	require.NoError(t, os.Chmod(filepath.Dir(path), 0o500))
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o700) })
	require.ErrorContains(t, writeCursorHooksDocument(path, document), "create temporary file")
	require.NoError(t, os.Chmod(filepath.Dir(path), 0o700))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, entries, 1, "failed write must not leave temporary files")
}
