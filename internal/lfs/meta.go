package lfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/fileutil"
)

// legacySessionNamespace is the UUIDv5 namespace for synthesizing session
// IDs for recordings predating the SessionID field. Generated once on
// 2026-05-01; MUST NEVER change.
//
// Changing this value would change EffectiveSessionID for every legacy
// recording in every ledger, breaking server-side dedup, cross-machine
// references, and any external system that has cached the value. Treat
// as a load-bearing constant — same status as a database table name. To
// regenerate it would require a coordinated migration across every ledger.
//
// The SageOx server performs the same UUIDv5 derivation with this same
// namespace when ingesting a meta.json with no session_id, so client and
// server always compute the same ses_-prefixed value for every legacy
// recording. If this constant changes, the server must change in lockstep.
var legacySessionNamespace = uuid.MustParse("5e6238b7-9403-4ee4-b5ec-8a6d37a5de14")

// SessionMeta is the git-tracked metadata + OID manifest for a session.
// Stored as meta.json in each session folder. When Files is populated,
// WriteSessionMeta also writes LFS pointer files (standard git-lfs naming)
// to replace content files, preventing LFS garbage collection.
type SessionMeta struct {
	Version     string `json:"version"` // "1.0"
	SessionName string `json:"session_name"`
	Username    string `json:"username"` // privacy-safe display name — via identity.AttributionDisplayName(). Shared in ledger. NOT an email.
	UserID      string `json:"user_id,omitempty"`
	AgentID     string `json:"agent_id"`

	// SessionID is the globally unique, content-bound identifier for THIS
	// specific recording. Format: "ses_<UUIDv7>". Populated at session
	// creation time and never regenerated. Independent of path/name so
	// renames, moves, and re-imports do not change identity.
	//
	// Do NOT confuse with OxSID (per-agent-instance, reused across many
	// recordings during a 24h prime window) or AgentID (per-agent, reused
	// across all of that agent's recordings).
	//
	// # Backwards compatibility
	//
	// Pre-existing meta.json files do not carry this field. The compat model:
	//
	//   - JSON tag is `omitempty` — older readers see no schema change;
	//     newer readers see "" for legacy sessions.
	//   - Version is NOT bumped — additive optional field, no breaking
	//     change to the on-disk format.
	//   - Legacy sessions on disk are NEVER backfilled automatically. The
	//     deterministic EffectiveSessionID() helper synthesizes a stable
	//     ses_<UUIDv5> from (RepoID, SessionName) on every read, so
	//     consumers always get a ses_-prefixed value without writing to
	//     old meta.json. Doctor offers an opt-in backfill (FixLevelSuggested).
	//   - All consumers MUST go through EffectiveSessionID() rather than
	//     reading SessionID directly. Direct reads return "" for legacy
	//     and silently break dedup/lookup.
	SessionID string `json:"session_id,omitempty"`

	AgentType           string    `json:"agent_type"` // "claude-code", "cursor", etc.
	Model               string    `json:"model,omitempty"`
	Title               string    `json:"title,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	EntryCount          int       `json:"entry_count,omitempty"`
	Summary             string    `json:"summary,omitempty"`
	StopReason          string    `json:"stop_reason,omitempty"` // how session ended: "stopped", "aborted", "recovered", ""
	RepoID              string    `json:"repo_id,omitempty"`
	SageoxScore         *float64  `json:"sageox_score,omitempty"`          // agent's self-reported contribution score (0.0-1.0)
	SageoxScoreCategory string    `json:"sageox_score_category,omitempty"` // named category: none, minor, moderate, significant, critical
	SageoxScoreReason   string    `json:"sageox_score_reason,omitempty"`   // detailed explanation of SageOx influence

	// SummaryStatus and ValidationError mirror the same-named fields on
	// pkg/sessionsummary.SummarizeResponse. SummaryStatus is the
	// structured signal — readers should prefer it over sniffing
	// Summary for sentinel error strings. ValidationError is ops-only;
	// it MUST NEVER be rendered as a user-visible session title or
	// summary. See ox-qqka for the leak this prevents.
	//
	// Both are omitempty so older readers and older on-disk meta.json
	// files keep working unchanged.
	SummaryStatus   string `json:"summary_status,omitempty"`
	ValidationError string `json:"validation_error,omitempty"`

	Files map[string]FileRef `json:"files"` // OID manifest: filename -> ref
}

// FileRef identifies a session content file by storage backend, OID
// (for LFS files), and size.
//
// # Storage tag
//
// The Storage field declares which backend holds the bytes:
//
//   - StorageLFS — content is in the LFS blob store, identified by OID.
//     The in-place git-tracked file is a ~130-byte pointer.
//   - StorageGit — content is committed directly to git as a regular blob
//     (small JSON, e.g. summary.json). OID is empty.
//
// # Backwards compatibility
//
// Pre-Storage meta.json files have FileRef{OID, Size} and no Storage field.
// JSON unmarshalling leaves Storage="" on those entries; the reader's
// canonical helper FileRef.EffectiveStorage() promotes empty to StorageLFS
// (the only legal value at the time those files were written). All call
// sites MUST go through EffectiveStorage() rather than reading f.Storage
// directly. Writers set Storage explicitly for new entries; legacy entries
// stay untouched on disk until something rewrites the manifest.
//
// See ADR-016 (delegation) and meta.json manifest refactor (bd ox-9mrk).
type FileRef struct {
	Storage string `json:"storage,omitempty"` // "lfs" | "git"; empty == "lfs" for legacy reads
	OID     string `json:"oid,omitempty"`     // "sha256:<hex>" — populated only for Storage=="lfs"
	Size    int64  `json:"size"`              // bytes (always populated)
}

// FileRef storage backends. Use these constants rather than string literals.
const (
	StorageLFS = "lfs"
	StorageGit = "git"
)

// EffectiveStorage returns the storage backend for this FileRef, promoting
// empty (legacy meta.json with no storage tag) to StorageLFS. All readers
// that branch on storage MUST use this helper.
func (f FileRef) EffectiveStorage() string {
	if f.Storage == "" {
		return StorageLFS
	}
	return f.Storage
}

// IsLFS reports whether this FileRef is stored in LFS (including legacy
// entries with no Storage field).
func (f FileRef) IsLFS() bool {
	return f.EffectiveStorage() == StorageLFS
}

// IsGit reports whether this FileRef is stored directly in git (no LFS).
func (f FileRef) IsGit() bool {
	return f.EffectiveStorage() == StorageGit
}

// HydrationStatus describes whether a session's content files are present locally.
type HydrationStatus string

const (
	// HydrationStatusHydrated means all content files are present locally.
	HydrationStatusHydrated HydrationStatus = "hydrated"
	// HydrationStatusDehydrated means no content files are present (only meta.json).
	HydrationStatusDehydrated HydrationStatus = "dehydrated"
	// HydrationStatusPartial means some content files are present.
	HydrationStatusPartial HydrationStatus = "partial"
)

// SessionMetaBuilder constructs SessionMeta with required fields and optional setters.
type SessionMetaBuilder struct {
	meta SessionMeta
}

// NewSessionMeta creates a builder with required fields pre-filled.
func NewSessionMeta(sessionName, username, agentID, agentType string, createdAt time.Time) *SessionMetaBuilder {
	return &SessionMetaBuilder{
		meta: SessionMeta{
			Version:     "1.0",
			SessionName: sessionName,
			Username:    username,
			AgentID:     agentID,
			AgentType:   agentType,
			CreatedAt:   createdAt,
			Files:       make(map[string]FileRef),
		},
	}
}

func (b *SessionMetaBuilder) Model(m string) *SessionMetaBuilder {
	b.meta.Model = m
	return b
}

func (b *SessionMetaBuilder) Title(t string) *SessionMetaBuilder {
	b.meta.Title = t
	return b
}

func (b *SessionMetaBuilder) Summary(s string) *SessionMetaBuilder {
	b.meta.Summary = s
	return b
}

func (b *SessionMetaBuilder) EntryCount(n int) *SessionMetaBuilder {
	b.meta.EntryCount = n
	return b
}

func (b *SessionMetaBuilder) UserID(id string) *SessionMetaBuilder {
	b.meta.UserID = id
	return b
}

func (b *SessionMetaBuilder) RepoID(id string) *SessionMetaBuilder {
	b.meta.RepoID = id
	return b
}

// SessionID stamps the per-recording ses_<UUIDv7>. Caller is expected to
// pass sessionid.GenerateSessionID() at session creation time. Never
// regenerated: MutateSessionMeta-based RMW paths preserve it via JSON
// round-trip.
func (b *SessionMetaBuilder) SessionID(id string) *SessionMetaBuilder {
	b.meta.SessionID = id
	return b
}

func (b *SessionMetaBuilder) StopReason(reason string) *SessionMetaBuilder {
	b.meta.StopReason = reason
	return b
}

func (b *SessionMetaBuilder) WithFiles(f map[string]FileRef) *SessionMetaBuilder {
	b.meta.Files = f
	return b
}

func (b *SessionMetaBuilder) SageoxScore(score float64, category, reason string) *SessionMetaBuilder {
	b.meta.SageoxScore = &score
	b.meta.SageoxScoreCategory = category
	b.meta.SageoxScoreReason = reason
	return b
}

// SummaryStatus stamps the lifecycle status (ok / pending /
// failed_validation / unrecoverable). Use the SummaryStatus* constants
// from pkg/sessionsummary, mirrored here.
func (b *SessionMetaBuilder) SummaryStatus(status string) *SessionMetaBuilder {
	b.meta.SummaryStatus = status
	return b
}

// ValidationError records the ops-facing validator diagnostic. Callers
// must never put this string into Title or Summary — it is engineer-
// visible only. See ox-qqka for the leak this prevents.
func (b *SessionMetaBuilder) ValidationError(msg string) *SessionMetaBuilder {
	b.meta.ValidationError = msg
	return b
}

// Build returns the constructed SessionMeta.
func (b *SessionMetaBuilder) Build() *SessionMeta {
	return &b.meta
}

const metaFilename = "meta.json"

// WriteSessionMeta writes meta.json to the given session directory.
// When meta.Files is populated, also replaces content files with LFS pointer
// files (standard git-lfs naming). Pointer write failures are non-fatal —
// session data is safe in LFS + meta.json regardless.
//
// Callers that need to push content files to git BEFORE replacing them with
// pointer stubs should use WriteSessionMetaOnly followed by WritePointerFiles
// after a successful push.
func WriteSessionMeta(sessionPath string, meta *SessionMeta) error {
	if err := WriteSessionMetaOnly(sessionPath, meta); err != nil {
		return err
	}

	// replace content files with LFS pointer files for GC protection
	if len(meta.Files) > 0 {
		if _, err := WritePointerFiles(sessionPath, meta.Files); err != nil {
			slog.Warn("LFS pointer file write failed", "error", err, "path", sessionPath)
		}
	}

	return nil
}

// WriteSessionMetaOnly writes meta.json without replacing content files with
// LFS pointer stubs. Use this when content files must remain intact until a
// successful git push — call WritePointerFiles separately after the push so that
// push failure never leaves a session with pointer stubs but no remote copy.
//
// The on-disk write is atomic via fileutil.AtomicWriteBytes (random temp +
// fsync + rename + parent dir fsync). The previous implementation used the
// literal "meta.json.tmp" as the temp path, which raced with concurrent
// writers — both rename'd the same temp inode and one writer saw ENOENT.
// Random suffix per write closes that loophole.
//
// Callers that mutate meta.json (read → modify → write) MUST do the entire
// RMW under MutateSessionMeta so the daemon and CLI don't lose each other's
// fields. WriteSessionMetaOnly itself is unlocked for backwards compat;
// MutateSessionMeta is the safe path.
func WriteSessionMetaOnly(sessionPath string, meta *SessionMeta) error {
	if meta == nil {
		return fmt.Errorf("nil session meta")
	}

	// Boundary guard: refuse to persist a meta.json whose user-visible
	// Title or Summary carries a known validator/error string. This is
	// the cross-layer invariant from ox-4ggw — even if a producer-side
	// fix regresses (ox-qqka, ox-wstd), the writer rejects the leak so
	// it never reaches consumers.
	if err := meta.Validate(); err != nil {
		return fmt.Errorf("session meta failed invariant: %w", err)
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session meta: %w", err)
	}

	metaPath := filepath.Join(sessionPath, metaFilename)
	if err := fileutil.AtomicWriteBytes(metaPath, data, 0o644); err != nil {
		return fmt.Errorf("write session meta: %w", err)
	}
	return nil
}

// MutateSessionMeta runs an exclusive read-modify-write under an advisory
// flock on meta.json. Any code path where the daemon and CLI both mutate
// the manifest (session_finalize summary write, session_upload artifact
// registration) MUST go through this so they serialize at the FS level.
//
// The mutator is given a fresh copy of the on-disk SessionMeta to mutate
// in place. If the file does not exist, mutator receives nil and may
// return a freshly-constructed *SessionMeta to write; returning nil
// without writing is a no-op (useful for "only update if exists"
// guards). Returning an error aborts the write.
func MutateSessionMeta(ctx context.Context, sessionPath string, mutate func(*SessionMeta) (*SessionMeta, error)) error {
	metaPath := filepath.Join(sessionPath, metaFilename)
	return fileutil.WithFileLock(ctx, metaPath, func() error {
		meta, readErr := ReadSessionMeta(sessionPath)
		if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
			return readErr
		}
		// pass nil if file truly missing; mutator decides whether to seed
		var arg *SessionMeta
		if readErr == nil {
			arg = meta
		}
		out, mutErr := mutate(arg)
		if mutErr != nil {
			return mutErr
		}
		if out == nil {
			return nil // mutator chose not to write
		}
		return WriteSessionMetaOnly(sessionPath, out)
	})
}

// ReadSessionMeta reads meta.json from the given session directory.
func ReadSessionMeta(sessionPath string) (*SessionMeta, error) {
	metaPath := filepath.Join(sessionPath, metaFilename)

	data, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Wrap with %w so callers can use errors.Is(err, fs.ErrNotExist)
			// or os.IsNotExist(err). Pre-fix this returned a plain
			// fmt.Errorf("meta.json not found...") which broke the
			// IsNotExist check at every caller (e.g. the repair tool's
			// "skip unfinished sessions" path silently flagged them as
			// errors instead).
			return nil, fmt.Errorf("meta.json not found in %s: %w", sessionPath, err)
		}
		return nil, fmt.Errorf("read session meta: %w", err)
	}

	var meta SessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse session meta: %w", err)
	}

	return &meta, nil
}

// CheckHydrationStatus checks which content files are present as real content
// (not LFS pointers) for a session. Files that are missing or contain only an
// LFS pointer are considered dehydrated.
func CheckHydrationStatus(sessionPath string, meta *SessionMeta) HydrationStatus {
	if meta == nil || len(meta.Files) == 0 {
		return HydrationStatusDehydrated
	}

	hydrated := 0
	total := len(meta.Files)

	for filename := range meta.Files {
		filePath := filepath.Join(sessionPath, filename)
		if _, err := os.Stat(filePath); err != nil {
			continue // missing = dehydrated
		}
		if !IsPointerFile(filePath) {
			hydrated++ // real content, not a pointer
		}
	}

	switch hydrated {
	case 0:
		return HydrationStatusDehydrated
	case total:
		return HydrationStatusHydrated
	default:
		return HydrationStatusPartial
	}
}

// CheckHydrationStatusWithCache checks hydration across the primary session path
// and a cache path. A file counts as hydrated if it exists as real content (not a
// pointer) in the primary path OR exists in the cache path (cache never has pointers).
func CheckHydrationStatusWithCache(sessionPath, cachePath string, meta *SessionMeta) HydrationStatus {
	if meta == nil || len(meta.Files) == 0 {
		return HydrationStatusDehydrated
	}

	hydrated := 0
	total := len(meta.Files)

	for filename := range meta.Files {
		// check primary path
		filePath := filepath.Join(sessionPath, filename)
		if info, err := os.Stat(filePath); err == nil && info.Size() > 0 && !IsPointerFile(filePath) {
			hydrated++
			continue
		}
		// check cache path
		if cachePath != "" {
			cacheFilePath := filepath.Join(cachePath, filename)
			if _, err := os.Stat(cacheFilePath); err == nil {
				hydrated++
				continue
			}
		}
	}

	switch hydrated {
	case 0:
		return HydrationStatusDehydrated
	case total:
		return HydrationStatusHydrated
	default:
		return HydrationStatusPartial
	}
}

// ResolveContentPath returns a path that holds REAL session content (not an
// LFS pointer stub) for filename, choosing in this order:
//
//  1. cacheDir/filename — the canonical hydrated location for content owned
//     by other team members. Cache files are full content by definition.
//  2. sessionDir/filename — only when it exists as real content (not a
//     pointer). This case applies to a coworker's own freshly-recorded
//     session before LFS upload; for any session synced from the ledger,
//     the in-place file MUST be a pointer.
//
// Returns "" when neither location has hydrated content (caller must hydrate).
//
// # CACHE-ONLY DESIGN — DO NOT WRITE TO sessionDir/filename
//
// This resolver enforces a load-bearing invariant: the in-place git-tracked
// file MUST stay as an LFS pointer for any session synced from the ledger.
// The cache is where hydrated content lives. Two failure modes if this
// invariant is broken:
//
//   - commitAndPushLedger globs *.jsonl/*.html/*.md inside the session dir
//     and stages whatever is there. A hydrated in-place raw.jsonl gets
//     committed as a regular git blob, replacing the LFS pointer reference
//     and breaking LFS linkage. The ledger then rejects future pushes for
//     any session whose meta.json references the now-orphaned OID.
//
//   - The daemon's session-finalize anti-entropy skips sessions whose
//     raw.jsonl IS a pointer (internal/daemon/agentwork/session_finalize.go).
//     When in-place is full content, the skip doesn't apply and the daemon
//     can re-finalize already-finalized sessions, racing with concurrent
//     regen and clobbering good summaries with failure-marker stubs.
//
// Both failures were observed in the 2026-04-25 Phase 2 regen:
// 31 of 71 sessions had their summaries clobbered, 2 had raw.jsonl
// committed as full git blobs. See bd ox-4ncz for the post-mortem.
//
// All readers (regenerate, view, lint, token-optimize) MUST consult this
// resolver. Hydration paths (downloadFileFromLFS, hydrateFromLedger) MUST
// write only to cacheDir.
func ResolveContentPath(sessionDir, cacheDir, filename string) string {
	if cacheDir != "" {
		cachePath := filepath.Join(cacheDir, filename)
		if info, err := os.Stat(cachePath); err == nil && info.Size() > 0 {
			return cachePath
		}
	}
	inPlace := filepath.Join(sessionDir, filename)
	if info, err := os.Stat(inPlace); err == nil && info.Size() > 0 && !IsPointerFile(inPlace) {
		return inPlace
	}
	return ""
}

// NewFileRef creates a FileRef for an LFS-stored file from its content
// bytes. Computes the OID and stamps Storage=lfs explicitly so future
// readers don't need to fall back to the empty-means-lfs legacy rule.
func NewFileRef(content []byte) FileRef {
	return FileRef{
		Storage: StorageLFS,
		OID:     "sha256:" + ComputeOID(content),
		Size:    int64(len(content)),
	}
}

// NewGitFileRef creates a FileRef for a file committed directly to git
// (no LFS). Used for small artifacts like summary.json that are not worth
// indirecting through LFS. OID is intentionally empty.
func NewGitFileRef(size int64) FileRef {
	return FileRef{
		Storage: StorageGit,
		Size:    size,
	}
}

// UpdateMetaSummary reads meta.json from sessionPath, updates BOTH the
// Title and Summary fields with the given string, and re-writes
// atomically. The caller always passes an AI-generated title — both
// fields get set so meta.json stays consistent regardless of which
// consumer reads which field.
//
// # Why both fields
//
// meta.Title is the canonical short descriptor (5-10 word session name).
// meta.Summary historically held a short descriptive string too, before
// meta.Title existed. Some consumers (older ox versions, tools downstream)
// read meta.Summary for display; newer ones read meta.Title. Setting
// both closes the ox-g5zw distiller bug where meta.Title was left empty
// because this function only touched Summary despite its callers always
// passing a Title. Result: 91/155 sessions shipped with empty titles on
// the ox team's ledger before a mass backfill. Fixing at source ensures
// the bug can't recur on new sessions.
//
// If a separate short-summary string is ever needed alongside the title,
// add a distinct UpdateMetaSummaryOnly function; don't reintroduce the
// single-field ambiguity.
func UpdateMetaSummary(sessionPath, title string) error {
	// Runs the entire read-modify-write under MutateSessionMeta's flock
	// so a daemon finalize concurrently writing Files / Summary doesn't
	// clobber this Title write (and vice versa). Without this the
	// title-write path — which is hit on every session push — was the
	// single most frequent unlocked RMW on meta.json. See ox-e1ot.
	//
	// We also need the LFS-pointer side of WriteSessionMeta to fire so
	// content files get replaced with pointers post-write; do it
	// outside the lock since pointer writes don't race with the
	// manifest mutation.
	var pointerFiles map[string]FileRef
	if err := MutateSessionMeta(context.Background(), sessionPath, func(meta *SessionMeta) (*SessionMeta, error) {
		if meta == nil {
			return nil, fmt.Errorf("meta.json not found in %s: cannot update title", sessionPath)
		}
		meta.Title = title
		meta.Summary = title

		// If a non-empty title is being written, the session HAS a successful
		// summary now — stamp SummaryStatus=ok and clear any stale failure
		// signals from a previous failed attempt (ox-wstd: sticky tombstones).
		// We hard-code "ok" here rather than importing pkg/sessionsummary
		// because internal/lfs is below it in the dep graph; both sides agree
		// on the literal "ok".
		if title != "" {
			meta.SummaryStatus = "ok"
			meta.ValidationError = ""
		}
		// Capture the snapshot of Files we want pointer-replaced after
		// the lock releases. Copy the map so the post-lock work can't
		// see a concurrent mutation.
		if len(meta.Files) > 0 {
			pointerFiles = make(map[string]FileRef, len(meta.Files))
			for k, v := range meta.Files {
				pointerFiles[k] = v
			}
		}
		return meta, nil
	}); err != nil {
		return err
	}

	// Replace content files with LFS pointer files for GC protection.
	// Best-effort, matches WriteSessionMeta's prior behavior; pointer
	// write failures don't invalidate the meta.json update.
	if len(pointerFiles) > 0 {
		if _, err := WritePointerFiles(sessionPath, pointerFiles); err != nil {
			slog.Warn("LFS pointer file write failed", "error", err, "path", sessionPath)
		}
	}
	return nil
}

// BareOID returns the hex digest without the "sha256:" prefix.
func (f FileRef) BareOID() string {
	if len(f.OID) > 7 && f.OID[:7] == "sha256:" {
		return f.OID[7:]
	}
	return f.OID
}

// PreservedSessionID reads meta.json at sessionDir and returns the
// SessionID found there. It is the canonical way for any republish path
// (CLI session stop, daemon recovery, orphan retry) to look up a
// previously-stamped ID before building a fresh meta.
//
// Three return shapes:
//
//   - ("ses_...", nil): meta.json exists with a populated SessionID.
//     Caller MUST chain .SessionID(returned) onto its builder so the
//     republish does not rotate the ID.
//   - ("", nil): meta.json is genuinely absent (NotExist) OR exists but
//     has no SessionID (legacy pre-rollout file). Caller may mint fresh
//     via sessionid.GenerateSessionID() (already done by sessionMetaBase).
//   - ("", non-nil err): meta.json exists but cannot be read or parsed
//     (corrupted, IO failure, permission). Caller MUST treat this as
//     fatal and surface the error — silently minting a fresh SessionID
//     here would rotate an ID that may already be cached by the server
//     or by other coworkers, breaking dedup.
//
// The strict "non-NotExist error is fatal" rule exists because there is
// no safe heuristic for "meta.json exists but I couldn't read it" — we
// don't know whether it had a SessionID we'd overwrite. Refusing to
// proceed is the only conservative choice.
func PreservedSessionID(sessionDir string) (string, error) {
	existing, err := ReadSessionMeta(sessionDir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil // genuinely first publish — caller mints fresh
	}
	if err != nil {
		return "", fmt.Errorf("read existing meta.json (refusing to silently rotate SessionID): %w", err)
	}
	if existing == nil {
		return "", nil
	}
	return existing.SessionID, nil
}

// EffectiveSessionID returns the canonical "ses_"-prefixed identifier for
// this recording, regardless of whether the recording predates the
// SessionID field.
//
//   - If meta.SessionID is non-empty (post-rollout), it is returned verbatim.
//   - Otherwise the result is a deterministic ses_<UUIDv5> derived from
//     (RepoID, SessionName) using legacySessionNamespace.
//
// Deterministic: calling EffectiveSessionID twice on the same legacy
// session always returns the same value.
//
// # Why UUIDv5 over (RepoID, SessionName) and not OxSID
//
// OxSID is per-prime, not per-recording (cmd/ox/agent_prime.go:514;
// reused at 540, 746, 1614, 1630, 1650). Two recordings produced by the
// same prime share an OxSID and would collide. SessionName is the only
// per-recording entropy already present in meta.json; using it here also
// avoids LFS hydration of raw.jsonl on dehydrated clones.
//
// All call sites that need a stable per-recording handle MUST go through
// this helper. Reading m.SessionID directly returns "" for legacy
// recordings and silently breaks dedup/lookup.
func (m *SessionMeta) EffectiveSessionID() string {
	if m.SessionID != "" {
		return m.SessionID
	}
	name := m.RepoID + "/" + m.SessionName
	return "ses_" + uuid.NewSHA1(legacySessionNamespace, []byte(name)).String()
}
