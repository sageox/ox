package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/stretchr/testify/require"
)

func TestSessionLogRedactsAtPersistenceBoundary(t *testing.T) {
	_, state := setupLogTest(t)
	const canary = "AKIAIOSFODNN7EXAMPLE"
	inst := &agentinstance.Instance{AgentID: "OxLog1"}
	require.NoError(t, runAgentSessionLog(io.Discard, inst, []string{"--role", "user", "--content", canary}))
	require.NoError(t, runAgentSessionLog(io.Discard, inst, []string{"--role", "tool", "--tool-name", "bash", "--tool-input", "echo " + canary, "--content", "tool result " + canary}))
	path := sessionLogTargetFile(state)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	if strings.Contains(string(data), canary) {
		t.Fatal("session log persisted a supplied synthetic credential")
	}
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	entries := readJSONLEntries(t, path)
	require.Len(t, entries, 2)
	require.Equal(t, float64(0), entries[0]["seq"])
	require.Equal(t, float64(1), entries[1]["seq"])
	require.Equal(t, "user", entries[0]["type"])
	require.Equal(t, "bash", entries[1]["tool_name"])
	require.NotEmpty(t, entries[0]["eid"])
	// The generic adapter consumes this native framing; redaction cannot wrap
	// entries or replace field names to establish its persistence boundary.
	require.NotEmpty(t, entries[0]["timestamp"])
}

func TestSessionLogEnforcesProjectPolicyBeforeAppend(t *testing.T) {
	root, state := setupLogTest(t)
	const private = "synthetic-private-log-content"
	policyPath := filepath.Join(root, ".sageox", "REDACT.md")
	require.NoError(t, os.WriteFile(policyPath, []byte("```redact\nliteral \""+private+"\" -> [REDACTED_PROJECT]\n```\n"), 0600))
	inst := &agentinstance.Instance{AgentID: "OxLog1"}
	require.NoError(t, runAgentSessionLog(io.Discard, inst, []string{"--role", "user", "--content", private}))
	path := sessionLogTargetFile(state)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	if strings.Contains(string(before), private) {
		t.Fatal("session log bypassed its project redaction rule")
	}
	require.NoError(t, os.WriteFile(policyPath, []byte("```redact\nregex \"[\" -> [REDACTED_INVALID]\n```\n"), 0600))
	require.Error(t, runAgentSessionLog(io.Discard, inst, []string{"--role", "user", "--content", "must not be appended"}))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
}
