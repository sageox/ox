package daemon

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/config"
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
	tcMetaFromOwner      = "from_owner"
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

// teammateLeaseForget is how long past its expiry a lease is remembered for a
// late confirm before it is dropped to bound memory.
const teammateLeaseForget = 10 * time.Minute

// noteLease is one take's hold on notes: nothing is acked or deleted until it
// is confirmed.
type noteLease struct {
	agentID string
	conv    string
	seq     int64
	expires time.Time
	ids     []string
	dirs    map[string]string // comment id -> trusted session folder
}

// ownerNote is what the server said about an owner's note: its conversation
// and the hash of the body it sent, so a body rewritten in the database loses
// the owner label.
type ownerNote struct {
	conv string
	sum  [sha256.Size]byte
}

// teammateCommentInbox stores received notes and hands them to the hook.
type teammateCommentInbox struct {
	logger   *slog.Logger
	registry func() *WhisperRegistry
	live     func() *LiveSessions
	// recipientFor names the agent recording the session in dir; "" when none.
	recipientFor func(dir string) string
	now          func() time.Time
	// enabled is session.teammate_notes for the checkout that recorded the
	// session in dir (the same switch its opens carry); nil means on.
	enabled func(dir string) bool
	// leases are the takes awaiting confirmation, by lease id.
	leases map[string]*noteLease
	// owners are the notes the server itself marked as the session owner's,
	// by comment id. Only this decides the owner label: the whisper database
	// sits in the Ledger checkout, so a row or trigger planted there must not
	// lend a teammate's note the user's voice. Memory only; every listed drain
	// answer refreshes it, so after a restart an owner note is at worst labeled
	// a teammate's until the next drain.
	owners map[string]ownerNote
	// beforeTake runs as a take is about to wait for mu; tests use it to
	// overlap two takes deterministically.
	beforeTake func()
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
	if !b.isEnabled(dir) {
		// this checkout opted out: drop every note unshown and unacked, say so
		// once. Debug, not info: the open already told the server, so a note
		// here is one admitted before that, or an older server's.
		if _, logged := b.offLogged.LoadOrStore(st.ConversationID, true); !logged {
			b.logger.DebugContext(ctx, "teammate_notes_off_dropping", "conversation_id", st.ConversationID, "count", len(comments), "setting", "session.teammate_notes", "env", "OX_TEAMMATE_NOTES")
		}
		if _, err := reg.RetainTeammateComments(st.ConversationID, nil); err != nil {
			b.logger.WarnContext(ctx, "teammate_comment_replace_failed", "conversation_id", st.ConversationID, "error", err)
		} else {
			b.forget(st.ConversationID, nil)
		}
		return
	}
	keep := make([]string, 0, len(comments))
	for _, c := range comments {
		keep = append(keep, c.ID)
	}
	if n, err := reg.RetainTeammateComments(st.ConversationID, keep); err != nil {
		// nothing was removed, so every lease still holds what it held
		b.logger.WarnContext(ctx, "teammate_comment_replace_failed", "conversation_id", st.ConversationID, "error", err)
	} else {
		if n > 0 {
			b.logger.InfoContext(ctx, "teammate_comments_dropped_unshown", "conversation_id", st.ConversationID, "count", n)
		}
		b.forget(st.ConversationID, keep)
	}
	b.learnOwners(st.ConversationID, comments)
	if len(comments) == 0 {
		return
	}
	agentID := b.recipientFor(dir)
	if agentID == "" {
		b.logger.DebugContext(ctx, "teammate_comment_no_recipient", "session", st.SessionID, "count", len(comments))
		return
	}
	b.retryShown(ctx, reg, b.live(), func() ([]whisperstore.WhisperEntry, error) {
		return reg.PeekShownTeammateComments(agentID, teammateCommentPeekLimit)
	})
	for _, c := range comments[:min(len(comments), sessionstream.MaxTeammateCommentsPerAppend)] {
		meta := map[string]string{
			tcMetaCommentID: c.ID, tcMetaAuthorName: c.AuthorName,
			tcMetaSessionDir: dir, tcMetaConversationID: st.ConversationID,
		}
		if c.AnchorSeq != nil {
			meta[tcMetaAnchorSeq] = strconv.FormatInt(*c.AnchorSeq, 10)
		}
		// from_owner is never stored: the database is not trusted with it
		// (see owners), and labeled adds it when a lease hands the note out
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

func (b *teammateCommentInbox) isEnabled(dir string) bool { return b.enabled == nil || b.enabled(dir) }

// forget makes keep the whole pending set of conv for the leases and the owner
// marks, as RetainTeammateComments just did for the database. A leased note
// that left the inbox must leave its lease too: if the server lists the same
// id again it is stored anew, and the old lease's late confirm must not
// consume or ack that new entry. mu held.
func (b *teammateCommentInbox) forget(conv string, keep []string) {
	gone := func(id string) bool { return !slices.Contains(keep, id) }
	for _, l := range b.leases {
		if l.conv == conv {
			l.ids = slices.DeleteFunc(l.ids, gone)
		}
	}
	for id, o := range b.owners {
		if o.conv == conv && gone(id) {
			delete(b.owners, id)
		}
	}
}

// learnOwners records which listed notes the server marked as the owner's. mu held.
func (b *teammateCommentInbox) learnOwners(conv string, comments []sessionstream.TeammateComment) {
	for _, c := range comments {
		if !c.FromOwner {
			delete(b.owners, c.ID)
			continue
		}
		if b.owners == nil {
			b.owners = map[string]ownerNote{}
		}
		b.owners[c.ID] = ownerNote{conv: conv, sum: sha256.Sum256([]byte(c.Body))}
	}
}

// labeled is e as the hook may show it. from_owner is exactly "true" only when
// the server marked this note, in this conversation and with this very body,
// as the owner's; whatever the database row says is discarded. mu held.
func (b *teammateCommentInbox) labeled(e whisperstore.WhisperEntry) whisperstore.WhisperEntry {
	meta := maps.Clone(e.Metadata)
	if meta == nil {
		meta = map[string]string{}
	}
	delete(meta, tcMetaFromOwner)
	if o, ok := b.owners[e.ID]; ok && o.conv == meta[tcMetaConversationID] && o.sum == sha256.Sum256([]byte(e.Content)) {
		meta[tcMetaFromOwner] = "true"
	}
	e.Metadata = meta
	return e
}

// leaseHolder is the unexpired lease holding comment id, or "". mu held. An
// expired lease is skipped, not discarded: its owner may still confirm late, and
// only a lease that actually takes its notes supersedes it (supersede).
func (b *teammateCommentInbox) leaseHolder(id string) string {
	now := b.now()
	for leaseID, l := range b.leases {
		if !now.After(l.expires) && slices.Contains(l.ids, id) {
			return leaseID
		}
	}
	return ""
}

// heldByOtherThan is the unexpired lease other than self holding id, or "".
func (b *teammateCommentInbox) heldByOtherThan(id, self string) string {
	if h := b.leaseHolder(id); h != self {
		return h
	}
	return ""
}

// supersede takes ids away from every other lease: a new lease now holds them, so
// the old lease's late confirm must not consume or ack them. A lease left with
// no notes is forgotten, as is one long expired. mu held.
func (b *teammateCommentInbox) supersede(except string, ids []string) {
	now := b.now()
	for leaseID, l := range b.leases {
		if leaseID != except {
			l.ids = slices.DeleteFunc(l.ids, func(id string) bool { return slices.Contains(ids, id) })
		}
		if len(l.ids) == 0 || now.After(l.expires.Add(teammateLeaseForget)) {
			delete(b.leases, leaseID)
		}
	}
}

// lease holds the agent's notes for its current conversation, at most
// MaxTeammateCommentsPerAppend, and returns them with a lease id. It logs no
// ack and deletes nothing: a note is consumed only when the hook confirms it
// wrote it (confirm), so a hook that timed out or died loses nothing. A note
// is leased only when it could be acked on confirm: if its folder is untrusted
// or its ack log cannot be written it stays pending. Only this conversation's
// notes are looked at: another conversation's stay pending for it (the same
// agent can be between two streams), and its own retention removes them.
// Notes held by a live lease are skipped, so two hooks racing never show one
// note twice. The folder in an entry's metadata is not trusted input: it must
// be a direct child of the sessions cache, as for a drain request.
func (b *teammateCommentInbox) lease(ctx context.Context, p TeammateCommentsTakePayload) (TeammateCommentsTakeResponse, error) {
	if p.AgentID == "" || p.ConversationID == "" || p.DeliveredAtSeq < 1 {
		return TeammateCommentsTakeResponse{}, errors.New("agent_id, conversation_id and a delivered_at_seq of at least 1 are required")
	}
	reg, live := b.registry(), b.live()
	if reg == nil {
		return TeammateCommentsTakeResponse{}, nil
	}
	if b.beforeTake != nil {
		b.beforeTake()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.retryShown(ctx, reg, live, func() ([]whisperstore.WhisperEntry, error) {
		return reg.PeekShownTeammateComments(p.AgentID, teammateCommentPeekLimit)
	})
	waiting, err := reg.PeekTeammateCommentsForConversation(p.AgentID, p.ConversationID, teammateCommentPeekLimit)
	if err != nil {
		return TeammateCommentsTakeResponse{}, fmt.Errorf("take teammate comments: %w", err)
	}
	l := &noteLease{agentID: p.AgentID, conv: p.ConversationID, seq: p.DeliveredAtSeq, expires: b.now().Add(teammateLeaseTTL), dirs: map[string]string{}}
	var shown []whisperstore.WhisperEntry
	// the switch is read under mu, per session folder and once per take: a
	// take that waited for mu while notes were turned off must see the "off"
	enabled := map[string]bool{}
	for _, e := range waiting {
		if len(shown) == sessionstream.MaxTeammateCommentsPerAppend {
			break
		}
		if b.leaseHolder(e.ID) != "" {
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
		on, seen := enabled[dir]
		if !seen {
			on = b.isEnabled(dir)
			enabled[dir] = on
		}
		if !on {
			continue // the session's checkout turned notes off; the next drain drops it
		}
		if err := b.checkReceiptLog(live, p.ConversationID); err != nil {
			b.logger.WarnContext(ctx, "teammate_comment_ack_log_unwritable", "comment_id", e.ID, "error", err)
			continue
		}
		l.ids = append(l.ids, e.ID)
		l.dirs[e.ID] = dir
		shown = append(shown, b.labeled(e))
	}
	if len(shown) == 0 {
		return TeammateCommentsTakeResponse{}, nil
	}
	if b.leases == nil {
		b.leases = map[string]*noteLease{}
	}
	id := uuid.NewString()
	b.leases[id] = l
	b.supersede(id, l.ids)
	return TeammateCommentsTakeResponse{LeaseID: id, Entries: shown}, nil
}

// confirm commits a lease: the hook wrote the notes it names. A note goes
// pending -> leased -> shown (receipt owed) -> done:
//
//  1. mark the notes shown (with the seq they were shown at): from here they are
//     never offered again, whatever else fails. If this fails nothing was acked
//     and the notes stay leased (they expire and are shown again);
//  2. log each receipt in its session folder;
//  3. only after a receipt is durably logged, delete its note.
//
// A receipt that cannot be logged leaves its note shown, and retryShown logs it
// on a later take or drain. A confirm after the lease expired still counts when
// no other lease took the notes since (the hook did show them). A note that
// left the inbox meanwhile (retracted, purged) is never acked.
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
	pending, err := reg.PeekTeammateCommentsForConversation(l.agentID, l.conv, teammateCommentPeekLimit*2)
	if err != nil {
		return fmt.Errorf("confirm teammate comments: %w", err)
	}
	var settle []string
	for _, e := range pending {
		if slices.Contains(l.ids, e.ID) && slices.Contains(p.CommentIDs, e.ID) && b.heldByOtherThan(e.ID, p.LeaseID) == "" {
			settle = append(settle, e.ID)
		}
	}
	if len(settle) == 0 {
		delete(b.leases, p.LeaseID)
		return nil
	}
	if err := reg.MarkTeammateCommentsShown(l.seq, settle...); err != nil {
		b.logger.WarnContext(ctx, "teammate_comment_mark_shown_failed", "error", err)
		return fmt.Errorf("mark teammate comments shown: %w", err)
	}
	delete(b.leases, p.LeaseID)
	for _, id := range settle {
		delete(b.owners, id) // shown: never leased again
	}
	b.logReceipts(ctx, reg, live, l.conv, l.seq, l.dirs, settle)
	return nil
}

// logReceipts logs the receipt of each shown note in its folder, then deletes
// the notes whose receipt was recorded. mu held.
func (b *teammateCommentInbox) logReceipts(ctx context.Context, reg *WhisperRegistry, live *LiveSessions, conv string, seq int64, dirs map[string]string, ids []string) {
	var done []string
	kicked := map[string]bool{}
	rdir, err := b.receiptsDir(live, conv)
	if err != nil {
		b.logger.WarnContext(ctx, "teammate_comment_ack_log_failed", "conversation_id", conv, "error", err)
		return
	}
	for _, id := range ids {
		dir := dirs[id]
		if err := livesession.RecordTeammateCommentAck(rdir, id, seq); err != nil {
			// the note stays shown: it is never offered again, and retryShown
			// logs the receipt when the folder is writable again
			b.logger.WarnContext(ctx, "teammate_comment_ack_log_failed", "comment_id", id, "error", err)
			continue
		}
		done = append(done, id)
		if dir != "" && !kicked[dir] && live != nil {
			kicked[dir] = true
			live.request(ctx, dir)
		}
	}
	if err := reg.DeleteTeammateComments(done...); err != nil {
		// the receipts are logged, so the delete can wait: the notes stay shown
		b.logger.WarnContext(ctx, "teammate_comment_consume_failed", "error", err)
	}
}

// retryShown logs the receipts of notes that were shown but whose receipt could
// not be logged then, and deletes them once it is. The receipt log is
// machine-local (keyed by conversation), so a note whose session folder is no
// longer trusted or present still gets its receipt; the folder is only used to
// kick a drain. mu held.
func (b *teammateCommentInbox) retryShown(ctx context.Context, reg *WhisperRegistry, live *LiveSessions, peek func() ([]whisperstore.WhisperEntry, error)) {
	shown, err := peek()
	if err != nil {
		b.logger.WarnContext(ctx, "teammate_comment_shown_peek_failed", "error", err)
		return
	}
	for _, e := range shown {
		seq, perr := strconv.ParseInt(e.Metadata["shown_seq"], 10, 64)
		conv := e.Metadata[tcMetaConversationID]
		if perr != nil || seq < 1 || conv == "" {
			b.logger.WarnContext(ctx, "teammate_comment_shown_unrecoverable", "comment_id", e.ID)
			continue
		}
		dir := ""
		if live != nil {
			dir, _ = live.trusted(e.Metadata[tcMetaSessionDir])
		}
		b.logReceipts(ctx, reg, live, conv, seq, map[string]string{e.ID: dir}, []string{e.ID})
	}
}

// teammateShownDrainLimit bounds one drain of owed receipts. One query of this
// size, not a loop of 20-row pages: a head of rows that cannot be recovered must
// not strand the receipts behind it.
const teammateShownDrainLimit = 1000

// drainAllShown logs every owed receipt (up to teammateShownDrainLimit), for the
// sweep that also flushes closed sessions: a stream that closed with receipts
// still owed is otherwise retried by nothing, since no hook or listing follows.
func (b *teammateCommentInbox) drainAllShown(ctx context.Context) {
	reg, live := b.registry(), b.live()
	if reg == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.retryShown(ctx, reg, live, func() ([]whisperstore.WhisperEntry, error) {
		return reg.AllShownTeammateComments(teammateShownDrainLimit)
	})
}

// receiptsDir is the conversation's machine-local receipt folder.
func (b *teammateCommentInbox) receiptsDir(live *LiveSessions, conv string) (string, error) {
	if live == nil {
		return "", errors.New("no live sessions: nowhere to keep receipts")
	}
	return live.receiptsDir(conv)
}

func (b *teammateCommentInbox) checkReceiptLog(live *LiveSessions, conv string) error {
	rdir, err := b.receiptsDir(live, conv)
	if err != nil {
		return err
	}
	return livesession.CheckTeammateAckLog(rdir)
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
	// a receipt still owed is logged before the stream's folder stops being
	// swept; only then are the rest of its notes dropped
	b.retryShown(ctx, reg, live, func() ([]whisperstore.WhisperEntry, error) {
		return reg.ShownTeammateCommentsForConversation(st.ConversationID, teammateShownDrainLimit)
	})
	n, err := reg.RetainTeammateComments(st.ConversationID, nil)
	if err != nil {
		b.logger.WarnContext(ctx, "teammate_comment_purge_failed", "session", sessionName, "error", err)
		return
	}
	b.forget(st.ConversationID, nil)
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

// teammateNotesSwitch resolves session.teammate_notes for the checkout that
// recorded the session in dir. Clones and worktrees of one repo share this
// daemon, and each checkout's hook reads its own setting, so the open and the
// inbox must too: a session recorded in a checkout that opted out must not be
// offered notes because the daemon started in one that did not. The
// recording's workspace counts only when it is a checkout of the daemon's own
// repo; otherwise (marker gone after the recording stopped, unreadable, or
// naming somewhere else) the daemon's checkout decides.
func teammateNotesSwitch(daemonRoot string) func(dir string) bool {
	repoID := config.GetRepoID(daemonRoot)
	return func(dir string) bool {
		root := daemonRoot
		if st, err := session.ReadRecordingStateFile(dir); err == nil && st != nil && st.WorkspacePath != "" &&
			repoID != "" && config.GetRepoID(st.WorkspacePath) == repoID {
			root = st.WorkspacePath
		}
		return config.TeammateNotesEnabled(root)
	}
}
