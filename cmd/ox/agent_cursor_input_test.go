package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/session/cursorpaths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cursorConversationID = "123e4567-e89b-12d3-a456-426614174000"

func cursorInputFixture(t *testing.T, sourceBody string) (raw []byte, projectRoot, homeDir, sourcePath string) {
	t.Helper()
	homeDir = t.TempDir()
	projectRoot = filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.MkdirAll(projectRoot, 0o755))
	sourcePath, err := cursorpaths.SessionPath(homeDir, projectRoot, cursorConversationID)
	require.NoError(t, err)
	if sourceBody != "" {
		require.NoError(t, os.MkdirAll(filepath.Dir(sourcePath), 0o755))
		require.NoError(t, os.WriteFile(sourcePath, []byte(sourceBody), 0o600))
	}
	payload := map[string]any{
		"conversation_id": cursorConversationID,
		"session_id":      cursorConversationID,
		"generation_id":   "generation-a",
		"workspace_roots": []string{projectRoot},
		"transcript_path": sourcePath,
		"opaque":          map[string]any{"nested": []any{true, "value"}},
	}
	raw, err = json.Marshal(payload)
	require.NoError(t, err)
	return raw, projectRoot, homeDir, sourcePath
}

func TestNormalizeCursorHookInput_PreservesOpaqueFieldsAndNormalizesIdentity(t *testing.T) {
	raw, projectRoot, homeDir, sourcePath := cursorInputFixture(t, `{"type":"turn_ended","status":"success"}`+"\n")
	input, err := normalizeCursorHookInput(raw, projectRoot, homeDir)
	require.NoError(t, err)
	require.NotNil(t, input)
	assert.Equal(t, cursorConversationID, input.ConversationID)
	assert.Equal(t, "generation-a", input.GenerationID)
	assert.Equal(t, sourcePath, input.SourcePath)
	assert.False(t, input.SourcePending)

	var normalized map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(input.NormalizedRaw, &normalized))
	var sessionID, hint string
	require.NoError(t, json.Unmarshal(normalized["session_id"], &sessionID))
	require.NoError(t, json.Unmarshal(normalized["session_file_hint"], &hint))
	assert.Equal(t, cursorConversationID, sessionID)
	assert.Equal(t, sourcePath, hint)
	assert.JSONEq(t, `{"nested":[true,"value"]}`, string(normalized["opaque"]))
}

func TestNormalizeCursorHookInput_RejectsCrossChatAndWorkspaceFallbacks(t *testing.T) {
	raw, projectRoot, homeDir, _ := cursorInputFixture(t, "")
	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields))

	fields["session_id"] = "123e4567-e89b-12d3-a456-426614174001"
	conflicting, err := json.Marshal(fields)
	require.NoError(t, err)
	_, err = normalizeCursorHookInput(conflicting, projectRoot, homeDir)
	require.ErrorContains(t, err, "identity-conflict")

	fields["session_id"] = "" // native optional session_id may be present but empty
	emptySessionID, err := json.Marshal(fields)
	require.NoError(t, err)
	_, err = normalizeCursorHookInput(emptySessionID, projectRoot, homeDir)
	require.NoError(t, err)

	fields["session_id"] = cursorConversationID
	fields["workspace_roots"] = []string{projectRoot, filepath.Join(projectRoot, "other")}
	multiRoot, err := json.Marshal(fields)
	require.NoError(t, err)
	_, err = normalizeCursorHookInput(multiRoot, projectRoot, homeDir)
	require.ErrorContains(t, err, "workspace-mismatch")

	fields["workspace_roots"] = []string{projectRoot}
	fields["transcript_path"] = nil
	// Direct host invocations can retain a stale generic hint. Native null is
	// the authoritative pending-source representation and must remove it.
	fields["session_file_hint"] = "/stale/generic.jsonl"
	pendingRaw, err := json.Marshal(fields)
	require.NoError(t, err)
	pending, err := normalizeCursorHookInput(pendingRaw, projectRoot, homeDir)
	require.NoError(t, err)
	assert.True(t, pending.SourcePending)
	var normalized map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(pending.NormalizedRaw, &normalized))
	assert.NotContains(t, normalized, "session_file_hint")
}

func TestCursorBoundaryForHook_UsesLastCompletedTurnAtSessionStart(t *testing.T) {
	first := `{"type":"turn_ended","status":"success"}` + "\n"
	second := `{"role":"user","message":{"content":[]}}` + "\n"
	raw, projectRoot, homeDir, _ := cursorInputFixture(t, first+second)
	input, err := normalizeCursorHookInput(raw, projectRoot, homeDir)
	require.NoError(t, err)

	boundary, err := cursorBoundaryForHook(input, string(agentx.CursorEventSessionStart))
	require.NoError(t, err)
	assert.Equal(t, int64(len(first)), boundary.Offset)
	assert.False(t, boundary.KnownZero)
	assert.NotEmpty(t, boundary.SourcePrefixSHA256)

	promptBoundary, err := cursorBoundaryForHook(input, string(agentx.CursorEventBeforeSubmitPrompt))
	require.NoError(t, err)
	assert.Equal(t, int64(len(first+second)), promptBoundary.Offset)
}

func TestCursorBoundaryForHook_PendingSourceIsKnownZero(t *testing.T) {
	raw, projectRoot, homeDir, _ := cursorInputFixture(t, "")
	input, err := normalizeCursorHookInput(raw, projectRoot, homeDir)
	require.NoError(t, err)
	require.True(t, input.SourcePending)

	boundary, err := cursorBoundaryForHook(input, string(agentx.CursorEventBeforeSubmitPrompt))
	require.NoError(t, err)
	assert.True(t, boundary.SourcePending)
	assert.True(t, boundary.KnownZero)
	assert.Zero(t, boundary.Offset)
	assert.Equal(t, cursorEmptyPrefixSHA256, boundary.SourcePrefixSHA256)
}

func TestCursorBoundaryForHook_PreservesCRLFByteOffsetsAndHash(t *testing.T) {
	old := "{\"type\":\"turn_ended\",\"status\":\"success\"}\r\n"
	newPrompt := "{\"role\":\"user\",\"message\":{\"content\":[]}}\r\n"
	raw, root, home, _ := cursorInputFixture(t, old+newPrompt)
	input, err := normalizeCursorHookInput(raw, root, home)
	require.NoError(t, err)
	boundary, err := cursorBoundaryForHook(input, "sessionStart")
	require.NoError(t, err)
	assert.Equal(t, int64(len(old)), boundary.Offset)
	hash := sha256.Sum256([]byte(old))
	assert.Equal(t, hex.EncodeToString(hash[:]), boundary.SourcePrefixSHA256)
}

func TestCursorBoundaryForHook_SessionStartDoesNotSkipFirstExportedPrompt(t *testing.T) {
	raw, projectRoot, homeDir, _ := cursorInputFixture(t, `{"role":"user","message":{"content":[]}}`+"\n")
	input, err := normalizeCursorHookInput(raw, projectRoot, homeDir)
	require.NoError(t, err)

	boundary, err := cursorBoundaryForHook(input, string(agentx.CursorEventSessionStart))
	require.NoError(t, err)
	assert.Zero(t, boundary.Offset)
	assert.True(t, boundary.KnownZero)
}

func TestNormalizeCursorHookInput_ReboundCapturedFreshStartupFixtures(t *testing.T) {
	projectRoot := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.MkdirAll(projectRoot, 0o755))
	homeDir := t.TempDir()

	// These fixtures preserve the observed native event shape. Rebinding only
	// substitutes fixture aliases with canonical IDs and an isolated workspace,
	// so the test neither consults a desktop export nor relaxes identity checks.
	rebind := func(name, conversationID string) []byte {
		t.Helper()
		path := cursorDesktopHookFixturePath(t, name)
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		var fields map[string]any
		require.NoError(t, json.Unmarshal(data, &fields))
		fields["conversation_id"] = conversationID
		fields["session_id"] = conversationID
		fields["workspace_roots"] = []string{projectRoot}
		// Verify direct prime cannot retain a generic bridge field when the
		// native source is null.
		fields["session_file_hint"] = "/stale/generic.jsonl"
		rebound, err := json.Marshal(fields)
		require.NoError(t, err)
		return rebound
	}

	firstID := "123e4567-e89b-12d3-a456-426614174010"
	before, err := normalizeCursorHookInput(rebind("0001-beforeSubmitPrompt", firstID), projectRoot, homeDir)
	require.NoError(t, err)
	assert.Equal(t, "generation-1", before.GenerationID)
	assert.True(t, before.SourcePending)
	isolateSessionMarkerDir(t)
	_, err = prepareCursorHookBoundary(before, string(agentx.CursorEventBeforeSubmitPrompt))
	require.NoError(t, err)

	start, err := normalizeCursorHookInput(rebind("0002-sessionStart", firstID), projectRoot, homeDir)
	require.NoError(t, err)
	assert.Empty(t, start.GenerationID)
	startBoundary, err := cursorBoundaryForHook(start, string(agentx.CursorEventSessionStart))
	require.NoError(t, err)
	assert.Empty(t, startBoundary.GenerationID)

	marker, err := prepareCursorHookBoundary(start, string(agentx.CursorEventSessionStart))
	require.NoError(t, err)
	assert.Equal(t, "generation-1", marker.CursorSourceBoundary.GenerationID)

	for _, fixture := range []struct {
		name string
		id   string
	}{
		{"0016-sessionStart", "123e4567-e89b-12d3-a456-426614174011"},
		{"0020-sessionStart", "123e4567-e89b-12d3-a456-426614174012"},
	} {
		input, err := normalizeCursorHookInput(rebind(fixture.name, fixture.id), projectRoot, homeDir)
		require.NoError(t, err, fixture.name)
		assert.Empty(t, input.GenerationID, fixture.name)
		boundary, err := cursorBoundaryForHook(input, string(agentx.CursorEventSessionStart))
		require.NoError(t, err, fixture.name)
		assert.True(t, boundary.SourcePending, fixture.name)
	}
}

func cursorDesktopHookFixturePath(t *testing.T, name string) string {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate Cursor desktop fixture")
	}
	path := filepath.Join(filepath.Dir(testFile), "..", "ox-adapter-cursor", "testdata", "desktop", "hooks", name+".stdin.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Cursor desktop fixture %s is unavailable: %v", name, err)
	}
	return path
}
