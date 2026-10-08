package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/conversation/read"
	"github.com/stretchr/testify/require"
)

// TestWalkthroughTextNamesAreaAndMarks: --text output for an area take says
// "a screen area" rather than a blank window name, and prints presenter marks
// as their own lines, ahead of a click at the same instant.
// Failure prevented: a human reading --text sees "window:  (800x600)" and
// marks indistinguishable from clicks.
func TestWalkthroughTextNamesAreaAndMarks(t *testing.T) {
	root := repoPath("..", "..", "internal", "conversation", "read", "testdata", "walkthrough-area", "discussions")
	env := read.New(root, time.Time{}).Walkthrough("cnv_01a0f490-0000-7000-8000-0000000000d1", read.WalkthroughOptions{})
	if !env.Success {
		t.Fatalf("walkthrough failed: %+v", env.Error)
	}
	var buf bytes.Buffer
	renderConversationWalkthroughText(&buf, env)
	out := buf.String()

	for _, want := range []string{
		"area: a screen area (800x600)",
		"mark     marked by the presenter (#1)",
		"pointer outside the area",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\nwindow:") {
		t.Errorf("an area take must not print a window line:\n%s", out)
	}
	mark, click := strings.Index(out, "marked by the presenter (#2)"), strings.Index(out, `click    AXButton "Pay now"`)
	if mark < 0 || click < 0 || mark > click {
		t.Errorf("the mark at 00:00:04 must print before the click at the same instant:\n%s", out)
	}
}

// Reject conflicting read/paid-work modes before a request can consume quota.
func TestWalkthroughCommandRecoveryModeValidation(t *testing.T) {
	useWalkthroughReader(t)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"retry read", []string{"--retry"}, "--retry requires"},
		{"extract without pin", []string{"--extract", "--cues", "1"}, "--extract needs"},
		{"extract without selector", []string{"--extract", "--revision", strings.Repeat("a", 64)}, "--extract needs"},
		{"extract frame budget", []string{"--extract", "--revision", strings.Repeat("a", 64), "--cues", "1", "--max-frames", "9"}, "--extract needs"},
		{"prepare and extract", []string{"--prepare", "--extract"}, "separate operations"},
		{"prepare with pin", []string{"--prepare", "--revision", "old"}, "separate operations"},
		{"prepare with cues", []string{"--prepare", "--cues", "1"}, "separate operations"},
		{"prepare with time", []string{"--prepare", "--from", "1s", "--to", "2s"}, "separate operations"},
		{"prepare with fetch", []string{"--prepare", "--fetch"}, "separate operations"},
		{"job with transcript", []string{"--job", "job-1", "--transcript"}, "separate operations"},
		{"job with cursor", []string{"--job", "job-1", "--cursor", "cursor"}, "separate operations"},
		{"job with extract", []string{"--job", "job-1", "--extract"}, "separate operations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := runConversationInProc(t, "walkthrough", append([]string{shareTestWalkthroughRec}, tc.args...)...)
			require.Error(t, err)
			env := decodeConvEnvelope(t, out)
			require.False(t, env.Success)
			require.Equal(t, read.ErrCodeInvalidSelector, env.Error.Code)
			require.Contains(t, env.Error.Message, tc.want)
		})
	}
}

func TestWalkthroughCommandFetchKeepsSourceOnMissingImages(t *testing.T) {
	useWalkthroughReader(t)
	out, _, err := runConversationInProc(t, "walkthrough", shareTestWalkthroughRec, "--fetch", "--cues", "1")
	require.NoError(t, err, out)
	env := decodeConvEnvelope(t, out)
	require.True(t, env.Success)
	require.Contains(t, string(env.Data), "Settings walkthrough")
}

func TestWalkthroughTextTranscriptPageRetainsContinuation(t *testing.T) {
	var out bytes.Buffer
	renderConversationWalkthroughText(&out, &read.Envelope{Data: &read.WalkthroughData{
		Title: "No screen evidence", Transcript: &read.WalkthroughTranscript{
			Cues:       []read.TranscriptCue{{N: 101, Start: "00:00:05", Text: "Keep the final panel\x1b[31m"}},
			NextCursor: "pinned-next-page",
		},
	}})
	require.Contains(t, out.String(), "[101] 00:00:05 Keep the final panel")
	require.Contains(t, out.String(), "next transcript cursor: pinned-next-page")
	require.NotContains(t, out.String(), "\x1b[31m")
}

// TestWalkthroughTextTargetLineJoinsOnlyWhatIsThere: a window target missing
// its app or title must not print a dangling separator.
func TestWalkthroughTextTargetLineJoinsOnlyWhatIsThere(t *testing.T) {
	for _, tc := range []struct {
		target read.WalkthroughTarget
		want   string
	}{
		{read.WalkthroughTarget{Kind: "window", Title: "Team", Width: 1280, Height: 800}, "window: Team (1280x800)"},
		{read.WalkthroughTarget{Kind: "window", App: "Browser", Width: 1280, Height: 800}, "window: Browser (1280x800)"},
		{read.WalkthroughTarget{Kind: "window", Width: 1280, Height: 800}, "window: (1280x800)"},
		{read.WalkthroughTarget{App: "Browser", Title: "Team"}, "window: Browser · Team"},
	} {
		target := tc.target
		var buf bytes.Buffer
		renderConversationWalkthroughText(&buf, &read.Envelope{Success: true, Data: &read.WalkthroughData{Title: "T", Target: &target}})
		if !strings.Contains(buf.String(), tc.want+"\n") {
			t.Errorf("%+v: want %q in:\n%s", tc.target, tc.want, buf.String())
		}
	}
}
