package sessionstream

import (
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
)

// StreamHash is a running SHA-256 over a layer's bytes [0, n).
//
// It is independent of batch boundaries: however a client splits or re-splits
// its resends, the hash at offset n is sha256(bytes[0:n]). The server persists
// the hash's internal state (MarshalState) so it can extend the hash per append
// without re-reading the stream, and the whole-stream sha256 at close is simply
// the final Sum. That makes "did we lose anything" one comparison end to end.
type StreamHash struct {
	h hash.Hash
	n int64
}

// NewStreamHash starts a hash at offset 0.
func NewStreamHash() *StreamHash { return &StreamHash{h: sha256.New()} }

// RestoreStreamHash resumes a hash from MarshalState output at offset n.
func RestoreStreamHash(state []byte, n int64) (*StreamHash, error) {
	if n < 0 {
		return nil, errors.New("stream hash offset must be non-negative")
	}
	h := sha256.New()
	if n == 0 && len(state) == 0 {
		return &StreamHash{h: h}, nil
	}
	u, ok := h.(encoding.BinaryUnmarshaler)
	if !ok {
		return nil, errors.New("sha256 state is not restorable on this platform")
	}
	if err := u.UnmarshalBinary(state); err != nil {
		return nil, fmt.Errorf("restore stream hash: %w", err)
	}
	return &StreamHash{h: h, n: n}, nil
}

// Write extends the hash; it never fails.
func (s *StreamHash) Write(p []byte) {
	_, _ = s.h.Write(p)
	s.n += int64(len(p))
}

// Len is the offset the hash currently covers.
func (s *StreamHash) Len() int64 { return s.n }

// Sum returns "sha256:<hex>" for bytes [0, Len()) without disturbing the state.
func (s *StreamHash) Sum() string {
	return "sha256:" + hex.EncodeToString(s.h.Sum(nil))
}

// MarshalState returns the opaque hash state for persistence.
func (s *StreamHash) MarshalState() ([]byte, error) {
	m, ok := s.h.(encoding.BinaryMarshaler)
	if !ok {
		return nil, errors.New("sha256 state is not marshalable on this platform")
	}
	return m.MarshalBinary()
}

// Clone copies the hash so a candidate append can be evaluated before commit.
func (s *StreamHash) Clone() (*StreamHash, error) {
	state, err := s.MarshalState()
	if err != nil {
		return nil, err
	}
	return RestoreStreamHash(state, s.n)
}

// EmptyStreamSHA256 is the prefix hash at offset 0.
var EmptyStreamSHA256 = NewStreamHash().Sum()

// BytesSHA256 renders "sha256:<hex>" for one byte slice.
func BytesSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// HexSHA256 renders the bare lowercase hex digest (S3 keys, media ids).
func HexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
