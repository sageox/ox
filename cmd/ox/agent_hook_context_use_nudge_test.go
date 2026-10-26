package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/stretchr/testify/require"
)

// Failure prevented: surfaced team context goes unreported because nothing
// ever asks — or the ask nags on every prompt, or fires after the agent
// already reported. The observable difference is whether the one-line
// reminder reaches the model.
func TestEmitContextUseNudge(t *testing.T) {
	surfacedDoc := contexttrace.Event{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamMemory, Doc: "MEMORY.md"}
	recallHit := contexttrace.Event{Type: contexttrace.EventProvided, Source: contexttrace.SourceLocalRecall, Ref: "sess-1", RefType: "session"}

	cases := []struct {
		name      string
		entries   int
		events    []contexttrace.Event
		score     bool
		notRecord bool
		wantFire  bool
		wantCount string
	}{
		{name: "fires with surfaced items and enough entries", entries: 8, events: []contexttrace.Event{surfacedDoc, recallHit}, wantFire: true, wantCount: "2 team context items"},
		{name: "too few entries", entries: 7, events: []contexttrace.Event{surfacedDoc}},
		{name: "nothing surfaced", entries: 20, events: []contexttrace.Event{{Type: contexttrace.EventInvoked, Source: contexttrace.SourceOxCLI, Command: "murmur list"}}},
		{name: "empty trace", entries: 20},
		{name: "used event already recorded", entries: 20, events: []contexttrace.Event{surfacedDoc, {Type: contexttrace.EventUsed, Evidence: contexttrace.EvidenceNone}}},
		{name: "legacy influenced counts as reported", entries: 20, events: []contexttrace.Event{surfacedDoc, {Type: contexttrace.EventInfluenced, Doc: "MEMORY.md"}}},
		{name: "score file exists", entries: 20, events: []contexttrace.Event{surfacedDoc}, score: true},
		{name: "not recording", notRecord: true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agentID := "OxNdg" + string(rune('A'+i))
			projectRoot := setupSessionTestProject(t)
			if !tc.notRecord {
				state := startTestRecording(t, projectRoot, agentID, "test")
				require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
					s.EntryCount = tc.entries
				}))
				require.NoError(t, contexttrace.NewWriter(state.SessionPath).AppendAll(tc.events))
			}
			if tc.score {
				require.NoError(t, session.WriteSageoxScoreCategory(agentID, session.ScoreMinor, "test"))
			}

			var buf bytes.Buffer
			emitContextUseNudge(&buf, projectRoot, agentID)
			out := buf.String()
			if !tc.wantFire {
				require.Empty(t, out)
				return
			}
			require.Contains(t, out, "<system-reminder>[ox] ")
			require.Contains(t, out, tc.wantCount)
			require.Contains(t, out, "ox agent "+agentID+" context used <ref> --why \"...\"")
			require.Contains(t, out, "ox agent "+agentID+" context used --none")
			require.Equal(t, 1, strings.Count(out, "\n"), "one line")

			// once per session
			var again bytes.Buffer
			emitContextUseNudge(&again, projectRoot, agentID)
			require.Empty(t, again.String(), "nudge must fire once per session")
		})
	}
}

// A new recording for the same agent re-arms the nudge: the stamp records
// which session it fired for, not merely that it ever fired.
func TestEmitContextUseNudge_NewSessionRearms(t *testing.T) {
	const agentID = "OxNdgRe"
	projectRoot := setupSessionTestProject(t)
	arm := func() {
		state := startTestRecording(t, projectRoot, agentID, "test")
		require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
			s.EntryCount = 10
		}))
		require.NoError(t, contexttrace.NewWriter(state.SessionPath).Append(contexttrace.Event{
			Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamMemory, Doc: "MEMORY.md",
		}))
	}

	arm()
	var first bytes.Buffer
	emitContextUseNudge(&first, projectRoot, agentID)
	require.NotEmpty(t, first.String())

	require.NoError(t, session.ClearRecordingStateForAgent(projectRoot, agentID))
	arm()
	var second bytes.Buffer
	emitContextUseNudge(&second, projectRoot, agentID)
	require.NotEmpty(t, second.String(), "a new session must re-arm the nudge")
}

func TestEmitContextUseNudge_EmptyInputsAreSilent(t *testing.T) {
	var buf bytes.Buffer
	emitContextUseNudge(&buf, "", "OxX")
	emitContextUseNudge(&buf, t.TempDir(), "")
	require.Empty(t, buf.String())
}
