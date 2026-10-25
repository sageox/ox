package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/version"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateUpgradeTarget(t *testing.T) {
	tests := []struct {
		method installMethod
		target string
		want   bool
	}{
		{installGoInstall, "v0.42.0", false},
		{installGoInstall, "v0.42.0-rc.1", false},
		{installGoInstall, "v0.42.0-rc.1+build.7", true},
		{installGoInstall, "v0.42.0+build.7", true},
		{installGoInstall, "v0.42.0+incompatible", false},
		{installGoInstall, "0.42.0", true},
		{installGoInstall, "v00.42.0", true},
		{installGoInstall, "v0.42.0.next", true},
		{installGoInstall, "v0.42.0-01", true},
		{installGoInstall, "latest", true},
		{installGoInstall, "v", true},
		{installGoInstall, "main", true},
		{installGoInstall, "v0.42", true},
		// binary (self-replace) installs can now honor a pinned tag by
		// downloading that release's tarball — no longer an error.
		{installBinary, "v0.42.0", false},
		{installBinary, "0.42.0", false},
		{installBinary, "v0.42.0-rc.1", false},
		{installBinary, "v0.42.0-rc.1+build.7", false},
		{installBinary, "v00.42.0", true},
		{installBinary, "v0.42.0.next", true},
		{installBinary, "v0.42.0-01", true},
		{installBinary, "latest", true},
		{installBinary, "v", true},
		{installBinary, "../../release", true},
		{installHomebrew, "v0.42.0", true},
		{installSource, "v0.42.0", true},
		{installBinary, "", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.method)+tt.target, func(t *testing.T) {
			err := validateUpgradeTarget(tt.method, tt.target)
			if tt.want {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// The selected release must match the installer arguments and reported result,
// whether it came from an explicit pin, the cache, or a live release lookup.
func TestUpgradeTargetSelection(t *testing.T) {
	if testing.Short() {
		t.Skip("short: runs isolated package-manager subprocesses")
	}
	if runtime.GOOS == "windows" {
		t.Skip("fake installers use POSIX shell scripts; PATH isolation requires executable scripts")
	}
	oldVersion := version.Version
	version.Version = "0.42.0"
	t.Cleanup(func() { version.Version = oldVersion })

	for _, tt := range []struct {
		name          string
		method        installMethod
		cached        string
		latest        string
		target        string
		offline       bool
		installerExit int
		staleOx       bool // the install lands behind an older ox on PATH
		requirePin    bool
		wantStatus    string
		wantNew       string
		wantMessage   string
		wantInstall   bool
		wantFetch     bool
	}{
		{name: "source pin while current", method: installSource, target: "v0.43.0", wantStatus: "failed", wantMessage: "source upgrades cannot safely honor a pinned release"},
		{name: "source pin while offline", method: installSource, target: "v0.43.0", offline: true, wantStatus: "failed", wantMessage: "source upgrades cannot safely honor a pinned release"},
		{name: "homebrew pin while current", method: installHomebrew, target: "v0.43.0", wantStatus: "failed", wantMessage: "homebrew upgrades cannot safely honor a pinned release"},
		{name: "homebrew pin while offline", method: installHomebrew, target: "v0.43.0", offline: true, wantStatus: "failed", wantMessage: "homebrew upgrades cannot safely honor a pinned release"},
		{name: "go pin without cache", method: installGoInstall, target: "v0.43.0", wantStatus: "upgraded", wantNew: "0.43.0", wantInstall: true},
		{name: "go floating target", method: installGoInstall, target: "latest", wantStatus: "failed", wantMessage: "--target must be a release version"},
		{name: "go empty target version", method: installGoInstall, target: "v", wantStatus: "failed", wantMessage: "--target must be a release version"},
		{name: "go unprefixed revision", method: installGoInstall, target: "0.42.0", wantStatus: "failed", wantMessage: "--target must be a release version"},
		{name: "go leading zero revision", method: installGoInstall, target: "v00.42.0", wantStatus: "failed", wantMessage: "--target must be a release version"},
		{name: "go dotted revision", method: installGoInstall, target: "v0.42.0.next", wantStatus: "failed", wantMessage: "--target must be a release version"},
		{name: "go prerelease metadata revision", method: installGoInstall, target: "v0.42.0-rc.1+build.7", wantStatus: "failed", wantMessage: "--target must be a release version"},
		{name: "go prerelease pin", method: installGoInstall, target: "v0.42.0-rc.1", wantStatus: "upgraded", wantNew: "0.42.0-rc.1", wantInstall: true},
		{name: "binary floating target", method: installBinary, target: "latest", wantStatus: "failed", wantMessage: "--target must be a release version"},
		{name: "binary empty target version", method: installBinary, target: "v", wantStatus: "failed", wantMessage: "--target must be a release version"},
		{name: "go pin while offline", method: installGoInstall, target: "v0.43.0", offline: true, wantStatus: "upgraded", wantNew: "0.43.0", wantInstall: true},
		{name: "go pin with current cache", method: installGoInstall, cached: "v0.42.0", target: "v0.43.0", wantStatus: "upgraded", wantNew: "0.43.0", wantInstall: true},
		{name: "go current pin without update", method: installGoInstall, target: "v0.42.0", wantStatus: "upgraded", wantNew: "0.42.0", wantInstall: true},
		{name: "go older pin with newer cache", method: installGoInstall, cached: "v99.0.0", target: "v0.41.0", wantStatus: "upgraded", wantNew: "0.41.0", wantInstall: true},
		{name: "go current pin with newer cache", method: installGoInstall, cached: "v99.0.0", target: "v0.42.0", wantStatus: "upgraded", wantNew: "0.42.0", wantInstall: true},
		{name: "go newer pin with newer cache", method: installGoInstall, cached: "v99.0.0", target: "v0.43.0", wantStatus: "upgraded", wantNew: "0.43.0", wantInstall: true},
		{name: "go failed pin with newer cache", method: installGoInstall, cached: "v99.0.0", target: "v0.43.0", installerExit: 37, wantStatus: "failed", wantNew: "0.43.0", wantMessage: "exit status 37", wantInstall: true},
		{name: "go install behind an older ox on PATH", method: installGoInstall, target: "v0.43.0", staleOx: true, wantStatus: "failed", wantNew: "0.43.0", wantMessage: "reports v0.42.0, not v0.43.0: another ox comes first on PATH", wantInstall: true},
		{name: "go strict pin while offline", method: installGoInstall, target: "v0.43.0", offline: true, requirePin: true, wantStatus: "upgraded", wantNew: "0.43.0", wantInstall: true},
		{name: "go cached release while offline", method: installGoInstall, cached: "v0.43.0", offline: true, wantStatus: "upgraded", wantNew: "0.43.0", wantInstall: true},
		{name: "go cached release with newer latest", method: installGoInstall, cached: "v0.43.0", latest: "v0.44.0", wantStatus: "upgraded", wantNew: "0.43.0", wantInstall: true},
		{name: "go live release without cache", method: installGoInstall, latest: "v0.43.0", wantStatus: "upgraded", wantNew: "0.43.0", wantInstall: true, wantFetch: true},
		{name: "go failed cached release install", method: installGoInstall, cached: "v0.43.0", installerExit: 37, wantStatus: "failed", wantNew: "0.43.0", wantMessage: "exit status 37", wantInstall: true},
		{name: "go unavailable release without cache", method: installGoInstall, offline: true, wantStatus: "failed", wantMessage: "latest lookup unavailable", wantFetch: true},
		{name: "go live release requires explicit pin", method: installGoInstall, latest: "v0.43.0", requirePin: true, wantStatus: "failed", wantNew: "0.43.0", wantMessage: "OX_UPGRADE_REQUIRE_PIN=1", wantFetch: true},
		{name: "go already current needs no pin", method: installGoInstall, requirePin: true, wantStatus: "up-to-date", wantFetch: true},
		{name: "no target still checks latest", method: installSource, wantStatus: "up-to-date", wantFetch: true},
		{name: "no target preserves manual upgrade", method: installSource, cached: "v99.0.0", wantStatus: "manual", wantNew: "99.0.0"},
		{name: "no target preserves strict pin requirement", method: installGoInstall, cached: "v99.0.0", requirePin: true, wantStatus: "failed", wantNew: "99.0.0", wantMessage: "OX_UPGRADE_REQUIRE_PIN=1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			noInputCLIEnv(t) // isolate auth and the daemon registry used after successful installs
			t.Setenv("OX_NO_DAEMON", "1")
			t.Setenv("OX_UPGRADE_REQUIRE_PIN", "")
			if tt.requirePin {
				t.Setenv("OX_UPGRADE_REQUIRE_PIN", "1")
			}
			useTestCacheDir(t)
			if tt.cached != "" {
				writeTestVersionCache(t, &versionCacheData{LatestVersion: tt.cached})
			}
			oldDetector := installMethodDetector
			installMethodDetector = func() installMethod { return tt.method }
			t.Cleanup(func() { installMethodDetector = oldDetector })
			binDir := t.TempDir()
			t.Setenv("PATH", binDir)
			// The ox on PATH reports whatever version was last installed. The
			// scripts use only shell builtins: PATH holds nothing but binDir.
			require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("HOME"), "ox-version"), []byte("0.42.0\n"), 0o600))
			install := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$HOME/install-args\"\n"
			goInstall := install
			if tt.installerExit == 0 && !tt.staleOx {
				goInstall += "printf '%s\\n' \"${2##*@v}\" > \"$HOME/ox-version\"\n"
			}
			for name, script := range map[string]string{
				"brew": install + fmt.Sprintf("exit %d\n", tt.installerExit),
				"go":   goInstall + fmt.Sprintf("exit %d\n", tt.installerExit),
				"ox":   "#!/bin/sh\nread -r v < \"$HOME/ox-version\"\nprintf '{\"version\": \"%s\"}\\n' \"$v\"\n",
			} {
				path := filepath.Join(binDir, name)
				require.NoError(t, os.WriteFile(path, []byte(script), 0o700))
				resolved, err := exec.LookPath(name)
				require.NoError(t, err)
				require.Equal(t, path, resolved, "must never run a real installer or ox")
			}
			oldFetcher := latestReleaseFetcher
			fetches := 0
			latestReleaseFetcher = func() (string, error) {
				fetches++
				if tt.offline {
					return "", errors.New("latest lookup unavailable")
				}
				if tt.latest != "" {
					return tt.latest, nil
				}
				return "v0.42.0", nil
			}
			t.Cleanup(func() { latestReleaseFetcher = oldFetcher })

			var stdout bytes.Buffer
			cmd := &cobra.Command{}
			cmd.Flags().Bool("json", true, "")
			cmd.Flags().String("target", tt.target, "")
			cmd.SetOut(&stdout)
			err := runUpgrade(cmd, nil)
			if tt.wantStatus == "failed" {
				var exit *commandExitError
				if assert.ErrorAs(t, err, &exit) {
					assert.Equal(t, 1, exit.ExitCode)
				}
			} else {
				assert.NoError(t, err)
			}
			var got upgradeResult
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
			assert.Equal(t, tt.method, got.InstallMethod)
			assert.Equal(t, tt.wantStatus, got.Status)
			assert.Equal(t, tt.wantNew, got.NewVersion)
			assert.Equal(t, "0.42.0", got.PreviousVersion)
			if tt.wantNew != "" {
				assert.Equal(t, "https://github.com/sageox/ox/releases/tag/v"+tt.wantNew, got.ReleaseURL)
			}
			if tt.wantStatus == "upgraded" {
				assert.Equal(t, "Upgraded to v"+tt.wantNew, got.Message)
			}
			if tt.wantMessage != "" {
				assert.Contains(t, got.Message, tt.wantMessage)
			}
			if tt.wantFetch {
				assert.Equal(t, 1, fetches)
			} else {
				assert.Zero(t, fetches, "an explicit or cached target must not depend on the latest release")
			}
			argsPath := filepath.Join(os.Getenv("HOME"), "install-args")
			if tt.wantInstall {
				args, err := os.ReadFile(argsPath)
				require.NoError(t, err, "the selected target must reach the installer")
				wantArgs := []string{"install", "github.com/sageox/ox/cmd/ox@v" + tt.wantNew}
				for _, pkg := range adapterPackages {
					wantArgs = append(wantArgs, pkg+"@v"+tt.wantNew)
				}
				assert.Equal(t, strings.Join(wantArgs, "\n")+"\n", string(args))
			} else {
				assert.NoFileExists(t, argsPath)
			}
		})
	}
}
