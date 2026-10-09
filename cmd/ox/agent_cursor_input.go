package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/session/cursorpaths"
)

// cursorNativeInput is the normalized, validated part of a Cursor hook
// payload. Raw JSON is normalized separately so agent-specific unknown fields
// survive forwarding to the normal prime path.
type cursorNativeInput struct {
	ConversationID string
	GenerationID   string
	WorkspacePath  string
	SourcePath     string // deterministic expected source, including pending
	SourcePending  bool
	NormalizedRaw  []byte
}

const cursorEmptyPrefixSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
const cursorBoundaryMaxBytes int64 = 64 << 20

// normalizeCursorHookInput validates Cursor's native identity and exactly one
// workspace before prime can use either. It never searches a project directory
// or chooses a transcript by time: the only permitted path is derived from the
// conversation UUID and canonical workspace.
func normalizeCursorHookInput(raw []byte, projectRoot, homeDir string) (*cursorNativeInput, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("missing-native-identity: Cursor hook payload is required")
	}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("hook-input-limit: Cursor hook payload is too large")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("missing-native-identity: invalid Cursor hook payload")
	}
	if raw, ok := fields["is_background_agent"]; ok {
		var background bool
		if json.Unmarshal(raw, &background) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || background {
			return nil, fmt.Errorf("unsupported-scope: Cursor background sessions are not supported")
		}
	}
	conversationID, err := requiredCursorString(fields, "conversation_id")
	if err != nil {
		return nil, err
	}
	if err := cursorpaths.ValidateConversationID(conversationID); err != nil {
		return nil, fmt.Errorf("missing-native-identity: %w", err)
	}
	if sessionID, present, err := optionalCursorString(fields, "session_id"); err != nil {
		return nil, err
	} else if present && sessionID != "" && sessionID != conversationID {
		return nil, fmt.Errorf("identity-conflict: Cursor session_id does not match conversation_id")
	}
	generationID, _, err := optionalCursorString(fields, "generation_id")
	if err != nil {
		return nil, err
	}
	workspaceRoots, ok := fields["workspace_roots"]
	if !ok {
		return nil, fmt.Errorf("workspace-mismatch: Cursor workspace_roots is required")
	}
	var roots []string
	if err := json.Unmarshal(workspaceRoots, &roots); err != nil || len(roots) != 1 || roots[0] == "" {
		return nil, fmt.Errorf("workspace-mismatch: Cursor requires exactly one workspace root")
	}
	workspacePath, err := canonicalCursorWorkspace(projectRoot)
	if err != nil {
		return nil, err
	}
	root, err := canonicalCursorWorkspace(roots[0])
	if err != nil || root != workspacePath {
		return nil, fmt.Errorf("workspace-mismatch: Cursor workspace does not match this project")
	}

	hint, hintPresent, err := optionalNullableCursorString(fields, "transcript_path")
	if err != nil {
		return nil, err
	}
	sourcePath, err := cursorpaths.ValidateSource(homeDir, workspacePath, conversationID, hint)
	if err != nil {
		return nil, fmt.Errorf("invalid-source-path: %w", err)
	}
	_, statErr := os.Stat(sourcePath)
	sourcePending := os.IsNotExist(statErr)
	if statErr != nil && !sourcePending {
		return nil, fmt.Errorf("source-unreadable: validate Cursor transcript")
	}

	// Preserve every unknown payload field while normalizing only the two fields
	// the ordinary host understands. json.RawMessage avoids lossy re-marshaling
	// of nested opaque values.
	fields["session_id"], _ = json.Marshal(conversationID)
	// A direct host call may carry a generic stale hint even when Cursor's
	// native path is absent. Never forward that unrelated hint.
	delete(fields, "session_file_hint")
	if hintPresent && hint != "" {
		fields["session_file_hint"], _ = json.Marshal(sourcePath)
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("missing-native-identity: normalize Cursor hook payload")
	}
	return &cursorNativeInput{
		ConversationID: conversationID,
		GenerationID:   generationID,
		WorkspacePath:  workspacePath,
		SourcePath:     sourcePath,
		SourcePending:  sourcePending,
		NormalizedRaw:  normalized,
	}, nil
}

func requiredCursorString(fields map[string]json.RawMessage, name string) (string, error) {
	value, present, err := optionalCursorString(fields, name)
	if err != nil || !present || value == "" {
		return "", fmt.Errorf("missing-native-identity: Cursor %s is required", name)
	}
	return value, nil
}

func optionalCursorString(fields map[string]json.RawMessage, name string) (string, bool, error) {
	raw, present := fields[name]
	if !present {
		return "", false, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", true, fmt.Errorf("missing-native-identity: Cursor %s must be a string", name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", true, fmt.Errorf("missing-native-identity: Cursor %s must be a string", name)
	}
	return value, true, nil
}

// Cursor emits transcript_path:null before it creates the native export. That
// one nullable field is an established pending-source representation; native
// identities remain strict strings.
func optionalNullableCursorString(fields map[string]json.RawMessage, name string) (string, bool, error) {
	raw, present := fields[name]
	if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false, nil
	}
	return optionalCursorString(fields, name)
}

func canonicalCursorWorkspace(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("workspace-mismatch: Cursor workspace must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("workspace-mismatch: resolve Cursor workspace")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("workspace-mismatch: Cursor workspace is unavailable")
	}
	return filepath.Clean(resolved), nil
}

// cursorBoundaryForHook takes the only allowed source and creates a durable
// start boundary. A missing exact export is intentionally a known byte zero.
// sessionStart may arrive after Cursor has inserted a new turn; it therefore
// starts after the last completed turn instead of sampling that later EOF.
func cursorBoundaryForHook(input *cursorNativeInput, event string) (CursorSourceBoundary, error) {
	if input == nil || input.ConversationID == "" {
		return CursorSourceBoundary{}, fmt.Errorf("missing-native-identity")
	}
	if event == string(agentx.CursorEventBeforeSubmitPrompt) && input.GenerationID == "" {
		return CursorSourceBoundary{}, fmt.Errorf("boundary-unavailable: Cursor generation_id is required for beforeSubmitPrompt")
	}
	boundary := CursorSourceBoundary{
		WorkspacePath: input.WorkspacePath,
		SourcePath:    input.SourcePath,
		SourcePending: input.SourcePending,
		GenerationID:  input.GenerationID,
	}
	if input.SourcePending {
		boundary.KnownZero = true
		boundary.SourcePrefixSHA256 = cursorEmptyPrefixSHA256
		return boundary, nil
	}
	offset, hash, err := cursorReadBoundary(input.SourcePath, event)
	if err != nil {
		return CursorSourceBoundary{}, err
	}
	boundary.Offset = offset
	boundary.KnownZero = offset == 0
	boundary.SourcePrefixSHA256 = hash
	return boundary, nil
}

// Select the offset and its hash from one stable source observation. Hashing a
// second open after choosing an offset could bind an old offset to a replacement.
func cursorReadBoundary(path, event string) (int64, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, "", fmt.Errorf("source-unreadable: open Cursor export boundary")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return 0, "", fmt.Errorf("source-unreadable: Cursor export is not regular")
	}
	if before.Size() > cursorBoundaryMaxBytes {
		return 0, "", fmt.Errorf("source-too-large: Cursor export exceeds boundary limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, cursorBoundaryMaxBytes+1))
	if err != nil {
		return 0, "", fmt.Errorf("source-unreadable: read Cursor export boundary")
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || int64(len(data)) != before.Size() || !before.ModTime().Equal(after.ModTime()) {
		return 0, "", fmt.Errorf("source-changed: Cursor export changed while selecting boundary")
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		return 0, "", fmt.Errorf("boundary-unavailable: Cursor export is incomplete")
	}
	offset := int64(len(data))
	if event == string(agentx.CursorEventSessionStart) {
		offset = 0
		for consumed := 0; consumed < len(data); {
			n := bytes.IndexByte(data[consumed:], '\n')
			line := data[consumed : consumed+n]
			consumed += n + 1
			if len(line) > 10<<20 {
				return 0, "", fmt.Errorf("boundary-unavailable: Cursor export record exceeds boundary limit")
			}
			var row struct {
				Type   string `json:"type"`
				Status string `json:"status"`
				Role   string `json:"role"`
			}
			if err := json.Unmarshal(line, &row); err != nil {
				return 0, "", fmt.Errorf("boundary-unavailable: Cursor export contains an invalid complete row")
			}
			if row.Type == "turn_ended" && row.Status != "" && row.Role == "" {
				offset = int64(consumed)
			}
		}
	}
	hash := sha256.Sum256(data[:offset])
	return offset, hex.EncodeToString(hash[:]), nil
}

func cursorPrefixSHA256(path string, offset int64) (string, error) {
	before, err := os.Stat(path)
	if err != nil || !before.Mode().IsRegular() || offset < 0 || offset > before.Size() || offset > cursorBoundaryMaxBytes {
		return "", fmt.Errorf("source-unreadable: validate Cursor transcript boundary")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("source-unreadable: open Cursor transcript")
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.CopyN(hash, file, offset); err != nil {
		return "", fmt.Errorf("source-unreadable: hash Cursor transcript boundary")
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return "", fmt.Errorf("source-changed: Cursor transcript changed while selecting boundary")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// cursorManualStartBoundary selects the explicit exported EOF for a manual
// Cursor recording. Unlike hook startup it cannot know whether Cursor still
// has an unexported prompt buffered, so it records only the verified file
// boundary and lets the caller state that limit honestly.
func cursorManualStartBoundary(projectRoot, conversationID string) (string, int64, string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", 0, "", fmt.Errorf("source-unreadable: resolve Cursor home directory")
	}
	sourcePath, err := cursorpaths.ValidateSource(homeDir, projectRoot, conversationID, "")
	if err != nil {
		return "", 0, "", fmt.Errorf("invalid-source-path: %w", err)
	}
	if _, err := os.Stat(sourcePath); err != nil {
		if os.IsNotExist(err) {
			return "", 0, "", fmt.Errorf("source-not-found: Cursor transcript is pending")
		}
		return "", 0, "", fmt.Errorf("source-unreadable: stat Cursor transcript")
	}
	offset, hash, err := cursorReadBoundary(sourcePath, "manual")
	if err != nil {
		return "", 0, "", err
	}
	return sourcePath, offset, hash, nil
}
