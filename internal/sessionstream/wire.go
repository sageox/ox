package sessionstream

import (
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Headers carried by live-capture requests.
const (
	// HeaderSignature carries the Ed25519 request signature (see Sign).
	HeaderSignature = "Ox-Signature"
	// HeaderPrefixHash carries the client's stream hash of bytes [0, offset).
	HeaderPrefixHash = "Ox-Prefix-Hash"
	// HeaderClientPending reports the client's unacknowledged backlog,
	// "bytes=<n>; oldest_ms=<n>". The server cannot observe a client that
	// cannot reach it, so lag is client-reported (ADR-247 alarms).
	HeaderClientPending = "Ox-Client-Pending"
	// HeaderNextOffset is set on a lines read: the offset to ask for next.
	HeaderNextOffset = "Ox-Next-Offset"
)

// ContentTypeNDJSON is the only accepted append body type.
const ContentTypeNDJSON = "application/x-ndjson"

// MaxBatchBytes caps one append body. 256 KiB holds ~350 average session lines
// (measured median line ~735 bytes, largest 27 KB) while bounding api-go memory
// per request; one MiB would be a whole p99 session in a single request.
const MaxBatchBytes = 256 * 1024

// MaxMediaBytes caps one pasted attachment for v2. Matches what the Roomshack
// twin accepted; larger files need the multipart lane, which is not built yet.
const MaxMediaBytes = 100 * 1024 * 1024

// MaxResendObjects bounds how many stored objects one resend may re-read to
// verify. A client only resends its unacknowledged range, which is one or two
// objects; a longer replay is answered with resync and stored_through, and the
// prefix hash on the next append proves the skipped bytes match. Without the
// cap, replaying many tiny stored batches costs one storage read each.
const MaxResendObjects = 8

// MediaUploadURLTTL is how long a presigned attachment PUT stays usable. It is
// also the window deletion must outwait: an upload URL issued just before the
// deletion fence can still write until it expires, so the delete workflow
// erases once now and again after this window has passed.
const MediaUploadURLTTL = 15 * time.Minute

// SignatureWindow bounds clock skew between a signed request and the server.
const SignatureWindow = 5 * time.Minute

// LayerKind is the conversation layer kind a streamed coding session writes.
const LayerKind = "session-jsonl"

// Action tells a client what to do after a refusal. Clients branch on Action,
// never on Reason, so new reasons never need a client release.
type Action string

const (
	// ActionRetry: send the same request again after RetryAfterMS, with jitter.
	ActionRetry Action = "retry"
	// ActionReauth: refresh the login token or re-register the signing key, then resend.
	ActionReauth Action = "reauth"
	// ActionResync: resend from StoredThrough (the server already holds less or more than assumed).
	ActionResync Action = "resync"
	// ActionNewLayer: this stream can never be continued; start a new layer.
	ActionNewLayer Action = "new_layer"
	// ActionStopAndWipe: the session was deleted; stop sending and erase local copies.
	ActionStopAndWipe Action = "stop_and_wipe"
	// ActionStop: a permanent refusal (permissions, feature off); stop and report.
	ActionStop Action = "stop"
	// ActionFix: the request itself is malformed; fix the client, do not resend as is.
	ActionFix Action = "fix"
)

// Reason values. Stable strings, safe to log and alarm on.
const (
	ReasonOffsetGap        = "offset_gap"
	ReasonPrefixMismatch   = "prefix_mismatch"
	ReasonBytesConflict    = "bytes_conflict"
	ReasonTornTail         = "torn_tail"
	ReasonNotNDJSON        = "not_ndjson"
	ReasonTooLarge         = "too_large"
	ReasonEmptyBody        = "empty_body"
	ReasonSessionDeleted   = "session_deleted"
	ReasonSessionClosed    = "session_closed"
	ReasonBadSignature     = "bad_signature"
	ReasonSignatureExpired = "signature_expired"
	ReasonUnknownKey       = "unknown_key"
	ReasonNotFound         = "not_found"
	ReasonFeatureOff       = "feature_off"
	ReasonForbidden        = "forbidden"
	ReasonInvalidRequest   = "invalid_request"
	ReasonUnavailable      = "unavailable"
	ReasonStreamMismatch   = "stream_mismatch"
	// ReasonRateLimited carries RetryAfterMS; live routes wrap the shared limiter
	// so even a 429 is an ErrorResponse a client can act on.
	ReasonRateLimited = "rate_limited"
	// ReasonUnauthenticated: no valid login token (action reauth).
	ReasonUnauthenticated = "unauthenticated"
)

// Line types the server and twin recognize inside a session stream. The stream
// is otherwise opaque to the server (tolerant readers); only these two shapes
// carry meaning for capture integrity.
const (
	// LineTypeGap declares bytes the client could not capture (spool overflow).
	// Loss is declared, never silent (ADR-024): {"type":"gap","reason":...,"lost_entries":N}.
	LineTypeGap = "gap"
	// LineTypeMedia cites a pasted attachment by content: a media line names its
	// attachment by lowercase-hex sha256 in field "sha256", uploaded out of band.
	LineTypeMedia = "media"
)

// ErrorResponse is every non-2xx body on live-capture routes.
type ErrorResponse struct {
	Error         string `json:"error"`
	Reason        string `json:"reason"`
	Action        Action `json:"action"`
	StoredThrough *int64 `json:"stored_through,omitempty"`
	RetryAfterMS  *int64 `json:"retry_after_ms,omitempty"`
	// Line is the 1-based line number a batch was refused for, when known.
	Line *int `json:"line,omitempty"`
}

// RegisterKeyRequest registers the caller's per-endpoint Ed25519 public key.
// The request itself is signed with the key being registered, which proves
// possession without a separate challenge round trip.
type RegisterKeyRequest struct {
	// PublicKey is the raw 32-byte Ed25519 key, base64url without padding.
	PublicKey string `json:"public_key"`
	// Label is a human label for Settings, such as "ox CLI · build-box".
	Label string `json:"label,omitempty"`
}

// RegisterKeyResponse confirms a registered key.
type RegisterKeyResponse struct {
	KeyID     string    `json:"key_id"`
	CreatedAt time.Time `json:"created_at"`
}

// OpenSessionRequest opens (or resumes) a streamed session. Idempotent: a
// repeat returns the current cursor, which is how a restarted client resumes.
type OpenSessionRequest struct {
	LayerID string `json:"layer_id"`
	RepoID  string `json:"repo_id"`
	// SessionID is the ox session id (ses_...) this conversation records. The
	// conversation id in the path must be its cnv_ twin (same UUID).
	SessionID string `json:"session_id"`
	// SessionName is the ox session folder name; the web addresses sessions by
	// (repo, name), so it is the join key between both homes.
	SessionName string `json:"session_name"`
	// Agent names the coding agent, such as "claude-code".
	Agent     string    `json:"agent"`
	StartedAt time.Time `json:"started_at"`
	// ParentSessionID is the ses_ id of the recorded session whose shell
	// launched this session's coding agent (a `claude -p` or `codex exec`
	// child), optional. The server keeps it only when that session is the same
	// person's, in the same team and repo; anything else is dropped, never
	// refused. Readers list this session inside its parent's.
	ParentSessionID string `json:"parent_session_id,omitempty"`
	// ContinuedFromSessionID is the ses_ id of the earlier recording this one
	// continues (the same native coding-agent session, reopened after its
	// recording ended), optional, kept on the same terms. Readers show the two
	// as one session.
	ContinuedFromSessionID string `json:"continued_from_session_id,omitempty"`
}

// SessionStatus is a streamed session's lifecycle as readers see it.
type SessionStatus string

const (
	StatusLive     SessionStatus = "live"
	StatusFinished SessionStatus = "finished"
	StatusDeleted  SessionStatus = "deleted"
)

// OpenSessionResponse returns where the stream stands.
type OpenSessionResponse struct {
	ConversationID string        `json:"conversation_id"`
	LayerID        string        `json:"layer_id"`
	Status         SessionStatus `json:"status"`
	StoredThrough  int64         `json:"stored_through"`
	PrefixSHA256   string        `json:"prefix_sha256"`
}

// AppendResponse answers a stored append. StoredThrough drives resends;
// PublishedThrough is status only and never gates freeing local bytes.
type AppendResponse struct {
	StoredThrough    int64  `json:"stored_through"`
	PublishedThrough int64  `json:"published_through"`
	PrefixSHA256     string `json:"prefix_sha256"`
}

// CloseRequest ends a session. StoredThrough and StreamSHA256 let the server
// refuse to finalize across a gap or a divergent stream.
type CloseRequest struct {
	LayerID       string `json:"layer_id"`
	StoredThrough int64  `json:"stored_through"`
	StreamSHA256  string `json:"stream_sha256"`
	// Summary is the client-written session summary (markdown), optional.
	// Client-first: the server generates one only when it is missing.
	Summary string `json:"summary,omitempty"`
}

// MaxSummaryBytes caps CloseRequest.Summary.
const MaxSummaryBytes = 64 * 1024

// LayerState is one layer's progress.
type LayerState struct {
	LayerID          string     `json:"layer_id"`
	Kind             string     `json:"kind"`
	StoredThrough    int64      `json:"stored_through"`
	PublishedThrough int64      `json:"published_through"`
	LastAppendAt     *time.Time `json:"last_append_at,omitempty"`
}

// SessionState is what readers get for a streamed session.
type SessionState struct {
	ConversationID string        `json:"conversation_id"`
	SessionID      string        `json:"session_id"`
	SessionName    string        `json:"session_name"`
	RepoID         string        `json:"repo_id"`
	Status         SessionStatus `json:"status"`
	Agent          string        `json:"agent"`
	OwnerUserID    string        `json:"owner_user_id"`
	StartedAt      time.Time     `json:"started_at"`
	ClosedAt       *time.Time    `json:"closed_at,omitempty"`
	Layers         []LayerState  `json:"layers"`
}

// ListSessionsResponse answers GET /api/v1/live/sessions?repo_id=...
type ListSessionsResponse struct {
	Sessions []SessionState `json:"sessions"`
}

// EventType is a content-free live notice. Notices never carry session text,
// so session content never lands in the event log (ADR-247 D6).
type EventType string

const (
	EventAdvanced EventType = "advanced"
	EventFinished EventType = "finished"
	EventDeleted  EventType = "deleted"
)

// Event is one SSE notice for a streamed session.
type Event struct {
	Type           EventType `json:"type"`
	ConversationID string    `json:"conversation_id"`
	LayerID        string    `json:"layer_id,omitempty"`
	StoredThrough  int64     `json:"stored_through,omitempty"`
}

// MediaRequest declares a pasted attachment before its bytes upload.
type MediaRequest struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	MIME   string `json:"mime"`
	Name   string `json:"name,omitempty"`
}

// MediaResponse returns where to upload. AlreadyStored means the same bytes
// are present (dedupe by sha256 within the conversation): skip the upload.
type MediaResponse struct {
	SHA256        string            `json:"sha256"`
	AlreadyStored bool              `json:"already_stored"`
	UploadURL     string            `json:"upload_url,omitempty"`
	UploadHeaders map[string]string `json:"upload_headers,omitempty"`
	ExpiresAt     *time.Time        `json:"expires_at,omitempty"`
}

// MaxReadBytes caps one lines read; reads return whole stream objects.
const MaxReadBytes = 4 * 1024 * 1024

// Route builders. Paths are part of the signed canonical string, so client and
// server must build them identically.
const (
	PathKeys     = "/api/v1/live/keys"
	PathSessions = "/api/v1/live/sessions"
	pathSessions = PathSessions + "/"
)

// SessionPath is the open (PUT), state (GET) and delete (DELETE) route.
func SessionPath(cnv string) string { return pathSessions + url.PathEscape(cnv) }

// LinesPath is the append (POST) and read (GET) route for one layer.
func LinesPath(cnv, layer string) string {
	return SessionPath(cnv) + "/layers/" + url.PathEscape(layer) + "/lines"
}

// AppendURI is the request URI (path and query) an append is signed over.
func AppendURI(cnv, layer string, offset int64) string {
	return LinesPath(cnv, layer) + "?offset=" + strconv.FormatInt(offset, 10)
}

// ClosePath ends a session.
func ClosePath(cnv string) string { return SessionPath(cnv) + "/close" }

// EventsPath is the SSE route.
func EventsPath(cnv string) string { return SessionPath(cnv) + "/events" }

// MediaPath declares an attachment upload.
func MediaPath(cnv string) string { return SessionPath(cnv) + "/media" }

// MediaObjectPath reads an attachment (redirects to a short-lived URL).
func MediaObjectPath(cnv, sha256Hex string) string {
	return MediaPath(cnv) + "/" + url.PathEscape(sha256Hex)
}

// MediaCompletePath confirms an attachment upload.
func MediaCompletePath(cnv, sha256Hex string) string {
	return MediaPath(cnv) + "/" + url.PathEscape(sha256Hex) + "/complete"
}

// ParseOffset reads the offset query parameter; it must be a non-negative integer.
func ParseOffset(raw string) (int64, error) {
	if raw == "" {
		return 0, fmt.Errorf("offset is required")
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("offset must be a non-negative integer")
	}
	return n, nil
}
