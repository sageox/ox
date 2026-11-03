package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/cli"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubInstall points every install seam at test doubles and returns a source
// directory holding a fake ox release.
func stubInstall(t *testing.T, loggedIn bool) (src string, logins *[]string, retired *int) {
	t.Helper()
	src = t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "ox"), []byte("ox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "ox-adapter-codex"), []byte("a"), 0o755))

	var calls []string
	var stopped int
	origExe, origLogged, origLogin, origRetire, origSys := installExecutable, installLoggedIn, installRunLogin, installRetireDaemons, installSystemBinDir
	origDir, origSkip := installDirFlag, installSkipLogin
	t.Cleanup(func() {
		installExecutable, installLoggedIn, installRunLogin, installRetireDaemons, installSystemBinDir = origExe, origLogged, origLogin, origRetire, origSys
		installDirFlag, installSkipLogin = origDir, origSkip
	})
	installExecutable = func() (string, error) { return filepath.Join(src, "ox"), nil }
	installLoggedIn = func() bool { return loggedIn }
	installRunLogin = func(oxPath string) error { calls = append(calls, oxPath); return nil }
	installRetireDaemons = func() int { stopped++; return 0 }
	installSystemBinDir = filepath.Join(t.TempDir(), "not-there")
	installDirFlag, installSkipLogin = "", false
	return src, &calls, &stopped
}

func runInstallCmd(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	c := &cobra.Command{}
	c.Flags().Bool("json", false, "")
	require.NoError(t, c.Flags().Parse(args))
	c.SetOut(&out)
	require.NoError(t, runInstall(c, nil))
	return out.String()
}

func TestRunInstallDefaultsToLocalBinAndLogsIn(t *testing.T) {
	_, logins, retired := stubInstall(t, false)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dest := filepath.Join(home, ".local", "bin")
	t.Setenv("PATH", dest)

	out := runInstallCmd(t)

	assert.Contains(t, out, "Installed ox and 1 adapter to "+dest)
	assert.FileExists(t, filepath.Join(dest, "ox"))
	assert.FileExists(t, filepath.Join(dest, "ox-adapter-codex"))
	assert.Equal(t, []string{filepath.Join(dest, "ox")}, *logins)
	assert.Zero(t, *retired)
	assert.NotContains(t, out, "is not on your PATH")
	assert.NotContains(t, out, "ox init", "a completed ox login already points at ox init")
}

func TestRunInstallPrefersWritableSystemDir(t *testing.T) {
	_, _, _ = stubInstall(t, true)
	installSystemBinDir = t.TempDir()
	t.Setenv("PATH", installSystemBinDir)

	out := runInstallCmd(t)

	assert.FileExists(t, filepath.Join(installSystemBinDir, "ox"))
	assert.Contains(t, out, "Already logged in")
	assert.Contains(t, out, "cd <repo> && ox init")
}

func TestRunInstallDirFlagReplacesAndWarnsOffPath(t *testing.T) {
	_, logins, retired := stubInstall(t, false)
	dest := filepath.Join(t.TempDir(), "bin")
	require.NoError(t, os.MkdirAll(dest, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dest, "ox"), []byte("old"), 0o755))
	installDirFlag, installSkipLogin = dest, true
	t.Setenv("PATH", t.TempDir())

	out := runInstallCmd(t)

	got, err := os.ReadFile(filepath.Join(dest, "ox"))
	require.NoError(t, err)
	assert.Equal(t, "ox", string(got))
	assert.Equal(t, 1, *retired, "daemons running the replaced ox restart")
	assert.Empty(t, *logins)
	assert.Contains(t, out, dest+" is not on your PATH")
	assert.Contains(t, out, `export PATH="`+dest+`:$PATH"`)
	assert.Contains(t, out, "cd <repo> && ox init")
}

func TestRunInstallAlreadyInPlace(t *testing.T) {
	src, _, _ := stubInstall(t, true)
	installDirFlag = src
	t.Setenv("PATH", src)

	out := runInstallCmd(t)

	assert.Contains(t, out, "ox is already installed at "+filepath.Join(src, "ox"))
}

func TestRunInstallWarnsWhenAnotherOxComesFirst(t *testing.T) {
	_, _, _ = stubInstall(t, true)
	other := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(other, "ox"), []byte("other"), 0o755))
	node := filepath.Join(t.TempDir(), "node_modules", ".bin")
	require.NoError(t, os.MkdirAll(node, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(node, "ox"), []byte("npx shim"), 0o755))
	dest := t.TempDir()
	installDirFlag = dest
	t.Setenv("PATH", node+string(os.PathListSeparator)+other+string(os.PathListSeparator)+dest)

	out := runInstallCmd(t)

	assert.Contains(t, out, "Another ox at "+filepath.Join(other, "ox"))
	assert.NotContains(t, out, "node_modules")
}

func TestRunInstallLeavesHomebrewAlone(t *testing.T) {
	_, logins, _ := stubInstall(t, false)
	cellar := filepath.Join(t.TempDir(), "Cellar", "ox", "0.25.0", "bin")
	require.NoError(t, os.MkdirAll(cellar, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellar, "ox"), []byte("brew"), 0o755))
	link := t.TempDir()
	require.NoError(t, os.Symlink(filepath.Join(cellar, "ox"), filepath.Join(link, "ox")))
	t.Setenv("PATH", link)
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := runInstallCmd(t)

	assert.Contains(t, out, "managed by Homebrew")
	assert.NoFileExists(t, filepath.Join(home, ".local", "bin", "ox"))
	assert.Equal(t, []string{filepath.Join(link, "ox")}, *logins)
}

func TestRunInstallNoInputSkipsLogin(t *testing.T) {
	_, logins, _ := stubInstall(t, false)
	installDirFlag = t.TempDir()
	t.Setenv("PATH", installDirFlag)
	cli.SetNoInput(true)
	t.Cleanup(func() { cli.SetNoInput(false) })

	out := runInstallCmd(t)

	assert.Empty(t, *logins)
	assert.Contains(t, out, "Log in with:")
}

func TestRunInstallErrors(t *testing.T) {
	t.Run("executable lookup fails", func(t *testing.T) {
		_, _, _ = stubInstall(t, true)
		installExecutable = func() (string, error) { return "", errors.New("boom") }
		require.ErrorContains(t, runInstall(&cobra.Command{}, nil), "locate running ox")
	})
	t.Run("login fails", func(t *testing.T) {
		_, _, _ = stubInstall(t, false)
		installDirFlag = t.TempDir()
		t.Setenv("PATH", installDirFlag)
		installRunLogin = func(string) error { return errors.New("denied") }
		require.ErrorContains(t, runInstall(&cobra.Command{}, nil), "denied")
	})
	t.Run("destination not writable", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root writes anywhere")
		}
		_, _, _ = stubInstall(t, true)
		dest := t.TempDir()
		require.NoError(t, os.Chmod(dest, 0o555))
		t.Cleanup(func() { _ = os.Chmod(dest, 0o755) })
		installDirFlag = dest
		require.ErrorContains(t, runInstall(&cobra.Command{}, nil), "ox install --dir")
	})
}

func TestAdapterSuffix(t *testing.T) {
	assert.Equal(t, "", adapterSuffix(0))
	assert.Equal(t, " and 1 adapter", adapterSuffix(1))
	assert.Equal(t, " and 11 adapters", adapterSuffix(11))
}

func TestRunInstalledLogin(t *testing.T) {
	dir := t.TempDir()
	fail := filepath.Join(dir, "fail")
	require.NoError(t, os.WriteFile(fail, []byte("#!/bin/sh\nexit 3\n"), 0o755))
	ok := filepath.Join(dir, "ok")
	require.NoError(t, os.WriteFile(ok, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	require.NoError(t, runInstalledLogin(ok))

	var exitErr *commandExitError
	require.ErrorAs(t, runInstalledLogin(fail), &exitErr)
	assert.Equal(t, 3, exitErr.ExitCode)

	require.ErrorContains(t, runInstalledLogin(filepath.Join(dir, "missing")), "run ox login")
}

func TestIsNpmInstall(t *testing.T) {
	assert.True(t, isNpmInstall("/home/u/.npm/_npx/abc/node_modules/@sageox/ox-linux-x64/bin/ox"))
	assert.False(t, isNpmInstall("/usr/local/bin/ox"))
}

func TestRunInstallLeavesGlobalNpmAlone(t *testing.T) {
	_, _, _ = stubInstall(t, true)
	pkg := filepath.Join(t.TempDir(), "lib", "node_modules", "@sageox", "ox", "bin")
	require.NoError(t, os.MkdirAll(pkg, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pkg, "ox.js"), []byte("launcher"), 0o755))
	bin := t.TempDir()
	require.NoError(t, os.Symlink(filepath.Join(pkg, "ox.js"), filepath.Join(bin, "ox")))
	installSystemBinDir = bin
	t.Setenv("PATH", bin)

	out := runInstallCmd(t)

	assert.Contains(t, out, "installed globally with npm")
	target, err := os.Readlink(filepath.Join(bin, "ox"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(pkg, "ox.js"), target, "npm's link is untouched")
}

func TestRunInstallFishPathHint(t *testing.T) {
	_, _, _ = stubInstall(t, true)
	installDirFlag = t.TempDir()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("SHELL", "/usr/bin/fish")

	out := runInstallCmd(t)

	assert.Contains(t, out, "fish_add_path "+installDirFlag)
	assert.NotContains(t, out, "export PATH")
}

func TestRunInstallJSON(t *testing.T) {
	_, logins, _ := stubInstall(t, false)
	dest := t.TempDir()
	installDirFlag = dest
	t.Setenv("PATH", t.TempDir())

	out := runInstallCmd(t, "--json")

	var got installResult
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	assert.Equal(t, "installed", got.Status)
	assert.Equal(t, filepath.Join(dest, "ox"), got.OxPath)
	assert.ElementsMatch(t, []string{"ox", "ox-adapter-codex"}, got.Binaries)
	assert.False(t, got.LoggedIn)
	assert.Equal(t, []string{"ox login", "cd <repo> && ox init"}, got.Next)
	assert.Empty(t, *logins, "--json never starts the interactive login")
}
