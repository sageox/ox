package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/testguard"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const readSyncTestRepoID = "repo_019ff2f5-2079-7be1-b05e-8caad2772e61"

// Run the shipped flags, RunE, and root hooks together: accidentally entering
// the ordinary sync prelude must not start daemon or human-auth activity.
func runReadSyncInProc(t *testing.T, args ...string) (ledger.ReadSyncResult, string, error) {
	t.Helper()
	cmd := &cobra.Command{
		Use:                "sync",
		RunE:               syncCmd.RunE,
		PersistentPreRunE:  rootCmd.PersistentPreRunE,
		PersistentPostRunE: rootCmd.PersistentPostRunE,
		SilenceErrors:      true,
		SilenceUsage:       true,
	}
	cmd.Flags().AddFlagSet(syncCmd.Flags())
	cmd.Flags().AddFlagSet(rootCmd.PersistentFlags())
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		value, changed := flag.Value.String(), flag.Changed
		deferRestore := func() {
			_ = flag.Value.Set(value)
			flag.Changed = changed
		}
		t.Cleanup(deferRestore)
		require.NoError(t, flag.Value.Set(flag.DefValue))
		flag.Changed = false
	})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	var result ledger.ReadSyncResult
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result), "stdout must be one JSON result: %s; err=%v", stdout.String(), err)
	return result, stderr.String(), err
}

// Failure prevented: invalid machine flags start the ordinary sync daemon or
// leak an arbitrary supplied credential through a flag-parser error message.
func TestReadSyncInvalidInvocationFailsBeforeSideEffects(t *testing.T) {
	t.Setenv("SAGEOX_ENDPOINT", "https://sageox.ai")
	t.Setenv("SAGEOX_TOKEN", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	for _, args := range [][]string{
		{"--read-only"},
		{"--repo", readSyncTestRepoID},
		{"--read-only", "--repo", "../../secret"},
		{"--read-only", "--repo", readSyncTestRepoID, "--timeout", "0s"},
		{"--read-only", "--repo", readSyncTestRepoID, "--team", "team"},
		{"--read-only", "--repo", readSyncTestRepoID, "--all-teams"},
		{"--read-only", "--repo", readSyncTestRepoID, "--remove-team", "team"},
		{"--read-only", "--repo", readSyncTestRepoID, "--config", "secret"},
		{"--read-only", "--repo", readSyncTestRepoID, "--profile"},
		{"--read-only", "--repo", readSyncTestRepoID, "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			result, stderr, err := runReadSyncInProc(t, append(args, "--json")...)
			require.Equal(t, 2, exitCodeOf(t, err))
			require.Equal(t, "invalid_arguments", result.ErrorClass)
			require.NotNil(t, result.Coverage.Paths)
			require.False(t, result.Ready)
			require.Empty(t, stderr)
		})
	}
}

// Failure prevented: source-project endpoint selection or a disk login causes a
// hosted read without a selected TAT to authenticate as an unrelated coworker.
func TestReadSyncMissingTokenIgnoresProjectAndHumanContext(t *testing.T) {
	t.Setenv("SAGEOX_TOKEN", "")
	t.Setenv("SAGEOX_ENDPOINT", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	oldProjectGetter, oldLoginGetter := endpoint.ProjectEndpointGetter, endpoint.LoggedInEndpointsGetter
	endpoint.ProjectEndpointGetter = func(string) string { t.Fatal("read consulted project identity"); return "" }
	endpoint.LoggedInEndpointsGetter = func() []string { t.Fatal("read consulted disk login identity"); return nil }
	t.Cleanup(func() {
		endpoint.ProjectEndpointGetter, endpoint.LoggedInEndpointsGetter = oldProjectGetter, oldLoginGetter
	})
	oldContext := cliCtx
	cliCtx = nil
	t.Cleanup(func() { cliCtx = oldContext })

	result, stderr, err := runReadSyncInProc(t, "--read-only", "--repo", readSyncTestRepoID, "--json")
	require.Equal(t, 1, exitCodeOf(t, err))
	require.Equal(t, "denied", result.ErrorClass)
	require.Equal(t, endpoint.Default, result.Endpoint)
	require.Equal(t, config.DefaultLedgerPath(readSyncTestRepoID, endpoint.Default), result.Path)
	require.Nil(t, cliCtx, "read-only command must skip telemetry/config initialization")
	require.Empty(t, stderr)
}

// Failure prevented: malformed endpoints disclose embedded passwords, while a
// relative data home or legacy path override silently defeats caller isolation.
func TestReadSyncRejectsUnsafeEndpointAndDataHome(t *testing.T) {
	t.Setenv("SAGEOX_TOKEN", "")
	t.Setenv("OX_XDG_DISABLE", "")
	for _, tc := range []struct{ name, endpoint, dataHome, legacy string }{
		{"userinfo", "https://user:secret@sageox.ai", t.TempDir(), ""},
		{"http", "http://sageox.ai", t.TempDir(), ""},
		{"path", "https://sageox.ai/other", t.TempDir(), ""},
		{"query", "https://sageox.ai?secret=value", t.TempDir(), ""},
		{"relative data home", "https://sageox.ai", "relative", ""},
		{"legacy override", "https://sageox.ai", t.TempDir(), "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SAGEOX_ENDPOINT", tc.endpoint)
			t.Setenv("XDG_DATA_HOME", tc.dataHome)
			t.Setenv("OX_XDG_DISABLE", tc.legacy)
			result, stderr, err := runReadSyncInProc(t, "--read-only", "--repo", readSyncTestRepoID, "--json")
			require.Equal(t, 2, exitCodeOf(t, err))
			require.Equal(t, "invalid_arguments", result.ErrorClass)
			require.Empty(t, result.Endpoint)
			require.Empty(t, stderr)
		})
	}
}

// Failure prevented: discovery errors become empty-ledger successes or leak
// response bodies, and a blocked discovery escapes the caller's total budget.
func TestReadSyncDiscoveryFailureContract(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("SAGEOX_TOKEN", validTeamToken)
	for _, tc := range []struct {
		name, class string
		status      int
		body        string
		wait        bool
	}{
		{"revoked", "denied", http.StatusUnauthorized, validTeamToken, false},
		{"forbidden", "denied", http.StatusForbidden, validTeamToken, false},
		{"old server", "unavailable", http.StatusNotFound, "not supported", false},
		{"missing ledger", "missing_ledger", http.StatusOK, `{"ledger":null}`, false},
		{"missing capability", "unavailable", http.StatusOK, `{"ledger":{"status":"ready","repo_url":"https://git.example.com/ledger.git"}}`, false},
		{"malformed", "unavailable", http.StatusOK, validTeamToken, false},
		{"timeout", "interrupted", 0, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/v1/cli/repos/"+readSyncTestRepoID, r.URL.Path)
				assert.Equal(t, "Bearer "+validTeamToken, r.Header.Get("Authorization"))
				if tc.wait {
					<-r.Context().Done()
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			oldTransport := http.DefaultTransport
			http.DefaultTransport = server.Client().Transport
			defer func() { http.DefaultTransport = oldTransport }()
			t.Setenv("SAGEOX_ENDPOINT", server.URL)
			args := []string{"--read-only", "--repo", readSyncTestRepoID, "--json"}
			if tc.wait {
				args = append(args, "--timeout", "30ms")
			}
			result, stderr, err := runReadSyncInProc(t, args...)
			require.Equal(t, 1, exitCodeOf(t, err))
			require.Equal(t, tc.class, result.ErrorClass)
			require.False(t, result.Ready)
			require.Nil(t, result.LastSuccessfulSync)
			require.Empty(t, stderr)
			encoded, err := json.Marshal(result)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), validTeamToken)
		})
	}
}

// Failure prevented: checking one caller's missing cache looks inside another
// caller's checkout or requires a credential despite being a local check.
func TestReadSyncLocalCheckUsesSelectedDataHome(t *testing.T) {
	t.Setenv("SAGEOX_ENDPOINT", endpoint.Default)
	t.Setenv("SAGEOX_TOKEN", "")
	t.Setenv("OX_XDG_DISABLE", "")
	var firstPath string
	for range 2 {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
		result, _, err := runReadSyncInProc(t, "--read-only", "--repo", readSyncTestRepoID, "--check", "--json")
		require.Equal(t, 1, exitCodeOf(t, err))
		require.Equal(t, config.DefaultLedgerPath(readSyncTestRepoID, endpoint.Default), result.Path)
		require.NotEqual(t, "denied", result.ErrorClass)
		require.False(t, result.Ready)
		require.Nil(t, result.LastSuccessfulSync)
		require.NotEqual(t, firstPath, result.Path)
		firstPath = result.Path
	}
}

// Failure prevented: the pre-Cobra machine path misses an invalid flag and
// invokes dotenv, daemon feature lookup, or interactive friction recovery.
func TestReadSyncRequestedIncludesMalformedReadFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"sync", "--read-only", "--repo", readSyncTestRepoID}, true},
		{[]string{"--json", "sync", "--read-only=true"}, true},
		{[]string{"sync", "--read-only=invalid"}, true},
		{[]string{"sync", "--timeout=broken"}, true},
		{[]string{"sync", "--repo", readSyncTestRepoID}, true},
		{[]string{"sync", "--check=false"}, true},
		{[]string{"sync", "--read-only=false"}, false},
		{[]string{"sync", "--read-only=0"}, false},
		{[]string{"sync", "--read-only", "--read-only=false"}, false},
		{[]string{"sync", "--", "--read-only"}, false},
		{[]string{"sync", "--team", "team"}, false},
		{[]string{"status", "--json"}, false},
		{[]string{"query", "sync", "--repo", readSyncTestRepoID, "--unknown"}, false},
		{[]string{"query", "git-credential-helper", "--read-repo", readSyncTestRepoID}, false},
		{[]string{"help", "sync", "--repo", readSyncTestRepoID}, false},
		{[]string{"--config", "sync", "query", "text", "--repo", readSyncTestRepoID}, false},
		{[]string{"-c", "git-credential-helper", "query", "text", "--read-url", "url"}, false},
		{[]string{"--", "sync", "--read-only"}, false},
		{[]string{"unknown-command", "sync", "--read-only"}, false},
		{[]string{"--config", "config.yaml", "sync", "--read-only"}, true},
		{[]string{"-cconfig.yaml", "sync", "--read-only"}, true},
		{[]string{"-vq", "sync", "--read-only"}, true},
		{[]string{"git-credential-helper", "--read-endpoint=https://test.sageox.ai", "get"}, true},
		{[]string{"git-credential-helper", "--read-repo", readSyncTestRepoID, "get"}, true},
		{[]string{"git-credential-helper", "get"}, false},
	} {
		require.Equal(t, tc.want, headlessLedgerReadRequested(tc.args), "%v", tc.args)
	}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	require.Equal(t, 2, writeReadSyncUsageError(cmd, []string{"sync", "--read-only", "--unknown=" + validTeamToken, "--json"}))
	require.NotContains(t, out.String(), validTeamToken)
	var result ledger.ReadSyncResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	require.Equal(t, "invalid_arguments", result.ErrorClass)
}

// Failure prevented: JSON-mode invalid arguments unexpectedly create files in
// the working directory (for example, --profile bypassing the hosted guard).
func TestReadSyncProfileCannotWriteFiles(t *testing.T) {
	t.Setenv("SAGEOX_ENDPOINT", endpoint.Default)
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := t.TempDir()
	t.Chdir(dir)
	_, _, err := runReadSyncInProc(t, "--read-only", "--repo", readSyncTestRepoID, "--profile", "--json")
	require.Equal(t, 2, exitCodeOf(t, err))
	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, files)
}

// Failure prevented: package-level command tests miss main's early dotenv and
// feature/IPC setup, allowing source-controlled configuration into TAT selection.
func TestReadSyncProcessIgnoresDotenvAndSanitizesParserErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real CLI subprocesses")
	}
	bin, err := os.Executable()
	require.NoError(t, err)
	for _, tc := range []struct {
		name, class string
		args        []string
		code        int
	}{
		{"dotenv", "denied", []string{"--repo", readSyncTestRepoID}, 1},
		{"parser", "invalid_arguments", []string{"--unknown=" + validTeamToken}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("SAGEOX_TOKEN="+validTeamToken+"\nSAGEOX_ENDPOINT=http://source-controlled.invalid\n"), 0o600))
			args := append([]string{"-test.run=^TestReadSyncProcessHelper$", "--", "sync", "--read-only", "--json"}, tc.args...)
			cmd := testguard.OxCmd(t, bin, dir, []string{
				"OX_TEST_READ_SYNC_HELPER=1", "XDG_DATA_HOME=" + t.TempDir(), "HOME=" + t.TempDir(),
			}, args...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit)
			require.Equal(t, tc.code, exit.ExitCode(), "stderr=%s", stderr.String())
			var result ledger.ReadSyncResult
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &result), "stdout=%s stderr=%s", stdout.String(), stderr.String())
			require.Equal(t, tc.class, result.ErrorClass)
			require.NotContains(t, stdout.String()+stderr.String(), validTeamToken)
			files, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Len(t, files, 1)
		})
	}
}

// Failure prevented: query text or a flag value named sync changes an unrelated
// command's initialization and parser errors into ledger-read handling.
func TestReadSyncProcessPreservesOtherCommandErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real CLI subprocesses")
	}
	bin, err := os.Executable()
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"ordinary query", []string{"query", "ordinary", "--repo", readSyncTestRepoID, "--unknown"}},
		{"sync query", []string{"query", "sync", "--repo", readSyncTestRepoID, "--unknown"}},
		{"helper query", []string{"query", "git-credential-helper", "--read-repo", readSyncTestRepoID, "--unknown"}},
		{"config value", []string{"--config", "sync", "query", "ordinary", "--repo", readSyncTestRepoID, "--unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"-test.run=^TestReadSyncProcessHelper$", "--"}, tc.args...)
			cmd := testguard.OxCmd(t, bin, t.TempDir(), []string{
				"OX_TEST_READ_SYNC_HELPER=1", "XDG_DATA_HOME=" + t.TempDir(), "HOME=" + t.TempDir(),
			}, args...)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit)
			assert.Equal(t, 1, exit.ExitCode(), "output=%s", out)
			assert.Contains(t, string(out), "unknown flag:")
			assert.NotContains(t, string(out), "Ledger read failed")
			assert.NotContains(t, string(out), "Use: ox sync --read-only")
		})
	}
}

func TestReadSyncProcessHelper(t *testing.T) {
	if os.Getenv("OX_TEST_READ_SYNC_HELPER") != "1" {
		return
	}
	// TestMain moves away from cmd.Dir; restore this subprocess's fixture so
	// main actually encounters the source-controlled .env being tested.
	t.Chdir(packageDir)
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
			main()
			return
		}
	}
	t.Fatal("missing subprocess command arguments")
}

// Failure prevented: Git's nested scoped helper reloads a source .env and
// silently supplies a token that the hosted caller never selected.
func TestReadSyncCredentialHelperDoesNotLoadDotenv(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real credential-helper subprocess")
	}
	bin, err := os.Executable()
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("SAGEOX_TOKEN="+validTeamToken+"\nSAGEOX_ENDPOINT=https://test.sageox.ai\n"), 0o600))
	cmd := testguard.OxCmd(t, bin, dir, []string{
		"OX_TEST_READ_SYNC_HELPER=1", "XDG_DATA_HOME=" + t.TempDir(), "HOME=" + t.TempDir(),
	}, "-test.run=^TestReadSyncProcessHelper$", "--", "git-credential-helper",
		"--read-endpoint=https://test.sageox.ai", "--read-repo="+readSyncTestRepoID,
		"--read-url=https://test.sageox.ai/api/v1/cli/repos/"+readSyncTestRepoID+"/ledger.git", "get")
	cmd.Stdin = strings.NewReader("protocol=https\nhost=test.sageox.ai\npath=api/v1/cli/repos/" + readSyncTestRepoID + "/ledger.git\n\n")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "output=%s", out)
	require.Equal(t, "quit=true\n\n", string(out))
}

// Failure prevented: human output reports success on an unavailable checkout,
// or a failed JSON write produces a successful process exit.
func TestReadSyncOutputContract(t *testing.T) {
	for _, tc := range []struct {
		name               string
		code               int
		class, out, errOut string
	}{
		{"ready", 0, "", "Ledger ready: /selected/ledger (HEAD abc123)\n", ""},
		{"denied", 1, "denied", "", "Ledger read failed: denied\n"},
		{"usage", 2, "invalid_arguments", "", "Ledger read failed: invalid_arguments\nUse: ox sync --read-only --repo repo_<uuid> [--timeout 5m] [--check] [--json]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			result := ledger.ReadSyncResult{Path: "/selected/ledger", Head: "abc123", Ready: tc.code == 0, ErrorClass: tc.class}
			err := finishReadSync(cmd, result, false, tc.code)
			if tc.code == 0 {
				require.NoError(t, err)
			} else {
				require.Equal(t, tc.code, exitCodeOf(t, err))
			}
			require.Equal(t, tc.out, stdout.String())
			require.Equal(t, tc.errOut, stderr.String())
		})
	}
	r, w := io.Pipe()
	require.NoError(t, r.Close())
	defer w.Close()
	cmd := &cobra.Command{}
	cmd.SetOut(w)
	require.Equal(t, 1, exitCodeOf(t, finishReadSync(cmd, ledger.ReadSyncResult{Ready: true}, true, 0)))
}
