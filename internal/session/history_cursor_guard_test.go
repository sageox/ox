package session

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreCapturedHistoryRejectsActiveCursorBeforeAnyMutation(t *testing.T) {
	projectRoot := setupRecordingTest(t, t.TempDir())
	original, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(projectRoot))
	t.Cleanup(func() { require.NoError(t, os.Chdir(original)) })

	native := []byte(`{"role":"user","message":{"content":[{"type":"text","text":"native"}]}}` + "\n")
	nativePath := filepath.Join(t.TempDir(), "cursor-native.jsonl")
	require.NoError(t, os.WriteFile(nativePath, native, 0o600))
	hash := sha256.Sum256(native)
	state, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID:            "OxCursorHistory",
		AgentSessionID:     "123e4567-e89b-12d3-a456-426614174099",
		AdapterName:        "cursor",
		SessionFile:        nativePath,
		WorkspacePath:      projectRoot,
		WatchMode:          "tail",
		StartOffset:        int64(len(native)),
		StartOffsetKnown:   true,
		SourcePrefixSHA256: hex.EncodeToString(hash[:]),
	})
	require.NoError(t, err)
	rawPath := filepath.Join(state.SessionPath, rawFilename)
	raw := []byte(`{"type":"assistant","content":"already captured","seq":1}` + "\n")
	require.NoError(t, os.WriteFile(rawPath, raw, 0o600))
	require.NoError(t, UpdateRecordingStateForAgent(projectRoot, state.AgentID, func(current *RecordingState) {
		current.EntryCount = 1
	}))
	before, err := LoadRecordingStateForAgent(projectRoot, state.AgentID)
	require.NoError(t, err)
	require.NotNil(t, before)

	history := validCursorGuardHistory(state.AgentID)
	_, err = StoreCapturedHistory(history, state.AgentID, true)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrHistoryStorageFailed)
	assert.Contains(t, strings.ToLower(err.Error()), "unsupported")
	assert.Contains(t, strings.ToLower(err.Error()), "cursor")

	afterNative, err := os.ReadFile(nativePath)
	require.NoError(t, err)
	assert.Equal(t, native, afterNative)
	afterRaw, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	assert.Equal(t, raw, afterRaw)
	after, err := LoadRecordingStateForAgent(projectRoot, state.AgentID)
	require.NoError(t, err)
	require.NotNil(t, after)
	assert.Equal(t, before.EntryCount, after.EntryCount)
	assert.Equal(t, before.StartOffset, after.StartOffset)
	assert.Equal(t, before.SourceOffset, after.SourceOffset)
	assert.Equal(t, before.SourcePrefixSHA256, after.SourcePrefixSHA256)
}

func TestStoreCapturedHistoryStillMergesWithActiveGenericRecording(t *testing.T) {
	projectRoot := setupRecordingTest(t, t.TempDir())
	original, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(projectRoot))
	t.Cleanup(func() { require.NoError(t, os.Chdir(original)) })
	state, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxGenericHistory", AdapterName: "generic",
	})
	require.NoError(t, err)
	rawPath := filepath.Join(state.SessionPath, rawFilename)
	require.NoError(t, os.WriteFile(rawPath, []byte(`{"type":"assistant","content":"current","seq":1}`+"\n"), 0o600))

	stored, err := StoreCapturedHistory(validCursorGuardHistory(state.AgentID), state.AgentID, true)
	require.NoError(t, err)
	assert.Equal(t, rawPath, stored)
	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "prior planning")
	assert.Contains(t, string(data), "current")
}

func validCursorGuardHistory(agentID string) *CapturedHistory {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	return &CapturedHistory{
		Meta: &HistoryMeta{
			SchemaVersion: HistorySchemaVersion, CapturedAt: now,
			Source: "agent_reconstruction", AgentID: agentID,
		},
		Entries: []HistoryEntry{{Seq: 1, Type: "user", Content: "prior planning", Timestamp: now}},
	}
}
