package nativeimport

import (
	"fmt"
	"io"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/version"
)

// NativeSourceImport is the native_sessions source recorded for an imported
// session. It is how the next run recognizes the native session as already
// imported even under another name.
const NativeSourceImport = "import"

// RawHeader is what an imported session's raw.jsonl header records.
type RawHeader struct {
	SessionID string // derived ses_ ID
	AgentType string // adapter name: claude-code or codex
	RepoID    string
	Username  string
	NativeID  string
	StartedAt time.Time // first native record
	StoppedAt time.Time // last native activity
}

// NativeSessions is the native_sessions entry an import records.
func (h RawHeader) NativeSessions() []lfs.NativeSession {
	return []lfs.NativeSession{{
		ID:        h.NativeID,
		Source:    NativeSourceImport,
		FirstSeen: h.StartedAt.UTC(),
		LastSeen:  h.StoppedAt.UTC(),
	}}
}

// WriteRaw writes an imported session's raw.jsonl through the strict raw
// writer, the redaction chokepoint live capture uses: a header carrying the
// session's identity, every converted entry in order, and a footer. It returns
// how many entries were written. projectRoot selects the team, repo and user
// REDACT.md rules; a policy the strict writer rejects refuses the write.
func WriteRaw(rawPath, projectRoot string, h RawHeader, raw []adapters.RawEntry) (int, error) {
	entries, err := convertRedactedEntries(projectRoot, raw)
	if err != nil {
		return 0, err
	}
	w, err := session.NewRawSnapshotWriter(rawPath, projectRoot)
	if err != nil {
		return 0, err
	}
	defer w.Close()

	stopped := h.StoppedAt.UTC()
	meta := &session.StoreMeta{
		Version:        "1.0",
		CreatedAt:      h.StartedAt.UTC(),
		SessionID:      h.SessionID,
		AgentType:      h.AgentType,
		Username:       h.Username,
		RepoID:         h.RepoID,
		OxVersion:      version.Version,
		NativeSessions: h.NativeSessions(),
		StoppedAt:      &stopped,
	}
	if err := w.WriteRaw(map[string]any{"type": "header", "metadata": meta}); err != nil {
		return 0, fmt.Errorf("write header: %w", err)
	}
	// One writer for every entry, in order: a credential command's output is
	// redacted only when its call went through the same writer first.
	for i := range entries {
		if err := w.WriteEntry(&entries[i]); err != nil {
			return 0, fmt.Errorf("write entry %d: %w", i, err)
		}
	}
	footer := map[string]any{
		"type":        "footer",
		"closed_at":   stopped.Format(time.RFC3339Nano),
		"entry_count": len(entries),
	}
	if err := w.WriteRaw(footer); err != nil {
		return 0, fmt.Errorf("write footer: %w", err)
	}
	if err := w.CloseAndSync(); err != nil {
		return 0, err
	}
	return len(entries), nil
}

// PreviewEntries returns exactly the entries an import retains, through the
// same conversion, custom rules and command-correlated redaction as WriteRaw.
// The stream writer mutates entries in place; its output is discarded, so
// inspecting a session creates no recording, staging directory or summary.
func PreviewEntries(projectRoot string, raw []adapters.RawEntry) ([]session.Entry, error) {
	entries, err := convertRedactedEntries(projectRoot, raw)
	if err != nil {
		return nil, err
	}
	w, err := session.NewRawStreamWriter(io.Discard, projectRoot)
	if err != nil {
		return nil, err
	}
	defer w.Close()
	for i := range entries {
		if err := w.WriteEntry(&entries[i]); err != nil {
			return nil, fmt.Errorf("redact entry %d: %w", i, err)
		}
	}
	return entries, nil
}

func convertRedactedEntries(projectRoot string, raw []adapters.RawEntry) ([]session.Entry, error) {
	entries := session.ConvertRawEntries(raw)
	redactor, problems := session.NewRedactorWithCustomRules(projectRoot)
	if len(problems) > 0 {
		return nil, fmt.Errorf("invalid redaction policy (%d errors)", len(problems))
	}
	// The writer's pattern layer covers message text only. The hook path runs
	// this pass first so tool input and output get the same rules; so does import.
	redactor.RedactEntries(entries)
	return entries, nil
}
