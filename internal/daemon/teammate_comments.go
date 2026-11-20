package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionstream"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// Notes teammates send to a live session's agent (bead sageox-l2i2i.14).
//
// A drain returns the undelivered notes the server holds; the daemon stores each
// as a whisper addressed to the one agent recording that session (never a
// broadcast), and the agent's PostToolUse hook takes them once. Taking a note
// logs its delivery for the next drain to send to the server as an ack.

// teammate-comment whisper metadata keys.
const (
	tcMetaCommentID      = "comment_id"
	tcMetaAuthorName     = "author_name"
	tcMetaAnchorSeq      = "anchor_seq"
	tcMetaSessionDir     = "session_dir"
	tcMetaConversationID = "conversation_id"
)

// TeammateCommentsTakePayload asks for the notes waiting for one agent.
// DeliveredAtSeq is the recording's entry count after the hook captured the
// tool call the notes will be shown after.
type TeammateCommentsTakePayload struct {
	AgentID string `json:"agent_id"`
	// ConversationID is the stream the hook is capturing now; notes sent to any
	// other conversation are dropped, never shown or acked.
	ConversationID string `json:"conversation_id"`
	DeliveredAtSeq int64  `json:"delivered_at_seq"`
}

// TeammateCommentsTakeResponse carries the leased notes, oldest first, and the
// lease id the hook confirms once it has written them. Until then the notes are
// merely held back from other takes.
type TeammateCommentsTakeResponse struct {
	LeaseID string                      `json:"lease_id,omitempty"`
	Entries []whisperstore.WhisperEntry `json:"entries"`
}

// TeammateCommentsConfirmPayload commits a lease: the hook wrote CommentIDs.
type TeammateCommentsConfirmPayload struct {
	AgentID    string   `json:"agent_id"`
	LeaseID    string   `json:"lease_id"`
	CommentIDs []string `json:"comment_ids"`
}

// teammateLeaseTTL is how long leased notes are held back from other takes. A
// hook confirms within milliseconds; an unconfirmed lease (the hook timed out
// or died) expires and its notes are pending again.
const teammateLeaseTTL = 30 * time.Second

// noteLease is one take's hold on notes: nothing is acked or deleted until it
// is confirmed.
type noteLease struct {
	agentID string
	seq     int64
	expires time.Time
	ids     []string
	dirs    map[string]string // comment id -> trusted session folder
}

// teammateCommentInbox stores received notes and hands them to the hook.
type teammateCommentInbox struct {
	logger   *slog.Logger
	registry func() *WhisperRegistry
	live     func() *LiveSessions
	// recipientFor names the agent recording the session in dir; "" when none.
	recipientFor func(dir string) string
	now          func() time.Time
	// enabled is the machine-local switch (session.teammate_notes); nil means on.
	enabled func() bool
	// leases are the takes awaiting confirmation, by lease id.
	leases map[string]*noteLease
	// offLogged: the switch was seen off and logged once for this conversation.
	offLogged sync.Map
	// mu serializes receive, take and purge: two hooks running at once must not
	// both show one note.
	mu sync.Mutex
}

// teammateCommentPeekLimit bounds how many waiting notes one take examines. A
// note that cannot be shown (its folder is untrusted, its ack cannot be
// written) stays pending, so a few of them must not starve the rest.
const teammateCommentPeekLimit = 20

// receive applies a drain's answer for the stream in dir. The server's list is
// authoritative: it REPLACES the pending set for this conversation, so a note
// the owner opted out of, the author deleted or a delivery settled is dropped
// unshown. Notes then go to the agent recording dir; with no such agent nothing
// new is stored (the server re-sends on every append until acked).
func (b *teammateCommentInbox) receive(ctx context.Context, dir string, st livesession.State, comments []sessionstream.TeammateComment) {
	reg := b.registry()
	if reg == nil || st.ConversationID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.isEnabled() {
		// this machine opted out: drop every note unshown and unacked, say so once
		if _, logged := b.offLogged.LoadOrStore(st.ConversationID, true); !logged {
			b.logger.InfoContext(ctx, "teammate_notes_off_dropping", "conversation_id", st.ConversationID, "setting", "session.teammate_notes", "env", "OX_TEAMMATE_NOTES")
		}
		if _, err := reg.RetainTeammateComments(st.ConversationID, nil); err != nil {
			b.logger.WarnContext(ctx, "teammate_comment_replace_failed", "conversation_id", st.ConversationID, "error", err)
		}
		return
	}
	keep := make([]string, 0, len(comments))
	for _, c := range comments {
		keep = append(keep, c.ID)
	}
	if n, err := reg.RetainTeammateComments(st.ConversationID, keep); err != nil {
		b.logger.WarnContext(ctx, "teammate_comment_replace_failed", "conversation_id", st.ConversationID, "error", err)
	} else if n > 0 {
		b.logger.InfoContext(ctx, "teammate_comments_dropped_unshown", "conversation_id", st.ConversationID, "count", n)
	}
	if len(comments) == 0 {
		return
	}
	agentID := b.recipientFor(dir)
	if agentID == "" {
		b.logger.DebugContext(ctx, "teammate_comment_no_recipient", "session", st.SessionID, "count", len(comments))
		return
	}
	for _, c := range comments[:min(len(comments), sessionstream.MaxTeammateCommentsPerAppend)] {
		meta := map[string]string{
			tcMetaCommentID: c.ID, tcMetaAuthorName: c.AuthorName,
			tcMetaSessionDir: dir, tcMetaConversationID: st.ConversationID,
		}
		if c.AnchorSeq != nil {
			meta[tcMetaAnchorSeq] = strconv.FormatInt(*c.AnchorSeq, 10)
		}
		stored, err := reg.AddTeammateComment(whisperstore.WhisperEntry{
			ID: c.ID, Scope: "ledger", Type: whisperstore.WhisperTrigger,
			Source: whisperstore.SourceTeammateComment, Topic: "teammate-comment",
			Content: c.Body, Importance: whisperstore.ImportanceNormal,
			CreatedAt: b.now(), AgentID: agentID, Metadata: meta,
		})
		if err != nil {
			b.logger.WarnContext(ctx, "teammate_comment_store_failed", "comment_id", c.ID, "error", err)
			continue
		}
		if stored {
			b.logger.InfoContext(ctx, "teammate_comment_stored", "comment_id", c.ID, "session_id", st.SessionID)
		}
	}
}

func (b *teammateCommentInbox) isEnabled() bool { return b.enabled == nil || b.enabled() }

// leaseHolder is the unexpired lease holding comment id, or "". mu held.
func (b *teammateCommentInbox) leaseHolder(id string) string {
	now := b.now()
	for leaseID, l := range b.leases {
		if now.After(l.expires) {
			delete(b.leases, leaseID)
			continue
		}
		if slices.Contains(l.ids, id) {
			return leaseID
		}
	}
	return ""
}

// lease holds the agent's notes for its current conversation, at most
// MaxTeammateCommentsPerAppend, and returns them with a lease id. It logs no
// ack and deletes nothing: a note is consumed only when the hook confirms it
// wrote it (confirm), so a hook that timed out or died loses nothing. A note
// is leased only when it could be acked on confirm: if its folder is untrusted
// or its ack log cannot be written it stays pending. A note sent to another
// conversation is dropped, never acked here. Notes held by a live lease are
// skipped, so two hooks racing never show one note twice. The folder in an
// entry's metadata is not trusted input: it must be a direct child of the
// sessions cache, as for a drain request.
func (b *teammateCommentInbox) lease(ctx context.Context, p TeammateCommentsTakePayload) (TeammateCommentsTakeResponse, error) {
	if p.AgentID == "" || p.ConversationID == "" || p.DeliveredAtSeq < 1 {
		return TeammateCommentsTakeResponse{}, errors.New("agent_id, conversation_id and a delivered_at_seq of at least 1 are required")
	}
	reg, live := b.registry(), b.live()
	if reg == nil || !b.isEnabled() {
		return TeammateCommentsTakeResponse{}, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	waiting, err := reg.PeekTeammateComments(p.AgentID, teammateCommentPeekLimit)
	if err != nil {
		return TeammateCommentsTakeResponse{}, fmt.Errorf("take teammate comments: %w", err)
	}
	l := &noteLease{agentID: p.AgentID, seq: p.DeliveredAtSeq, expires: b.now().Add(teammateLeaseTTL), dirs: map[string]string{}}
	var shown []whisperstore.WhisperEntry
	var foreign []string
	for _, e := range waiting {
		if len(shown) == sessionstream.MaxTeammateCommentsPerAppend {
			break
		}
		if b.leaseHolder(e.ID) != "" {
			continue
		}
		if e.Metadata[tcMetaConversationID] != p.ConversationID {
			b.logger.WarnContext(ctx, "teammate_comment_dropped_other_conversation", "comment_id", e.ID)
			foreign = append(foreign, e.ID)
			continue
		}
		dir, ok := "", false
		if live != nil {
			dir, ok = live.trusted(e.Metadata[tcMetaSessionDir])
		}
		if !ok {
			b.logger.WarnContext(ctx, "teammate_comment_ack_untrusted_dir", "comment_id", e.ID)
			continue
		}
		if err := livesession.CheckTeammateAckLog(dir); err != nil {
			b.logger.WarnContext(ctx, "teammate_comment_ack_log_unwritable", "comment_id", e.ID, "error", err)
			continue
		}
		l.ids = append(l.ids, e.ID)
		l.dirs[e.ID] = dir
		shown = append(shown, e)
	}
	if err := reg.DeleteTeammateComments(foreign...); err != nil {
		b.logger.WarnContext(ctx, "teammate_comment_drop_failed", "error", err)
	}
	if len(shown) == 0 {
		return TeammateCommentsTakeResponse{}, nil
	}
	if b.leases == nil {
		b.leases = map[string]*noteLease{}
	}
	id := uuid.NewString()
	b.leases[id] = l
	return TeammateCommentsTakeResponse{LeaseID: id, Entries: shown}, nil
}

// confirm commits a lease: the hook wrote the notes it names. Only now are the
// notes deleted and their delivery acks logged, in that order, so a failed
// delete leaves no sendable ack for a note that stays pending. A confirm after
// the lease expired still counts when no other lease took the notes since (the
// hook did show them); one a second hook has since leased is refused, so the
// second hook's confirm alone acks them. A note that left the inbox meanwhile
// (retracted, purged) is never acked.
func (b *teammateCommentInbox) confirm(ctx context.Context, p TeammateCommentsConfirmPayload) error {
	reg, live := b.registry(), b.live()
	if reg == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	l := b.leases[p.LeaseID]
	if l == nil || l.agentID != p.AgentID {
		return errors.New("unknown or already confirmed teammate comments lease")
	}
	delete(b.leases, p.LeaseID)
	pending, err := reg.PeekTeammateComments(l.agentID, teammateCommentPeekLimit*2)
	if err != nil {
		return fmt.Errorf("confirm teammate comments: %w", err)
	}
	var settle []string
	for _, e := range pending {
		if slices.Contains(l.ids, e.ID) && slices.Contains(p.CommentIDs, e.ID) && b.leaseHolder(e.ID) == "" {
			settle = append(settle, e.ID)
		}
	}
	if len(settle) == 0 {
		return nil
	}
	if err := reg.DeleteTeammateComments(settle...); err != nil {
		// no ack was written: the notes stay pending and are shown again
		b.logger.WarnContext(ctx, "teammate_comment_consume_failed", "error", err)
		return fmt.Errorf("consume teammate comments: %w", err)
	}
	kicked := map[string]bool{}
	for _, id := range settle {
		dir := l.dirs[id]
		if err := livesession.RecordTeammateCommentAck(dir, id, l.seq); err != nil {
			b.logger.WarnContext(ctx, "teammate_comment_ack_log_failed", "comment_id", id, "error", err)
			continue
		}
		if !kicked[dir] && live != nil {
			kicked[dir] = true
			live.request(ctx, dir)
		}
	}
	return nil
}

// purgeSession drops every note held for the stream a stopped recording wrote,
// keyed by its conversation id (read from the folder's own stream state under
// the sessions cache), not by the folder path stored in entries.
func (b *teammateCommentInbox) purgeSession(ctx context.Context, sessionName string) {
	reg, live := b.registry(), b.live()
	if reg == nil || live == nil || sessionName == "" {
		return
	}
	dir, ok := live.trusted(filepath.Join(live.sessionsDir, sessionName))
	if !ok {
		return
	}
	st, err := livesession.ReadState(dir)
	if err != nil || st.ConversationID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := reg.RetainTeammateComments(st.ConversationID, nil)
	if err != nil {
		b.logger.WarnContext(ctx, "teammate_comment_purge_failed", "session", sessionName, "error", err)
		return
	}
	if n > 0 {
		b.logger.InfoContext(ctx, "teammate_comments_purged", "session", sessionName, "count", n)
	}
}

// agentRecordingDir finds the agent whose live recording writes to dir.
func agentRecordingDir(projectRoot string, agents func() []string) func(dir string) string {
	return func(dir string) string {
		want := canonicalDir(dir)
		for _, id := range agents() {
			state, err := session.LoadRecordingStateForAgent(projectRoot, id)
			if err != nil || state == nil || state.StoppedAt != nil || state.SessionPath == "" {
				continue
			}
			if canonicalDir(state.SessionPath) == want {
				return id
			}
		}
		return ""
	}
}
