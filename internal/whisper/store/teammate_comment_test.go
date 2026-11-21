package store

import (
	"testing"
	"time"
)

func teammateEntry(id, agentID string, at time.Time) WhisperEntry {
	return WhisperEntry{
		ID: id, Scope: "session", Type: WhisperTrigger, Source: SourceTeammateComment,
		Topic: "teammate-comment", Content: "body " + id, Importance: ImportanceNormal,
		CreatedAt: at, AgentID: agentID,
	}
}

// A teammate note is addressed to one agent and carries no label here, so the
// broadcast reader must never return it, to the recipient or anyone else.
func TestTeammateCommentNeverReturnedByBroadcastReader(t *testing.T) {
	s := mustOpen(t)
	now := time.Now()
	if err := s.Add(teammateEntry("tc1", "agent-a", now.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"agent-a", "agent-b"} {
		got, err := s.GetWhispers(agent, AttentionAll, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range got {
			if e.Source == SourceTeammateComment {
				t.Fatalf("GetWhispers(%s) returned a teammate comment unlabeled", agent)
			}
		}
	}
}

// Inspection routes by recipient: only the addressed agent sees the entry.
func TestTeammateCommentPageRoutedByRecipientOnly(t *testing.T) {
	s := mustOpen(t)
	if err := s.Add(teammateEntry("tc1", "agent-a", time.Now())); err != nil {
		t.Fatal(err)
	}
	has := func(agent string) bool {
		page, _, err := s.GetWhispersPage(agent, time.Time{}, 50)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page {
			if e.Source == SourceTeammateComment {
				return true
			}
		}
		return false
	}
	if !has("agent-a") {
		t.Fatal("recipient cannot see its own note on inspection")
	}
	if has("agent-b") || has("") {
		t.Fatal("another agent or an unscoped view saw a note addressed to agent-a")
	}
}

func TestTeammateCommentTakeIsOnceAndRecipientScoped(t *testing.T) {
	s := mustOpen(t)
	now := time.Now()
	if err := s.Add(
		teammateEntry("tc2", "agent-a", now.Add(-time.Second)),
		teammateEntry("tc1", "agent-a", now.Add(-time.Minute)),
		teammateEntry("tc3", "agent-b", now),
		makeEntry("w1", "lint", ImportanceNormal, now),
	); err != nil {
		t.Fatal(err)
	}

	other, err := s.TakeTeammateComments("agent-b", 5)
	if err != nil || len(other) != 1 || other[0].ID != "tc3" {
		t.Fatalf("agent-b take = %v, %v; want only tc3", other, err)
	}
	got, err := s.TakeTeammateComments("agent-a", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "tc1" || got[1].ID != "tc2" {
		t.Fatalf("take = %v, want tc1 then tc2 (oldest first)", got)
	}
	again, err := s.TakeTeammateComments("agent-a", 5)
	if err != nil || len(again) != 0 {
		t.Fatalf("second take = %v, %v; want nothing", again, err)
	}
	if got, err := s.TakeTeammateComments("", 5); err != nil || len(got) != 0 {
		t.Fatalf("empty agent id take = %v, %v; want nothing", got, err)
	}
}

func TestTeammateCommentTakeHonorsLimitAndLeavesRest(t *testing.T) {
	s := mustOpen(t)
	now := time.Now()
	for i, id := range []string{"a", "b", "c"} {
		if err := s.Add(teammateEntry(id, "agent-a", now.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := s.TakeTeammateComments("agent-a", 2)
	rest, _ := s.TakeTeammateComments("agent-a", 2)
	if len(first) != 2 || len(rest) != 1 || rest[0].ID != "c" {
		t.Fatalf("first=%v rest=%v", first, rest)
	}
}

// The server's list replaces a conversation's pending set; other
// conversations and non-notes are untouched.
func TestTeammateCommentRetainReplacesOneConversationsPendingSet(t *testing.T) {
	s := mustOpen(t)
	now := time.Now()
	mk := func(id, conv string) WhisperEntry {
		e := teammateEntry(id, "agent-a", now)
		e.Metadata = map[string]string{"conversation_id": conv}
		return e
	}
	if err := s.Add(mk("a", "c1"), mk("b", "c1"), mk("c", "c2"), makeEntry("w", "lint", ImportanceNormal, now)); err != nil {
		t.Fatal(err)
	}
	n, err := s.RetainTeammateComments("c1", []string{"b"})
	if err != nil || n != 1 {
		t.Fatalf("retain removed %d, %v; want 1", n, err)
	}
	if n, _ := s.RetainTeammateComments("c2", nil); n != 1 {
		t.Fatalf("purge of c2 removed %d, want 1", n)
	}
	left, _ := s.PeekTeammateComments("agent-a", 10)
	if len(left) != 1 || left[0].ID != "b" {
		t.Fatalf("left = %v, want only b", left)
	}
	if n, _ := s.RetainTeammateComments("", nil); n != 0 {
		t.Fatal("an empty conversation id removed notes")
	}
}

// A note the hook already showed leaves the pending set but stays readable
// until its receipt is recorded, and each shown-state reader sees exactly the
// notes it is scoped to.
func TestTeammateCommentShownStateLeavesPendingUntilReceiptDeleted(t *testing.T) {
	s := mustOpen(t)
	now := time.Now()
	mk := func(id, agent, conv string, at time.Time) WhisperEntry {
		e := teammateEntry(id, agent, at)
		e.Metadata = map[string]string{"conversation_id": conv}
		return e
	}
	if err := s.Add(
		mk("a1", "agent-a", "c1", now.Add(-3*time.Minute)),
		mk("a2", "agent-a", "c2", now.Add(-2*time.Minute)),
		mk("b1", "agent-b", "c1", now.Add(-time.Minute)),
		mk("a3", "agent-a", "c1", now),
	); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkTeammateCommentsShown(7); err != nil {
		t.Fatalf("marking nothing shown: %v", err)
	}
	if err := s.MarkTeammateCommentsShown(7, "a1", "a2", "b1"); err != nil {
		t.Fatal(err)
	}

	pending, _ := s.PeekTeammateComments("agent-a", 10)
	if len(pending) != 1 || pending[0].ID != "a3" {
		t.Fatalf("pending = %v, want only a3", pending)
	}
	shown, err := s.PeekShownTeammateComments("agent-a", 10)
	if err != nil || len(shown) != 2 || shown[0].ID != "a1" || shown[1].ID != "a2" {
		t.Fatalf("shown for agent-a = %v, %v; want a1,a2 oldest first", shown, err)
	}
	if shown[0].Metadata["shown_seq"] != "7" {
		t.Fatalf("shown_seq = %q, want 7", shown[0].Metadata["shown_seq"])
	}
	byConv, _ := s.ShownTeammateCommentsForConversation("c1", 10)
	if len(byConv) != 2 || byConv[0].ID != "a1" || byConv[1].ID != "b1" {
		t.Fatalf("shown for c1 = %v, want a1,b1", byConv)
	}
	all, _ := s.AllShownTeammateComments(10)
	if len(all) != 3 {
		t.Fatalf("all shown = %d, want 3", len(all))
	}

	if err := s.DeleteTeammateComments(); err != nil {
		t.Fatalf("deleting nothing: %v", err)
	}
	if err := s.DeleteTeammateComments("a1", "a2", "b1"); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.AllShownTeammateComments(10); len(all) != 0 {
		t.Fatalf("shown after receipt delete = %v, want none", all)
	}
}

// An empty recipient or a non-positive limit matches nothing rather than
// widening to every agent's notes.
func TestTeammateCommentShownReadersRefuseEmptyScopeAndZeroLimit(t *testing.T) {
	s := mustOpen(t)
	if err := s.Add(teammateEntry("a1", "agent-a", time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkTeammateCommentsShown(1, "a1"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PeekShownTeammateComments("", 10); got != nil || err != nil {
		t.Fatalf("empty agent = %v, %v", got, err)
	}
	if got, err := s.ShownTeammateCommentsForConversation("", 10); got != nil || err != nil {
		t.Fatalf("empty conversation = %v, %v", got, err)
	}
	if got, err := s.ShownTeammateCommentsForConversation("c1", 0); got != nil || err != nil {
		t.Fatalf("zero limit = %v, %v", got, err)
	}
	if got, err := s.AllShownTeammateComments(0); got != nil || err != nil {
		t.Fatalf("zero limit all = %v, %v", got, err)
	}
}

// Storage failures surface as errors, never as an empty "no notes" answer that
// would let the caller believe nothing is owed.
func TestTeammateCommentStorageFailuresAreReturnedNotSwallowed(t *testing.T) {
	t.Run("closed store", func(t *testing.T) {
		s := mustOpen(t)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AllShownTeammateComments(5); err == nil {
			t.Fatal("query on closed store returned no error")
		}
		if err := s.MarkTeammateCommentsShown(1, "x"); err == nil {
			t.Fatal("mark on closed store returned no error")
		}
	})
	t.Run("unscannable row", func(t *testing.T) {
		s := mustOpen(t)
		// sqlite lets a non-integer primary key hold NULL, which cannot scan into a string id
		if _, err := s.db.Load().Exec(`INSERT INTO whispers (id, scope, type, source, topic, content, importance, created_at, agent_id)
			VALUES (NULL, 's', 't', ?, 'x', 'c', 'normal', '2026-01-01T00:00:00Z', 'agent-a')`, SourceTeammateComment); err != nil {
			t.Fatal(err)
		}
		if _, err := s.PeekTeammateComments("agent-a", 5); err == nil {
			t.Fatal("scan of NULL id returned no error")
		}
	})
	t.Run("corrupt metadata fails the query", func(t *testing.T) {
		s := mustOpen(t)
		// json_extract rejects the bad row while sqlite sorts, so Query itself fails
		if _, err := s.db.Load().Exec(`INSERT INTO whispers (id, scope, type, source, topic, content, importance, created_at, agent_id, metadata) VALUES
			('good', 's', 't', ?, 'x', 'c', 'normal', '2026-01-01T00:00:00Z', 'agent-a', '{"state":"shown"}'),
			('bad', 's', 't', ?, 'x', 'c', 'normal', '2026-01-02T00:00:00Z', 'agent-a', 'not json')`, SourceTeammateComment, SourceTeammateComment); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AllShownTeammateComments(5); err == nil {
			t.Fatal("malformed metadata json returned no error")
		}
	})
}
