package read

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/vtt"
	"github.com/stretchr/testify/require"
)

// Rebind a fixture after intentional producer payload changes so each test
// exercises its own validity check instead of failing at the outer hash check.
func rebindEvidenceFixture(t *testing.T, root, dir string) string {
	t.Helper()
	read := func(name string) []byte {
		b, e := os.ReadFile(filepath.Join(root, walkthroughFolder, dir, name))
		require.NoError(t, e)
		return b
	}
	var index evidenceIndex
	require.NoError(t, json.Unmarshal(read("index.json"), &index))
	index.TranscriptRevision = digest(read("transcript.vtt"))
	index.Revision = digest([]byte(index.SourceRevision + "\n" + index.TranscriptRevision + "\n" + digest(read("frames.json")) + "\n" + digest(read("observations.json")) + "\nwalkthrough-evidence-v2"))
	b, e := json.Marshal(index)
	require.NoError(t, e)
	writeWalkthroughFile(t, root, dir+"/index.json", string(b))
	return index.Revision
}

func TestEvidenceRejectsMalformedProducerPayloads(t *testing.T) {
	cases := []struct {
		name, file, content, want string
		rebind                    bool
	}{
		{"index JSON", "index.json", "{", "index unreadable", false},
		{"index schema", "index.json", `{"spec":"keyframe/3"}`, "index unreadable", false},
		{"invalid digest", "index.json", `{"spec":"keyframe/2","revision":"bad"}`, "identity or payload path", false},
		{"catalog JSON", "frames.json", "[", "catalog unreadable", true},
		{"catalog cap", "frames.json", "[" + strings.Repeat("{},", maxKeyframes) + "{}]", "catalog unreadable", true},
		{"invalid WebVTT", "transcript.vtt", "invalid transcript", "valid WebVTT", true},
		{"frame path escape", "frames.json", `[{"id":"frame-safe","sha256":"` + strings.Repeat("a", 64) + `","width":3,"height":2,"path":"../outside.jpg"}]`, "identity, geometry or path", true},
		{"frame geometry", "frames.json", `[{"id":"frame-safe","sha256":"` + strings.Repeat("a", 64) + `","width":0,"height":2,"path":"frame-` + strings.Repeat("a", 64) + `.jpg"}]`, "identity, geometry or path", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, _, dir := stageEvidence(t, 2)
			writeWalkthroughFile(t, root, dir+"/"+tc.file, tc.content)
			if tc.rebind {
				rebindEvidenceFixture(t, root, dir)
			}
			env := New(root, time.Time{}).Walkthrough(walkthroughCnv, WalkthroughOptions{})
			require.False(t, env.Success)
			require.Equal(t, ErrCodeReadError, env.Error.Code)
			require.Contains(t, env.Error.Message, tc.want)
		})
	}
}

func TestEvidenceWindowTruncationAndUnfetchedIdentity(t *testing.T) {
	root, _, dir := stageEvidence(t, 3)
	b, err := os.ReadFile(filepath.Join(root, walkthroughFolder, dir, "frames.json"))
	require.NoError(t, err)
	var frames []evidenceFrame
	require.NoError(t, json.Unmarshal(b, &frames))
	second := frames[0]
	second.ID += "-second"
	second.TimestampSeconds = 1
	frames = append(frames, second)
	b, err = json.Marshal(frames)
	require.NoError(t, err)
	writeWalkthroughFile(t, root, dir+"/frames.json", string(b))
	rev := rebindEvidenceFixture(t, root, dir)
	writeWalkthroughFile(t, root, dir+"/"+frames[0].Path, fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize 512\n", frames[0].SHA256))
	data := walkthroughData(t, New(root, time.Time{}).Walkthrough(walkthroughCnv, WalkthroughOptions{Revision: rev, HasWindow: true, FromOffset: 0, ToOffset: 2 * time.Second, Limit: 1}))
	require.True(t, data.Window.Truncated)
	require.Equal(t, 2, data.Window.Total)
	require.Len(t, data.Moments, 1)
	require.Equal(t, "00:00:02.000", data.Window.To)
	require.Equal(t, "unfetched", data.Moments[0].Frame.Availability)
	require.NotEmpty(t, data.Moments[0].Frame.FetchCommand)
	require.Empty(t, data.Moments[0].Frame.LocalImage)
	require.Len(t, data.Transcript.Cues, 3)
}

func TestPinnedNativeObservationsStayWithinDisclosureWindow(t *testing.T) {
	root, _, dir := stageEvidence(t, 3)
	rows := []any{"invalid row", map[string]any{"missing": "clock"}, map[string]any{"s": 0, "x": 999}}
	for i := 0; i < DefaultMomentLimit+3; i++ {
		rows = append(rows, map[string]any{"s": 1.5, "x": i})
	}
	obs := map[string]any{"hints": rows, "screen_context": map[string]any{"pointer": rows, "ax_nodes": rows}, "layers": []any{"pointer"}, "unknown": strings.Repeat("private", 100)}
	b, err := json.Marshal(obs)
	require.NoError(t, err)
	writeWalkthroughFile(t, root, dir+"/observations.json", string(b))
	rev := rebindEvidenceFixture(t, root, dir)
	data := walkthroughData(t, New(root, time.Time{}).Walkthrough(walkthroughCnv, WalkthroughOptions{Revision: rev, CueFirst: 2, CueLast: 2}))
	require.NotContains(t, data.Observations, "unknown")
	require.Equal(t, []int{2, 2}, data.Window.Cues)
	hints := data.Observations["hints"].(map[string]any)
	require.Equal(t, DefaultMomentLimit+3, hints["total"])
	require.Equal(t, DefaultMomentLimit, hints["returned"])
	require.Equal(t, true, hints["truncated"])
	for _, row := range hints["rows"].([]any) {
		require.Equal(t, 1.5, row.(map[string]any)["s"])
	}
	screen := data.Observations["screen_context"].(map[string]any)
	require.Equal(t, hints, screen["pointer"])
	require.Equal(t, hints, screen["ax_nodes"])
	clipped := filterEvidenceObservations(b, nil, WalkthroughOptions{HasWindow: true, FromOffset: 2 * time.Second, ToOffset: 3 * time.Second})
	require.Equal(t, 0, clipped["hints"].(map[string]any)["returned"])
	require.Nil(t, filterEvidenceObservations([]byte("{"), nil, WalkthroughOptions{}))
	metadata := filterEvidenceObservations([]byte(`{"hints":{"status":"unavailable"}}`), nil, WalkthroughOptions{})
	require.Equal(t, "unavailable", metadata["hints"].(map[string]any)["status"])
}

func TestEvidenceNativePayloadCannotExpandUnboundedly(t *testing.T) {
	wide := map[string]any{}
	for i := 0; i < 30; i++ {
		wide[fmt.Sprintf("field-%d", i)] = i
	}
	require.Len(t, boundedObservation(wide, 0), 24)
	var deep any = "hidden"
	for i := 0; i < 8; i++ {
		deep = []any{deep}
	}
	bounded := boundedObservation(deep, 0)
	for i := 0; i < 7; i++ {
		bounded = bounded.([]any)[0]
	}
	require.Nil(t, bounded)
	require.Len(t, boundedObservation(make([]any, DefaultMomentLimit+1), 0), DefaultMomentLimit)
}

func TestEvidenceCursorRejectsInvalidOffsets(t *testing.T) {
	cues := make([]vtt.Cue, DefaultCueWindow+1)
	for i := range cues {
		cues[i] = vtt.Cue{Index: i + 1, Start: time.Duration(i) * time.Second, End: time.Duration(i+1) * time.Second, Text: "source"}
	}
	page, e := walkthroughTranscriptPage(cues, "transcript", "evidence", WalkthroughOptions{})
	require.Nil(t, e)
	scope := strings.Split(page.NextCursor, ":")[0]
	for _, offset := range []string{"-1", "not-a-number", "999999"} {
		_, e = walkthroughTranscriptPage(cues, "transcript", "evidence", WalkthroughOptions{Cursor: scope + ":" + offset})
		require.NotNil(t, e)
		require.Equal(t, ErrCodeInvalidSelector, e.Code)
	}
}

func TestEvidenceImageVerificationChecksGeometryAndFileBoundary(t *testing.T) {
	root, rev, _ := stageEvidence(t, 2)
	r := New(root, time.Time{})
	data := walkthroughData(t, r.Walkthrough(walkthroughCnv, WalkthroughOptions{Revision: rev}))
	frame := data.Moments[0].Frame
	require.NotEmpty(t, frame.LocalImage)
	require.True(t, r.verifiedEvidenceImage(frame.LocalImage, frame.SHA256, 3, 2))
	require.False(t, r.verifiedEvidenceImage(frame.LocalImage, frame.SHA256, 4, 2))
	require.False(t, r.verifiedEvidenceImage(filepath.Join(root, "missing.jpg"), frame.SHA256, 3, 2))
	require.False(t, r.verifiedEvidenceImage(root, frame.SHA256, 3, 2))
	absent := New(filepath.Join(t.TempDir(), "missing", "discussions"), time.Time{})
	require.False(t, absent.verifiedEvidenceImage(frame.LocalImage, frame.SHA256, 3, 2))
}

func TestLegacyWalkthroughTranscriptPaginationDetectsSourceDrift(t *testing.T) {
	root := copyTree(t, walkthroughRoot)
	var source strings.Builder
	source.WriteString("WEBVTT\n\n")
	for i := 0; i < DefaultCueWindow+1; i++ {
		fmt.Fprintf(&source, "%s --> %s\nsource %d\n\n", formatVTTTimestamp(time.Duration(i)*time.Second), formatVTTTimestamp(time.Duration(i+1)*time.Second), i)
	}
	writeWalkthroughFile(t, root, "transcript.vtt", source.String())
	reader := New(root, time.Time{})
	env := reader.Walkthrough(walkthroughCnv, WalkthroughOptions{Transcript: true})
	data := walkthroughData(t, env)
	require.Empty(t, data.Moments)
	require.NotEmpty(t, data.Transcript.NextCursor)
	require.Contains(t, env.Guidance, "--cursor "+data.Transcript.NextCursor)
	next := walkthroughData(t, reader.Walkthrough(walkthroughCnv, WalkthroughOptions{Transcript: true, Cursor: data.Transcript.NextCursor}))
	require.Len(t, next.Transcript.Cues, 1)
	writeWalkthroughFile(t, root, "transcript.vtt", strings.ReplaceAll(source.String(), "source", "changed"))
	env = reader.Walkthrough(walkthroughCnv, WalkthroughOptions{Transcript: true, Cursor: data.Transcript.NextCursor})
	require.False(t, env.Success)
	require.Equal(t, ErrCodeInvalidSelector, env.Error.Code)
}
