package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// hiddenCLIs are third-party tools ox execs that keep per-user state under the
// home directory or in the OS keychain. `claude auth status` rewrote the
// developer's ~/.claude.json, and `gh` wrote ~/.local/state/gh/device-id; worse,
// a test's outcome depended on whether the developer happened to be logged in.
// Tests see none of them installed, as on CI. A test that needs one prepends its
// own fake to PATH.
//
// Hidden, not stubbed: a stub that fails reads as installed-but-logged-out, a
// third state that doctor reports as a warning and that neither CI nor most
// developers have.
var hiddenCLIs = []string{"claude", "codex", "gemini", "gh"}

// credentialsFileEnv makes ox store git credentials in a file instead of the OS
// keychain.
const credentialsFileEnv = "OX_GIT_CREDENTIALS_FILE"

// developerEnv records each variable isolateHome changed, as the test binary
// started with it, so useDeveloperHome can put it back.
var developerEnv = map[string]*string{}

// isolateHome points every home-derived location at <sandbox>/home and removes
// hiddenCLIs from PATH, then refuses to run if any of it did not take.
//
// `go test ./cmd/ox/` used to write into the developer's real home: it replaced
// ~/.local/share/sageox/cli/known-repos.json, left session caches and an auth
// lock under ~/.local/share/sageox/ and ~/.config/sageox/, and ran the real
// `claude auth status`. Fixing writers one at a time leaves the next one to be
// found the same way; with the home in the sandbox, a test that forgets to
// isolate itself writes into a directory that is removed after the run.
func isolateHome(sandbox string) error {
	// Pin the Go toolchain's directories BEFORE moving the home: on macOS
	// GOCACHE defaults under ~/Library/Caches, so tests that `go build` a binary
	// would otherwise compile from a cold cache, and GOENV holds the developer's
	// `go env -w` settings (GOPRIVATE, GOFLAGS).
	if err := pinGoEnv(); err != nil {
		return err
	}

	home := filepath.Join(sandbox, "home")
	mirrors := filepath.Join(sandbox, "path")
	for _, dir := range []string{home, mirrors} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
	}

	// USERPROFILE is what os.UserHomeDir reads on Windows. An inherited
	// GIT_CONFIG_GLOBAL would take precedence over the sandbox's ~/.gitconfig.
	setenv := map[string]string{
		"HOME": home, "USERPROFILE": home,
		"GIT_CONFIG_GLOBAL": filepath.Join(home, ".gitconfig"),
	}
	// Unset rather than point into the sandbox, so the defaults derived from HOME
	// are what tests exercise. CLAUDE_CONFIG_DIR and CODEX_HOME relocate those
	// agents' state, which ox reads and imports.
	unset := []string{
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME",
		"CLAUDE_CONFIG_DIR", "CODEX_HOME",
	}

	// Hiding relies on symlinks, which need a privilege on Windows, and
	// hard-linked executables there would load DLLs from the mirror. No CI job
	// runs this package on Windows, and a port nobody exercises is how the next
	// fail-open gets written, so the real CLIs stay reachable there; the home is
	// still isolated.
	if runtime.GOOS != "windows" {
		path, err := hideCLIs(os.Getenv("PATH"), mirrors)
		if err != nil {
			return fmt.Errorf("hide %s from PATH: %w", strings.Join(hiddenCLIs, ", "), err)
		}
		setenv["PATH"] = path
	}

	for key, value := range setenv {
		recordDeveloperEnv(key)
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	for _, key := range unset {
		recordDeveloperEnv(key)
		if err := os.Unsetenv(key); err != nil {
			return err
		}
	}

	// ox keeps git credentials in the OS keychain unless OX_GIT_CREDENTIALS_FILE
	// is set, and the keychain does not live under HOME: tests that reached it
	// wrote sageox-git items into the developer's real macOS login keychain, and
	// with HOME sandboxed they raise a blocking "Keychain Not Found" dialog
	// instead. Point the file store at the path ox would use anyway, so only the
	// backend changes; testguard hands the variable to every ox subprocess.
	// MockInit covers in-process tests that clear the variable.
	recordDeveloperEnv(credentialsFileEnv)
	if err := os.Setenv(credentialsFileEnv, paths.GitCredentialsFile()); err != nil {
		return err
	}
	keyring.MockInit()

	// Without the developer's ~/.gitconfig, git has no identity to commit with.
	// CI sets one globally, which the sandbox now hides too. A fixed identity
	// also keeps settings such as commit.gpgsign out of the tests.
	gitconfig := "[user]\n\tname = ox test\n\temail = ox-test@example.com\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(gitconfig), 0o600); err != nil {
		return err
	}

	return verifyHomeSandbox(home)
}

// hideCLIs returns pathList with each directory that holds one of hiddenCLIs
// replaced by a mirror under mirrors: a directory of symlinks to everything else
// in it. git, go, and every other tool in that directory stay reachable.
func hideCLIs(pathList, mirrors string) (string, error) {
	dirs := filepath.SplitList(pathList)
	mirrored := map[string]string{} // a directory can appear on PATH more than once
	for i, dir := range dirs {
		if mirror, ok := mirrored[dir]; ok {
			dirs[i] = mirror
			continue
		}
		if dir == "" || !holdsHiddenCLI(dir) {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", err
		}
		mirror := filepath.Join(mirrors, strconv.Itoa(len(mirrored)))
		if err := os.Mkdir(mirror, 0o700); err != nil {
			return "", err
		}
		for _, entry := range entries {
			if slices.Contains(hiddenCLIs, entry.Name()) {
				continue
			}
			if err := os.Symlink(filepath.Join(dir, entry.Name()), filepath.Join(mirror, entry.Name())); err != nil {
				return "", err
			}
		}
		mirrored[dir] = mirror
		dirs[i] = mirror
	}
	return strings.Join(dirs, string(os.PathListSeparator)), nil
}

func holdsHiddenCLI(dir string) bool {
	for _, name := range hiddenCLIs {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func recordDeveloperEnv(key string) {
	if value, ok := os.LookupEnv(key); ok {
		developerEnv[key] = &value
	} else {
		developerEnv[key] = nil
	}
}

// verifyHomeSandbox checks the redirect from the outside, the way the code under
// test will see it. Calling into internal/paths here also pins its cached home
// directory to the sandbox for the rest of the run.
func verifyHomeSandbox(home string) error {
	inside := func(path string) bool { return strings.HasPrefix(path, home+string(filepath.Separator)) }

	if got, err := os.UserHomeDir(); err != nil || got != home {
		return fmt.Errorf("os.UserHomeDir() = %q (err=%v), want the home sandbox %s", got, err, home)
	}
	if got, want := os.Getenv("GIT_CONFIG_GLOBAL"), filepath.Join(home, ".gitconfig"); got != want {
		return fmt.Errorf("GIT_CONFIG_GLOBAL = %q, want the sandbox's %s", got, want)
	}
	for name, dir := range map[string]string{
		"paths.ConfigDir()": paths.ConfigDir(),
		"paths.DataDir()":   paths.DataDir(),
		"paths.CacheDir()":  paths.CacheDir(),
	} {
		if !inside(dir) {
			return fmt.Errorf("%s = %q, outside the home sandbox %s. internal/paths caches the "+
				"home directory on first use, so something resolved it during package init, "+
				"before TestMain ran: likely a package-level var that calls internal/paths. "+
				"Resolve that path when it is used instead", name, dir, home)
		}
	}
	// The ox binaries tests start get their environment from testguard, not from
	// this process, so check the keychain switch survives that hop.
	want := credentialsFileEnv + "=" + os.Getenv(credentialsFileEnv)
	if !inside(os.Getenv(credentialsFileEnv)) || !slices.Contains(testguard.MinimalEnv(nil), want) {
		return fmt.Errorf("ox subprocesses would not get %s; they would store git credentials in the "+
			"developer's OS keychain", want)
	}
	if runtime.GOOS != "windows" {
		for _, name := range hiddenCLIs {
			if got, err := exec.LookPath(name); !errors.Is(err, exec.ErrNotFound) {
				return fmt.Errorf("%s is still on PATH at %q (err=%v)", name, got, err)
			}
		}
	}
	return nil
}

// pinGoEnv fixes the Go toolchain's home-derived directories to the values they
// have right now, so they survive the move of HOME.
func pinGoEnv() error {
	keys := []string{"GOCACHE", "GOMODCACHE", "GOPATH", "GOENV"}
	// A test binary that a test re-executes gets these from the parent through
	// testguard. Running `go` there would start Go's detached telemetry sidecar,
	// which writes under the child's HOME, usually the parent's t.TempDir, after
	// the child exits, and the parent's TempDir cleanup then fails.
	if !slices.ContainsFunc(keys, func(key string) bool { return os.Getenv(key) == "" }) {
		return nil
	}
	if _, err := exec.LookPath("go"); err != nil {
		return nil // no toolchain on PATH: no test can build a binary either
	}
	cmd := exec.Command("go", append([]string{"env"}, keys...)...)
	cmd.Dir = packageDir
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("go env: %w: %s", err, exitErr.Stderr)
		}
		return fmt.Errorf("go env: %w", err)
	}
	values := strings.Split(strings.TrimRight(string(out), "\r\n"), "\n")
	if len(values) != len(keys) {
		return fmt.Errorf("go env %s: got %d lines, want %d", strings.Join(keys, " "), len(values), len(keys))
	}
	for i, key := range keys {
		if value := strings.TrimRight(values[i], "\r"); value != "" {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return nil
}

// useDeveloperHome undoes isolateHome for the rest of the test: the developer's
// real home, agent config, and PATH. Only opt-in live tests that deliberately
// drive the developer's own logged-in CLIs may call it. Like t.Setenv, it
// cannot be used in a parallel test.
func useDeveloperHome(t *testing.T) {
	t.Helper()
	for key, value := range developerEnv {
		if value != nil {
			t.Setenv(key, *value)
			continue
		}
		t.Setenv(key, "") // registers the restore
		_ = os.Unsetenv(key)
	}
}

// Failure prevented: a test binary that a test re-executes with HOME set to a
// t.TempDir runs `go env` there. Go's telemetry sidecar keeps writing into that
// HOME after the child exits, and the parent's TempDir cleanup fails with
// "directory not empty".
func TestReexecutedTestBinaryLeavesItsHomeEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real test-binary subprocess")
	}
	bin, err := os.Executable()
	require.NoError(t, err)
	home := t.TempDir()
	cmd := testguard.OxCmd(t, bin, t.TempDir(), []string{"HOME=" + home}, "-test.run=^$")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "output=%s", out)

	var written []string
	require.NoError(t, filepath.WalkDir(home, func(path string, _ fs.DirEntry, err error) error {
		if path != home {
			written = append(written, strings.TrimPrefix(path, home))
		}
		return err
	}))
	assert.Empty(t, written, "the child wrote into the HOME its parent removes")
}
