package daemon

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

func TestActiveAgentIDsNilBeforeHeartbeatHandlerExists(t *testing.T) {
	t.Parallel()
	d := &Daemon{}
	if got := d.activeAgentIDs(); got != nil {
		t.Fatalf("activeAgentIDs = %v, want nil", got)
	}
}

func TestActiveAgentIDsListsAgentsTheHeartbeatSaw(t *testing.T) {
	t.Parallel()
	d := &Daemon{heartbeat: NewHeartbeatHandler(slog.Default())}
	d.heartbeat.Handle("", mustMarshal(HeartbeatPayload{RepoPath: "/repo-a", AgentID: "Oxagent1"}))
	got := d.activeAgentIDs()
	if len(got) != 1 || got[0] != "Oxagent1" {
		t.Fatalf("activeAgentIDs = %v, want [Oxagent1]", got)
	}
}

// A store that dies between lease and confirm must fail the confirm loudly and
// keep the lease: the notes were shown, but nothing may claim they were settled.
func TestTeammateConfirmReportsAStoreThatFailsAfterTheLease(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	e.srv.QueueTeammateComment(srvComment("tcm_1", "x"))
	e.drainOnce()
	resp, err := box.lease(t.Context(), TeammateCommentsTakePayload{AgentID: "agent-a", ConversationID: e.conv(), DeliveredAtSeq: 1})
	if err != nil || resp.LeaseID == "" {
		t.Fatalf("lease = %+v, %v", resp, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	err = box.confirm(t.Context(), TeammateCommentsConfirmPayload{AgentID: "agent-a", LeaseID: resp.LeaseID, CommentIDs: []string{"tcm_1"}})
	if err == nil {
		t.Fatal("confirm on a dead store returned nil")
	}
}

// With no live sessions the receipt folder cannot be found: the confirm still
// settles the note, and the missing ack is logged instead of crashing.
func TestTeammateConfirmWithNoReceiptFolderStillSettlesTheNote(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	e.srv.QueueTeammateComment(srvComment("tcm_1", "x"))
	e.drainOnce()
	resp, err := box.lease(t.Context(), TeammateCommentsTakePayload{AgentID: "agent-a", ConversationID: e.conv(), DeliveredAtSeq: 1})
	if err != nil || len(resp.Entries) != 1 {
		t.Fatalf("lease = %+v, %v", resp, err)
	}
	box.live = func() *LiveSessions { return nil }
	if err := box.confirm(t.Context(), TeammateCommentsConfirmPayload{AgentID: "agent-a", LeaseID: resp.LeaseID, CommentIDs: []string{"tcm_1"}}); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if got, _ := box.registry().PeekShownTeammateComments("agent-a", 5); len(got) != 1 {
		t.Fatalf("shown notes = %v; the unlogged receipt must stay owed", got)
	}
	_ = store
}

// An insert the store refuses is an error, not a silent "stored", and nothing is
// marked relayed: the server's re-send must be able to store the note later.
func TestAddTeammateCommentSurfacesAnInsertFailureAndMarksNothing(t *testing.T) {
	t.Parallel()
	store, side := openStoreWithSideConn(t)
	if _, err := side.Exec(`CREATE TRIGGER refuse_whispers BEFORE INSERT ON whispers BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
	r := NewWhisperRegistry(store, slog.Default())
	entry := whisperstore.WhisperEntry{ID: "tcm_8", Source: whisperstore.SourceTeammateComment, AgentID: "a", CreatedAt: time.Now()}
	if stored, err := r.AddTeammateComment(entry); stored || err == nil {
		t.Fatalf("add = %v, %v; want an error", stored, err)
	}
	if seen, _ := store.IsRelayed("tcm_8", teammateCommentRelayScope); seen {
		t.Fatal("a failed insert was marked relayed: the comment would never be stored")
	}
}

// openStoreWithSideConn returns a store and a second connection to its file, to
// install a fault the store's own API cannot express.
func openStoreWithSideConn(t *testing.T) (*whisperstore.Store, *sql.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "w.db")
	store, err := whisperstore.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	side, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = side.Close() })
	return store, side
}

// The note is stored but cannot be marked relayed: report stored=true WITH the
// error, so the caller does not lose the note and the re-send is still deduped
// by the id check on the next append.
func TestAddTeammateCommentReportsStoredWhenOnlyTheRelayedMarkFails(t *testing.T) {
	t.Parallel()
	store, side := openStoreWithSideConn(t)
	if _, err := side.Exec(`CREATE TRIGGER refuse_relayed BEFORE INSERT ON relayed_murmurs BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
	r := NewWhisperRegistry(store, slog.Default())
	entry := whisperstore.WhisperEntry{ID: "tcm_9", Source: whisperstore.SourceTeammateComment, AgentID: "a", CreatedAt: time.Now()}
	stored, err := r.AddTeammateComment(entry)
	if !stored || err == nil {
		t.Fatalf("add = %v, %v; want stored=true with the mark error", stored, err)
	}
}

// A session folder whose state file cannot be parsed is skipped by the tick; it
// neither opens a stream nor takes the sweep down.
func TestLiveTickSkipsAFolderWithUnreadableState(t *testing.T) {
	e := newLiveEnv(t)
	bad := filepath.Join(e.sessionsDir, "2026-03-04T13-41-person-b-Oxbad")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "stream-state.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.live.Tick(t.Context())
	e.live.Wait()
}

// One failing receipt flush on one session logs once per error, not once per tick.
func TestAckFlushFailureIsReportedOncePerSessionAndError(t *testing.T) {
	t.Parallel()
	var buf lineCounter
	l := &LiveSessions{logger: slog.New(slog.NewTextHandler(&buf, nil))}
	boom := errors.New("receipt log oversized")
	for range 3 {
		l.reportAckFlushFailure(context.Background(), "/s/a", boom)
	}
	if buf.n != 1 {
		t.Fatalf("logged %d lines for one repeated failure, want 1", buf.n)
	}
	l.reportAckFlushFailure(context.Background(), "/s/b", boom)
	l.reportAckFlushFailure(context.Background(), "/s/a", errors.New("different"))
	if buf.n != 3 {
		t.Fatalf("logged %d lines, want 3 (a new session and a new error each log once)", buf.n)
	}
}

type lineCounter struct{ n int }

func (c *lineCounter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			c.n++
		}
	}
	return len(p), nil
}
