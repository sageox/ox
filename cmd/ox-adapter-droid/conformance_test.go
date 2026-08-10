package main

import (
	"os"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adaptertest"
)

// TestConformance runs the shared adapter conformance suite against a real
// droid transcript. realFixture and its provenance are documented in
// session_test.go — cwd, session id, and owner username anonymized, nothing
// else changed.
//
// The fixture's six lines break down as: 1 session_start (produces no
// entry), 3 user turns, 2 assistant turns — 5 entries total. ResumePoints
// below are computed from the fixture's real line lengths (via lineOffset)
// rather than hardcoded, so a re-capture of the fixture can't silently
// desync the resume points from real line boundaries.
func TestConformance(t *testing.T) {
	adaptertest.Run(t, adaptertest.Suite{
		Adapter: "droid",
		Provenance: "captured from a real droid 0.126.0 install, " +
			"~/.factory/sessions/<project-slug>/<uuid>.jsonl, cwd/session id/owner " +
			"anonymized — nothing else changed (see session_test.go)",

		ReadAll: func() ([]adapterprotocol.RawEntry, error) {
			entries, _, err := readSessionFile(realFixture)
			return entries, err
		},

		ReadFrom: func(offset int64) ([]adapterprotocol.RawEntry, int64, error) {
			return readFromOffset(realFixture, offset)
		},

		EndOffset: func() (int64, error) {
			info, err := os.Stat(realFixture)
			if err != nil {
				return 0, err
			}
			return info.Size(), nil
		},

		ResumePoints: func() ([]int64, error) {
			return []int64{
				lineOffset(t, realFixture, 3), // skips the line-2 entry
				lineOffset(t, realFixture, 5), // skips the line 2-4 entries
				// line 6 is the fixture's last line, so an offset there
				// equals EndOffset — already covered by the dedicated
				// EndOffset check in checkResume, and reflect.DeepEqual
				// would otherwise compare a nil slice against an empty
				// (non-nil) tail slice and fail for reasons unrelated to
				// the adapter.
			}, nil
		},

		Want: adaptertest.Want{
			MinEntries:     5,
			UserTurns:      3,
			AssistantTurns: 2,

			Unproven: []string{
				"tool calls and tool results — none of the real droid transcripts " +
					"available on this machine contain a tool_use/tool_result block " +
					"(see session_test.go TestReadSessionFile_RealTranscript)",
				"tool call/result correlation — even once a real tool-bearing transcript " +
					"is found, parseAssistantMessage builds tool_use/tool_result entries via " +
					"adapterruntime.ToolUseEntry/ToolResultEntry (no call_id), not the WithID " +
					"variants, even though droidBlock.ToolUseID is parsed off the JSON and " +
					"never used — pairing would need that wired up first",
			},
		},
	})
}

// lineOffset returns the byte offset immediately after the first n lines of
// path (each line's length including its trailing newline).
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
