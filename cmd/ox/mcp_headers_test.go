package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/useragent"
	"github.com/stretchr/testify/require"
)

// Failure prevented: an MCP helper leaks credentials, emits malformed headers, or starts network telemetry.
func TestMCPHeadersLocalOutput(t *testing.T) {
	tests := []struct {
		name, repoID, rawConfig string
		sageoxDir, initialized  bool
		nested, yaml            bool
		want                    string
	}{
		{name: "outside a repo"},
		{name: "directory alone is not initialized", sageoxDir: true},
		{name: "initialized repo", initialized: true, repoID: "repo_test_mcp", want: "repo_test_mcp"},
		{name: "nested directory", initialized: true, nested: true, repoID: "repo_test_nested", want: "repo_test_nested"},
		{name: "yaml config", initialized: true, yaml: true, repoID: "repo_test_yaml", want: "repo_test_yaml"},
		{name: "missing id", initialized: true},
		{name: "header injection", initialized: true, repoID: "repo_test\r\nInjected: value"},
		{name: "whitespace", initialized: true, repoID: "repo test"},
		{name: "oversized id", initialized: true, repoID: strings.Repeat("x", 129)},
		{name: "maximum valid id", initialized: true, repoID: strings.Repeat("x", 128), want: strings.Repeat("x", 128)},
		{name: "corrupt config", initialized: true, rawConfig: "{"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recoveryEnv(t)
			t.Setenv("OX_PROJECT_ROOT", "")
			useragent.ResetForTesting()
			t.Cleanup(useragent.ResetForTesting)
			previousContext, previousPreRun := cliCtx, preRunReached
			cliCtx, preRunReached = nil, false
			t.Cleanup(func() { cliCtx, preRunReached = previousContext, previousPreRun })

			project := t.TempDir()
			if tt.sageoxDir || tt.initialized {
				require.NoError(t, os.Mkdir(filepath.Join(project, ".sageox"), 0o700))
			}
			if tt.initialized {
				data, err := json.Marshal(map[string]string{"repo_id": tt.repoID})
				require.NoError(t, err)
				filename := "config.json"
				if tt.yaml {
					filename, data = "config.yaml", []byte("repo_id: "+tt.repoID+"\n")
				}
				if tt.rawConfig != "" {
					data = []byte(tt.rawConfig)
				}
				require.NoError(t, os.WriteFile(filepath.Join(project, ".sageox", filename), data, 0o600))
			}
			if tt.nested {
				project = filepath.Join(project, "nested")
				require.NoError(t, os.Mkdir(project, 0o700))
			}
			t.Chdir(project)

			// Both credential sources are present, but neither belongs in headers.
			storedCredential, envCredential := "fake-mcp-stored-credential", "fake-mcp-env-credential"
			t.Setenv("SAGEOX_TOKEN", envCredential)
			t.Setenv("OX_XDG_DISABLE", "")
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			authPath, err := auth.GetAuthFilePath()
			require.NoError(t, err)
			store := auth.AuthStore{Tokens: map[string]*auth.StoredToken{
				"http://127.0.0.1:9": {
					AccessToken: storedCredential, RefreshToken: storedCredential,
					ExpiresAt: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
					UserInfo:  auth.UserInfo{UserID: "usr_test_mcp"},
				},
			}}
			data, err := json.Marshal(store)
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Dir(authPath), 0o700))
			require.NoError(t, os.WriteFile(authPath, data, 0o600))

			run := runRecovery(t, "mcp", "headers")
			require.Zero(t, run.code)
			require.Nil(t, cliCtx, "helper initialized telemetry context")
			require.False(t, preRunReached, "root pre-run must be skipped")
			if strings.Contains(run.stdout, storedCredential) || strings.Contains(run.stdout, envCredential) {
				t.Fatal("helper exposed a credential")
			}
			if !tt.initialized {
				require.Empty(t, run.stdout)
				return
			}
			var headers map[string]string
			decoder := json.NewDecoder(strings.NewReader(run.stdout))
			require.NoError(t, decoder.Decode(&headers), "stdout must contain only allowed headers")
			require.Equal(t, io.EOF, decoder.Decode(new(any)), "stdout must contain exactly one JSON object")
			require.NotNil(t, headers, "stdout must be a JSON object")
			for key := range headers {
				if key != "X-Sageox-Repo-Id" {
					t.Fatal("helper emitted an unexpected header")
				}
			}
			if tt.want == "" {
				require.Empty(t, headers, "invalid and missing ids must be dropped")
			} else {
				require.Equal(t, tt.want, headers["X-Sageox-Repo-Id"])
			}
		})
	}
}

// Failure prevented: an internal helper appears as a public command or loses its registered entry point.
func TestMCPHeadersHidden(t *testing.T) {
	found, rest, err := rootCmd.Find([]string{"mcp", "headers"})
	require.NoError(t, err)
	require.Empty(t, rest)
	require.Same(t, mcpHeadersCmd, found)
	require.True(t, mcpCmd.Hidden)
	require.True(t, found.Hidden)
}

// Failure prevented: a rejected helper invocation acquires a context that main sends to PostHog.
func TestMCPHeadersRejectedArgumentsStayLocal(t *testing.T) {
	for _, args := range [][]string{
		{"mcp", "headers", "extra"},
		{"mcp", "headers", "--unknown"},
		{"mcp", "unknown"},
		{"mcp", "headers", "--config="},
		{"mcp headers"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			recoveryEnv(t)
			t.Setenv("DO_NOT_TRACK", "")
			t.Setenv("SAGEOX_FRICTION", "true")
			t.Setenv("CLAUDECODE", "1")
			cliCtx = nil

			run := runRecovery(t, args...)
			require.NotZero(t, run.code, "invalid helper invocations must still be rejected")
			require.Empty(t, run.stdout)
			if cliCtx != nil {
				t.Fatal("shared error recovery created a telemetry context for the local helper")
			}
		})
	}
}

// Failure prevented: startup probes the daemon before the helper can skip the root lifecycle.
func TestMCPHeadersSkipDaemonStartup(t *testing.T) {
	recoveryEnv(t)
	t.Setenv("OX_PROJECT_ROOT", "")
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", "/tmp") // Keep the Unix socket path below the platform limit.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DO_NOT_TRACK", "")
	useragent.ResetForTesting()
	t.Cleanup(useragent.ResetForTesting)
	project := t.TempDir()
	require.NoError(t, config.SaveProjectConfig(project, &config.ProjectConfig{RepoID: "repo_test_local"}))
	t.Chdir(project)
	dials, _ := fakeDaemonDials(t)
	cliCtx = nil

	run := runRecovery(t, "mcp", "headers")
	require.Zero(t, run.code)
	require.Zero(t, dials.Load(), "local headers must never contact the daemon")
	require.Nil(t, cliCtx)
}

// Failure prevented: a broken stdout destination re-enters telemetry or friction recovery.
func TestMCPHeadersOutputFailureStaysLocal(t *testing.T) {
	recoveryEnv(t)
	t.Setenv("OX_PROJECT_ROOT", "")
	t.Setenv("DO_NOT_TRACK", "")
	project := t.TempDir()
	require.NoError(t, config.SaveProjectConfig(project, &config.ProjectConfig{RepoID: "repo_test_output"}))
	t.Chdir(project)
	useragent.ResetForTesting()
	t.Cleanup(useragent.ResetForTesting)
	mcpHeadersCmd.SetOut(failingWriter{})
	t.Cleanup(func() { mcpHeadersCmd.SetOut(nil) })
	cliCtx = nil

	run := runRecovery(t, "mcp", "headers")
	require.NotZero(t, run.code)
	require.Contains(t, run.stderr, "write failed")
	require.Nil(t, cliCtx)
}

// Failure prevented: flags or their values select the wrong command's startup policy.
func TestMCPHeadersInvocationLookup(t *testing.T) {
	recoveryEnv(t)
	for _, tt := range []struct {
		args []string
		want bool
	}{
		{[]string{"mcp", "headers"}, true},
		{[]string{"mcp headers"}, true},
		{[]string{"--json", "mcp", "headers"}, true},
		{[]string{"--config", "local.yaml", "mcp", "headers"}, true},
		{[]string{"mcp", "headers", "--unknown"}, true},
		{[]string{"mcp", "unknown"}, true},
		{[]string{"status", "--config", "mcp"}, false},
		{[]string{"--config=mcp", "status"}, false},
		{[]string{"status", "mcp"}, false},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			require.Equal(t, tt.want, localMCPRequested(tt.args))
		})
	}
}
