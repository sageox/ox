package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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

func TestIsHomebrewInstall(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		expect bool
	}{
		{"homebrew arm64", "/opt/homebrew/bin/ox", true},
		{"homebrew x86", "/usr/local/Cellar/ox/0.5.1/bin/ox", true},
		{"linuxbrew", "/home/linuxbrew/.linuxbrew/bin/ox", true},
		{"gobin", "/Users/dev/go/bin/ox", false},
		{"random", "/usr/local/bin/ox", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// test the prefix-based fast path using the real homebrewPrefixes var
			got := hasHomebrewPrefix(tt.path)
			if got != tt.expect {
				t.Errorf("hasHomebrewPrefix(%q) = %v, want %v", tt.path, got, tt.expect)
			}
		})
	}
}

// hasHomebrewPrefix checks the fast-path prefix match using the real
// homebrewPrefixes package var, without shelling out to brew.
func hasHomebrewPrefix(oxPath string) bool {
	for _, prefix := range homebrewPrefixes {
		if strings.HasPrefix(oxPath, prefix) {
			return true
		}
	}
	return false
}
