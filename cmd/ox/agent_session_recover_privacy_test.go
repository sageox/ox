//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/require"
)

func TestCachedRecoveryRedactsLegacyRawBeforeCopy(t *testing.T) {
	f := newRecoverCollisionFixture(t, collisionRecoveredID, nil)
	const canary = "AKIAIOSFODNN7EXAMPLE"
	rawPath := filepath.Join(f.sessionPath, ledgerFileRaw)
	legacy := strings.ReplaceAll(string(f.recoveredRaw), "please recover the original name", canary)
	require.NoError(t, os.WriteFile(rawPath, []byte(legacy), 0600))
	state, err := session.LoadRecordingStateForAgent(f.projectRoot, "OxRcvr")
	require.NoError(t, err)
	output, err := publishCachedRecording(&agentinstance.Instance{AgentID: "OxRcvr"}, f.projectRoot, state, rawPath)
	require.NoError(t, err)
	if strings.Contains(output.SummaryPrompt, canary) {
		t.Fatal("cached recovery exposed a supplied synthetic credential in its returned prompt")
	}
	destination := filepath.Join(f.ledgerSessionDir, ledgerFileRaw)
	data, err := os.ReadFile(destination)
	require.NoError(t, err)
	if strings.Contains(string(data), canary) {
		t.Fatal("cached recovery copied a supplied synthetic credential into the ledger")
	}
	info, err := os.Stat(destination)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	entries := readJSONLEntries(t, destination)
	require.Len(t, entries, 4)
	require.Equal(t, "header", entries[0]["type"])
	require.Equal(t, "user", entries[1]["type"])
	require.Equal(t, "recovered", entries[2]["content"])
	require.Equal(t, "footer", entries[3]["type"])
	// Recovery preserves its source; only the newly persisted copy is rewritten.
	source, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	require.Equal(t, legacy, string(source))
}

func TestCachedRecoveryGuardedSnapshotPolicyAndRollback(t *testing.T) {
	const private = "synthetic-private-recovery-content"
	for _, mode := range []string{"custom policy", "invalid policy", "invalid JSONL"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox"), 0755))
			policy := "```redact\nliteral \"" + private + "\" -> [REDACTED_PROJECT]\n```\n"
			if mode == "invalid policy" {
				policy = "```redact\nregex \"[\" -> [REDACTED_INVALID]\n```\n"
			}
			require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "REDACT.md"), []byte(policy), 0600))
			dir := t.TempDir()
			source := filepath.Join(t.TempDir(), ledgerFileRaw)
			destination := filepath.Join(dir, ledgerFileRaw)
			const original = "{\"type\":\"user\",\"content\":\"prior safe recording\"}\n"
			require.NoError(t, os.WriteFile(destination, []byte(original), 0600))
			raw := "{\"type\":\"header\",\"metadata\":{\"model\":\"" + private + "\"}}\n{\"type\":\"user\",\"content\":\"" + private + "\",\"seq\":9007199254740993,\"unknown\":{\"safe\":\"preserved\"}}\n{\"type\":\"footer\",\"entry_count\":1}\n"
			if mode == "invalid JSONL" {
				raw += "{invalid trailing recording\n"
			}
			require.NoError(t, os.WriteFile(source, []byte(raw), 0600))
			err := copyRecoveredRawSession(source, destination, root)
			data, readErr := os.ReadFile(destination)
			require.NoError(t, readErr)
			if mode != "custom policy" {
				require.Error(t, err)
				require.Equal(t, original, string(data))
			} else {
				require.NoError(t, err)
				if strings.Contains(string(data), private) {
					t.Fatal("recovered snapshot bypassed its project policy")
				}
				require.Contains(t, string(data), "9007199254740993", "redaction must preserve integer identity")
				require.Contains(t, string(data), `"safe":"preserved"`)
				require.Len(t, readJSONLEntries(t, destination), 3)
			}
			files, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Len(t, files, 1, "recovery must not retain a partial snapshot")
		})
	}
}

func TestCachedRecoveryMalformedPolicyPreservesRecording(t *testing.T) {
	f := newRecoverCollisionFixture(t, collisionRecoveredID, nil)
	policyPath := filepath.Join(f.projectRoot, ".sageox", "REDACT.md")
	require.NoError(t, os.WriteFile(policyPath, []byte("```redact\nregex \"[\" -> [REDACTED_INVALID]\n```\n"), 0600))
	state, err := session.LoadRecordingStateForAgent(f.projectRoot, "OxRcvr")
	require.NoError(t, err)
	output, err := publishCachedRecording(&agentinstance.Instance{AgentID: "OxRcvr"}, f.projectRoot, state, filepath.Join(f.sessionPath, ledgerFileRaw))
	require.Error(t, err)
	require.Nil(t, output)
	survivor, err := session.LoadRecordingStateForAgent(f.projectRoot, "OxRcvr")
	require.NoError(t, err)
	require.NotNil(t, survivor)
	require.Equal(t, state.SessionID, survivor.SessionID)
	require.NoFileExists(t, filepath.Join(f.ledgerSessionDir, ledgerFileRaw))
}
