package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
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
	if err := os.Mkdir(filepath.Join(e.rdir(), "teammate-comment-acks"), 0o700); err != nil { // the log path is unusable
		t.Fatal(err)
	}
	got, err := box.take(t.Context(), TeammateCommentsTakePayload{AgentID: "agent-a", ConversationID: e.conv(), DeliveredAtSeq: 4})
	if err != nil || len(got) != 0 {
		t.Fatalf("take = %v, %v; a note whose ack cannot be logged must not be shown", got, err)
	}
	if ids := stillPending(t, store, "agent-a"); len(ids) != 1 || ids[0] != "tcm_1" {
		t.Fatalf("pending = %v, want tcm_1 kept", ids)
	}
	if err := os.Remove(filepath.Join(e.rdir(), "teammate-comment-acks")); err != nil {
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
	if _, err := os.Stat(filepath.Join(e.rdir(), "teammate-comment-acks")); err == nil {
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
func ackLogExists(e *liveEnv) bool {
	b, err := os.ReadFile(filepath.Join(e.rdir(), "teammate-comment-acks"))
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
	if ackLogExists(e) {
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
		if ackLogExists(e) {
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

func blockSQL(t *testing.T, store *whisperstore.Store, event string) func() {
	t.Helper()
	other, err := sql.Open("sqlite", store.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	if _, err := other.Exec(`CREATE TRIGGER block_` + event + ` BEFORE ` + event + ` ON whispers BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := other.Exec(`DROP TRIGGER block_` + event); err != nil {
			t.Fatal(err)
		}
	}
}

// Failure prevented (N1): a note acked although it could not even be marked
// shown. The mark comes first; if it fails nothing is acked and the note stays
// leased, so it is offered again once the lease expires.
func TestTeammateCommentFailedMarkSendsNoAck(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	box.now = func() time.Time { return clock }
	e.srv.QueueTeammateComment(srvComment("tcm_1", "x"))
	e.drainOnce()
	l, _ := box.lease(t.Context(), e.payload(5))
	unblock := blockSQL(t, store, "UPDATE")
	if err := box.confirm(t.Context(), TeammateCommentsConfirmPayload{AgentID: "agent-a", LeaseID: l.LeaseID, CommentIDs: []string{"tcm_1"}}); err == nil {
		t.Fatal("confirm reported success although the note could not be marked shown")
	}
	e.live.Wait()
	if ackLogExists(e) {
		t.Fatal("an ack was logged for a note that was not marked shown")
	}
	unblock()
	clock = clock.Add(teammateLeaseTTL + time.Second)
	if got, _ := box.take(t.Context(), e.payload(6)); len(got) != 1 {
		t.Fatalf("the note could not reach a later hook: %v", got)
	}
}

// R2: a failed delete after the receipt was logged is harmless. The note is
// shown (never offered again), the receipt is out, and the row goes away on the
// next attempt.
func TestTeammateCommentFailedDeleteAfterReceiptNeverReshows(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	e.srv.QueueTeammateComment(srvComment("tcm_1", "x"))
	e.drainOnce()
	l, _ := box.lease(t.Context(), e.payload(5))
	unblock := blockSQL(t, store, "DELETE")
	if err := box.confirm(t.Context(), TeammateCommentsConfirmPayload{AgentID: "agent-a", LeaseID: l.LeaseID, CommentIDs: []string{"tcm_1"}}); err != nil {
		t.Fatalf("confirm = %v; the receipt is logged, so the delete can wait", err)
	}
	e.live.Wait()
	if seq, ok := e.srv.DeliveredAtSeq("tcm_1"); !ok || seq != 5 {
		t.Fatalf("delivered_at_seq = %d, %v; want the receipt out at seq 5", seq, ok)
	}
	if got, _ := box.lease(t.Context(), e.payload(6)); len(got.Entries) != 0 {
		t.Fatalf("a shown note was offered again: %v", got.Entries)
	}
	unblock()
	if got, _ := box.lease(t.Context(), e.payload(7)); len(got.Entries) != 0 {
		t.Fatal("a shown note was offered again after the delete recovered")
	}
	if rows, _ := store.PeekShownTeammateComments("agent-a", 5); len(rows) != 0 {
		t.Fatalf("shown rows left after the retry: %d", len(rows))
	}
}

// Failure prevented (R2): the ack log became unwritable after the lease, so
// confirm deleted the note and returned success with no receipt to retry; the
// session then closed and the teammate never saw "delivered". Now the note
// stays shown, is never offered again, and its receipt is logged (before the
// stop-time purge) as soon as the folder is writable.
func TestTeammateCommentReceiptSurvivesAnUnwritableLogAndTheClose(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	e.srv.QueueTeammateComment(srvComment("tcm_1", "x"))
	e.drainOnce()
	l, _ := box.lease(t.Context(), e.payload(5))
	log := filepath.Join(e.rdir(), "teammate-comment-acks")
	if err := os.Remove(log); err != nil { // the probe created it empty
		t.Fatal(err)
	}
	if err := os.Mkdir(log, 0o700); err != nil { // now unwritable
		t.Fatal(err)
	}
	if err := box.confirm(t.Context(), TeammateCommentsConfirmPayload{AgentID: "agent-a", LeaseID: l.LeaseID, CommentIDs: []string{"tcm_1"}}); err != nil {
		t.Fatalf("confirm = %v", err)
	}
	if rows, _ := store.PeekShownTeammateComments("agent-a", 5); len(rows) != 1 {
		t.Fatalf("shown rows = %d; the note must wait for its receipt", len(rows))
	}
	if got, _ := box.lease(t.Context(), e.payload(6)); len(got.Entries) != 0 {
		t.Fatal("a shown note was offered again")
	}
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	// the owner stops the session: the receipt is logged before the purge, then closed
	svc := &daemonServiceImpl{d: &Daemon{logger: slog.Default(), ctx: t.Context(), teammateComments: box}}
	svc.SessionFinalize(SessionFinalizeIPCPayload{SessionName: filepath.Base(e.dir)})
	if res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", 5*time.Second); err != nil || !res.Done {
		t.Fatalf("close = %+v, %v", res, err)
	}
	if seq, ok := e.srv.DeliveredAtSeq("tcm_1"); !ok || seq != 5 {
		t.Fatalf("delivered_at_seq = %d, %v; the receipt must survive the failure and the close", seq, ok)
	}
}

// Failure prevented (CIbp): agent B's take discarding agent A's expired lease,
// so A's late confirm failed although no lease had replaced it.
func TestTeammateCommentLateConfirmSurvivesAnotherAgentsTake(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	box.now = func() time.Time { return clock }
	e.srv.QueueTeammateComment(srvComment("tcm_1", "x"))
	e.drainOnce()
	a, _ := box.lease(t.Context(), e.payload(5))
	addPending(t, store, "tcm_b", "agent-b", map[string]string{"session_dir": e.dir, "conversation_id": e.conv()})
	clock = clock.Add(2 * teammateLeaseTTL)
	if got, _ := box.lease(t.Context(), TeammateCommentsTakePayload{AgentID: "agent-b", ConversationID: e.conv(), DeliveredAtSeq: 6}); len(got.Entries) != 1 {
		t.Fatalf("agent-b lease = %v", got.Entries)
	}
	if err := box.confirm(t.Context(), TeammateCommentsConfirmPayload{AgentID: "agent-a", LeaseID: a.LeaseID, CommentIDs: []string{"tcm_1"}}); err != nil {
		t.Fatalf("a late confirm nobody superseded was refused: %v", err)
	}
	e.live.Wait()
	if seq, ok := e.srv.DeliveredAtSeq("tcm_1"); !ok || seq != 5 {
		t.Fatalf("delivered_at_seq = %d, %v; want 5", seq, ok)
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
	if ackLogExists(e) {
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

func addClosedSessionDirs(t *testing.T, e *liveEnv, n int) []string {
	t.Helper()
	var dirs []string
	for i := range n {
		dir := filepath.Join(e.sessionsDir, fmt.Sprintf("2026-03-0%dT10-00-person-a-Oxc%d", i+1, i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(livesession.State{ConversationID: fmt.Sprintf("cnv_c%d", i), Status: livesession.StatusClosed})
		if err := os.WriteFile(filepath.Join(dir, "stream-state.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, canonicalDir(dir))
	}
	return dirs
}

// Failure prevented (R3): closed sessions' receipt retries run inline in the tick,
// before live work, each with its own timeout, so a dead server stalled every
// active session behind them. Now live work is scheduled first, the flushes
// share one budget, and one tick works at most four sessions.
func TestTickFlushesClosedSessionsLastAndBounded(t *testing.T) {
	e := newLiveEnv(t)
	closed := addClosedSessionDirs(t, e, 6)
	e.spool(lineN(1) + lineN(2)) // the active session has bytes to send
	var calls []string
	e.live.flushAcks = func(ctx context.Context, _ livesession.Config, dir string, _ time.Duration, _ time.Time) (bool, error) {
		if len(calls) == 0 {
			// the active session's drain must be able to finish while the first
			// flush is still "on the network": only possible if it was scheduled first
			deadline := time.After(20 * time.Second)
			for e.stored() == "" {
				select {
				case <-deadline:
					t.Error("the active session's drain never ran: the flush blocked it")
					return true, nil
				case <-time.After(5 * time.Millisecond):
				}
			}
		}
		calls = append(calls, dir)
		return true, nil
	}
	e.live.Tick(t.Context())
	e.live.Wait()
	if len(calls) != liveAckFlushMax {
		t.Fatalf("flushed %d sessions in one tick, want the cap %d (of %d closed)", len(calls), liveAckFlushMax, len(closed))
	}
	first := calls[0]
	calls = nil
	e.live.Tick(t.Context())
	if len(calls) == 0 || calls[0] == first {
		t.Fatalf("the next tick started at %v again; the starting point must rotate", calls)
	}
}

// One shared budget: once it is spent, no further session is flushed this tick.
func TestTickFlushBudgetIsSharedAcrossSessions(t *testing.T) {
	e := newLiveEnv(t)
	addClosedSessionDirs(t, e, 3)
	e.live.ackFlushBudget = 50 * time.Millisecond
	calls := 0
	e.live.flushAcks = func(ctx context.Context, _ livesession.Config, _ string, _ time.Duration, _ time.Time) (bool, error) {
		calls++
		<-ctx.Done() // a dead server: the flush uses all the time it is given
		return true, ctx.Err()
	}
	e.live.Tick(t.Context())
	if calls != 1 {
		t.Fatalf("%d flushes ran; the first exhausted the tick's budget, so the rest must wait for the next tick", calls)
	}
}

// rdir is the conversation's machine-local receipt folder (never inside the
// session folder), created.
func (e *liveEnv) rdir() string {
	dir, err := livesession.ReceiptsDir(e.cfg, e.conv())
	if err != nil {
		e.t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	return dir
}

func addShownRows(t *testing.T, e *liveEnv, store *whisperstore.Store, n int) []string {
	t.Helper()
	var ids []string
	for i := range n {
		id := fmt.Sprintf("tcm_s%02d", i)
		addPending(t, store, id, "agent-a", map[string]string{"session_dir": e.dir, "conversation_id": e.conv()})
		ids = append(ids, id)
	}
	if err := store.MarkTeammateCommentsShown(7, ids...); err != nil {
		t.Fatal(err)
	}
	return ids
}

func loggedReceiptIDs(t *testing.T, e *liveEnv) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.rdir(), "teammate-comment-acks"))
	if err != nil {
		return map[string]bool{}
	}
	out := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		var a sessionstream.TeammateCommentAck
		if json.Unmarshal([]byte(l), &a) == nil && a.ID != "" {
			out[a.ID] = true
		}
	}
	return out
}

// Failure prevented (T2): purge retried one page of 20 owed receipts, and the
// rest stayed owed for good once the stream stopped.
func TestPurgeDrainsEveryOwedReceiptNotJustTheFirstPage(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	t.Cleanup(e.live.Wait)
	ids := addShownRows(t, e, store, 45)
	box.purgeSession(t.Context(), filepath.Base(e.dir))
	got := loggedReceiptIDs(t, e)
	for _, id := range ids {
		if !got[id] {
			t.Fatalf("the receipt of %s past the first page was never logged (%d of %d)", id, len(got), len(ids))
		}
	}
	if rows, _ := store.AllShownTeammateComments(100); len(rows) != 0 {
		t.Fatalf("%d shown rows are still owed", len(rows))
	}
}

// And with no stop signal at all, the closed-session sweep drains them and the
// production send path delivers every one to the server.
func TestTickDrainsOwedReceiptsOfAClosedSessionBeyondTheFirstPage(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	t.Cleanup(e.live.Wait)
	e.live.drainShown = box.drainAllShown
	e.drainOnce() // opens the stream
	if res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", 5*time.Second); err != nil || !res.Done {
		t.Fatalf("close = %+v, %v", res, err)
	}
	ids := addShownRows(t, e, store, 45)
	e.live.Tick(t.Context())
	e.live.Wait()
	for _, id := range ids {
		if _, ok := e.srv.DeliveredAtSeq(id); !ok {
			t.Fatalf("the receipt of %s never reached the server through Tick", id)
		}
	}
}
