package main

import (
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adaptertest"
)

// TestConformance runs the shared adapter conformance suite against a real
// droid transcript. realFixture and its provenance are documented in
// session_test.go — cwd, session id, and owner username anonymized, nothing
// else changed.
//
// The fixture's five lines break down as: 1 session_start (produces no
// entry), 3 user turns, 2 assistant turns — 5 entries total. Byte offsets for
// ResumePoints below were computed from the real fixture's line lengths and
// mark the start of line 3, line 5, and line 6.
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
			_, offset, err := readFromOffset(realFixture, 0)
			return offset, err
		},

		// offset 8387 = start of line 3 (skips the line-2 entry)
		// offset 11903 = start of line 5 (skips the line 2-4 entries)
		// offset 12140 = start of line 6 (skips the line 2-5 entries)
		ResumePoints: func() ([]int64, error) {
			return []int64{8387, 11903, 12140}, nil
		},

		Want: adaptertest.Want{
			MinEntries:     5,
			UserTurns:      3,
			AssistantTurns: 2,

			Unproven: []string{
				"tool calls and tool results — none of the real droid transcripts " +
					"available on this machine contain a tool_use/tool_result block " +
					"(see session_test.go TestReadSessionFile_RealTranscript)",
			},
		},
	})
}
