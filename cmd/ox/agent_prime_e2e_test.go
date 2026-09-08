//go:build !short

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/pkg/adapterprotocol"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initializedE2E gives a fully initialized project — the real user journey, so
// prime runs against state ox itself produced rather than a hand-built fixture.
func initializedE2E(t *testing.T) *oxE2E {
	t.Helper()
	env := newOxE2E(t)
	withInitFlags(t, env.TeamID)
	require.NoError(t, runInit(), "harness setup: ox init must succeed")
	return env
}

// TestRunAgentPrime_RunsEndToEndAndRecordsSkillTiming drives runAgentPrime end
// to end for the first time — every prior prime test asserted on helpers and
// explicitly "simulated the check in runAgentPrime"; nothing ran the function.
//
// Scope note, learned from a red-first run: this test does NOT prove the skill
// reconcile happened. The skills_reconcile timing key is written
// unconditionally, outside the branch, so it survives even when the reconcile
// call is deleted — a neutered call site still passed this test. That gate
// lives in TestRunAgentPrime_ReconcilesWhenTheRecordedRevisionIsStale, which
// does go red. What this one prevents is a prime that cannot complete at all
// against a freshly initialized repo.
func TestRunAgentPrime_RunsEndToEndAndRecordsSkillTiming(t *testing.T) {
	initializedE2E(t)

	var buf bytes.Buffer
	cmd := agentPrimeCmd
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	require.NoError(t, cmd.Flags().Set("agent", "claude-code"))
	require.NoError(t, cmd.Flags().Set("format", "json"))
	t.Cleanup(func() {
		_ = cmd.Flags().Set("agent", "")
		_ = cmd.Flags().Set("format", "")
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})

	require.NoError(t, runAgentPrime(cmd, nil), "prime must succeed in an initialized repo")

	out := buf.String()
	require.NotEmpty(t, out, "prime must produce output")

	// The reconcile is on the hot path, so it always reports its cost — that
	// timing key is the observable proof the call site ran.
	assert.Contains(t, out, "skills_reconcile",
		"prime must record skill-inventory timing on every run")
	assert.Contains(t, out, "\"agent_id\"",
		"prime must issue an agent identity — without it every downstream ox command fails")
}

// TestRunAgentPrime_ReconcilesWhenTheRecordedRevisionIsStale covers the branch
// prime takes after an ox upgrade: the binary's catalog no longer matches what
// this checkout materialized, so prime rebuilds the inventory and reports how
// many files it wrote.
//
// Deliberately NOT tested here: repairing a file someone deleted while the
// recorded revision still matches. Prime's compare is a revision check, not a
// filesystem scan, so it cannot see that — the daemon's skills-inventory-drift
// tick owns it. Asserting it here would be asserting a behavior ox does not
// have and does not want on the session hot path.
//
// Failure prevented: an agent priming against skills from an older ox after an
// upgrade, with no error anywhere, because the reconcile stopped running.
func TestRunAgentPrime_ReconcilesWhenTheRecordedRevisionIsStale(t *testing.T) {
	env := initializedE2E(t)

	// Select a skills target. Adapter binaries are not on PATH in a test, so
	// init records none — and prime deliberately refuses to install into a repo
	// that never selected one. This is the seed a real `ox init` performs when
	// the Claude Code adapter is present.
	_, err := reconcileSelectedSkills(env.Root, []adapterprotocol.SkillTarget{{
		Key: "claude-project", Root: ".claude/skills",
		Format: adapterprotocol.SkillFormatAgentSkillsV1,
		Scope:  adapterprotocol.SkillScopeProject,
	}})
	require.NoError(t, err, "harness setup: selecting a skills target must succeed")

	// Find a materialized skill and remove it, then age the recorded revision so
	// prime's compare actually fires. Together these are the post-upgrade shape.
	victim := ""
	skillsRoot := filepath.Join(env.Root, ".claude", "skills")
	entries, err := os.ReadDir(skillsRoot)
	require.NoError(t, err, "selecting a target must have materialized skills")
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "ox-cli-") {
			victim = filepath.Join(skillsRoot, e.Name(), "SKILL.md")
			break
		}
	}
	require.NotEmpty(t, victim, "expected at least one materialized ox-cli-* skill")
	require.NoError(t, os.Remove(victim))

	statePath := skillmanager.StatePath(env.Root)
	state, err := os.ReadFile(statePath)
	require.NoError(t, err, "selecting a target must have written machine-local state")
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(state, &parsed))
	source, ok := parsed["source"].(map[string]any)
	require.True(t, ok, "state must record its source")
	source["revision"] = "stale-revision-from-an-older-ox"
	rewritten, err := json.Marshal(parsed)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath, rewritten, 0o600))

	var buf bytes.Buffer
	cmd := agentPrimeCmd
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	require.NoError(t, cmd.Flags().Set("agent", "claude-code"))
	require.NoError(t, cmd.Flags().Set("format", "json"))
	t.Cleanup(func() {
		_ = cmd.Flags().Set("agent", "")
		_ = cmd.Flags().Set("format", "")
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})

	require.NoError(t, runAgentPrime(cmd, nil))

	assert.FileExists(t, victim,
		"a stale recorded revision must make prime rebuild the inventory, restoring the missing skill")
	assert.Contains(t, buf.String(), "skills_reconciled",
		"prime must report how many files it wrote when the reconcile actually did work")
}

// A source created after prime must join the existing recording, including after a state-write failure.
func TestPrimeCodexRecording_ReprimeDiscoversDelayedSource(t *testing.T) {
	adapterBin := buildCodexCaptureAdapter(t)
	for _, readOnlyState := range []bool{false, true} {
		name := "writable state"
		if readOnlyState {
			name = "state write recovers"
		}
		t.Run(name, func(t *testing.T) {
			if readOnlyState && os.Geteuid() == 0 {
				t.Skip("root can write files despite read-only permissions")
			}
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
			t.Setenv("OX_XDG_DISABLE", "")
			f := newDraftLedgerFixture(t)
			t.Chdir(f.projectRoot)
			defaultLedger, err := ledger.DefaultPath()
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Dir(defaultLedger), 0o755))
			runGit(t, f.projectRoot, "clone", "--quiet", f.barePath, defaultLedger)
			oldCfg := cfg
			cfg = &config.Config{}
			t.Cleanup(func() { cfg = oldCfg })
			oxConfigSetRepo(t, "session_recording", "auto")
			adapter, err := adapters.NewExternalAdapter(adapterBin)
			require.NoError(t, err)
			adapters.Register(adapter)
			t.Cleanup(func() {
				adapters.Unregister("codex")
				_ = adapter.Close()
			})

			const agentID, nativeID = "OxCodexLate", "codex-test-session"
			firstStatus := startSessionRecording(f.projectRoot, agentID, "codex", "", "", "")
			require.NotNil(t, firstStatus)
			require.True(t, firstStatus.Recording)
			first, err := session.LoadRecordingStateForAgent(f.projectRoot, agentID)
			require.NoError(t, err)
			require.NotNil(t, first)
			require.Equal(t, "tail", first.WatchMode)
			require.Empty(t, first.SessionFile)
			rawBefore, err := os.ReadFile(filepath.Join(first.SessionPath, "raw.jsonl"))
			require.NoError(t, err)
			markerPath := filepath.Join(first.SessionPath, ".recording.json")
			if readOnlyState {
				require.NoError(t, os.Chmod(markerPath, 0o400))
				t.Cleanup(func() { _ = os.Chmod(markerPath, 0o600) })
			}

			// Learn the native ID while its file is still unavailable. Keep it for daemon discovery.
			require.NotNil(t, startSessionRecording(f.projectRoot, agentID, "codex", "", "", nativeID))
			waiting, err := session.LoadRecordingStateForAgent(f.projectRoot, agentID)
			require.NoError(t, err)
			require.NotNil(t, waiting)
			if readOnlyState {
				assert.Empty(t, waiting.AgentSessionID, "failed state writes must leave the prior recording intact")
			} else {
				assert.Equal(t, nativeID, waiting.AgentSessionID)
				// A subsequent environment without a native ID must not erase the saved one.
				require.NotNil(t, startSessionRecording(f.projectRoot, agentID, "codex", "", "", ""))
			}
			assert.Empty(t, waiting.SessionFile)
			assert.Equal(t, first.SessionID, waiting.SessionID)

			source := writeCodexSessionFile(t, os.Getenv("HOME"), f.projectRoot)
			beforePrime := first.StartedAt.Add(-time.Minute)
			require.NoError(t, os.Chtimes(source, beforePrime, beforePrime))
			data, err := os.ReadFile(source)
			require.NoError(t, err)
			sibling := filepath.Join(filepath.Dir(source), "newer-sibling.jsonl")
			require.NoError(t, os.WriteFile(sibling, []byte(strings.ReplaceAll(string(data), nativeID, "sibling-session")), 0o600))

			foundStatus := startSessionRecording(f.projectRoot, agentID, "codex", "", "", nativeID)
			require.NotNil(t, foundStatus)
			assert.Equal(t, source, foundStatus.File)
			if readOnlyState {
				pending, err := session.LoadRecordingStateForAgent(f.projectRoot, agentID)
				require.NoError(t, err)
				require.NotNil(t, pending)
				assert.Empty(t, pending.SessionFile, "discovery must preserve a recording when its source path cannot yet be saved")
				require.NoError(t, os.Chmod(markerPath, 0o600))
				require.NotNil(t, startSessionRecording(f.projectRoot, agentID, "codex", "", "", nativeID))
			}
			found, err := session.LoadRecordingStateForAgent(f.projectRoot, agentID)
			require.NoError(t, err)
			require.NotNil(t, found)
			assert.Equal(t, nativeID, found.AgentSessionID)
			assert.Equal(t, source, found.SessionFile)
			assert.Equal(t, first.SessionID, found.SessionID, "re-prime must recover the existing recording")
			rawAfter, err := os.ReadFile(filepath.Join(found.SessionPath, "raw.jsonl"))
			require.NoError(t, err)
			assert.Equal(t, rawBefore, rawAfter, "discovery must not replace the recording's captured data")
		})
	}
}
