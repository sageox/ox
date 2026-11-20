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
