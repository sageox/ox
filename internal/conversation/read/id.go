package read

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/conversation/uri"
)

// Id prefixes (D16). cnv_ and rec_ share one UUID by literal prefix swap;
// INDEX.json keys by recording_id, so everything normalizes to rec_
// internally.
const (
	prefixConversation = "cnv_"
	prefixRecording    = "rec_"
	prefixTopic        = "tp_"
)

// ID is a validated, normalized conversation identifier.
type ID struct {
	// RecordingID is the rec_-prefixed form — the internal lookup key.
	RecordingID string
	// ConversationID is the cnv_-prefixed twin (same UUID).
	ConversationID string
	// Address is non-nil when the input was a sageox:// citation URI; its
	// selectors are carried through to the transcript query (D16).
	Address *uri.Address
}

// ParseID validates raw as one of the accepted id forms — cnv_<uuidv7>,
// rec_<uuidv7>, or a full sageox:// citation URI — and nothing else (D16:
// no folder names, no UUID prefixes). Ids arrive inside untrusted content,
// so validation is strict before any use.
func ParseID(raw string) (*ID, *Error) {
	switch {
	case strings.HasPrefix(raw, uri.Scheme):
		addr, err := uri.Parse(raw)
		if err != nil {
			return nil, newError(ErrCodeInvalidID, fmt.Sprintf("invalid citation URI: %v", err))
		}
		u := strings.TrimPrefix(addr.Conversation, prefixConversation)
		return &ID{
			RecordingID:    prefixRecording + u,
			ConversationID: addr.Conversation,
			Address:        addr,
		}, nil
	case strings.HasPrefix(raw, prefixConversation):
		u := raw[len(prefixConversation):]
		if !isUUIDv7(u) {
			return nil, newError(ErrCodeInvalidID, fmt.Sprintf("%q is not a valid cnv_ UUIDv7 id", truncateID(raw)))
		}
		return &ID{RecordingID: prefixRecording + u, ConversationID: raw}, nil
	case strings.HasPrefix(raw, prefixRecording):
		u := raw[len(prefixRecording):]
		if !isUUIDv7(u) {
			return nil, newError(ErrCodeInvalidID, fmt.Sprintf("%q is not a valid rec_ UUIDv7 id", truncateID(raw)))
		}
		return &ID{RecordingID: raw, ConversationID: prefixConversation + u}, nil
	default:
		return nil, newError(ErrCodeInvalidID,
			fmt.Sprintf("%q is not an accepted id; use cnv_<uuidv7>, rec_<uuidv7>, or a sageox:// citation URI", truncateID(raw)))
	}
}

// ValidateTopicID checks a tp_<uuidv7> topic id (D21: exact full ids only —
// no title or ordinal matching).
func ValidateTopicID(raw string) *Error {
	if !strings.HasPrefix(raw, prefixTopic) || !isUUIDv7(raw[len(prefixTopic):]) {
		return newError(ErrCodeInvalidID,
			fmt.Sprintf("%q is not a valid tp_ UUIDv7 topic id; copy the exact id from ox conversation topics", truncateID(raw)))
	}
	return nil
}

// isUUIDv7 validates a canonical lowercase UUIDv7 by delegating to the uri
// package's strict parser (the single validator for untrusted ids): the
// candidate is wrapped as a bare conversation URI, and the round trip must
// yield exactly that conversation with no layer, revision, or selectors —
// so a candidate smuggling '/', '@', or '#' can never pass.
func isUUIDv7(candidate string) bool {
	addr, err := uri.Parse(uri.Scheme + prefixConversation + candidate)
	if err != nil {
		return false
	}
	return addr.Conversation == prefixConversation+candidate &&
		addr.Layer == "" && addr.Revision == 0 && addr.Selectors.IsZero()
}

// truncateID bounds untrusted input reproduced in error messages.
func truncateID(s string) string {
	const max = 80
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
