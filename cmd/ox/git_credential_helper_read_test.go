package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

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
