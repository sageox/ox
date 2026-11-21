package sessionstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// BatchError explains why an append body was refused. Line is 1-based, 0 when
// the problem is not tied to one line.
type BatchError struct {
	Reason string
	Line   int
	Msg    string
}

func (e *BatchError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s at line %d: %s", e.Reason, e.Line, e.Msg)
	}
	return e.Reason + ": " + e.Msg
}

// ValidateBatch checks an append body: non-empty, at most MaxBatchBytes, UTF-8,
// ending in a newline, and every non-blank line one JSON object. A body that
// ends mid-line is a torn tail: refused rather than stored, because a later
// append would otherwise splice into the middle of a record.
//
// It deliberately does not validate line fields: the session JSONL schema is
// the client's (ox raw.jsonl), and a strict server reader would make every new
// optional field a deploy-ordering problem (tolerant-readers rule).
func ValidateBatch(body []byte) error {
	if len(body) == 0 {
		return &BatchError{Reason: ReasonEmptyBody, Msg: "body is empty"}
	}
	if len(body) > MaxBatchBytes {
		return &BatchError{Reason: ReasonTooLarge, Msg: fmt.Sprintf("body is %d bytes, max %d", len(body), MaxBatchBytes)}
	}
	if !utf8.Valid(body) {
		return &BatchError{Reason: ReasonNotNDJSON, Msg: "body is not valid UTF-8"}
	}
	if body[len(body)-1] != '\n' {
		return &BatchError{Reason: ReasonTornTail, Line: bytes.Count(body, []byte{'\n'}) + 1, Msg: "last line has no newline"}
	}
	line := 0
	for rest := body; len(rest) > 0; {
		i := bytes.IndexByte(rest, '\n')
		raw := rest[:i]
		rest = rest[i+1:]
		line++
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 {
			continue
		}
		if trimmed[0] != '{' || !json.Valid(trimmed) {
			return &BatchError{Reason: ReasonNotNDJSON, Line: line, Msg: "line is not one JSON object"}
		}
	}
	return nil
}
