package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
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

func skipWithoutFilePermissions(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("reason: root ignores file permissions, so the I/O failure cannot be simulated")
	}
	if runtime.GOOS == "windows" {
		t.Skip("reason: os.Chmod cannot remove read access on Windows, so the I/O failure cannot be simulated")
	}
}

// lockStreamState makes the live stream's state unreadable until restore, so
// the stream cannot take a batch committed in the meantime.
func (f *liveFixture) lockStreamState() (restore func()) {
	f.t.Helper()
	path := filepath.Join(f.dir(), "stream-state.json")
	require.NoError(f.t, os.Chmod(path, 0o200))
	f.t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	return func() { require.NoError(f.t, os.Chmod(path, 0o600)) }
}

// nextGap makes the stream's next write and decodes the first line it added:
// the gap it must declare ahead of its own line.
func (f *liveFixture) nextGap() liveGapLine {
	f.t.Helper()
	before := f.spoolBytes()
	_, err := livesession.Spool(context.Background(), f.dir(), []byte(`{"type":"msg","content":"the next write"}`+"\n"), livesession.SpoolOptions{})
	require.NoError(f.t, err)
	first, _, _ := bytes.Cut(f.spoolBytes()[len(before):], []byte("\n"))
	return decodeLiveGap(f.t, first)
}

// Failure prevented (ox#1931 review K6, then finding 5): a hook commits a
// batch to raw.jsonl and moves the cursor past it, then the live stream cannot
// take it. No capture reads those bytes again, so the stream would skip them
// without a trace. The hook declares them: the stream's next write carries a
// spool_write_failed gap of exactly their size.
func TestHookDeclaresABatchTheStreamCouldNotTakeAsALiveGap(t *testing.T) {
	skipWithoutFilePermissions(t)
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("spooled")
	rawBefore := len(f.rawBytes())
	spoolBefore := f.spoolBytes()

	restore := f.lockStreamState()
	f.turn("never spooled")
	restore()

	lost := int64(len(f.rawBytes()) - rawBefore)
	require.Positive(t, lost, "precondition: the batch was committed to raw.jsonl")
	assert.Equal(t, spoolBefore, f.spoolBytes(), "precondition: the stream could not take the batch")
	g := f.nextGap()
	assert.Equal(t, "spool_write_failed", g.Reason)
	assert.Equal(t, lost, g.Bytes, "the gap must be the size of the batch the stream never got")
}

// Failure prevented (finding 5): the capture used to read its committed batch
// back from raw.jsonl to spool it, after the cursor had moved. A raw.jsonl that
// could not be read back cost the stream the batch. The append now hands the
// stream the bytes it wrote, so the batch arrives whole regardless.
func TestHookSpoolsABatchRawJSONLCannotReadBack(t *testing.T) {
	skipWithoutFilePermissions(t)
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("spooled")
	rawPath := filepath.Join(f.dir(), "raw.jsonl")

	require.NoError(t, os.Chmod(rawPath, 0o200)) // appendable, never readable
	t.Cleanup(func() { _ = os.Chmod(rawPath, 0o600) })
	f.turn("committed while raw.jsonl is unreadable")
	require.NoError(t, os.Chmod(rawPath, 0o600))

	assert.Equal(t, string(f.rawBytes()), string(f.spoolBytes()), "the spool must be exactly what the capture writer put in raw.jsonl")
	assert.NoFileExists(t, filepath.Join(f.dir(), "spool-lost"), "a batch that reached the stream is not a loss")
}

// Failure prevented (finding 5): stop's final drain committed the transcript's
// last turn to raw.jsonl, then could not hand it to the live stream, and nothing
// said so. The stream closed without that turn and with no gap in its place.
// The drain declares the batch: the next write carries a gap of its size.
func TestStopFinalDrainDeclaresABatchTheStreamCouldNotTake(t *testing.T) {
	skipWithoutFilePermissions(t)
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("hooked question")
	at := f.state.StartedAt.Add(30 * time.Second)
	appendClaudeEntries(t, f.sourceFile, at,
		`{"type":"user","timestamp":"`+at.Format(time.RFC3339Nano)+`","message":{"role":"user","content":"unhooked last question"}}`)
	rawBefore := f.rawBytes()
	spoolBefore := f.spoolBytes()

	restore := f.lockStreamState()
	f.stop()
	restore()

	drained := finalDrainBatch(t, rawBefore, f.rawBytes())
	require.Contains(t, string(drained), "unhooked last question", "precondition: the final drain committed the last turn")
	assert.Equal(t, spoolBefore, f.spoolBytes(), "precondition: the stream could not take the batch")
	g := f.nextGap()
	assert.Equal(t, "spool_write_failed", g.Reason)
	assert.Equal(t, int64(len(drained)), g.Bytes, "the gap must be the size of the batch the stream never got")
}

// finalDrainBatch is what stop's final drain appended to raw.jsonl: the lines
// after before, up to the carrier footer stop stamps after the drain.
func finalDrainBatch(t *testing.T, before, after []byte) []byte {
	t.Helper()
	require.True(t, bytes.HasPrefix(after, before), "stop rewrote raw.jsonl instead of appending to it")
	tail := after[len(before):]
	end := 0
	for line := range bytes.Lines(tail) {
		var rec struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal(line, &rec))
		if rec.Type == "footer" {
			break
		}
		end += len(line)
	}
	return tail[:end]
}
