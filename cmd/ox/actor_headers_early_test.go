package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/useragent"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func earlyActorTestRoot(t *testing.T) *cobra.Command {
	t.Helper()
	useragent.ResetForTesting()
	t.Cleanup(useragent.ResetForTesting)
	previousContext, previousConfig, previousPreRun := cliCtx, cfg, preRunReached
	cliCtx, cfg, preRunReached = nil, nil, false
	t.Cleanup(func() {
		cliCtx, cfg, preRunReached = previousContext, previousConfig, previousPreRun
	})
	home := t.TempDir()
	t.Chdir(home)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv(config.EnvUserConfig, filepath.Join(home, "config.yaml"))
	t.Setenv("AGENT_ENV", "codex")
	t.Setenv("DO_NOT_TRACK", "1")

	// Fresh flags avoid changing the shared command tree while exercising the
	// shipped pre-run hook and the full ox feedback command path.
	root := &cobra.Command{
		Use:               "ox",
		PersistentPreRunE: rootCmd.PersistentPreRunE,
		SilenceErrors:     true,
		SilenceUsage:      true,
	}
	root.PersistentFlags().String("config", "", "")
	root.PersistentFlags().Bool("json", false, "")
	root.AddGroup(&cobra.Group{ID: "diagnostics", Title: "Diagnostics"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	return root
}

// Failure prevented: anonymous feedback skips telemetry startup and loses the command's actor headers.
func TestFeedbackEarlyPreludePreservesRequestActor(t *testing.T) {
	root := earlyActorTestRoot(t)
	feedbackTestDependencies(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/v1/support/cases", r.URL.Path)
		assert.Empty(t, r.Header.Get("Authorization"))
		assert.Equal(t, "agent", r.Header.Get(useragent.HeaderActor))
		assert.Equal(t, "codex", r.Header.Get(useragent.HeaderAgentType))
		_, _ = io.WriteString(w, `{"case_id":"`+feedbackTestReceipt+`"}`)
	}))
	t.Cleanup(server.Close)
	t.Setenv("SAGEOX_ENDPOINT", server.URL)

	root.AddCommand(newFeedbackCommand())
	root.SetIn(strings.NewReader(feedbackTestReport))
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"feedback", "--file", "-", "--anonymous", "--json"})
	require.NoError(t, root.Execute())
	require.True(t, preRunReached, "submission must run through the actual root prelude")
	require.Equal(t, int32(1), requests.Load(), "feedback must actually reach the local support endpoint")
	assert.Contains(t, output.String(), feedbackTestReceipt)
	assert.Nil(t, cliCtx, "feedback must not bootstrap the telemetry context")
	assert.Nil(t, cfg, "feedback must not initialize the ordinary command config")
}

// Failure prevented: headless ledger discovery skips telemetry startup and sends requests without an actor.
func TestHeadlessReadEarlyPreludePreservesRequestActor(t *testing.T) {
	root := earlyActorTestRoot(t)
	const repoID = "repo_01936d5a-0000-7abc-8def-0123456789ab"
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/v1/cli/repos/"+repoID, r.URL.Path)
		assert.Equal(t, "agent", r.Header.Get(useragent.HeaderActor))
		assert.Equal(t, "codex", r.Header.Get(useragent.HeaderAgentType))
		_, _ = fmt.Fprintf(w, `{"ledger":{"status":"ready","read_url":"https://%s/api/v1/cli/repos/%s/ledger.git"}}`, r.Host, repoID)
	}))
	t.Cleanup(server.Close)
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	previousTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	cmd := &cobra.Command{Use: "sync"}
	cmd.Flags().Bool("read-only", false, "")
	root.AddCommand(cmd)
	require.NoError(t, cmd.Flags().Set("read-only", "true"))
	require.True(t, isHeadlessLedgerRead(cmd))
	require.NoError(t, rootCmd.PersistentPreRunE(cmd, nil))
	require.True(t, preRunReached)
	require.Nil(t, cliCtx, "headless reads must not bootstrap the telemetry context")
	require.Nil(t, cfg, "headless reads must not load local command preferences")

	readURL, err := api.NewRepoClientWithEndpoint(server.URL).GetLedgerReadURL(t.Context(), repoID, "oxt_test_1ljPfr")
	require.NoError(t, err)
	assert.Equal(t, server.URL+"/api/v1/cli/repos/"+repoID+"/ledger.git", readURL)
	require.Equal(t, int32(1), requests.Load(), "discovery must actually reach the local endpoint")
	assert.Nil(t, cliCtx)
}
