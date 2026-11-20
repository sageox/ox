package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

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
	isolateAgentDetection(t)
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
