package main

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// Failure prevented: an incomplete read scope reaches the human credential
// helper, or helper maintenance operations accidentally disclose a team token.
func TestLedgerReadCredentialHelperDispatch(t *testing.T) {
	const repoID = "repo_01936d5a-0000-7abc-8def-0123456789ab"
	resource := "api/v1/cli/repos/" + repoID + "/ledger.git"
	t.Setenv("SAGEOX_ENDPOINT", "https://sageox.ai")
	t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")
	for _, tc := range []struct {
		name, operation string
		flags           map[string]string
		want            string
	}{
		{"implicit get", "", map[string]string{"read-endpoint": "https://sageox.ai", "read-repo": repoID, "read-url": "https://sageox.ai/" + resource}, "username=ox\npassword=oxt_test_1ljPfr\n\n"},
		{"explicit get", "get", map[string]string{"read-endpoint": "https://sageox.ai", "read-repo": repoID, "read-url": "https://sageox.ai/" + resource}, "username=ox\npassword=oxt_test_1ljPfr\n\n"},
		{"endpoint alone", "get", map[string]string{"read-endpoint": ""}, "quit=true\n\n"},
		{"repo alone", "get", map[string]string{"read-repo": ""}, "quit=true\n\n"},
		{"url alone", "get", map[string]string{"read-url": ""}, "quit=true\n\n"},
		{"store", "store", map[string]string{"read-url": ""}, ""},
		{"erase", "erase", map[string]string{"read-url": ""}, ""},
		{"unknown operation", "unknown", map[string]string{"read-url": ""}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "git-credential-helper"}
			for _, name := range []string{"read-endpoint", "read-repo", "read-url"} {
				cmd.Flags().String(name, "", "")
			}
			require.False(t, isHeadlessLedgerRead(cmd))
			for name, value := range tc.flags {
				require.NoError(t, cmd.Flags().Set(name, value))
			}
			require.True(t, isHeadlessLedgerRead(cmd), "even an empty explicit scope must bypass human startup")
			input := strings.NewReader("protocol=https\nhost=sageox.ai\npath=" + resource + "\n\n")
			var out bytes.Buffer
			cmd.SetIn(input)
			cmd.SetOut(&out)
			var args []string
			if tc.operation != "" {
				args = []string{tc.operation}
			}
			require.NoError(t, runGitCredentialHelper(cmd, args))
			require.Equal(t, tc.want, out.String())
			require.Zero(t, input.Len())
		})
	}
	require.False(t, isHeadlessLedgerRead(&cobra.Command{Use: "status"}))
}

// Failure prevented: corrupt credential input falls through to another helper,
// or an unwritable protocol output is reported as successful authentication.
func TestLedgerReadCredentialHelperProtocolFailures(t *testing.T) {
	const repoID = "repo_01936d5a-0000-7abc-8def-0123456789ab"
	resource := "api/v1/cli/repos/" + repoID + "/ledger.git"
	readURL := "https://sageox.ai/" + resource
	t.Setenv("SAGEOX_ENDPOINT", "https://sageox.ai")
	t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")
	for _, request := range []string{
		"protocol=http\nhost=sageox.ai\npath=" + resource + "\n\n",
		"protocol=https\nhost=sageox.ai\npath=/" + resource + "\n\n",
		"protocol=https\nhost=sageox.ai\rforeign.example\npath=" + resource + "\n\n",
		"protocol=https\nhost=sageox.ai\npath=" + strings.Repeat("a", 1<<20),
	} {
		var out bytes.Buffer
		require.NoError(t, helperReadGet(strings.NewReader(request), &out, "https://sageox.ai", repoID, readURL))
		require.Equal(t, "quit=true\n\n", out.String())
	}
	for _, request := range []string{"", "protocol=https\nhost=sageox.ai\npath=" + resource + "\n\n"} {
		reader, writer := io.Pipe()
		require.NoError(t, reader.Close())
		err := helperReadGet(strings.NewReader(request), writer, "https://sageox.ai", repoID, readURL)
		require.NoError(t, writer.Close())
		require.ErrorIs(t, err, io.ErrClosedPipe)
	}
}

// Failure prevented: diagnosing a rejected read credential logs its token,
// request path, or arbitrary input error instead of a safe rejection category.
func TestLedgerReadCredentialHelperRejectionLogsAreSanitized(t *testing.T) {
	const repoID = "repo_01936d5a-0000-7abc-8def-0123456789ab"
	resource := "api/v1/cli/repos/" + repoID + "/ledger.git"
	t.Setenv("SAGEOX_ENDPOINT", "https://sageox.ai")
	t.Setenv("SAGEOX_TOKEN", "private-credential")
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, tc := range []struct{ request, reason string }{
		{"path=" + strings.Repeat("private-input", 1<<17), "malformed_request"},
		{"protocol=https\nhost=private-host.example\npath=private-path\n\n", "unsafe_scope"},
		{"protocol=https\nhost=sageox.ai\npath=" + resource + "\n\n", "token_unavailable"},
	} {
		logs.Reset()
		var out bytes.Buffer
		require.NoError(t, helperReadGet(strings.NewReader(tc.request), &out, "https://sageox.ai", repoID, "https://sageox.ai/"+resource))
		require.Equal(t, "quit=true\n\n", out.String())
		require.Contains(t, logs.String(), "reason="+tc.reason)
		require.NotContains(t, logs.String(), "private-")
		require.NotContains(t, logs.String(), resource)
	}
}

// Failure prevented: the read helper releases a TAT to a same-host foreign
// resource or falls through to a human identity after token removal/rotation.
func TestLedgerReadCredentialHelperScopeAndIdentity(t *testing.T) {
	const repoID = "repo_01936d5a-0000-7abc-8def-0123456789ab"
	resource := "api/v1/cli/repos/" + repoID + "/ledger.git"
	readURL := "https://sageox.ai/" + resource
	t.Setenv("SAGEOX_ENDPOINT", "https://sageox.ai")
	for _, tc := range []struct {
		name, token, host, path string
		allowed                 bool
	}{
		{"selected", "oxt_test_1ljPfr", "sageox.ai", resource, true},
		{"rotated", "oxt_rotated_1lKvCA", "sageox.ai", resource, true},
		{"protocol child", "oxt_test_1ljPfr", "sageox.ai", resource + "/git-upload-pack", true},
		{"LFS child", "oxt_test_1ljPfr", "sageox.ai", resource + "/info/lfs/objects/batch", true},
		{"removed", "", "sageox.ai", resource, false},
		{"malformed", "oxt_test_bad", "sageox.ai", resource, false},
		{"human token", "oxp_test_4bDZfN", "sageox.ai", resource, false},
		{"opaque token", "human-token", "sageox.ai", resource, false},
		{"GitLab host", "oxt_test_1ljPfr", "git.sageox.ai", resource, false},
		{"hostless path", "oxt_test_1ljPfr", "sageox.ai", "", false},
		{"foreign path", "oxt_test_1ljPfr", "sageox.ai", "api/v1/cli/repos/other/ledger.git", false},
		{"push", "oxt_test_1ljPfr", "sageox.ai", resource + "/git-receive-pack", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SAGEOX_TOKEN", tc.token)
			var out bytes.Buffer
			request := fmt.Sprintf("protocol=https\nhost=%s\npath=%s\n\n", tc.host, tc.path)
			require.NoError(t, helperReadGet(strings.NewReader(request), &out, "https://sageox.ai", repoID, readURL))
			if tc.allowed {
				require.Equal(t, "username=ox\npassword="+tc.token+"\n\n", out.String())
			} else {
				require.Equal(t, "quit=true\n\n", out.String())
			}
		})
	}
	t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")
	t.Setenv("SAGEOX_ENDPOINT", "https://different.example")
	var out bytes.Buffer
	require.NoError(t, helperReadGet(strings.NewReader("protocol=https\nhost=sageox.ai\npath="+resource+"\n\n"), &out, "https://sageox.ai", repoID, readURL))
	require.Equal(t, "quit=true\n\n", out.String())
}

// Failure prevented: an executable path containing shell separators executes
// another command with the selected credential in its inherited environment.
func TestCredentialHelperExecutableIsShellQuoted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable path and shell fixture")
	}
	exe := filepath.Join(t.TempDir(), "ox;false")
	require.NoError(t, os.WriteFile(exe, []byte("#!/bin/sh\nprintf '%s' \"$1\"\n"), 0o700))
	cmd := exec.Command("sh", "-c", shellQuote(exe)+" git-credential-helper")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	require.Equal(t, "git-credential-helper", string(out))
}
