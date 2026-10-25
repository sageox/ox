//go:build !short

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunAgentPrime_KeepsWarningsOffStderr initializes against the E2E mock
// cloud, then shuts the cloud down and primes: the KB source hits connection
// refused and logs "kb fetch: list failed" at WARN — the exact line that,
// riding the hook's 2>&1, convinced a coworker in the 2026-09-11 eval pilot
// that no team context had loaded (case 04: 0.25 with the plugin, 1.00
// without). A 404 would not do: the source treats that as "feature off" and
// stays quiet; only a real transport failure produces the WARN.
//
// Failure prevented: a transient WARN at session start canceling the prime
// payload it precedes. Red-first: with logger.InitPayloadMode removed from
// runAgentPrime this fails on the stderr assertion; the diagnostics file
// assertion proves the warning was rerouted, not swallowed.
func TestRunAgentPrime_KeepsWarningsOffStderr(t *testing.T) {
	env := initializedE2E(t)
	t.Cleanup(func() { logger.Init(false) })

	// the KB fetch only runs for a repo with a synced team context; stage the
	// row `ox init` + a daemon sync would have written
	teamDir := t.TempDir()
	localCfg := fmt.Sprintf("\n[[team_contexts]]\nteam_id = %q\nteam_name = %q\nslug = %q\npath = %q\nlast_sync = 0001-01-01T00:00:00Z\n",
		env.TeamID, "E2E Team", "e2e-team", teamDir)
	require.NoError(t, os.WriteFile(filepath.Join(env.Root, ".sageox", "config.local.toml"), []byte(localCfg), 0o600))

	// the cloud goes away between init and prime — the offline-laptop shape
	env.Server.Close()

	// swap the process stderr so the handler prime installs binds to the pipe
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	captured := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		captured <- buf.String()
	}()

	var out bytes.Buffer
	cmd := agentPrimeCmd
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	require.NoError(t, cmd.Flags().Set("agent", "claude-code"))
	t.Cleanup(func() {
		_ = cmd.Flags().Set("agent", "")
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})

	runErr := runAgentPrime(cmd, nil)

	os.Stderr = origStderr
	_ = w.Close()
	stderr := <-captured
	_ = r.Close()

	require.NoError(t, runErr, "prime must succeed in an initialized repo")
	require.Contains(t, out.String(), "<ox-prime", "prime must still emit its payload")

	assert.NotContains(t, stderr, "level=WARN",
		"WARN diagnostics must not reach stderr — the hook pipes it into the model's context")
	assert.NotContains(t, stderr, "kb fetch",
		"the KB fetch failure must not precede the payload on stderr")

	// the warning is rerouted, not lost: the mock 404s the KB route, so the
	// source must have logged it somewhere a human can still find
	logPath := os.ExpandEnv("$XDG_CACHE_HOME/sageox/logs/agent-payload.log")
	logged, err := os.ReadFile(logPath)
	require.NoError(t, err, "payload diagnostics file must exist at %s", logPath)
	assert.True(t, strings.Contains(string(logged), "kb fetch: list failed"),
		"the KB warning must be preserved in %s, got:\n%s", logPath, logged)
}

// TestRunAgentPrime_HookModeFitsClaudeHookCap drives the real prime with a
// SessionStart hook payload on stdin against a team context large enough to
// blow the cap, and asserts what Claude Code would actually inject.
//
// Failure prevented: Claude Code persisting the whole prime to a file and
// injecting a 2 KB preview — "Team rules I can see: none" (eval case 09,
// 2026-09-11). Red-first: with the HookOutputBudget assignment removed from
// runAgentPrime the size assertion fails.
func TestRunAgentPrime_HookModeFitsClaudeHookCap(t *testing.T) {
	env := initializedE2E(t)
	t.Cleanup(func() { logger.Init(false) })

	// a team context with an always-rule, memory, and enough docs to push the
	// full prime well past the cap
	teamDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(teamDir, "agents", "rules"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(teamDir, "docs"), 0o755))
	rule := "---\nname: retry-policy\ndescription: how clients retry\nvisibility: always\n---\n\n# Retry policy\n\n" + strings.Repeat("Retry with capped exponential backoff, max 3 attempts, never on 4xx.\n", 12)
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "agents", "rules", "retry-policy.md"), []byte(rule), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "MEMORY.md"), []byte("# memory\n\n"+strings.Repeat("- a durable team fact worth keeping\n", 15)), 0o644))
	for i := 0; i < 24; i++ {
		doc := fmt.Sprintf("---\ntitle: Doc %02d\ndescription: reference doc %02d\nvisibility: indexed\nwhen: when task %02d comes up in the upload service\n---\n\n# Doc %02d\n\nbody\n", i, i, i, i)
		require.NoError(t, os.WriteFile(filepath.Join(teamDir, "docs", fmt.Sprintf("doc-%02d.md", i)), []byte(doc), 0o644))
	}
	localCfg := fmt.Sprintf("\n[[team_contexts]]\nteam_id = %q\nteam_name = %q\nslug = %q\npath = %q\nlast_sync = 0001-01-01T00:00:00Z\n",
		env.TeamID, "E2E Team", "e2e-team", teamDir)
	require.NoError(t, os.WriteFile(filepath.Join(env.Root, ".sageox", "config.local.toml"), []byte(localCfg), 0o600))

	// the hook payload arrives on stdin, exactly as Claude Code sends it
	origStdin := os.Stdin
	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = io.WriteString(w, `{"session_id":"cap-e2e","hook_event_name":"SessionStart","source":"startup"}`)
	require.NoError(t, err)
	_ = w.Close()
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin; _ = r.Close() })

	var out bytes.Buffer
	cmd := agentPrimeCmd
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	require.NoError(t, cmd.Flags().Set("agent", "claude-code"))
	t.Cleanup(func() {
		_ = cmd.Flags().Set("agent", "")
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})
	require.NoError(t, runAgentPrime(cmd, nil))

	emitted := out.String()
	assert.LessOrEqual(t, len(emitted), primeHookBudget,
		"a hook-driven prime must fit Claude Code's %d-char hook cap or the model sees a 2 KB preview", claudeHookOutputCap)
	assert.Contains(t, emitted, `name="retry-policy" visibility="always"`, "the always-rule must survive the trim")
	assert.Contains(t, emitted, "<consult-first>", "consult-first must survive the trim")
	require.Contains(t, emitted, `<deferred path="`, "an over-cap prime must point at its full bundle")

	start := strings.Index(emitted, `<deferred path="`) + len(`<deferred path="`)
	fullPath := emitted[start : start+strings.Index(emitted[start:], `"`)]
	full, err := os.ReadFile(fullPath)
	require.NoError(t, err, "the full bundle must exist at the path the pointer names")
	assert.Greater(t, len(full), len(emitted))
	assert.Contains(t, string(full), "doc-23.md", "the full bundle carries everything the trim left out")
}
