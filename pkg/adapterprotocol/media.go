package adapterprotocol

// MaxMediaBytes caps one pasted image. It matches the live session server's
// per-attachment cap (sessionstream.MaxMediaBytes); a larger image is skipped
// with a declared gap rather than uploaded and refused.
const MaxMediaBytes = 100 * 1024 * 1024

// Why an image was skipped (MediaRef.Skipped). Each becomes a declared gap in
// the session stream, never a silent drop.
const (
	MediaSkippedTooLarge        = "too_large"
	MediaSkippedUnsupportedType = "unsupported_type"
	MediaSkippedMalformed       = "malformed"
)

// MediaRef is an image pasted into a user turn.
//
// Data carries the decoded bytes from the adapter to the daemon once; the
// daemon stages them on disk and drops them (they are content: never logged,
// never written to raw.jsonl). Skipped is set instead of Data when the adapter
// refused the image; SHA256 and Size are then best-effort.
type MediaRef struct {
	// SHA256 is the lowercase hex digest of the decoded bytes.
	SHA256 string `json:"sha256,omitempty"`
	MIME   string `json:"mime,omitempty"`
	Size   int64  `json:"size,omitempty"`
	// Position is the image's index among the turn's content blocks.
	Position int    `json:"position"`
	Data     []byte `json:"data,omitempty"`
	Skipped  string `json:"skipped,omitempty"`
}

// AllowedMediaMIME reports whether the live session accepts an image type. The
// server only requires a non-empty mime; this is the set the v3 viewer renders
// and Claude Code can paste.
func AllowedMediaMIME(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}
