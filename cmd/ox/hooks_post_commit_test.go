package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hooks_post_commit_test.go verifies the reverse-index append path: each
// commit during an active recording lands in RecordingState.ProducedCommits.
// Failure prevented: silent regression of the session→commits index that
// `ox session view` Commits section depends on.

func postCommitEnv(t *testing.T) (string, string, string) {
	t.Helper()
	projectRoot, sessionName, agentID := trailerRewriteEnv(t)
	return projectRoot, sessionName, agentID
}

// TestPostCommitHook_AppendsHEADToActiveRecording verifies that, with an
// active recording, runHooksPostCommit appends HEAD's SHA to
// state.ProducedCommits exactly once per call.
func TestPostCommitHook_AppendsHEADToActiveRecording(t *testing.T) {
	projectRoot, sessionName, agentID := postCommitEnv(t)

	sha := commitWithTrailer(t, projectRoot, "first")
	require.NoError(t, runHooksPostCommit(nil, nil))

	state := loadRecordingForTest(t, projectRoot, sessionName)
	require.Equal(t, []string{sha}, state.ProducedCommits,
		"first post-commit must record HEAD sha exactly once")

	// idempotency: re-running on the same HEAD must NOT duplicate
	require.NoError(t, runHooksPostCommit(nil, nil))
	state = loadRecordingForTest(t, projectRoot, sessionName)
	assert.Equal(t, []string{sha}, state.ProducedCommits,
		"running post-commit twice on the same HEAD must not duplicate")

	// second commit appends a second entry
	sha2 := commitWithTrailer(t, projectRoot, "second")
	require.NoError(t, runHooksPostCommit(nil, nil))
	state = loadRecordingForTest(t, projectRoot, sessionName)
	assert.Equal(t, []string{sha, sha2}, state.ProducedCommits)

	_ = agentID
}

// TestPostCommitHook_NoopWithoutRecording verifies the handler exits 0
// and writes nothing when no recording is active. The hook must NEVER
// block a commit.
func TestPostCommitHook_NoopWithoutRecording(t *testing.T) {
	projectRoot, _, agentID := postCommitEnv(t)
	require.NoError(t, session.ClearRecordingStateForAgent(projectRoot, agentID))
	t.Setenv("SAGEOX_AGENT_ID", "")

	commitWithoutTrailer(t, projectRoot, "no recording")
	assert.NoError(t, runHooksPostCommit(nil, nil),
		"post-commit must never error when there is no active recording")
}

// loadRecordingForTest reads the recording state JSON from disk.
// Returns dereferenced struct so tests can compare field-by-field.
func loadRecordingForTest(t *testing.T, projectRoot, sessionName string) session.RecordingState {
	t.Helper()
	path := filepath.Join(projectRoot, "sessions", sessionName, ".recording.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var s session.RecordingState
	require.NoError(t, json.Unmarshal(data, &s))
	return s
}

// (anchor to silence unused import on platforms where time/json aren't otherwise used)
var _ = time.Now

// writeSurfacedTrace seeds the active recording's context-trace with surfaced
// refs so the post-commit citation scan has something to match against.
func writeSurfacedTrace(t *testing.T, projectRoot, sessionName string, events ...contexttrace.Event) string {
	t.Helper()
	path := filepath.Join(projectRoot, "sessions", sessionName, contexttrace.FileName)
	require.NoError(t, contexttrace.NewFileWriter(path).AppendAll(events))
	return path
}

func citedCommitEvents(t *testing.T, tracePath string) []contexttrace.Event {
	t.Helper()
	all, err := contexttrace.ReadEvents(tracePath)
	require.NoError(t, err)
	var out []contexttrace.Event
	for _, ev := range all {
		if ev.Type == contexttrace.EventUsed && ev.Evidence == contexttrace.EvidenceCitedCommit {
			out = append(out, ev)
		}
	}
	return out
}

// TestPostCommitHook_RecordsCitedRefs proves that naming a surfaced ref (or
// its title) in a commit message lands a provable cited_commit `used` event in
// the live trace, with the matcher's word-boundary rules intact, and that the
// scan is idempotent per (ref, commit).
// Failure prevented: the commit-message citation layer silently stops
// contributing to attribution, or double-counts on a re-fired hook.
func TestPostCommitHook_RecordsCitedRefs(t *testing.T) {
	surfaced := []contexttrace.Event{
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamWhisper, Ref: "Ox7f2a", Title: "auth middleware rollout", FromName: "Dana", URL: "https://sageox.ai/m/Ox7f2a", Seq: 2},
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceLocalRecall, Ref: "2026-07-01-ryan-Ox1", RefType: "session", Seq: 4},
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamMemory, Doc: "MEMORY.md"},
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamDocs, Doc: "x.md"},
	}
	tests := []struct {
		name     string
		subject  string
		wantRefs []string
	}{
		{"murmur id cited", "fix(auth): follow Dana's note Ox7f2a", []string{"Ox7f2a"}},
		{"session name cited", "refactor per 2026-07-01-ryan-Ox1", []string{"2026-07-01-ryan-Ox1"}},
		{"title phrase cited", "feat: finish the auth middleware rollout", []string{"Ox7f2a"}},
		{"two refs cited", "chore: Ox7f2a and 2026-07-01-ryan-Ox1", []string{"Ox7f2a", "2026-07-01-ryan-Ox1"}},
		{"ref inside longer token is not a citation", "fix: Ox7f2abc regression", nil},
		{"doc name cited", "docs: update MEMORY.md", []string{"MEMORY.md"}},
		{"doc name below the length floor is not a citation", "docs: update x.md", nil},
		{"nothing cited", "fix: unrelated change", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectRoot, sessionName, _ := postCommitEnv(t)
			t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
			tracePath := writeSurfacedTrace(t, projectRoot, sessionName, surfaced...)

			sha := commitWithTrailer(t, projectRoot, tt.subject)
			require.NoError(t, runHooksPostCommit(nil, nil))

			got := citedCommitEvents(t, tracePath)
			var refs []string
			for _, ev := range got {
				refs = append(refs, ev.Ref)
				assert.Equal(t, sha[:7], ev.Commit, "commit must be the short sha")
				assert.Equal(t, contexttrace.MechanismCitation, ev.Mechanism)
				assert.Equal(t, contexttrace.SourceOnDemand, ev.Source)
				if ev.Ref == "Ox7f2a" {
					assert.Equal(t, "murmur", ev.RefType)
					assert.Equal(t, "auth middleware rollout", ev.Title)
					assert.Equal(t, "https://sageox.ai/m/Ox7f2a", ev.URL)
					assert.Equal(t, 2, ev.SurfacedSeq)
				}
			}
			assert.Equal(t, tt.wantRefs, refs)

			// Idempotent per (ref, commit): a re-fired hook on the same HEAD
			// adds nothing.
			state := loadRecordingForTest(t, projectRoot, sessionName)
			recordCommitCitations(projectRoot, &state, sha)
			assert.Len(t, citedCommitEvents(t, tracePath), len(tt.wantRefs))

			// Still a no-op without a recording / trace, and never an error.
			recordCommitCitations(projectRoot, nil, sha)
			recordCommitCitations(projectRoot, &session.RecordingState{SessionPath: t.TempDir()}, sha)
		})
	}
}

// TestPostCommitHook_CitationSeqIsEntryCount anchors the cited_commit event to
// the recording's current EntryCount — the same seq frame the whisper and
// invoked writers use — so "used after surfaced" comparisons hold.
func TestPostCommitHook_CitationSeqIsEntryCount(t *testing.T) {
	projectRoot, sessionName, agentID := postCommitEnv(t)
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
		s.EntryCount = 17
	}))
	tracePath := writeSurfacedTrace(t, projectRoot, sessionName,
		contexttrace.Event{Type: contexttrace.EventProvided, Source: contexttrace.SourceProjectWhisper, WhisperID: "Ox9b1c", From: "riley", Seq: 5})

	commitWithTrailer(t, projectRoot, "fix: per Ox9b1c")
	require.NoError(t, runHooksPostCommit(nil, nil))

	got := citedCommitEvents(t, tracePath)
	require.Len(t, got, 1)
	assert.Equal(t, 17, got[0].Seq)
	assert.Equal(t, 5, got[0].SurfacedSeq)
	assert.Equal(t, "Ox9b1c", got[0].Ref)
}

// TestPostCommitHook_CitationRespectsOptOut: with recap.trace_consultations
// off, a cited ref is not written (ADR-027 opt-out), and the commit index
// still updates.
func TestPostCommitHook_CitationRespectsOptOut(t *testing.T) {
	projectRoot, sessionName, _ := postCommitEnv(t)
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	require.NoError(t, SetConfigValue("recap.trace_consultations", "off", ConfigLevelUser, ""))
	tracePath := writeSurfacedTrace(t, projectRoot, sessionName,
		contexttrace.Event{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamWhisper, Ref: "Ox7f2a", Seq: 1})

	sha := commitWithTrailer(t, projectRoot, "fix: per Ox7f2a")
	require.NoError(t, runHooksPostCommit(nil, nil))

	assert.Empty(t, citedCommitEvents(t, tracePath))
	assert.Equal(t, []string{sha}, loadRecordingForTest(t, projectRoot, sessionName).ProducedCommits)
}

// TestPostCommitHook_CountsCitedUseOnStatusline: a commit that names a
// surfaced item counts it as used on the recording harness session's status
// line, once per item however many commits cite it. Failure prevented: the
// strongest evidence of use (the coworker's own commit) never reaching the
// bar, or every re-citing commit inflating "used".
func TestPostCommitHook_CountsCitedUseOnStatusline(t *testing.T) {
	const harnessSession = "c1a2b3c4-0000-4000-8000-0000000000aa"
	tests := []struct {
		name     string
		subjects []string
		want     oxactivity.Counts
	}{
		{"one citation", []string{"fix(auth): follow Ox7f2a"}, oxactivity.Counts{Murmurs: 1, Used: 1}},
		{"cited by two commits counts once", []string{"fix: per Ox7f2a", "test: cover Ox7f2a"}, oxactivity.Counts{Murmurs: 1, Used: 1}},
		{"two items cited", []string{"chore: Ox7f2a and 2026-07-01-ryan-Ox1"}, oxactivity.Counts{Sessions: 1, Murmurs: 1, Used: 2}},
		{"nothing cited", []string{"fix: unrelated change"}, oxactivity.Counts{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectRoot, sessionName, _ := postCommitEnv(t)
			t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
			// the shared fixture's agent ID is not one a state file may be named
			// by; re-key the recording to a valid ID with a harness session
			const agentID = "OxPcU1"
			state := loadRecordingForTest(t, projectRoot, sessionName)
			state.AgentID = agentID
			state.AgentSessionID = harnessSession
			data, err := json.Marshal(state)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(projectRoot, "sessions", sessionName, ".recording.json"), data, 0o644))
			t.Setenv("SAGEOX_AGENT_ID", agentID)
			writeSurfacedTrace(t, projectRoot, sessionName,
				contexttrace.Event{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamWhisper, Ref: "Ox7f2a", Seq: 2},
				contexttrace.Event{Type: contexttrace.EventProvided, Source: contexttrace.SourceLocalRecall, Ref: "2026-07-01-ryan-Ox1", RefType: "session", Seq: 4},
			)

			for _, subject := range tt.subjects {
				commitWithTrailer(t, projectRoot, subject)
				require.NoError(t, runHooksPostCommit(nil, nil))
			}

			got := oxactivity.LoadState(projectRoot, oxactivity.StateKey(agentID, harnessSession))
			assert.Equal(t, tt.want, got.Counts())
		})
	}

	t.Run("recording without a harness session writes no state", func(t *testing.T) {
		projectRoot, sessionName, _ := postCommitEnv(t)
		t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
		writeSurfacedTrace(t, projectRoot, sessionName,
			contexttrace.Event{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamWhisper, Ref: "Ox7f2a", Seq: 2})
		commitWithTrailer(t, projectRoot, "fix: per Ox7f2a")
		require.NoError(t, runHooksPostCommit(nil, nil))
		assert.NoDirExists(t, filepath.Join(projectRoot, ".sageox", "cache", "statusline"))
	})
}
