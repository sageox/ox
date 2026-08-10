package main

import (
	"os"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adaptertest"
)

// TestConformance runs the shared cross-adapter conformance suite
// (pkg/adaptertest) against testdata/session-real.jsonl — a real Codex CLI
// 0.118.0 session (captured from ox's own integration test harness running
// against a scratch project, ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl),
// with the temp working directory and session id anonymized.
//
// Provenance: Codex CLI 0.118.0 (codex_exec, gpt-5.4), captured 2026-08-09,
// cwd/session-id anonymized; conversation content (an "ox agent prime"
// integration-test run, including one real tool-call failure) unchanged.
//
// ReadAll/ReadFrom deliberately use readCodexFile/readCodexFromOffset — the
// RAW per-line parse, before mergeToolEntries — rather than handleRead's
// merged output. mergeToolEntries folds a resolved call+result into a single
// entry (both ToolName and ToolOutput set), which the shared suite's pairing
// model does not represent: it expects a call entry (name, no output) and a
// separate result entry (output, same call_id) so it can prove the two
// correlate. Testing the raw layer proves the class of bug this fixture
// exists to catch — call_id round-tripping correctly across non-adjacent
// lines — directly; mergeToolEntries' own cross-batch merge behavior is
// exercised by TestMergeToolEntries in session_test.go, not by this suite.
func TestConformance_RealTranscript(t *testing.T) {
	adaptertest.Run(t, adaptertest.Suite{
		Adapter:    "codex",
		Provenance: "Codex CLI 0.118.0, real ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl transcript, captured 2026-08-09, cwd/session-id anonymized",

		ReadAll: func() ([]adapterprotocol.RawEntry, error) {
			return readCodexFile(realCodexFixture)
		},

		ReadFrom: func(offset int64) ([]adapterprotocol.RawEntry, int64, error) {
			return readCodexFromOffset(realCodexFixture, offset)
		},
		EndOffset: func() (int64, error) {
			info, err := os.Stat(realCodexFixture)
			if err != nil {
				return 0, err
			}
			return info.Size(), nil
		},
		ResumePoints: func() ([]int64, error) {
			return []int64{
				lineOffset(t, realCodexFixture, 6),  // skips the system + user turns
				lineOffset(t, realCodexFixture, 18), // skips through the second tool result
			}, nil
		},

		Want: adaptertest.Want{
			MinEntries:     11,
			UserTurns:      1,
			AssistantTurns: 3,
			ToolCalls:      3,
			ToolResults:    3,
			PairedResults:  3,
			// the transcript's second tool call (write_stdin re-running `ox
			// agent prime`) genuinely failed with exit code 1 — this is the
			// regression fixture for isCodexToolError's real exec_command
			// output shape (see TestIsCodexToolError_RealExecCommandFormat).
			ErroredResults: 1,

			Unproven: []string{
				"mergeToolEntries' cross-batch pending-call merge (a call read in one " +
					"incremental window, its result in a later one) — this fixture's calls " +
					"and results are all read in a single batch here, so the pendingCallStore " +
					"path in serve.go is not exercised by this suite; see TestMergeToolEntries",
			},
		},
	})
}

const realCodexFixture = "testdata/session-real.jsonl"

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
