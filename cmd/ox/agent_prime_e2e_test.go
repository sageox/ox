//go:build !short

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
