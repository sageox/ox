package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"sync"
	"time"

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

// TeammateCommentsTakeResponse carries the notes, oldest first. Entries are
// taken: a second request does not return them again.
type TeammateCommentsTakeResponse struct {
	Entries []whisperstore.WhisperEntry `json:"entries"`
}

// teammateCommentInbox stores received notes and hands them to the hook.
type teammateCommentInbox struct {
	logger   *slog.Logger
	registry func() *WhisperRegistry
	live     func() *LiveSessions
	// recipientFor names the agent recording the session in dir; "" when none.
	recipientFor func(dir string) string
	now          func() time.Time
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

// take returns the agent's notes for its current conversation, at most
// MaxTeammateCommentsPerAppend, and logs a delivery ack for each in its session
// folder. A note is consumed only when it can be shown AND acked: if its
// folder is untrusted or the ack cannot be written it stays pending for the
// next take. A note sent to another conversation is dropped, never acked here.
// The folder in an entry's metadata is not trusted input: it must be a direct
// child of the sessions cache, as for a drain request.
func (b *teammateCommentInbox) take(ctx context.Context, p TeammateCommentsTakePayload) ([]whisperstore.WhisperEntry, error) {
	if p.AgentID == "" || p.ConversationID == "" || p.DeliveredAtSeq < 1 {
		return nil, errors.New("agent_id, conversation_id and a delivered_at_seq of at least 1 are required")
	}
	reg, live := b.registry(), b.live()
	if reg == nil {
		return nil, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	waiting, err := reg.PeekTeammateComments(p.AgentID, teammateCommentPeekLimit)
	if err != nil {
		return nil, fmt.Errorf("take teammate comments: %w", err)
	}
	var shown []whisperstore.WhisperEntry
	var settled []string
	kicked := map[string]bool{}
	for _, e := range waiting {
		if len(shown) == sessionstream.MaxTeammateCommentsPerAppend {
			break
		}
		if e.Metadata[tcMetaConversationID] != p.ConversationID {
			b.logger.WarnContext(ctx, "teammate_comment_dropped_other_conversation", "comment_id", e.ID)
			settled = append(settled, e.ID)
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
		if err := livesession.RecordTeammateCommentAck(dir, e.ID, p.DeliveredAtSeq); err != nil {
			b.logger.WarnContext(ctx, "teammate_comment_ack_log_failed", "comment_id", e.ID, "error", err)
			continue
		}
		settled = append(settled, e.ID)
		shown = append(shown, e)
		if !kicked[dir] {
			kicked[dir] = true
			live.request(ctx, dir)
		}
	}
	if err := reg.DeleteTeammateComments(settled...); err != nil {
		// not shown: they stay pending and the ack lines already logged are
		// idempotent on the server
		b.logger.WarnContext(ctx, "teammate_comment_consume_failed", "error", err)
		return nil, fmt.Errorf("consume teammate comments: %w", err)
	}
	return shown, nil
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
