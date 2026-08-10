package main

import (
	"os"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adaptertest"
)

// TestConformance runs the shared cross-adapter conformance suite
// (pkg/adaptertest) against testdata/session-v3.jsonl — copied verbatim from
// a transcript a real pi (Sage engineering CLI) binary wrote, with only the
// cwd and file paths anonymized. See session_test.go's "Line parsing" and
// "Whole-file reading" sections for the fixture's provenance.
//
// The fixture's four entries: one user turn, one tool call ("read"), its
// paired tool result, and one final assistant turn. ResumePoints are
// computed from the fixture's real line lengths (via lineOffset) rather than
// hardcoded, so a re-capture of the fixture can't silently desync the resume
// points from real line boundaries.
func TestConformance_RealTranscript(t *testing.T) {
	adaptertest.Run(t, adaptertest.Suite{
		Adapter:    "pi",
		Provenance: "real pi v3 session transcript, testdata/session-v3.jsonl, cwd/paths anonymized — nothing else changed (see session_test.go)",

		ReadAll: func() ([]adapterprotocol.RawEntry, error) {
			return readPiFile(fixtureTranscript)
		},

		ReadFrom: func(offset int64) ([]adapterprotocol.RawEntry, int64, error) {
			return readPiFromOffset(fixtureTranscript, offset)
		},
		EndOffset: func() (int64, error) {
			info, err := os.Stat(fixtureTranscript)
			if err != nil {
				return 0, err
			}
			return info.Size(), nil
		},
		ResumePoints: func() ([]int64, error) {
			return []int64{
				lineOffset(t, fixtureTranscript, 4), // skips the user turn (line 4)
				lineOffset(t, fixtureTranscript, 6), // skips through the tool result (lines 4-6)
			}, nil
		},

		Want: adaptertest.Want{
			MinEntries:     4,
			UserTurns:      1,
			AssistantTurns: 1,
			ToolCalls:      1,
			ToolResults:    1,
			PairedResults:  1,

			Unproven: []string{
				"errored tool results — the fixture's one tool call (read) succeeds; " +
					"no real transcript on this machine contains a failed tool call, " +
					"so IsError=true is not exercised against real data",
			},
		},
	})
}

// lineOffset returns the byte offset immediately after the first n lines of
// path (each line's length including its trailing newline). Computed from
// the fixture itself rather than hardcoded, so a re-capture of the fixture
// can't silently desync the resume points from real line boundaries.
func lineOffset(t *testing.T, path string, n int) int64 {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.SplitAfter(string(data), "\n")
	var offset int64
	for i := 0; i < n && i < len(lines); i++ {
		offset += int64(len(lines[i]))
	}
	return offset
}
