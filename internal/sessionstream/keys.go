package sessionstream

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Object layout in the S3 session store (ADR-247 D4). Keys lead with the
// owning team so a later bring-your-own-bucket is a prefix move.
//
//	team/<team>/conversations/<cnv>/conversation.json
//	team/<team>/conversations/<cnv>/layers/session-jsonl.<clyr>/bytes/<offset:016d>.ndjson
//	team/<team>/conversations/<cnv>/layers/session-jsonl.<clyr>/layer.json
//	team/<team>/conversations/<cnv>/layers/session-jsonl.<clyr>/index.json
//	team/<team>/conversations/<cnv>/attachments/sha256/<hex>

var (
	teamIDPattern = regexp.MustCompile(`^team_[a-z0-9]{10,24}$`)
	// cnv_ and clyr_ ids carry a UUIDv7; the server checks the version nibble
	// so a client cannot mint keys that sort before the conversation existed.
	uuidV7Tail       = `[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`
	conversationIDRe = regexp.MustCompile(`^cnv_` + uuidV7Tail + `$`)
	layerIDRe        = regexp.MustCompile(`^clyr_` + uuidV7Tail + `$`)
	sha256HexRe      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// offsetDigits zero-pads offsets so a lexicographic S3 listing is offset order.
// 16 digits covers 10 PB per layer.
const offsetDigits = 16

// ValidConversationID reports whether s is a cnv_ UUIDv7 id.
func ValidConversationID(s string) bool { return conversationIDRe.MatchString(s) }

// ValidLayerID reports whether s is a clyr_ UUIDv7 id.
func ValidLayerID(s string) bool { return layerIDRe.MatchString(s) }

// ValidSHA256Hex reports whether s is a lowercase 64-char hex digest.
func ValidSHA256Hex(s string) bool { return sha256HexRe.MatchString(s) }

// ConversationPrefix is the root of everything stored for one conversation.
// Deletion erases every object version under it.
func ConversationPrefix(teamID, cnv string) (string, error) {
	if !teamIDPattern.MatchString(teamID) {
		return "", fmt.Errorf("invalid team id %q", teamID)
	}
	if !ValidConversationID(cnv) {
		return "", fmt.Errorf("invalid conversation id %q", cnv)
	}
	return "team/" + teamID + "/conversations/" + cnv + "/", nil
}

// LayerPrefix is the folder of one layer (ADR-057 Invariant 26: a layer is a folder).
func LayerPrefix(teamID, cnv, layer string) (string, error) {
	root, err := ConversationPrefix(teamID, cnv)
	if err != nil {
		return "", err
	}
	if !ValidLayerID(layer) {
		return "", fmt.Errorf("invalid layer id %q", layer)
	}
	return root + "layers/" + LayerKind + "." + layer + "/", nil
}

// BytesPrefix is where a layer's stream objects live.
func BytesPrefix(teamID, cnv, layer string) (string, error) {
	p, err := LayerPrefix(teamID, cnv, layer)
	if err != nil {
		return "", err
	}
	return p + "bytes/", nil
}

// BytesKey is the object holding the stream bytes that start at offset.
func BytesKey(teamID, cnv, layer string, offset int64) (string, error) {
	if offset < 0 {
		return "", fmt.Errorf("offset must be non-negative")
	}
	p, err := BytesPrefix(teamID, cnv, layer)
	if err != nil {
		return "", err
	}
	return p + fmt.Sprintf("%0*d", offsetDigits, offset) + ".ndjson", nil
}

// ParseBytesKeyOffset returns the start offset encoded in a BytesKey.
func ParseBytesKeyOffset(key string) (int64, bool) {
	i := strings.LastIndex(key, "/bytes/")
	if i < 0 || !strings.HasSuffix(key, ".ndjson") {
		return 0, false
	}
	digits := strings.TrimSuffix(key[i+len("/bytes/"):], ".ndjson")
	if len(digits) != offsetDigits {
		return 0, false
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// LayerManifestKey is the ADR-057 layer envelope written at finalize.
func LayerManifestKey(teamID, cnv, layer string) (string, error) {
	p, err := LayerPrefix(teamID, cnv, layer)
	if err != nil {
		return "", err
	}
	return p + "layer.json", nil
}

// LayerIndexKey lists every stream object with its size and sha256, plus the
// whole-stream sha256: the self-describing inventory a customer copy needs.
func LayerIndexKey(teamID, cnv, layer string) (string, error) {
	p, err := LayerPrefix(teamID, cnv, layer)
	if err != nil {
		return "", err
	}
	return p + "index.json", nil
}

// ConversationManifestKey is the ADR-057 conversation.json.
func ConversationManifestKey(teamID, cnv string) (string, error) {
	root, err := ConversationPrefix(teamID, cnv)
	if err != nil {
		return "", err
	}
	return root + "conversation.json", nil
}

// AttachmentKey is a pasted attachment, addressed by content.
func AttachmentKey(teamID, cnv, sha256Hex string) (string, error) {
	root, err := ConversationPrefix(teamID, cnv)
	if err != nil {
		return "", err
	}
	if !ValidSHA256Hex(sha256Hex) {
		return "", fmt.Errorf("invalid sha256 %q", sha256Hex)
	}
	return root + "attachments/sha256/" + sha256Hex, nil
}

// IndexObject is one stream object in index.json.
type IndexObject struct {
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// LayerIndex is index.json: every object, contiguous from 0, and the
// whole-stream hash a reader can verify a reassembled copy against.
type LayerIndex struct {
	SchemaVersion int           `json:"$schema_version"`
	Conversation  string        `json:"conversation_id"`
	LayerID       string        `json:"layer_id"`
	Kind          string        `json:"kind"`
	Length        int64         `json:"length"`
	StreamSHA256  string        `json:"stream_sha256"`
	Objects       []IndexObject `json:"objects"`
}

// CheckContiguous reports the first gap or overlap in offset-ordered objects.
// Finalize refuses to publish across one (stuck beats lost, ADR-024).
func CheckContiguous(objects []IndexObject) (int64, error) {
	var next int64
	for _, o := range objects {
		if o.Offset != next {
			return next, fmt.Errorf("stream is not contiguous at offset %d (next object starts at %d)", next, o.Offset)
		}
		if o.Size <= 0 {
			return next, fmt.Errorf("empty stream object at offset %d", o.Offset)
		}
		next += o.Size
	}
	return next, nil
}
