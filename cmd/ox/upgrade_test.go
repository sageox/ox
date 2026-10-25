package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/version"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectInstallMethod_DevBuild(t *testing.T) {
	// detectInstallMethod checks version.BuildDate and version.Version
	// which are set via ldflags. In test builds, BuildDate is "unknown"
	// so it should return installSource.
	method := detectInstallMethod()
	if method != installSource {
		t.Errorf("expected installSource for test build, got %s", method)
	}
}

// A `go install …@version` build has no ldflags, so by BuildDate alone it looks
// like a dev build. Failure prevented: ox upgrade answered go-install users with
// "Dev build detected" and never upgraded them.
func TestDetectInstallMethod_GoInstallBuild(t *testing.T) {
	old := readBuildInfo
	t.Cleanup(func() { readBuildInfo = old })
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{
			Path:    "github.com/sageox/ox",
			Version: "v0.17.2",
			Sum:     "h1:0000000000000000000000000000000000000000000=",
		}}, true
	}
	assert.Equal(t, installGoInstall, detectInstallMethod())
}

// `go install github.com/sageox/ox/cmd/ox@<version>` refuses a module whose
// go.mod has replace or exclude directives. Failure prevented: v0.17.0 and
// v0.17.1 shipped a replace, which broke ox upgrade for go-install users and
// the install.sh fallback.
func TestGoModAllowsVersionedGoInstall(t *testing.T) {
	data, err := os.ReadFile(repoPath("..", "..", "go.mod"))
	require.NoError(t, err)
	for i, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && (fields[0] == "replace" || fields[0] == "exclude") {
			t.Errorf("go.mod:%d: %s", i+1, line)
		}
	}
}

// Failure prevented: ox upgrade reported "Upgraded" while the ox a shell runs
// was unchanged.
func TestConfirmUpgradeOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ox binaries are POSIX shell scripts")
	}
	for _, tt := range []struct {
		name    string
		method  installMethod
		script  string // body of the fake ox on PATH; empty means no ox on PATH
		want    string // version confirmed on success
		wantErr string
	}{
		{name: "reports the new version", method: installBinary, script: `printf '{"version": "0.17.1"}\n'`, want: "0.17.1"},
		{name: "reports it with a v prefix", method: installGoInstall, script: `printf '{"version": "v0.17.1"}\n'`, want: "0.17.1"},
		{name: "still the old version", method: installBinary, script: `printf '{"version": "0.17.0"}\n'`, wantErr: "reports v0.17.0, not v0.17.1: another ox comes first on PATH"},
		{name: "a pinned installer's version must match", method: installGoInstall, script: `printf '{"version": "0.17.2"}\n'`, wantErr: "reports v0.17.2, not v0.17.1"},
		// brew installs its tap's newest formula, which can be past the selected release
		{name: "homebrew installed a newer release", method: installHomebrew, script: `printf '{"version": "0.17.2"}\n'`, want: "0.17.2"},
		{name: "homebrew did not upgrade", method: installHomebrew, script: `printf '{"version": "0.17.0"}\n'`, wantErr: "sageox/tap does not have it yet"},
		{name: "fails to run", method: installBinary, script: "exit 3", wantErr: "could not confirm the upgrade"},
		{name: "prints something other than ox JSON", method: installBinary, script: "echo hello", wantErr: "could not confirm the upgrade"},
		{name: "no ox on PATH", method: installBinary, want: "0.17.1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			binDir := t.TempDir()
			if tt.script != "" {
				require.NoError(t, os.WriteFile(filepath.Join(binDir, "ox"), []byte("#!/bin/sh\n"+tt.script+"\n"), 0o755))
			}
			t.Setenv("PATH", binDir)
			got, err := confirmUpgradeOnPath(tt.method, "0.17.0", "0.17.1")
			if tt.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
			assert.Contains(t, err.Error(), filepath.Join(binDir, "ox"), "the message must name the binary a shell runs")
		})
	}

	// An ox found through a relative PATH entry is still what a shell runs, so
	// the lookup error must not read as "no ox on PATH".
	t.Run("ox found through a relative PATH entry", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "bin"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "bin", "ox"), []byte("#!/bin/sh\nprintf '{\"version\": \"0.17.0\"}\\n'\n"), 0o755))
		t.Chdir(dir)
		t.Setenv("PATH", "bin")
		_, err := confirmUpgradeOnPath(installHomebrew, "0.17.0", "0.17.1")
		require.ErrorContains(t, err, "could not confirm the upgrade")
	})
}

// Failure prevented: on 2026-09-23 the tap never got v0.17.1, brew exited 0
// with "already installed", and ox upgrade printed "Upgraded to v0.17.1",
// cleared the update notice, and left Homebrew users on v0.17.0. The reverse
// must not fail either: brew installs the tap's newest formula, which can be
// past a release selected from a stale cache.
func TestRunUpgrade_HomebrewReportsWhatIsOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake brew and ox are POSIX shell scripts")
	}
	for _, tt := range []struct {
		name       string
		onPath     string // version the ox on PATH reports after brew runs
		wantStatus string
		wantNew    string
	}{
		{name: "tap has no newer formula", onPath: "0.17.0", wantStatus: "failed", wantNew: "0.17.1"},
		{name: "tap is past the selected release", onPath: "0.17.2", wantStatus: "upgraded", wantNew: "0.17.2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			noInputCLIEnv(t) // isolates the daemon registry a successful upgrade touches
			t.Setenv("OX_NO_DAEMON", "1")
			useTestCacheDir(t)
			oldVersion := version.Version
			version.Version = "0.17.0"
			t.Cleanup(func() { version.Version = oldVersion })
			oldDetector, oldFetcher := installMethodDetector, latestReleaseFetcher
			installMethodDetector = func() installMethod { return installHomebrew }
			latestReleaseFetcher = func() (string, error) { return "v0.17.1", nil }
			t.Cleanup(func() { installMethodDetector, latestReleaseFetcher = oldDetector, oldFetcher })

			binDir := t.TempDir()
			for name, script := range map[string]string{
				"brew": "#!/bin/sh\nexit 0\n",
				"ox":   fmt.Sprintf("#!/bin/sh\nprintf '{\"version\": \"%s\"}\\n'\n", tt.onPath),
			} {
				require.NoError(t, os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o755))
			}
			t.Setenv("PATH", binDir)

			var stdout bytes.Buffer
			cmd := &cobra.Command{}
			cmd.Flags().Bool("json", true, "")
			cmd.Flags().String("target", "", "")
			cmd.SetOut(&stdout)
			var err error
			captureStderr(t, func() { err = runUpgrade(cmd, nil) })

			var got upgradeResult
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
			assert.Equal(t, tt.wantStatus, got.Status)
			assert.Equal(t, tt.wantNew, got.NewVersion)
			assert.Equal(t, "https://github.com/sageox/ox/releases/tag/v"+tt.wantNew, got.ReleaseURL)
			if tt.wantStatus == "failed" {
				var exit *commandExitError
				require.ErrorAs(t, err, &exit)
				assert.Contains(t, got.Message, "reports v0.17.0, not v0.17.1")
				cached := readVersionCache()
				require.NotNil(t, cached, "a failed upgrade must keep the update notice")
				assert.Equal(t, "v0.17.1", cached.LatestVersion)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "Upgraded to v"+tt.wantNew, got.Message)
		})
	}
}

func TestOutputUpgradeResultJSONIncludesPostUpgradeMaintenance(t *testing.T) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	result := upgradeResult{
		Status:          "upgraded",
		PreviousVersion: "0.14.0",
		NewVersion:      "0.15.0",
		InstallMethod:   installBinary,
		DaemonsStopped:  2,
	}

	if err := outputUpgradeResult(cmd, result, true); err != nil {
		t.Fatalf("outputUpgradeResult: %v", err)
	}
	var got upgradeResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if got.DaemonsStopped != 2 {
		t.Fatalf("daemons_stopped = %d, want 2", got.DaemonsStopped)
	}
}

// Installer logs must remain visible without corrupting JSON stdout, even on failure.
func TestUpgradeInstallerOutputStreams(t *testing.T) {
	if testing.Short() {
		t.Skip("short: runs package-manager subprocesses")
	}
	if runtime.GOOS == "windows" {
		t.Skip("fake installers use POSIX shell scripts; PATH isolation requires executable scripts")
	}

	for _, installer := range []struct {
		name string
		run  func(bool) error
	}{
		{"brew", upgradeViaHomebrew},
		{"go", func(quiet bool) error { return upgradeViaGoInstallWithTarget(quiet, "v0.99.0") }},
	} {
		for _, mode := range []struct {
			name  string
			quiet bool
		}{
			{"human", false},
			{"json", true},
		} {
			for _, exitCode := range []int{0, 37} {
				t.Run(fmt.Sprintf("%s/%s/exit_%d", installer.name, mode.name, exitCode), func(t *testing.T) {
					binDir := t.TempDir()
					fakePath := filepath.Join(binDir, installer.name)
					script := fmt.Sprintf("#!/bin/sh\nprintf 'installer stdout\\n'\nprintf 'installer stderr\\n' >&2\nexit %d\n", exitCode)
					require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
					t.Setenv("PATH", binDir)
					resolved, err := exec.LookPath(installer.name)
					require.NoError(t, err)
					require.Equal(t, fakePath, resolved, "must never invoke a real package manager")

					var runErr error
					var stderr string
					stdout := captureStdoutForPlanCLI(t, func() {
						stderr = captureStderr(t, func() { runErr = installer.run(mode.quiet) })
					})
					if exitCode == 0 {
						require.NoError(t, runErr)
					} else {
						var exitErr *exec.ExitError
						require.ErrorAs(t, runErr, &exitErr)
						assert.Equal(t, exitCode, exitErr.ExitCode())
					}
					if mode.quiet {
						assert.Empty(t, stdout, "installer logs must not contaminate JSON stdout")
						assert.Contains(t, stderr, "installer stdout\n")
						assert.Contains(t, stderr, "installer stderr\n")
					} else {
						assert.Contains(t, stdout, "Running:")
						assert.Contains(t, stdout, "installer stdout\n")
						assert.Equal(t, "installer stderr\n", stderr)
					}
				})
			}
		}
	}
}

// Paths are symlink-resolved, as detectInstallMethod passes them. Failure
// prevented: an ox installed by install.sh or go install was treated as
// Homebrew whenever the formula was also installed, so `brew upgrade` updated a
// copy the user was not running.
func TestIsHomebrewInstall(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		expect bool
	}{
		{"homebrew arm64", "/opt/homebrew/Cellar/ox/0.17.0/bin/ox", true},
		{"homebrew x86", "/usr/local/Cellar/ox/0.5.1/bin/ox", true},
		{"linuxbrew", "/home/linuxbrew/.linuxbrew/Cellar/ox/0.17.0/bin/ox", true},
		{"custom prefix", "/Users/dev/homebrew/Cellar/ox/0.17.0/bin/ox", true},
		{"another formula's keg", "/opt/homebrew/Cellar/oxygen/1.0/bin/ox", false},
		{"install.sh beside Intel Homebrew", "/usr/local/bin/ox", false},
		{"install.sh user dir", "/Users/dev/.local/bin/ox", false},
		{"gobin", "/Users/dev/go/bin/ox", false},
	}

	if runtime.GOOS != "windows" {
		// A brew that reports the formula as installed must not matter.
		binDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(binDir, "brew"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
		t.Setenv("PATH", binDir)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, isHomebrewInstall(tt.path))
		})
	}
}
