package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/testguard"
	"github.com/sageox/ox/internal/updatenotice"
	"github.com/sageox/ox/internal/version"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureUpgradeOutput redirects os.Stdout as well as the cobra writer.
//
// The --json branch writes through cmd.OutOrStdout(); the human-readable branches
// use fmt.Printf and go straight to os.Stdout. Capturing only the cobra writer
// silently returned an empty string for every text case.
func captureUpgradeOutput(t *testing.T, result upgradeResult, jsonOut bool) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	realStdout := os.Stdout
	os.Stdout = w
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	outErr := outputUpgradeResult(cmd, result, jsonOut)
	os.Stdout = realStdout
	_ = w.Close()
	piped, _ := io.ReadAll(r)
	if outErr != nil {
		t.Fatalf("outputUpgradeResult: %v", outErr)
	}
	return buf.String() + string(piped)
}

// Binary pins must reach the requested release independently of the latest
// release cache/API, and failures must identify the release actually requested.
func TestUpgradeBinaryTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("short: exercises the binary installer over local HTTPS")
	}
	for _, tt := range []struct {
		name    string
		target  string
		cached  string
		version string
	}{
		{"no cache and offline release lookup", "v0.42.0", "", "0.42.0"},
		{"different cached release", "0.42.0", "v99.0.0", "0.42.0"},
		{"prerelease with metadata", "v0.42.0-rc.1+build.7", "", "0.42.0-rc.1+build.7"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			noInputCLIEnv(t)
			t.Setenv("PATH", t.TempDir()) // no Homebrew or Go installation to detect
			useTestCacheDir(t)
			if tt.cached != "" {
				writeTestVersionCache(t, &versionCacheData{LatestVersion: tt.cached, CheckedAt: time.Now()})
			}
			oldVersion, oldBuildDate := version.Version, version.BuildDate
			version.Version, version.BuildDate = "0.16.0", "2026-09-21T00:00:00Z"
			t.Cleanup(func() { version.Version, version.BuildDate = oldVersion, oldBuildDate })
			oldFetcher := latestReleaseFetcher
			fetched := false
			latestReleaseFetcher = func() (string, error) {
				fetched = true
				return "", errors.New("latest release unavailable")
			}
			t.Cleanup(func() { latestReleaseFetcher = oldFetcher })

			requests := make(chan string, 1)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.Host + r.URL.Path
				// Refuse the download before staging or replacing any binary.
				http.Error(w, "release unavailable", http.StatusNotFound)
			}))
			t.Cleanup(server.Close)
			transport := server.Client().Transport.(*http.Transport).Clone()
			transport.TLSClientConfig.ServerName = server.Certificate().DNSNames[0]
			transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}
			oldTransport := http.DefaultTransport
			http.DefaultTransport = transport
			t.Cleanup(func() {
				http.DefaultTransport = oldTransport
				transport.CloseIdleConnections()
			})

			var stdout bytes.Buffer
			cmd := &cobra.Command{}
			cmd.Flags().Bool("json", true, "")
			cmd.Flags().String("target", tt.target, "")
			cmd.SetOut(&stdout)
			err := runUpgrade(cmd, nil)
			var exit *commandExitError
			require.ErrorAs(t, err, &exit)
			assert.Equal(t, 1, exit.ExitCode)
			var got upgradeResult
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
			assert.Equal(t, installBinary, got.InstallMethod)
			assert.Equal(t, "failed", got.Status)
			assert.Equal(t, tt.version, got.NewVersion)
			assert.Equal(t, "https://github.com/sageox/ox/releases/tag/v"+tt.version, got.ReleaseURL)
			assert.Contains(t, got.Message, "fetch checksums")
			assert.False(t, fetched, "an explicit pin must not fetch the latest release")
			select {
			case request := <-requests:
				assert.Equal(t, "github.com/sageox/ox/releases/download/v"+tt.version+"/checksums.txt", request)
			default:
				t.Error("the requested release was never fetched")
			}
		})
	}
}

// TestOutputUpgradeResult_JSONCarriesTheMachineFields: `ox upgrade --json` is a
// scripted surface. Losing a field silently breaks automation that has no other
// way to tell what happened.
func TestOutputUpgradeResult_JSONCarriesTheMachineFields(t *testing.T) {
	out := captureUpgradeOutput(t, upgradeResult{
		Status:     "upgraded",
		Message:    "Upgraded to v0.15.0",
		ReleaseURL: "https://example.invalid/releases/v0.15.0",
		NewVersion: "0.15.0",
	}, true)

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--json did not emit valid JSON: %v\n%s", err, out)
	}
	for _, key := range []string{"status", "message", "new_version"} {
		if _, ok := got[key]; !ok {
			t.Errorf("--json output is missing %q: %v", key, got)
		}
	}
}

// Failure exit handling must preserve successful text output and its restart guidance.
func TestOutputUpgradeResult_SuccessfulText(t *testing.T) {
	for _, tt := range []struct {
		name    string
		stopped int
	}{
		{name: "no running daemons"},
		{name: "old daemons stopped", stopped: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := captureUpgradeOutput(t, upgradeResult{
				Status:         "upgraded",
				Message:        "Upgraded to v0.15.0",
				ReleaseURL:     "https://example.invalid/releases/v0.15.0",
				DaemonsStopped: tt.stopped,
			}, false)
			assert.Contains(t, out, "Upgraded to v0.15.0")
			assert.Contains(t, out, "https://example.invalid/releases/v0.15.0")
			assert.Contains(t, out, "Restart your terminal")
			if tt.stopped > 0 {
				assert.Contains(t, out, "stopped so they restart on the new version")
			} else {
				assert.NotContains(t, out, "Daemons:")
			}
		})
	}
}

// TestOutputUpgradeResult_UpToDateSaysSoWithoutClaimingAnUpgrade: reporting an
// upgrade that did not happen would send someone hunting for a version change
// that was never made.
func TestOutputUpgradeResult_UpToDateSaysSoWithoutClaimingAnUpgrade(t *testing.T) {
	out := captureUpgradeOutput(t, upgradeResult{
		Status:  "up-to-date",
		Message: "Already on the latest version (v0.15.0)",
	}, false)

	if !strings.Contains(out, "Already on the latest version") {
		t.Errorf("up-to-date result did not report itself:\n%s", out)
	}
	if strings.Contains(strings.ToLower(out), "release notes") {
		t.Errorf("up-to-date result advertised release notes for an upgrade that did not happen:\n%s", out)
	}
}

// TestOutputUpgradeResult_ManualPathStillTellsTheUserWhatToDo: when ox cannot
// upgrade itself (Homebrew, a source build), silence would leave the user stuck.
func TestOutputUpgradeResult_ManualPathStillTellsTheUserWhatToDo(t *testing.T) {
	out := captureUpgradeOutput(t, upgradeResult{
		Status:     "manual",
		Message:    "Run `brew upgrade sageox/tap/ox`",
		ReleaseURL: "https://example.invalid/releases/v0.15.0",
	}, false)

	if !strings.Contains(out, "brew upgrade") {
		t.Errorf("manual path did not surface the instruction:\n%s", out)
	}
}

// Failed upgrades must fail the command after rendering exactly one diagnostic.
func TestUpgradeFailureOutput(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		name := "text"
		if jsonOutput {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			result := upgradeResult{Status: "failed", Message: "installer failed"}
			err := outputUpgradeResult(cmd, result, jsonOutput)
			var exit *commandExitError
			require.ErrorAs(t, err, &exit)
			assert.Equal(t, 1, exit.ExitCode)
			if jsonOutput {
				var got upgradeResult
				require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
				assert.Equal(t, result, got)
				assert.Empty(t, stderr.String())
			} else {
				assert.Empty(t, stdout.String())
				assert.Equal(t, 1, strings.Count(stderr.String(), result.Message))
			}
		})
	}
	t.Run("JSON write failure", func(t *testing.T) {
		cmd := &cobra.Command{}
		cmd.SetOut(failingWriter{})
		err := outputUpgradeResult(cmd, upgradeResult{Status: "failed", Message: "installer failed"}, true)
		require.ErrorContains(t, err, "write failed")
		var exit *commandExitError
		assert.False(t, errors.As(err, &exit), "an unwritten result must not suppress the output error")
	})
}

// A failed lookup or cache write must never turn an unknown/newer version into
// an "already latest" claim. Successful lookups and cached updates still work.
func TestUpgradeVersionCheckOutcome(t *testing.T) {
	for _, tt := range []struct {
		name       string
		cached     string
		latest     string
		fetchError bool
		blockCache bool
		wantStatus string
		wantNew    string
		wantFetch  bool
	}{
		{name: "lookup unavailable", fetchError: true, wantStatus: "failed", wantFetch: true},
		{name: "lookup unavailable with current cache", cached: version.Version, fetchError: true, wantStatus: "failed", wantFetch: true},
		{name: "missing release tag", wantStatus: "failed", wantFetch: true},
		{name: "empty version after prefix", latest: "v", wantStatus: "failed", wantFetch: true},
		{name: "already current", latest: "v" + strings.TrimPrefix(version.Version, "v"), wantStatus: "up-to-date", wantFetch: true},
		{name: "running newer than release", latest: "v0.0.1", wantStatus: "up-to-date", wantFetch: true},
		{name: "new release", latest: "v99.0.0", wantStatus: "manual", wantNew: "99.0.0", wantFetch: true},
		{name: "new release with unwritable cache", latest: "v99.0.0", blockCache: true, wantStatus: "manual", wantNew: "99.0.0", wantFetch: true},
		{name: "cached update", cached: "v99.0.0", wantStatus: "manual", wantNew: "99.0.0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			useTestCacheDir(t)
			if tt.cached != "" {
				writeTestVersionCache(t, &versionCacheData{LatestVersion: tt.cached, CheckedAt: time.Now()})
			}
			if tt.blockCache {
				updatenotice.Path = t.TempDir() // a directory cannot be overwritten as a cache file
			}
			oldFetcher := latestReleaseFetcher
			fetched := false
			latestReleaseFetcher = func() (string, error) {
				fetched = true
				if tt.fetchError {
					return "", errors.New("release lookup unavailable")
				}
				return tt.latest, nil
			}
			t.Cleanup(func() { latestReleaseFetcher = oldFetcher })

			var stdout bytes.Buffer
			cmd := &cobra.Command{}
			cmd.Flags().Bool("json", true, "")
			cmd.Flags().String("target", "", "")
			cmd.SetOut(&stdout)
			err := runUpgrade(cmd, nil)
			if tt.wantStatus == "failed" {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			var got upgradeResult
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
			assert.Equal(t, tt.wantStatus, got.Status)
			assert.Equal(t, tt.wantNew, got.NewVersion)
			assert.Equal(t, tt.wantFetch, fetched)
			if tt.fetchError {
				assert.Contains(t, got.Message, "release lookup unavailable")
			}
		})
	}
}

// inHomebrewKeg places oxBin at a Homebrew keg path, which is how a release
// build detects a Homebrew install. It hard-links rather than symlinks because
// detection resolves symlinks back to the original path, and copies when the
// link would cross filesystems.
func inHomebrewKeg(t *testing.T, oxBin string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "Cellar", "ox", "0.0.0", "bin")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "ox")
	if err := os.Link(oxBin, path); err != nil {
		data, err := os.ReadFile(oxBin)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, data, 0o755))
	}
	return path
}

// Offline checks and unsupported targets must fail once in both output modes;
// target validation must not depend on a successful release lookup.
func TestUpgradeCLI(t *testing.T) {
	skipIntegration(t)
	oxBin := testguard.BuildOxBinary(t, repoPath("..", ".."))
	for _, tt := range []struct {
		name    string
		target  string
		message string
	}{
		{"offline lookup", "", "check for updates"},
		{"unsupported target", "v99.0.0", "--target is supported only"},
	} {
		for _, jsonOutput := range []bool{false, true} {
			name := "text"
			if jsonOutput {
				name = "json"
			}
			t.Run(tt.name+"/"+name, func(t *testing.T) {
				env := noInputCLIEnv(t) // empty cache and an unreachable proxy, never a real install
				args := []string{"upgrade"}
				bin := oxBin
				if tt.target != "" {
					if runtime.GOOS == "windows" {
						t.Skip("fake Homebrew detection uses a POSIX shell script")
					}
					binDir := t.TempDir()
					// Release builds (CI's OX_TEST_OX_BINARY) detect Homebrew from
					// this Cellar path; development builds detect source. Both must
					// reject the target without running an installer, and this
					// brew fails if one runs.
					require.NoError(t, os.WriteFile(filepath.Join(binDir, "brew"), []byte("#!/bin/sh\nexit 91\n"), 0o700))
					env = append(env, "PATH="+binDir)
					args = append(args, "--target="+tt.target)
					bin = inHomebrewKeg(t, oxBin)
				}
				if jsonOutput {
					args = append(args, "--json")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := testguard.OxCmdContext(t, ctx, bin, t.TempDir(), env, args...)
				var stdout, stderr bytes.Buffer
				cmd.Stdout = &stdout
				cmd.Stderr = &stderr
				err := cmd.Run()
				require.NoError(t, ctx.Err(), "stdout=%s stderr=%s", stdout.String(), stderr.String())
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit, "stdout=%s stderr=%s", stdout.String(), stderr.String())
				assert.Equal(t, 1, exit.ExitCode())
				if jsonOutput {
					var got upgradeResult
					require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
					assert.Equal(t, "failed", got.Status)
					assert.Contains(t, got.Message, tt.message)
					assert.NotContains(t, stderr.String(), tt.message, "the error should appear only in the JSON result")
				} else {
					assert.Empty(t, stdout.String())
					assert.Equal(t, 1, strings.Count(stderr.String(), tt.message))
				}
			})
		}
	}
}
