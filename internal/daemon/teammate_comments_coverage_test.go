package daemon

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionstream"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// With no ledger store there is nowhere to keep a note: every registry call is a
// quiet no-op, not an error and not a panic.
func TestWhisperRegistryTeammateCallsWithNoLedgerStoreAreNoOps(t *testing.T) {
	r := NewWhisperRegistry(nil, slog.Default())
	if stored, err := r.AddTeammateComment(whisperstore.WhisperEntry{ID: "x"}); stored || err != nil {
		t.Fatalf("add = %v, %v", stored, err)
	}
	if got, err := r.PeekTeammateComments("a", 5); got != nil || err != nil {
		t.Fatalf("peek = %v, %v", got, err)
	}
	if got, err := r.PeekShownTeammateComments("a", 5); got != nil || err != nil {
		t.Fatalf("peek shown = %v, %v", got, err)
	}
	if got, err := r.ShownTeammateCommentsForConversation("c", 5); got != nil || err != nil {
		t.Fatalf("shown for conversation = %v, %v", got, err)
	}
	if got, err := r.AllShownTeammateComments(5); got != nil || err != nil {
		t.Fatalf("all shown = %v, %v", got, err)
	}
	if err := r.MarkTeammateCommentsShown(1, "x"); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteTeammateComments("x"); err != nil {
		t.Fatal(err)
	}
	if n, err := r.RetainTeammateComments("c", nil); n != 0 || err != nil {
		t.Fatalf("retain = %d, %v", n, err)
	}
}

// A store that fails must surface as an error (or a logged skip), never as a
// note that looks stored or delivered.
func TestWhisperRegistryTeammateCallsReportAFailingStore(t *testing.T) {
	store, err := whisperstore.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	r := NewWhisperRegistry(store, slog.Default())
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	entry := whisperstore.WhisperEntry{ID: "x", Source: whisperstore.SourceTeammateComment, AgentID: "a", CreatedAt: time.Now()}
	if stored, err := r.AddTeammateComment(entry); stored || err == nil {
		t.Fatalf("add on a closed store = %v, %v", stored, err)
	}
	if _, err := r.PeekTeammateComments("a", 5); err == nil {
		t.Error("peek")
	}
	if _, err := r.PeekShownTeammateComments("a", 5); err == nil {
		t.Error("peek shown")
	}
	if _, err := r.ShownTeammateCommentsForConversation("c", 5); err == nil {
		t.Error("shown for conversation")
	}
	if _, err := r.AllShownTeammateComments(5); err == nil {
		t.Error("all shown")
	}
	if r.MarkTeammateCommentsShown(1, "x") == nil {
		t.Error("mark shown")
	}
	if r.DeleteTeammateComments("x") == nil {
		t.Error("delete")
	}
	if _, err := r.RetainTeammateComments("c", []string{"k"}); err == nil {
		t.Error("retain")
	}
	if _, err := r.RetainTeammateComments("c", nil); err == nil {
		t.Error("retain all")
	}
}

// The store's own guards: empty arguments match nothing, and a closed store fails
// every teammate query loudly.
func TestStoreTeammateQueriesGuardEmptyInputAndFailOnAClosedStore(t *testing.T) {
	store, err := whisperstore.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.ShownTeammateCommentsForConversation("", 5); got != nil || err != nil {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := store.AllShownTeammateComments(0); got != nil || err != nil {
		t.Fatalf("%v %v", got, err)
	}
	if err := store.MarkTeammateCommentsShown(1); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteTeammateComments(); err != nil {
		t.Fatal(err)
	}
	if got, err := store.TakeTeammateComments("a", 5); got != nil || err != nil {
		t.Fatalf("take of nothing = %v, %v", got, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TakeTeammateComments("a", 5); err == nil {
		t.Error("take on a closed store")
	}
	if err := store.DeleteTeammateComments("x"); err == nil {
		t.Error("delete on a closed store")
	}
	if err := store.MarkTeammateCommentsShown(1, "x"); err == nil {
		t.Error("mark on a closed store")
	}
}

// The inbox keeps working, and says so, when its pieces fail.
func TestInboxLogsAndSkipsWhenTheStoreFails(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	t.Cleanup(e.live.Wait)
	e.srv.QueueTeammateComment(sessionstream.TeammateComment{ID: "tcm_1", AuthorName: "A", Body: "x"})
	anchor := int64(7)
	e.srv.QueueTeammateComment(sessionstream.TeammateComment{ID: "tcm_2", AuthorName: "A", Body: "y", AnchorSeq: &anchor})
	e.drainOnce()
	if got, _ := box.lease(t.Context(), e.payload(5)); len(got.Entries) != 2 || got.Entries[1].Metadata["anchor_seq"] != "7" {
		t.Fatalf("anchor not kept: %+v", got)
	}
	// every query now fails
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	box.receive(t.Context(), e.dir, livesession.State{ConversationID: e.conv()}, []sessionstream.TeammateComment{{ID: "tcm_9", Body: "z"}})
	if _, err := box.lease(t.Context(), e.payload(6)); err == nil {
		t.Fatal("lease on a failing store reported success")
	}
	if err := box.confirm(t.Context(), TeammateCommentsConfirmPayload{AgentID: "agent-a", LeaseID: "none"}); err == nil {
		t.Fatal("confirm of an unknown lease")
	}
	box.purgeSession(t.Context(), filepath.Base(e.dir))
	box.drainAllShown(t.Context())
	box.enabled = func() bool { return false }
	box.receive(t.Context(), e.dir, livesession.State{ConversationID: e.conv()}, nil) // off: drop, with a failing store
}

// Inputs the inbox must refuse rather than guess at.
func TestInboxRefusesUnusableInput(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	t.Cleanup(e.live.Wait)
	box.receive(t.Context(), e.dir, livesession.State{}, []sessionstream.TeammateComment{{ID: "x"}}) // no conversation
	if ids := stillPending(t, store, "agent-a"); len(ids) != 0 {
		t.Fatalf("stored %v for no conversation", ids)
	}
	box.purgeSession(t.Context(), "")
	box.purgeSession(t.Context(), "../outside")
	box.purgeSession(t.Context(), "no-such-session")
	if _, err := box.receiptsDir(nil, e.conv()); err == nil {
		t.Fatal("receipts kept with no live sessions")
	}
	if err := box.checkReceiptLog(nil, e.conv()); err == nil {
		t.Fatal("receipt log checked with no live sessions")
	}
	// shown rows with no usable seq or conversation are reported, not guessed at
	addPending(t, store, "tcm_bad", "agent-a", map[string]string{"conversation_id": e.conv()})
	if err := store.MarkTeammateCommentsShown(0, "tcm_bad"); err != nil {
		t.Fatal(err)
	}
	box.drainAllShown(t.Context())
	if rows, _ := store.AllShownTeammateComments(5); len(rows) != 1 {
		t.Fatalf("an unrecoverable shown row was dropped: %d", len(rows))
	}
	// an unusable lease request
	if _, err := box.lease(t.Context(), TeammateCommentsTakePayload{}); err == nil {
		t.Fatal("empty lease request accepted")
	}
	// nil registry: everything is a quiet no-op
	nilBox := &teammateCommentInbox{logger: slog.Default(), registry: func() *WhisperRegistry { return nil }, live: func() *LiveSessions { return nil }, now: time.Now}
	if got, err := nilBox.lease(t.Context(), e.payload(1)); err != nil || len(got.Entries) != 0 {
		t.Fatalf("%v %v", got, err)
	}
	if err := nilBox.confirm(t.Context(), TeammateCommentsConfirmPayload{}); err != nil {
		t.Fatal(err)
	}
	nilBox.drainAllShown(t.Context())
	nilBox.purgeSession(t.Context(), "x")
	nilBox.receive(t.Context(), e.dir, livesession.State{ConversationID: "c"}, nil)
}

// A confirm that names nothing still leased clears the lease and acks nothing.
func TestConfirmOfNotesThatLeftTheInboxIsANoOp(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	t.Cleanup(e.live.Wait)
	e.srv.QueueTeammateComment(srvComment("tcm_1", "x"))
	e.drainOnce()
	l, _ := box.lease(t.Context(), e.payload(5))
	if err := store.DeleteTeammateComments("tcm_1"); err != nil { // retracted meanwhile
		t.Fatal(err)
	}
	if err := box.confirm(t.Context(), TeammateCommentsConfirmPayload{AgentID: "agent-a", LeaseID: l.LeaseID, CommentIDs: []string{"tcm_1"}}); err != nil {
		t.Fatal(err)
	}
	e.live.Wait()
	if ackLogExists(e) {
		t.Fatal("a retracted note was acked")
	}
}

// At most five notes go to one hook call; the rest wait for the next.
func TestLeaseHandsOutAtMostFiveNotes(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	t.Cleanup(e.live.Wait)
	for i := range 8 {
		addPending(t, store, "tcm_n"+string(rune('a'+i)), "agent-a", map[string]string{"session_dir": e.dir, "conversation_id": e.conv()})
	}
	got, _ := box.lease(t.Context(), e.payload(5))
	if len(got.Entries) != sessionstream.MaxTeammateCommentsPerAppend {
		t.Fatalf("%d notes in one lease", len(got.Entries))
	}
}

// Foreign notes are dropped when the delete itself fails: the lease still works.
func TestForeignNoteDropFailureDoesNotBlockTheLease(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	t.Cleanup(e.live.Wait)
	addPending(t, store, "tcm_old", "agent-a", map[string]string{"session_dir": e.dir, "conversation_id": "cnv_other"})
	unblock := blockSQL(t, store, "DELETE")
	defer unblock()
	if got, err := box.lease(t.Context(), e.payload(5)); err != nil || len(got.Entries) != 0 {
		t.Fatalf("lease = %+v, %v", got, err)
	}
}

// The daemon service delegates to the inbox, and is a no-op without one.
func TestDaemonServiceTeammateCallsDelegateToTheInbox(t *testing.T) {
	bare := &daemonServiceImpl{d: &Daemon{logger: slog.Default(), ctx: t.Context()}}
	if got, err := bare.TeammateCommentsTake(TeammateCommentsTakePayload{}); err != nil || got.LeaseID != "" {
		t.Fatalf("%v %v", got, err)
	}
	if err := bare.TeammateCommentsConfirm(TeammateCommentsConfirmPayload{}); err != nil {
		t.Fatal(err)
	}
	e, box, _ := newCommentEnv(t, "agent-a")
	t.Cleanup(e.live.Wait)
	e.srv.QueueTeammateComment(srvComment("tcm_1", "x"))
	e.drainOnce()
	svc := &daemonServiceImpl{d: &Daemon{logger: slog.Default(), ctx: t.Context(), teammateComments: box}}
	got, err := svc.TeammateCommentsTake(e.payload(5))
	if err != nil || len(got.Entries) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	if err := svc.TeammateCommentsConfirm(TeammateCommentsConfirmPayload{AgentID: "agent-a", LeaseID: got.LeaseID, CommentIDs: []string{"tcm_1"}}); err != nil {
		t.Fatal(err)
	}
}

// The recipient of a stream's notes is the live agent whose recording writes to
// that folder, and nobody else.
func TestAgentRecordingDirFindsOnlyTheLiveRecordingOfThatFolder(t *testing.T) {
	root := t.TempDir()
	cache := t.TempDir()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("XDG_DATA_HOME", cache)
	if err := os.MkdirAll(filepath.Join(root, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"config_version":"2","repo_id":"test-repo-notes","endpoint":"http://test.sageox.local","session_publishing":"manual"}`
	if err := os.WriteFile(filepath.Join(root, ".sageox", "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := session.StartRecording(root, session.StartRecordingOptions{AgentID: "OxNote1", AdapterName: "claude-code", Username: "u"})
	if err != nil {
		t.Fatal(err)
	}
	find := agentRecordingDir(root, func() []string { return []string{"OxNobody", "OxNote1"} })
	if got := find(state.SessionPath); got != "OxNote1" {
		t.Fatalf("recipient = %q", got)
	}
	if got := find(t.TempDir()); got != "" {
		t.Fatalf("another folder resolved to %q", got)
	}
	if got := agentRecordingDir(root, func() []string { return nil })(state.SessionPath); got != "" {
		t.Fatalf("no agents resolved to %q", got)
	}
}
