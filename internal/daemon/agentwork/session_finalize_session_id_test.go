package agentwork

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
)

// TestWriteMetaAndUploadLFS_SessionIDPrecedence covers the SessionID
// precedence chain in writeMetaAndUploadLFS (session_finalize.go
// ~lines 1217-1234): lfs.PreservedSessionID(sessionDir) (an existing
// meta.json) beats the raw.jsonl header ID (stored.Meta.SessionID), via
// session.ResolveSessionID(preserved, header); a fresh
// sessionid.GenerateSessionID() is minted only when both are empty.
//
// Each fixture is built the way production builds it, not hand-rolled:
//   - The header ID comes from actually parsing a raw.jsonl file through
//     session.ReadSessionFromPath — the same call ProcessResult makes
//     before invoking writeMetaAndUploadLFS — so a header session_id is
//     exercised through the same sessionid.IsValidSessionID canonical-
//     encoding gate that session.ParseStoreMeta applies in production.
//   - The preserved ID comes from an actual meta.json written via the
//     same lfs.NewSessionMeta/WriteSessionMetaOnly path production uses.
func TestWriteMetaAndUploadLFS_SessionIDPrecedence(t *testing.T) {
	continuedFromSessionID := sessionid.GenerateSessionID()
	tests := []struct {
		name string
		// preservedID, when non-empty, is written into a pre-existing
		// meta.json before writeMetaAndUploadLFS runs (simulating a
		// prior finalize attempt or a CLI-stamped ID).
		preservedID string
		// headerID, when non-empty, is written into the raw.jsonl
		// header's "session_id" key (the start-minted, crash-safe
		// carrier for when .recording.json is already gone).
		headerID string
	}{
		{
			name:        "preserved and header both present, different values: preserved wins",
			preservedID: sessionid.GenerateSessionID(),
			headerID:    sessionid.GenerateSessionID(),
			// Mutation this reds: session.ResolveSessionID(headerID, preservedSessionID)
			// — swapped argument order in writeMetaAndUploadLFS.
		},
		{
			name:     "header only, no meta.json on disk: header wins",
			headerID: sessionid.GenerateSessionID(),
			// Mutation this reds: deleting the `if stored.Meta != nil {
			// headerID = stored.Meta.SessionID }` fallback — headerID would
			// stay "" and fall through to a fresh mint instead.
		},
		{
			name: "neither preserved nor header, legacy raw: fresh mint",
			// Mutation this reds: removing the `if sessionIDForMeta == ""
			// { sessionIDForMeta = sessionid.GenerateSessionID() }`
			// fresh-mint fallback — meta.json would persist an empty SessionID.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewSessionFinalizeHandler(slog.Default())
			handler.skipGit = true
			// skipLFS=false but projectRoot="" — LFS block is skipped at the early-return guard

			sessionName := "2026-06-01T09-00-testuser-Ox7f3a"
			ledgerPath := t.TempDir()
			sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
			if err := os.MkdirAll(sessionDir, 0o755); err != nil {
				t.Fatal(err)
			}

			// raw.jsonl header — parsed below via session.ReadSessionFromPath,
			// the crash-safe carrier read the real finalize path uses. Only a
			// canonical ses_ value in "session_id" survives ParseStoreMeta's
			// sessionid.IsValidSessionID gate into stored.Meta.SessionID.
			sessionIDField := ""
			if tt.headerID != "" {
				sessionIDField = fmt.Sprintf(`,"session_id":%q`, tt.headerID)
			}
			rawContent := fmt.Sprintf(`{"type":"header","metadata":{"version":"1.0","agent_id":"Ox7f3a","agent_type":"claude-code","continued_from_session_id":%q%s}}
{"type":"user","content":"hello","seq":1}
{"type":"assistant","content":"hi","seq":2}
`, continuedFromSessionID, sessionIDField)
			rawPath := filepath.Join(sessionDir, "raw.jsonl")
			if err := os.WriteFile(rawPath, []byte(rawContent), 0o644); err != nil {
				t.Fatal(err)
			}

			// pre-existing meta.json carrying the preserved SessionID, written
			// via the same builder + writer production uses at session start.
			if tt.preservedID != "" {
				priorMeta := lfs.NewSessionMeta(sessionName, "testuser", "Ox7f3a", "claude-code", time.Now()).
					SessionID(tt.preservedID).
					Build()
				if err := lfs.WriteSessionMetaOnly(sessionDir, priorMeta); err != nil {
					t.Fatal(err)
				}
			}

			stored, err := session.ReadSessionFromPath(rawPath)
			if err != nil {
				t.Fatalf("ReadSessionFromPath: %v", err)
			}
			// sanity: confirm the fixture's header actually parsed to the
			// intended headerID before exercising writeMetaAndUploadLFS.
			var gotHeaderID string
			if stored.Meta != nil {
				gotHeaderID = stored.Meta.SessionID
			}
			if gotHeaderID != tt.headerID {
				t.Fatalf("test setup: header parse produced SessionID=%q, want %q", gotHeaderID, tt.headerID)
			}

			summaryResp := &session.SummarizeResponse{Title: "x", Summary: "x"}
			payload := &SessionFinalizePayload{SessionDir: sessionDir, LedgerPath: ledgerPath}

			if _, err := handler.writeMetaAndUploadLFS(payload, stored, summaryResp); err != nil {
				t.Fatalf("writeMetaAndUploadLFS returned unexpected error: %v", err)
			}

			meta, err := lfs.ReadSessionMeta(sessionDir)
			if err != nil {
				t.Fatalf("ReadSessionMeta after writeMetaAndUploadLFS: %v", err)
			}
			if meta.ContinuedFromSessionID != continuedFromSessionID {
				t.Errorf("ContinuedFromSessionID = %q, want %q", meta.ContinuedFromSessionID, continuedFromSessionID)
			}

			switch {
			case tt.preservedID != "":
				if meta.SessionID != tt.preservedID {
					t.Errorf("SessionID = %q, want preserved %q", meta.SessionID, tt.preservedID)
				}
			case tt.headerID != "":
				if meta.SessionID != tt.headerID {
					t.Errorf("SessionID = %q, want header %q", meta.SessionID, tt.headerID)
				}
			default:
				if meta.SessionID == "" {
					t.Fatal("SessionID is empty; expected a freshly minted ses_ ID")
				}
				if !sessionid.IsValidSessionID(meta.SessionID) {
					t.Errorf("SessionID %q is not a valid canonical session ID", meta.SessionID)
				}
			}
		})
	}
}
