package paths

import (
	"fmt"
	"os/user"
	"path/filepath"
	"sync"
	"testing"
)

// Under `go test`, DataDir refuses to resolve to the developer's real ox data
// directory. That directory holds machine-local registries
// (started-recordings.json, live-conversations.json) whose entries tie live
// recordings to this machine. A test that never sandboxed it left its own
// entries there, and an agent later deleted the registry believing it held
// only test leftovers; it held live recordings' entries.
//
// The real location comes from the OS account database (os/user), never from
// $HOME: getHomeDir caches $HOME on first use, so a test that sets HOME after
// that still resolves the real home. That late-HOME case is the one this
// catches, and a guard that read $HOME would be fooled by it.
//
// Child processes are unaffected: a re-executed test binary that inherits a
// sandboxed XDG_DATA_HOME or HOME resolves inside that sandbox, not here. Not
// covered: a developer's own XDG_DATA_HOME exported to a non-default location,
// which this cannot tell apart from a test's sandbox.

// realDataDirs returns the real user's data directories in XDG and legacy
// mode, or nil when the account has no home on record (nothing to protect).
var realDataDirs = sync.OnceValue(func() []string {
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		return nil
	}
	return []string{
		filepath.Join(u.HomeDir, ".local", "share", "sageox"),
		filepath.Join(u.HomeDir, ".sageox", "data"),
	}
})

// refuseRealDataDirUnderTest panics when a test binary resolves dir to the
// real user's data directory. A panic, not an error, because DataDir has no
// error return and every caller would otherwise write there.
func refuseRealDataDirUnderTest(dir string) {
	if !testing.Testing() {
		return
	}
	dir = filepath.Clean(dir)
	for _, forbidden := range realDataDirs() {
		if dir == forbidden {
			panic(fmt.Sprintf("paths.DataDir resolved to the real ox data directory %s under go test. "+
				"Sandbox the package: func TestMain(m *testing.M) { os.Exit(testguard.RunWithDataSandbox(m)) }, "+
				"or per test, t.Setenv(\"XDG_DATA_HOME\", t.TempDir()). "+
				"A per-test t.Setenv(\"HOME\", ...) does not move it: paths caches the home directory on first use.", forbidden))
		}
	}
}
