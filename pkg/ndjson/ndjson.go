// Package ndjson provides shared NDJSON (newline-delimited JSON) framing
// utilities for both daemon IPC and adapter serve-mode communication.
//
// Both the CLI-daemon Unix socket IPC and the daemon-adapter stdin/stdout
// IPC use NDJSON. This package ensures consistent buffer sizes (1MB max line),
// explicit Err() checking, and compact encoding across all IPC layers.
package ndjson

import (
	"bufio"
	"encoding/json"
	"io"
)

// MaxLineBytes is the maximum size of a single NDJSON line (1MB).
// This matches the existing daemon IPC limit and prevents unbounded
// memory allocation from malformed or malicious input.
const MaxLineBytes = 1 << 20 // 1MB

// Scanner wraps bufio.Scanner with the correct buffer size for NDJSON lines.
// Always check Err() after Scan() returns false to distinguish EOF from errors.
type Scanner struct {
	s *bufio.Scanner
}

// NewScanner creates a Scanner that reads NDJSON lines from r with a 1MB buffer.
func NewScanner(r io.Reader) *Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, MaxLineBytes), MaxLineBytes)
	return &Scanner{s: s}
}

// Scan advances to the next NDJSON line. Returns false at EOF or on error.
func (sc *Scanner) Scan() bool {
	return sc.s.Scan()
}

// Bytes returns the current line as a byte slice.
// The slice is only valid until the next call to Scan.
func (sc *Scanner) Bytes() []byte {
	return sc.s.Bytes()
}

// Text returns the current line as a string.
func (sc *Scanner) Text() string {
	return sc.s.Text()
}

// Err returns the first non-EOF error encountered by the Scanner.
func (sc *Scanner) Err() error {
	return sc.s.Err()
}

// Encoder wraps json.Encoder with compact encoding enforced (no indentation).
// Each Encode call writes exactly one JSON object followed by a newline.
type Encoder struct {
	enc *json.Encoder
}

// NewEncoder creates an Encoder that writes compact NDJSON to w.
func NewEncoder(w io.Writer) *Encoder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &Encoder{enc: enc}
}

// Encode serializes v as a single compact JSON line followed by a newline.
func (e *Encoder) Encode(v any) error {
	return e.enc.Encode(v)
}

// Decode unmarshals a single NDJSON line (as bytes) into v.
// This is a convenience for decoding Scanner output.
func Decode(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
