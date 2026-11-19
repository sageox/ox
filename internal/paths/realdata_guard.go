package paths

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// Under `go test`, DataDir refuses to resolve to the developer's real ox data
// directory. That directory holds machine-local registries
// (started-recordings.json, live-conversations.json) whose entries tie live
// recordings to this machine. A test that never sandboxed it left its own
// entries there, and an agent later deleted the registry believing it held
// only test leftovers; it held live recordings' entries.
//
// Protected is the union of two sources, because neither alone is reliable:
//   - the home on the OS account record (os/user). It ignores a $HOME a test
//     moved, except where the pure-Go lookup has no record and falls back to
//     $HOME;
//   - HOME, USERPROFILE and XDG_DATA_HOME as the test binary started, captured
//     at package init before any TestMain can move them.
//
// Both sides are compared after resolving symlinks, so a HOME that aliases the
// real home is still caught.
//
// A source inside the temp dir is skipped: a re-executed test binary (a lock
// or process helper) inherits its parent test's sandbox as its starting HOME
// or XDG_DATA_HOME, and protecting that would fail the helper on a sandbox.
// Not covered: a developer's own data dir inside the temp dir.

// envAtInit holds the environment the test binary started with. Empty
// outside go test, so production binaries pay nothing for it.
var envAtInit = captureEnvAtInit()

type startEnv struct {
	home, userProfile, dataHome, tempDir string
}

func captureEnvAtInit() startEnv {
	if !testing.Testing() {
		return startEnv{}
	}
	return startEnv{
		home:        os.Getenv("HOME"),
		userProfile: os.Getenv("USERPROFILE"),
		dataHome:    os.Getenv("XDG_DATA_HOME"),
		tempDir:     os.TempDir(),
	}
}

// accountHome returns the home on the OS account record, or "" without one.
// A variable so a test can stand in for the pure-Go lookup's $HOME fallback.
var accountHome = func() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.HomeDir
}

// protectedDataDirs returns the resolved real data directories, XDG and legacy.
func protectedDataDirs() []string {
	tempDir := resolvePath(envAtInit.tempDir)
	usable := func(base string) bool {
		return filepath.IsAbs(base) && !strings.HasPrefix(resolvePath(base), tempDir+string(filepath.Separator))
	}
	var dirs []string
	for _, home := range []string{accountHome(), envAtInit.home, envAtInit.userProfile} {
		if usable(home) {
			dirs = append(dirs,
				resolvePath(filepath.Join(home, ".local", "share", "sageox")),
				resolvePath(filepath.Join(home, ".sageox", "data")))
		}
	}
	if usable(envAtInit.dataHome) {
		dirs = append(dirs, resolvePath(filepath.Join(envAtInit.dataHome, "sageox")))
	}
	return dirs
}

// resolvePath resolves symlinks in the longest existing prefix of p and keeps
// the rest, so a data directory that does not exist yet still compares.
func resolvePath(p string) string {
	p = filepath.Clean(p)
	var rest []string
	for dir := p; ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(append([]string{resolved}, rest...)...)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return p
		}
		rest = append([]string{filepath.Base(dir)}, rest...)
	}
}

// refuseRealDataDirUnderTest panics when a test binary resolves dir to the
// real user's data directory. A panic, not an error, because DataDir has no
// error return and every caller would otherwise write there.
func refuseRealDataDirUnderTest(dir string) {
	if !testing.Testing() {
		return
	}
	dir = resolvePath(dir)
	for _, forbidden := range protectedDataDirs() {
		if dir == forbidden {
			panic(fmt.Sprintf("paths.DataDir resolved to the real ox data directory %s under go test. "+
				"Sandbox the package: func TestMain(m *testing.M) { os.Exit(testguard.RunWithDataSandbox(m)) }, "+
				"or per test, t.Setenv(\"XDG_DATA_HOME\", t.TempDir()). "+
				"A per-test t.Setenv(\"HOME\", ...) does not move it: paths caches the home directory on first use.", forbidden))
		}
	}
}
