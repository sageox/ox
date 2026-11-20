package daemon

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/sessionstream"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// take is lease then confirm of everything leased: what one hook does when it
// shows every note. Tests of the two phases call lease and confirm themselves.
func (b *teammateCommentInbox) take(ctx context.Context, p TeammateCommentsTakePayload) ([]whisperstore.WhisperEntry, error) {
	resp, err := b.lease(ctx, p)
	if err != nil || resp.LeaseID == "" {
		return nil, err
	}
	ids := make([]string, len(resp.Entries))
	for i, e := range resp.Entries {
		ids[i] = e.ID
	}
	if err := b.confirm(ctx, TeammateCommentsConfirmPayload{AgentID: p.AgentID, LeaseID: resp.LeaseID, CommentIDs: ids}); err != nil {
		return nil, err
	}
	return resp.Entries, nil
}

func newCommentEnv(t *testing.T, recipient string) (*liveEnv, *teammateCommentInbox, *whisperstore.Store) {
	t.Helper()
	e := newLiveEnv(t)
	store, err := whisperstore.Open(filepath.Join(t.TempDir(), "whisper.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reg := NewWhisperRegistry(store, slog.Default())
	box := &teammateCommentInbox{
		logger: slog.Default(), registry: func() *WhisperRegistry { return reg },
		live:         func() *LiveSessions { return e.live },
		recipientFor: func(string) string { return recipient },
		now:          func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
	}
	e.live.onTeammateComments = box.receive
	e.spool(lineN(1) + lineN(2))
	return e, box, store
}

func (e *liveEnv) drainOnce() {
	e.t.Helper()
	e.live.Drain(e.t.Context(), e.dir)
	e.live.Wait()
}

func srvComment(id, body string) sessionstream.TeammateComment {
	return sessionstream.TeammateComment{ID: id, AuthorName: "Person A", Body: body}
}

// A note addressed to one agent must be invisible to every other reader and to
// the broadcast path, and delivered exactly once with its ack logged.
func TestTeammateCommentGoesToTheRecipientOnceAndIsAckedAtTheSeq(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	e.srv.QueueTeammateComment(srvComment("tcm_1", "look at the cache"))
	e.drainOnce()

	for _, agent := range []string{"agent-a", "agent-b"} {
		if got, _ := store.GetWhispers(agent, whisperstore.AttentionAll, nil); len(got) != 0 {
			t.Fatalf("broadcast reader for %s returned %v", agent, got)
		}
	}
	if other, err := box.take(t.Context(), TeammateCommentsTakePayload{AgentID: "agent-b", ConversationID: e.state().ConversationID, DeliveredAtSeq: 9}); err != nil || len(other) != 0 {
		t.Fatalf("another agent took %v, %v", other, err)
	}

	got, err := box.take(t.Context(), TeammateCommentsTakePayload{AgentID: "agent-a", ConversationID: e.state().ConversationID, DeliveredAtSeq: 9})
	if err != nil || len(got) != 1 || got[0].Content != "look at the cache" || got[0].Metadata["author_name"] != "Person A" {
		t.Fatalf("take = %+v, %v", got, err)
	}
	e.live.Wait() // the take kicked a drain that sends the ack
	if seq, ok := e.srv.DeliveredAtSeq("tcm_1"); !ok || seq != 9 {
		t.Fatalf("server delivered_at_seq = %d, %v; want 9", seq, ok)
	}
	if again, _ := box.take(t.Context(), TeammateCommentsTakePayload{AgentID: "agent-a", ConversationID: e.state().ConversationID, DeliveredAtSeq: 10}); len(again) != 0 {
		t.Fatalf("second take returned %v", again)
	}
}

// The server re-sends an undelivered comment on every append until its ack
// lands; taking deletes the stored note, so only the relayed record keeps the
// re-send from becoming a second note.
func TestTeammateCommentResentAfterTakeBeforeAckIsNotStoredAgain(t *testing.T) {
	e, box, _ := newCommentEnv(t, "agent-a")
	e.srv.QueueTeammateComment(srvComment("tcm_1", "once"))
	e.drainOnce()
	e.srv.RefuseNextAck(http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry)
	if got, _ := box.take(t.Context(), TeammateCommentsTakePayload{AgentID: "agent-a", ConversationID: e.state().ConversationID, DeliveredAtSeq: 4}); len(got) != 1 {
		t.Fatalf("first take = %v", got)
	}
	e.live.Wait()
	if _, ok := e.srv.DeliveredAtSeq("tcm_1"); ok {
		t.Fatal("setup: the ack landed")
	}

	e.spool(lineN(3))
	e.clock = e.clock.Add(time.Minute)
	e.drainOnce() // server re-sends tcm_1 on this append
	if again, _ := box.take(t.Context(), TeammateCommentsTakePayload{AgentID: "agent-a", ConversationID: e.state().ConversationID, DeliveredAtSeq: 5}); len(again) != 0 {
		t.Fatalf("a re-sent comment was stored and shown again: %v", again)
	}
}

func TestTeammateCommentWithNoRecipientIsLeftForTheServerToResend(t *testing.T) {
	e, box, store := newCommentEnv(t, "")
	e.srv.QueueTeammateComment(srvComment("tcm_1", "x"))
	e.drainOnce()
	if got, _ := store.TakeTeammateComments("", 5); len(got) != 0 {
		t.Fatalf("stored for nobody: %v", got)
	}
	box.recipientFor = func(string) string { return "agent-a" }
	e.spool(lineN(3))
	e.clock = e.clock.Add(time.Minute)
	e.drainOnce()
	if got, _ := box.take(t.Context(), TeammateCommentsTakePayload{AgentID: "agent-a", ConversationID: e.state().ConversationID, DeliveredAtSeq: 3}); len(got) != 1 {
		t.Fatalf("a comment skipped for want of a recipient was not delivered once one existed: %v", got)
	}
}

func (e *liveEnv) conv() string { return e.state().ConversationID }

func addPending(t *testing.T, store *whisperstore.Store, id, agent string, meta map[string]string) {
	t.Helper()
	meta["comment_id"] = id
	if err := store.Add(whisperstore.WhisperEntry{
		ID: id, Scope: "ledger", Type: whisperstore.WhisperTrigger, Source: whisperstore.SourceTeammateComment,
		Topic: "teammate-comment", Content: "x", Importance: whisperstore.ImportanceNormal, CreatedAt: time.Now(),
		AgentID: agent, Metadata: meta,
	}); err != nil {
		t.Fatal(err)
	}
}

func stillPending(t *testing.T, store *whisperstore.Store, agent string) []string {
	t.Helper()
	got, err := store.PeekTeammateComments(agent, 20)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	return ids
}

// Entry metadata is not trusted input: a folder outside the sessions cache
// must never receive an ack file, and the note it names stays pending (it was
// never shown, so it was never consumed).
func TestTeammateCommentAckNeverWritesOutsideTheSessionsDir(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	elsewhere := t.TempDir()
	addPending(t, store, "tcm_evil", "agent-a", map[string]string{"session_dir": elsewhere, "conversation_id": e.conv()})
	got, err := box.take(t.Context(), TeammateCommentsTakePayload{AgentID: "agent-a", ConversationID: e.conv(), DeliveredAtSeq: 2})
	if err != nil || len(got) != 0 {
		t.Fatalf("take = %v, %v; a note with no ack path must not be shown", got, err)
	}
	e.live.Wait()
	if _, err := os.Stat(filepath.Join(elsewhere, "teammate-comment-acks")); err == nil {
		t.Fatal("an ack file was written outside the sessions dir")
	}
	if ids := stillPending(t, store, "agent-a"); len(ids) != 1 {
		t.Fatalf("pending = %v, want tcm_evil kept for a retry", ids)
	}
}

// Failure prevented: a note shown (and so consumed) although its ack could not
// be written, leaving the teammate's receipt waiting forever.
func TestTeammateCommentStaysPendingWhenItsAckCannotBeWritten(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	e.srv.QueueTeammateComment(srvComment("tcm_1", "x"))
	e.drainOnce()
	if err := os.Mkdir(filepath.Join(e.dir, "teammate-comment-acks"), 0o700); err != nil { // the log path is unusable
		t.Fatal(err)
	}
	got, err := box.take(t.Context(), TeammateCommentsTakePayload{AgentID: "agent-a", ConversationID: e.conv(), DeliveredAtSeq: 4})
	if err != nil || len(got) != 0 {
		t.Fatalf("take = %v, %v; a note whose ack cannot be logged must not be shown", got, err)
	}
	if ids := stillPending(t, store, "agent-a"); len(ids) != 1 || ids[0] != "tcm_1" {
		t.Fatalf("pending = %v, want tcm_1 kept", ids)
	}
	if err := os.Remove(filepath.Join(e.dir, "teammate-comment-acks")); err != nil {
		t.Fatal(err)
	}
	if got, _ := box.take(t.Context(), TeammateCommentsTakePayload{AgentID: "agent-a", ConversationID: e.conv(), DeliveredAtSeq: 5}); len(got) != 1 {
		t.Fatalf("the retry did not show the note: %v", got)
	}
}

// Failure prevented: this session acking (and showing) a note sent to another
// conversation, such as an earlier recording by the same agent.
func TestTeammateCommentOfAnotherConversationIsDroppedNeverAcked(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	addPending(t, store, "tcm_old", "agent-a", map[string]string{"session_dir": e.dir, "conversation_id": "cnv_other"})
	got, err := box.take(t.Context(), TeammateCommentsTakePayload{AgentID: "agent-a", ConversationID: e.conv(), DeliveredAtSeq: 3})
	if err != nil || len(got) != 0 {
		t.Fatalf("take = %v, %v", got, err)
	}
	e.live.Wait()
	if _, err := os.Stat(filepath.Join(e.dir, "teammate-comment-acks")); err == nil {
		t.Fatal("this session logged an ack for another conversation's note")
	}
	if ids := stillPending(t, store, "agent-a"); len(ids) != 0 {
		t.Fatalf("pending = %v; the foreign note must be dropped", ids)
	}
}

// Failure prevented: a note the owner opted out of (the server stopped listing
// it) still reaching the agent because it was received earlier.
func TestTeammateCommentOptedOutAfterReceiptIsNotShown(t *testing.T) {
	e, box, _ := newCommentEnv(t, "agent-a")
	e.srv.QueueTeammateComment(srvComment("tcm_1", "pending"))
	e.drainOnce() // received
	e.srv.RemoveTeammateComment("tcm_1")
	e.spool(lineN(3))
	e.clock = e.clock.Add(time.Minute)
	e.drainOnce() // the server now lists nothing
	if got, _ := box.take(t.Context(), TeammateCommentsTakePayload{AgentID: "agent-a", ConversationID: e.conv(), DeliveredAtSeq: 3}); len(got) != 0 {
		t.Fatalf("a retracted note was shown: %v", got)
	}
}

// Failure prevented: a deleted note shown, or its siblings lost with it.
func TestTeammateCommentDeletedAfterReceiptIsNotShownButSiblingsAre(t *testing.T) {
	e, box, _ := newCommentEnv(t, "agent-a")
	e.srv.QueueTeammateComment(srvComment("tcm_1", "deleted later"))
	e.srv.QueueTeammateComment(srvComment("tcm_2", "kept"))
	e.drainOnce()
	e.srv.RemoveTeammateComment("tcm_1")
	e.spool(lineN(3))
	e.clock = e.clock.Add(time.Minute)
	e.drainOnce()
	got, _ := box.take(t.Context(), TeammateCommentsTakePayload{AgentID: "agent-a", ConversationID: e.conv(), DeliveredAtSeq: 3})
	if len(got) != 1 || got[0].ID != "tcm_2" {
		t.Fatalf("take = %v, want only tcm_2", got)
	}
}

func TestTeammateCommentTakeRefusesAnUnusableSeq(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	addPending(t, store, "t", "agent-a", map[string]string{"conversation_id": e.conv()})
	if _, err := box.take(t.Context(), TeammateCommentsTakePayload{AgentID: "agent-a", ConversationID: e.conv(), DeliveredAtSeq: 0}); err == nil {
		t.Fatal("take accepted seq 0")
	}
	if _, err := box.take(t.Context(), TeammateCommentsTakePayload{AgentID: "agent-a", DeliveredAtSeq: 3}); err == nil {
		t.Fatal("take accepted no conversation")
	}
	if ids := stillPending(t, store, "agent-a"); len(ids) != 1 {
		t.Fatal("a refused take still consumed the note")
	}
}

// The IPC handler passes the agent and the seq through and answers with the
// taken entries; a daemon with no handler answers an empty list, never an error.
func TestTeammateCommentsTakeIPCHandlerRoundTrip(t *testing.T) {
	s := NewServer(slog.Default())
	take := []byte(`{"agent_id":"agent-a","conversation_id":"cnv_x","delivered_at_seq":6}`)
	res := handleTeammateCommentsTake(s, Message{Type: MsgTypeTeammateCommentsTake, Payload: take}, nil)
	if res.Response == nil || !res.Response.Success || string(res.Response.Data) != `{"entries":[]}` {
		t.Fatalf("unwired handler = %+v", res.Response)
	}
	var got TeammateCommentsTakePayload
	var confirmed TeammateCommentsConfirmPayload
	s.SetTeammateCommentsTakeHandler(func(p TeammateCommentsTakePayload) (TeammateCommentsTakeResponse, error) {
		got = p
		return TeammateCommentsTakeResponse{LeaseID: "L1", Entries: []whisperstore.WhisperEntry{{ID: "tcm_1", Source: whisperstore.SourceTeammateComment}}}, nil
	})
	s.SetTeammateCommentsConfirmHandler(func(p TeammateCommentsConfirmPayload) error { confirmed = p; return nil })
	res = handleTeammateCommentsTake(s, Message{Type: MsgTypeTeammateCommentsTake, Payload: take}, nil)
	if got.AgentID != "agent-a" || got.DeliveredAtSeq != 6 || res.Response == nil || !res.Response.Success || !strings.Contains(string(res.Response.Data), `"lease_id":"L1"`) {
		t.Fatalf("payload seen = %+v, response = %+v", got, res.Response)
	}
	res = handleTeammateCommentsConfirm(s, Message{Type: MsgTypeTeammateCommentsConfirm, Payload: []byte(`{"agent_id":"agent-a","lease_id":"L1","comment_ids":["tcm_1"]}`)}, nil)
	if !res.Response.Success || confirmed.LeaseID != "L1" || len(confirmed.CommentIDs) != 1 {
		t.Fatalf("confirm = %+v, %+v", confirmed, res.Response)
	}
	if bad := handleTeammateCommentsTake(s, Message{Payload: []byte(`{`)}, nil); bad.Response == nil || bad.Response.Success {
		t.Fatal("a malformed payload must be refused")
	}
	if bad := handleTeammateCommentsConfirm(s, Message{Payload: []byte(`{`)}, nil); bad.Response == nil || bad.Response.Success {
		t.Fatal("a malformed confirm must be refused")
	}
}

// Failure prevented: a stopped recording's undelivered notes surviving it. Goes
// through the real finalize request, the in-process stop signal.
func TestTeammateCommentsPurgedWhenTheirRecordingStops(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	e.srv.QueueTeammateComment(srvComment("tcm_1", "pending"))
	e.drainOnce()
	addPending(t, store, "tcm_other", "agent-a", map[string]string{"conversation_id": "cnv_other_stream"})

	svc := &daemonServiceImpl{d: &Daemon{logger: slog.Default(), ctx: t.Context(), teammateComments: box}}
	svc.SessionFinalize(SessionFinalizeIPCPayload{SessionName: filepath.Base(e.dir)})

	ids := stillPending(t, store, "agent-a")
	if len(ids) != 1 || ids[0] != "tcm_other" {
		t.Fatalf("pending after stop = %v, want only the other stream's note", ids)
	}
}

// Failure prevented (round 3): a note lost for good after the owner opts out
// and back in. Dropping it must also forget it was stored, or the re-sent copy
// is skipped and holds one of the server's five slots forever.
func TestTeammateCommentOptedOutThenBackInIsDelivered(t *testing.T) {
	e, box, _ := newCommentEnv(t, "agent-a")
	e.srv.QueueTeammateComment(srvComment("tcm_1", "again"))
	e.drainOnce() // received
	e.srv.RemoveTeammateComment("tcm_1")
	e.spool(lineN(3))
	e.clock = e.clock.Add(time.Minute)
	e.drainOnce() // opted out: dropped unshown
	e.srv.QueueTeammateComment(srvComment("tcm_1", "again"))
	e.spool(lineN(4))
	e.clock = e.clock.Add(time.Minute)
	e.drainOnce() // opted back in: the server lists it again
	got, _ := box.take(t.Context(), TeammateCommentsTakePayload{AgentID: "agent-a", ConversationID: e.conv(), DeliveredAtSeq: 4})
	if len(got) != 1 || got[0].ID != "tcm_1" {
		t.Fatalf("take = %v, want the re-listed note", got)
	}
}

// Failure prevented (round 3): a server lookup error, which sends no list,
// read as "nothing is pending" and wiping a note that was never retracted.
func TestTeammateCommentLookupErrorKeepsThePendingNote(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	e.srv.QueueTeammateComment(srvComment("tcm_1", "keep me"))
	e.drainOnce()
	e.srv.FailTeammateLookup(true)
	e.spool(lineN(3))
	e.clock = e.clock.Add(time.Minute)
	e.drainOnce()
	if ids := stillPending(t, store, "agent-a"); len(ids) != 1 {
		t.Fatalf("pending = %v; an unlisted answer must keep what is held", ids)
	}
	if got, _ := box.take(t.Context(), TeammateCommentsTakePayload{AgentID: "agent-a", ConversationID: e.conv(), DeliveredAtSeq: 4}); len(got) != 1 {
		t.Fatalf("take = %v", got)
	}
}

func (e *liveEnv) payload(seq int64) TeammateCommentsTakePayload {
	return TeammateCommentsTakePayload{AgentID: "agent-a", ConversationID: e.conv(), DeliveredAtSeq: seq}
}

// ackLogExists reports whether an ack was logged: the log may exist empty (the
// lease-time writability probe creates it) without holding any ack.
func ackLogExists(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "teammate-comment-acks"))
	return err == nil && len(b) > 0
}

// Failure prevented (N1): a hook read timeout consumed the note and marked it
// delivered although nothing was shown. A lease alone acks and deletes nothing;
// when it expires unconfirmed, the note is offered again.
func TestTeammateCommentLeaseWithoutConfirmLosesNothing(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	box.now = func() time.Time { return clock }
	e.srv.QueueTeammateComment(srvComment("tcm_1", "x"))
	e.drainOnce()

	first, err := box.lease(t.Context(), e.payload(5))
	if err != nil || len(first.Entries) != 1 || first.LeaseID == "" {
		t.Fatalf("lease = %+v, %v", first, err)
	}
	// the hook gave up after its read timeout: no confirm ever arrives
	if ackLogExists(e.dir) {
		t.Fatal("a lease logged an ack before the hook showed anything")
	}
	if ids := stillPending(t, store, "agent-a"); len(ids) != 1 {
		t.Fatalf("pending = %v; a lease must not delete", ids)
	}
	clock = clock.Add(teammateLeaseTTL + time.Second)
	again, err := box.lease(t.Context(), e.payload(6))
	if err != nil || len(again.Entries) != 1 || again.LeaseID == first.LeaseID {
		t.Fatalf("after expiry lease = %+v, %v; want the note offered again", again, err)
	}
	if err := box.confirm(t.Context(), TeammateCommentsConfirmPayload{AgentID: "agent-a", LeaseID: again.LeaseID, CommentIDs: []string{"tcm_1"}}); err != nil {
		t.Fatal(err)
	}
	e.live.Wait()
	if seq, ok := e.srv.DeliveredAtSeq("tcm_1"); !ok || seq != 6 {
		t.Fatalf("delivered_at_seq = %d, %v; want 6, the seq of the lease that was confirmed", seq, ok)
	}
}

// Failure prevented: two hooks racing (parallel tool calls) each showing a note.
func TestTeammateCommentTwoHooksRacingShowANoteOnce(t *testing.T) {
	e, box, _ := newCommentEnv(t, "agent-a")
	e.srv.QueueTeammateComment(srvComment("tcm_1", "x"))
	e.drainOnce()
	a, _ := box.lease(t.Context(), e.payload(5))
	b, _ := box.lease(t.Context(), e.payload(5))
	if len(a.Entries) != 1 || len(b.Entries) != 0 {
		t.Fatalf("hook A got %d notes, hook B got %d; the note must go to one", len(a.Entries), len(b.Entries))
	}
}

// A confirm after the lease expired: still counts when nobody else leased the
// notes since (the hook did show them), and is refused when a second hook did,
// so the notes are acked once, by the lease that holds them.
func TestTeammateCommentConfirmAfterExpiry(t *testing.T) {
	t.Run("unclaimed notes are still consumed", func(t *testing.T) {
		e, box, store := newCommentEnv(t, "agent-a")
		clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		box.now = func() time.Time { return clock }
		e.srv.QueueTeammateComment(srvComment("tcm_1", "x"))
		e.drainOnce()
		l, _ := box.lease(t.Context(), e.payload(5))
		clock = clock.Add(2 * teammateLeaseTTL)
		if err := box.confirm(t.Context(), TeammateCommentsConfirmPayload{AgentID: "agent-a", LeaseID: l.LeaseID, CommentIDs: []string{"tcm_1"}}); err != nil {
			t.Fatal(err)
		}
		if ids := stillPending(t, store, "agent-a"); len(ids) != 0 {
			t.Fatalf("pending = %v; a late confirm of shown notes must consume them", ids)
		}
	})
	t.Run("notes leased again are acked by the second lease only", func(t *testing.T) {
		e, box, _ := newCommentEnv(t, "agent-a")
		clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		box.now = func() time.Time { return clock }
		e.srv.QueueTeammateComment(srvComment("tcm_1", "x"))
		e.drainOnce()
		first, _ := box.lease(t.Context(), e.payload(5))
		clock = clock.Add(2 * teammateLeaseTTL)
		second, _ := box.lease(t.Context(), e.payload(9))
		if len(second.Entries) != 1 {
			t.Fatalf("second lease = %+v", second)
		}
		if err := box.confirm(t.Context(), TeammateCommentsConfirmPayload{AgentID: "agent-a", LeaseID: first.LeaseID, CommentIDs: []string{"tcm_1"}}); err == nil {
			t.Fatal("a lease that expired and was superseded was confirmed")
		}
		if ackLogExists(e.dir) {
			t.Fatal("the stale confirm logged an ack")
		}
		if err := box.confirm(t.Context(), TeammateCommentsConfirmPayload{AgentID: "agent-a", LeaseID: second.LeaseID, CommentIDs: []string{"tcm_1"}}); err != nil {
			t.Fatal(err)
		}
		e.live.Wait()
		if seq, ok := e.srv.DeliveredAtSeq("tcm_1"); !ok || seq != 9 {
			t.Fatalf("delivered_at_seq = %d, %v; want 9", seq, ok)
		}
	})
	t.Run("an unknown or foreign lease is refused", func(t *testing.T) {
		e, box, _ := newCommentEnv(t, "agent-a")
		e.srv.QueueTeammateComment(srvComment("tcm_1", "x"))
		e.drainOnce()
		l, _ := box.lease(t.Context(), e.payload(5))
		if box.confirm(t.Context(), TeammateCommentsConfirmPayload{AgentID: "agent-a", LeaseID: "nope"}) == nil ||
			box.confirm(t.Context(), TeammateCommentsConfirmPayload{AgentID: "agent-b", LeaseID: l.LeaseID, CommentIDs: []string{"tcm_1"}}) == nil {
			t.Fatal("a bad lease id or another agent confirmed")
		}
	})
}

// Failure prevented (N1): a failed delete after the acks were logged, leaving a
// sendable ack for a note the hook never got. Delete first; no ack on failure.
func TestTeammateCommentFailedDeleteSendsNoAck(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	e.srv.QueueTeammateComment(srvComment("tcm_1", "x"))
	e.drainOnce()
	l, _ := box.lease(t.Context(), e.payload(5))

	other, err := sql.Open("sqlite", store.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Exec(`CREATE TRIGGER no_delete BEFORE DELETE ON whispers BEGIN SELECT RAISE(ABORT, 'delete refused'); END`); err != nil {
		t.Fatal(err)
	}
	if err := box.confirm(t.Context(), TeammateCommentsConfirmPayload{AgentID: "agent-a", LeaseID: l.LeaseID, CommentIDs: []string{"tcm_1"}}); err == nil {
		t.Fatal("confirm reported success although the delete failed")
	}
	e.live.Wait()
	if ackLogExists(e.dir) {
		t.Fatal("an ack was logged for a note whose delete failed")
	}
	e.drainOnce()
	if _, ok := e.srv.DeliveredAtSeq("tcm_1"); ok {
		t.Fatal("the server was told a note was delivered when it stayed pending")
	}
	if _, err := other.Exec(`DROP TRIGGER no_delete`); err != nil {
		t.Fatal(err)
	}
	if got, _ := box.take(t.Context(), e.payload(6)); len(got) != 1 {
		t.Fatalf("the note could not reach a later hook: %v", got)
	}
}

// N4: a machine that switched notes off drops every note unshown and unacked.
func TestTeammateCommentsOffDropsNotesUnshownAndUnacked(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	e.srv.QueueTeammateComment(srvComment("tcm_1", "x"))
	e.drainOnce() // received while on
	box.enabled = func() bool { return false }
	e.spool(lineN(3))
	e.clock = e.clock.Add(time.Minute)
	e.drainOnce() // the server still lists it
	if ids := stillPending(t, store, "agent-a"); len(ids) != 0 {
		t.Fatalf("pending = %v; an off switch must drop held notes", ids)
	}
	got, err := box.lease(t.Context(), e.payload(5))
	if err != nil || len(got.Entries) != 0 {
		t.Fatalf("lease = %+v, %v while off", got, err)
	}
	e.live.Wait()
	if ackLogExists(e.dir) {
		t.Fatal("a dropped note was acked")
	}
	box.enabled = nil // back on: the server's copy is stored and shown again
	e.spool(lineN(4))
	e.clock = e.clock.Add(time.Minute)
	e.drainOnce()
	if got, _ := box.take(t.Context(), e.payload(6)); len(got) != 1 {
		t.Fatalf("take after switching back on = %v", got)
	}
}
