//go:build slow

// conversation_e2e_walkthrough_test.go — hermetic binary-level scenarios for
// `ox walkthrough`: the real binary, behind the real access
// gate, reading a desktop-produced walkthrough out of a staged team context.
//
// The walkthrough folder is the read package's walkthrough-desktop fixture
// (pointer, ax-tree, and keyframe-hints layers as SageOx Desktop writes
// them, plus server keyframes), copied into the harness's discussions root.

package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const convE2EWalkthroughCnv = "cnv_01a0f488-0000-7000-8000-0000000000b1"

// stageWalkthroughDiscussions replaces the harness's discussions tree with
// the desktop walkthrough fixture.
func stageWalkthroughDiscussions(t *testing.T, e2e *conversationE2E) string {
	t.Helper()
	src := repoPath("..", "..", "internal", "conversation", "read", "testdata", "walkthrough-desktop", "discussions")
	dst := filepath.Join(e2e.primaryTeam.path, "discussions")
	require.NoError(t, os.RemoveAll(dst))
	require.NoError(t, filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	}))
	return dst
}

// TestConversationE2E_Walkthrough proves the shipped binary serves a
// walkthrough's moments through the access gate: clicks named by the
// accessibility layer, tied to their cues, with show pointing at the
// command. Failure prevented: the command exists in tests but is not
// registered, or bypasses the team gate, in the binary users run.
func TestConversationE2E_Walkthrough(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)
	stageWalkthroughDiscussions(t, e2e)

	out, exit := e2e.Run(t, "conversation", "show", convE2EWalkthroughCnv)
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.Contains(t, env.Guidance, "ox walkthrough "+convE2EWalkthroughCnv)

	out, exit = e2e.Run(t, "conversation", "walkthrough", convE2EWalkthroughCnv, "--cues", "2-3")
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ = decodeConversationEnvelope(t, out)
	require.True(t, env.Success, "out:\n%s", out)

	var data struct {
		ScreenRecording bool `json:"screen_recording"`
		Moments         []struct {
			At      string          `json:"at"`
			Cue     int             `json:"cue"`
			Kind    string          `json:"kind"`
			Element json.RawMessage `json:"element"`
		} `json:"moments"`
	}
	decodeConversationData(t, env, &data)
	require.True(t, data.ScreenRecording)
	require.Len(t, data.Moments, 5)
	require.Equal(t, "click", data.Moments[0].Kind)
	require.Equal(t, 2, data.Moments[0].Cue)
	require.Contains(t, string(data.Moments[0].Element), `"title":"Saved"`)
	require.NotContains(t, out, "SECRET", "typed values and URL queries never leave the reader")

	out, exit = e2e.Run(t, "conversation", "walkthrough", convE2EWalkthroughCnv, "--text")
	require.Equal(t, 0, exit, "out:\n%s", out)
	require.True(t, strings.Contains(out, `click    AXLink "Saved" #nav-saved`), "out:\n%s", out)
}
