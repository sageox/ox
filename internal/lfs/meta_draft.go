package lfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/sessionid"
)

// ErrNotDraft is returned when a draft write targets a directory whose
// existing meta.json is NOT a draft. That directory holds a finalized session
// and stamping draft=true over it would be a downgrade: every consumer would
// start treating real, uploaded turn data as provisional, and finalize would
// purge the directory the next time it ran.
var ErrNotDraft = errors.New("session directory holds a finalized session, not a draft")

// ErrDraftDirNotEmpty is returned when a draft write targets a directory that
// already contains the session transcript. Real turn data is present, so this
// is not a placeholder: labeling it draft:true would claim a zero-turn session
// that demonstrably has turns, and every draft-aware consumer (doctor's orphan
// sweep, the daemon's anti-entropy skip, abort's delete) would then treat real
// work as disposable.
//
// Scoped to raw.jsonl deliberately, NOT to all of ContentFiles. The other
// artifacts — summary.md, session.md, plan.md, context-trace.jsonl — can
// legitimately appear in a draft directory because the SageOx server may author
// a summary against the zero-turn draft and push it, and a `git pull --rebase`
// folds that into our tree. That case is anticipated and handled by the
// finalize-time purge; refusing to refresh the counters because of it would
// silently freeze server-visible progress for the rest of the session.
var ErrDraftDirNotEmpty = errors.New("session directory already contains the transcript")

// draftBlockingFile is the one artifact whose presence means "this is not a
// placeholder". It is also the file the LFS invariant is about: real bytes at
// the git-tracked <ledger>/sessions/<name>/raw.jsonl break linkage for the
// whole team.
const draftBlockingFile = "raw.jsonl"

// DraftInput is everything needed to author a draft placeholder.
//
// Deliberately identity + counters only. There is NO field that can hold
// transcript-derived text — not a title derived from the first user message,
// not a summary, not a preview. The privacy property of a draft is therefore a
// type-level guarantee rather than a code-review one: a caller physically
// cannot pass turn content through this struct.
//
// Note the absence of Title in particular. RecordingState carries a title
// derived from the user's first message, and an implementer reaching for "make
// the draft look nicer in the UI" would leak that first prompt into a
// git-tracked, pushed file at turn 2 — before the user has any indication the
// session is being shared. The server renders drafts by session name and
// counters; that is sufficient and it is safe.
type DraftInput struct {
	SessionName            string
	SessionID              string // ses_<UUIDv7> minted at StartRecording — REQUIRED
	ContinuedFromSessionID string
	Username               string // privacy-safe display name via identity.AttributionDisplayName(); never an email
	UserID                 string
	RepoID                 string
	AgentID                string
	AgentType              string
	Model                  string
	CreatedAt              time.Time
	TurnCount              int
	EntryCount             int
	Now                    time.Time
}

// Validate checks the required identity fields. A draft without a valid
// ses_ id is worthless — the entire point is that /c/<session_id> resolves —
// and one with a malformed id would produce a URL that never matches the
// finalized session's, silently splitting the record in two server-side.
func (in DraftInput) Validate() error {
	if in.SessionName == "" {
		return fmt.Errorf("draft requires a session name")
	}
	if !sessionid.IsValidSessionID(in.SessionID) {
		return fmt.Errorf("draft requires a valid ses_ session id, got %q", in.SessionID)
	}
	if in.ContinuedFromSessionID != "" && !sessionid.IsValidSessionID(in.ContinuedFromSessionID) {
		return fmt.Errorf("draft continuation requires a valid ses_ session id, got %q", in.ContinuedFromSessionID)
	}
	return nil
}

// WriteDraftSessionMeta creates or refreshes the draft placeholder meta.json in
// sessionDir.
//
// Goes through MutateSessionMeta so it holds the same advisory flock every
// other meta.json writer holds and can never interleave with a concurrent
// finalize. This is also why it must NOT be added to the
// `make check-session-meta-rmw` allowlist — it is already on the safe path.
//
// Two refusals, both of which make the caller safe to retry blindly:
//
//   - ErrNotDraft when an existing meta.json is not a draft. Covers the
//     finalize-landed-between-decision-and-write race, and a session-name
//     collision with a teammate's finalized session pulled in from the remote.
//   - ErrDraftDirNotEmpty when the transcript (raw.jsonl) already exists in the
//     directory. Covers a half-completed finalize and any path that has started
//     writing real turn data here. Scoped to raw.jsonl only — see the error's
//     doc for why server-authored summary artifacts must NOT block a refresh.
//
// The write sets Files to an empty (non-nil) map so downstream manifest
// consumers see a well-formed-but-empty manifest rather than a nil one, and so
// draft-ness cannot be confused with "meta.json we failed to parse".
func WriteDraftSessionMeta(ctx context.Context, sessionDir string, in DraftInput) error {
	if err := in.Validate(); err != nil {
		return err
	}

	// Refuse if the real transcript already exists here. Checked before the
	// lock because it is a property of the directory, not of meta.json, and
	// because a caller that trips this wants the error regardless of whether
	// meta.json is currently writable.
	if _, err := os.Stat(filepath.Join(sessionDir, draftBlockingFile)); err == nil {
		return fmt.Errorf("%w: %s", ErrDraftDirNotEmpty, draftBlockingFile)
	}

	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return fmt.Errorf("create draft session dir: %w", err)
	}

	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()

	return MutateSessionMeta(ctx, sessionDir, func(current *SessionMeta) (*SessionMeta, error) {
		if current != nil && !current.IsDraft() {
			return nil, fmt.Errorf("%w: %s", ErrNotDraft, in.SessionName)
		}

		// Preserve the id already published rather than the one we were
		// handed. A refresh that adopted the caller's id would rotate the
		// /c/ link mid-session if the caller's recording state had been
		// rebuilt — the exact failure the whole preserved-id machinery exists
		// to prevent.
		sessionID := in.SessionID
		if current != nil && current.SessionID != "" {
			sessionID = current.SessionID
		}
		continuedFromSessionID := in.ContinuedFromSessionID
		if current != nil && current.ContinuedFromSessionID != "" {
			continuedFromSessionID = current.ContinuedFromSessionID
		}

		next := &SessionMeta{
			Version:                "1.0",
			SessionName:            in.SessionName,
			SessionID:              sessionID,
			ContinuedFromSessionID: continuedFromSessionID,
			Username:               in.Username,
			UserID:                 in.UserID,
			RepoID:                 in.RepoID,
			AgentID:                in.AgentID,
			AgentType:              in.AgentType,
			Model:                  in.Model,
			CreatedAt:              in.CreatedAt,
			EntryCount:             in.EntryCount,
			Draft:                  true,
			TurnCount:              in.TurnCount,
			UpdatedAt:              &now,
			Files:                  map[string]FileRef{},
		}
		if current != nil && !current.CreatedAt.IsZero() {
			next.CreatedAt = current.CreatedAt
		}

		// Counters are monotonic. A refresh racing an older in-flight refresh
		// (or a recording state that lost an increment — the state file is an
		// unlocked load-modify-save) must never walk the server-visible
		// progress backwards, which would read as "the session is un-doing
		// work".
		if current != nil {
			if current.TurnCount > next.TurnCount {
				next.TurnCount = current.TurnCount
			}
			if current.EntryCount > next.EntryCount {
				next.EntryCount = current.EntryCount
			}
		}

		return next, nil
	})
}
