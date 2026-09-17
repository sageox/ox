package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/sessionprovenance"
	"github.com/stretchr/testify/require"
)

func TestLocalDeletionIntentOfflineBlocksImport(t *testing.T) {
	ledger := t.TempDir()
	store, err := session.OpenStoreReadOnly(ledger)
	require.NoError(t, err)
	dir := store.CacheSessionPath("local-session")
	require.NoError(t, os.MkdirAll(dir, 0700))
	source := sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: "019c6d2e-27b0-798d-aaed-b036114dc63a", Generation: strings.Repeat("a", 64), Ranges: []sessionprovenance.Range{{Start: 0, End: 100}}}
	data, err := json.Marshal(source)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".capture-source.json"), data, 0600))
	require.ErrorContains(t, preserveLocalDeletionIntent(dir, ""), "Ledger unavailable")
	pending, err := pendingLocalDeletion(ledger, "", source.NativeSessionID)
	require.NoError(t, err)
	require.True(t, pending)
	require.FileExists(t, filepath.Join(dir, ".capture-source.json"))
	require.ErrorContains(t, session.CheckCapturePublication(context.Background(), ledger, "local-session", filepath.Join(dir, "raw.jsonl"), &source), "pending local deletion")
	candidate := &importCandidate{}
	candidate.NativeID = source.NativeSessionID
	err = publishImportedSession(context.Background(), "", ledger, importDestination{}, candidate)
	require.ErrorContains(t, err, "pending local deletion")
}

func TestLocalDeletionIntentPublishesBeforeRemoval(t *testing.T) {
	bare, ledger := createBareAndClone(t)
	isolatePushEnv(t, ledger)
	dir := filepath.Join(t.TempDir(), "local-session")
	require.NoError(t, os.MkdirAll(dir, 0700))
	nativeID := "019c6d2e-27b0-798d-aaed-b036114dc63a"
	data, err := json.Marshal(session.RecordingState{AdapterName: "codex", AgentSessionID: nativeID})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), data, 0600))
	require.NoError(t, preserveLocalDeletionIntent(dir, ledger))
	remote := cloneBare(t, bare)
	record, err := session.ReadSourceRecord(remote, nativeID)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.True(t, record.Excludes(0, 100000))
	require.DirExists(t, dir)
}

func TestOfflinePauseIntentCannotBeOverriddenByImport(t *testing.T) {
	ledger := t.TempDir()
	store, err := session.OpenStoreReadOnly(ledger)
	require.NoError(t, err)
	dir := store.CacheSessionPath("paused-capture")
	require.NoError(t, os.MkdirAll(dir, 0700))
	id := "019c6d2e-27b0-798d-aaed-b036114dc63a"
	state := &session.RecordingState{AdapterName: "codex", AgentSessionID: id, SessionPath: dir, PauseCount: 1}
	require.Error(t, excludeNativeCapture(state, "paused"))
	require.FileExists(t, filepath.Join(dir, ".capture-exclusion-pending.json"))
	pending, err := pendingLocalDeletion(ledger, "", id)
	require.NoError(t, err)
	require.True(t, pending)
	// Legacy local state still establishes privacy intent without the new journal.
	require.NoError(t, os.Remove(filepath.Join(dir, ".capture-exclusion-pending.json")))
	b, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), b, 0600))
	pending, err = pendingLocalDeletion(ledger, "", id)
	require.NoError(t, err)
	require.True(t, pending)
	candidate := &importCandidate{}
	candidate.NativeID = id
	require.ErrorContains(t, publishImportedSession(context.Background(), "", ledger, importDestination{}, candidate), "recording exclusion")
}
