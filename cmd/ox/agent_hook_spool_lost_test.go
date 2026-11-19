package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/sessionstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// liveGapLine is the shape of a declared loss in the live spool.
type liveGapLine struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
	Bytes  int64  `json:"bytes"`
}

func decodeLiveGap(t *testing.T, line []byte) liveGapLine {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	var g liveGapLine
	if err := dec.Decode(&g); err != nil || g.Type != sessionstream.LineTypeGap {
		t.Fatalf("line %.160q is not a gap line: %v", line, err)
	}
	return g
}

// Failure prevented (ox#1931 review, K6): a hook commits a batch to raw.jsonl
// and moves the cursor past it, then cannot read the batch back to spool it.
// No capture reads those bytes again, so the live stream skipped them without
// a trace. The hook must declare them, so the stream's next write carries a
// spool_write_failed gap of their size where they belong.
func TestHookDeclaresABatchItCouldNotReadBackAsALiveGap(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("reason: root ignores file permissions, so the read-back failure cannot be simulated")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("spooled")
	rawPath := filepath.Join(f.dir(), "raw.jsonl")
	before, err := os.Stat(rawPath)
	require.NoError(t, err)
	spoolBefore := f.spoolBytes()

	// write-only: the append lands, reading the batch back fails
	require.NoError(t, os.Chmod(rawPath, 0o200))
	t.Cleanup(func() { _ = os.Chmod(rawPath, 0o600) })
	f.turn("never read back")
	require.NoError(t, os.Chmod(rawPath, 0o600))

	after, err := os.Stat(rawPath)
	require.NoError(t, err)
	lost := after.Size() - before.Size()
	require.Positive(t, lost, "precondition: the batch was committed to raw.jsonl")
	assert.Equal(t, spoolBefore, f.spoolBytes(), "a batch that could not be read back cannot be spooled")

	f.turn("the next write") // carries the declared loss ahead of its own batch
	gap, _, _ := bytes.Cut(f.spoolBytes()[len(spoolBefore):], []byte("\n"))
	g := decodeLiveGap(t, gap)
	assert.Equal(t, "spool_write_failed", g.Reason)
	assert.Equal(t, lost, g.Bytes, "the gap must be the size of the batch the stream never got")
}
