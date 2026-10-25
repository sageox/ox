//go:build slow

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/conversation/read"
	"github.com/stretchr/testify/require"
)

// TestWalkthroughProducerArtifactE2E uses artifacts from the actual workflow
// producer, catching cross-repository schema drift that hand-written fixtures
// cannot. The parent evaluation creates the video and invokes the real builder.
func TestWalkthroughProducerArtifactE2E(t *testing.T) {
	source := os.Getenv("SAGEOX_WALKTHROUGH_FIXTURE")
	if source == "" {
		t.Skip("set SAGEOX_WALKTHROUGH_FIXTURE to a real producer artifact directory")
	}
	raw, err := os.ReadFile(filepath.Join(source, "fixture.json"))
	require.NoError(t, err)
	var manifest struct {
		RecordingID string `json:"recording_id"`
		Revision    string `json:"revision"`
		CueCount    int    `json:"cue_count"`
	}
	require.NoError(t, json.Unmarshal(raw, &manifest))
	e2e := setupConversationE2E(t)
	folder := "2026-10-08-00-00-producer-evidence"
	discussionRoot := filepath.Join(e2e.primaryTeam.path, "discussions")
	target := filepath.Join(discussionRoot, folder)
	require.NoError(t, filepath.WalkDir(source, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(target, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0644)
	}))
	index := []any{map[string]any{"recording_id": manifest.RecordingID, "folder": folder, "title": "Producer evidence fixture"}}
	raw, err = json.Marshal(index)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(discussionRoot, "INDEX.json"), raw, 0644))
	// Changing the current transcript cannot change a pinned source snapshot.
	require.NoError(t, os.WriteFile(filepath.Join(target, "transcript.vtt"), []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nCURRENT MUTATED WORDS\n"), 0644))
	seen := map[int]bool{}
	cursor := ""
	for page := 0; page < 10; page++ {
		args := []string{"walkthrough", manifest.RecordingID, "--transcript", "--revision", manifest.Revision, "--json"}
		if cursor != "" {
			args = append(args, "--cursor", cursor)
		}
		out, exit := e2e.Run(t, args...)
		require.Zero(t, exit, out)
		env, _ := decodeConversationEnvelope(t, out)
		var data read.WalkthroughData
		decodeConversationData(t, env, &data)
		require.NotNil(t, data.Transcript)
		require.Equal(t, manifest.CueCount, data.Transcript.Total)
		for _, cue := range data.Transcript.Cues {
			require.False(t, seen[cue.N])
			seen[cue.N] = true
			require.NotContains(t, cue.Text, "CURRENT MUTATED")
		}
		cursor = data.Transcript.NextCursor
		if cursor == "" {
			break
		}
	}
	require.Empty(t, cursor, "pagination did not terminate")
	require.Len(t, seen, manifest.CueCount)
	out, exit := e2e.Run(t, "walkthrough", manifest.RecordingID, "--revision", manifest.Revision, "--fetch", "--json")
	require.Zero(t, exit, out)
	env, _ := decodeConversationEnvelope(t, out)
	var data read.WalkthroughData
	decodeConversationData(t, env, &data)
	require.NotEmpty(t, data.Moments)
	for _, moment := range data.Moments {
		if moment.Frame == nil {
			continue
		}
		require.NotEmpty(t, moment.Frame.LocalImage)
		b, e := os.ReadFile(moment.Frame.LocalImage)
		require.NoError(t, e)
		sum := sha256.Sum256(b)
		require.Equal(t, moment.Frame.SHA256, hex.EncodeToString(sum[:]))
	}
	// A window without a retained frame still discloses its source words.
	out, exit = e2e.Run(t, "walkthrough", manifest.RecordingID, "--revision", manifest.Revision, "--cues", "47", "--json")
	require.Zero(t, exit, out)
	env, _ = decodeConversationEnvelope(t, out)
	decodeConversationData(t, env, &data)
	require.Len(t, data.Transcript.Cues, 1)
	require.Equal(t, 47, data.Transcript.Cues[0].N)
	out, exit = e2e.Run(t, "walkthrough", manifest.RecordingID, "--revision", manifest.Revision[:63]+"z", "--json")
	require.NotZero(t, exit, out)
	require.Contains(t, out, "revision_unavailable")
}
