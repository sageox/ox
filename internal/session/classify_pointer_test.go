package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- GH #710 D2: pointer stubs are not transcripts ---
//
// A content-store pointer is ~130 bytes over 3 lines. The old
// HasSubstantiveEntries counted lines and reported "2+ lines = real
// content", so a stub read as a full transcript. The summarizer then
// produced an empty title, validation failed with "title too short
// (0 chars, minimum 3)", and the daemon retried forever — 21 duplicate
// "finalize session" commits across 5 sessions in the reported case.

// writePointerStub writes a spec-compliant LFS pointer, the exact shape
// ox commits for a synced session.
func writePointerStub(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, lfs.WritePointerFile(path, lfs.AssertUploaded(lfs.FileRef{
		OID:  "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		Size: 4242,
	})))
}

func TestClassifyRawFile(t *testing.T) {
	dir := t.TempDir()

	missing := filepath.Join(dir, "missing.jsonl")

	headerOnly := filepath.Join(dir, "header.jsonl")
	require.NoError(t, os.WriteFile(headerOnly,
		[]byte(`{"metadata":{"agent_id":"a"},"type":"header"}`+"\n"), 0o644))

	substantive := filepath.Join(dir, "real.jsonl")
	require.NoError(t, os.WriteFile(substantive,
		[]byte(`{"metadata":{"agent_id":"a"},"type":"header"}`+"\n"+
			`{"type":"user","content":"hello","seq":1}`+"\n"), 0o644))

	pointer := filepath.Join(dir, "pointer.jsonl")
	writePointerStub(t, pointer)

	tests := []struct {
		name string
		path string
		want RawKind
		why  string
	}{
		{"missing file", missing, RawMissing, "nothing to read"},
		{"header only", headerOnly, RawHeaderOnly, "a recording that captured nothing"},
		{"real transcript", substantive, RawSubstantive, "the only kind that may be summarized"},
		{
			"pointer stub", pointer, RawPointerStub,
			"THE #710 case: 3 lines of pointer must never read as transcript content",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ClassifyRawFile(tt.path), tt.why)
		})
	}
}

// TestHasSubstantiveEntries_RejectsPointerStub is the direct regression
// assertion. This fails against the pre-fix line-counting implementation.
func TestHasSubstantiveEntries_RejectsPointerStub(t *testing.T) {
	pointer := filepath.Join(t.TempDir(), "raw.jsonl")
	writePointerStub(t, pointer)

	// sanity: the stub really does have the multi-line shape that fooled
	// the old line-counting check.
	data, err := os.ReadFile(pointer)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(splitLines(string(data))), 2,
		"precondition: a pointer file has 2+ lines, which is why line counting was wrong")

	assert.False(t, HasSubstantiveEntries(pointer),
		"a pointer is a reference, not a transcript — treating it as content is what "+
			"fed an empty file to the summarizer and started the retry loop")
}

func TestHasSubstantiveEntries_AcceptsRealContent(t *testing.T) {
	real := filepath.Join(t.TempDir(), "raw.jsonl")
	require.NoError(t, os.WriteFile(real,
		[]byte(`{"metadata":{},"type":"header"}`+"\n"+`{"type":"user","content":"hi"}`+"\n"), 0o644))

	assert.True(t, HasSubstantiveEntries(real),
		"the fix must not make every session look empty")
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := range len(s) {
		if s[i] == '\n' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
