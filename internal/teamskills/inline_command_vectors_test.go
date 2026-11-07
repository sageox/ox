package teamskills

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

type inlineCommandVector struct {
	Name          string `json:"name"`
	Manifest      string `json:"manifest"`
	InlineCommand bool   `json:"inline_command"`
}

// TestClassify_InlineCommandSharedVectors pins ox's inline-command rule to the
// vector file the SageOx server is checked against too
// (packages/teamskills/testdata/ox-parity/, copied verbatim).
// Failure prevented: a skill with "Working tree: !`git status`" runs a command
// in Claude Code while one side calls it prose — ox installs it unreviewed, or
// the server withholds from chat a skill ox treats as portable.
func TestClassify_InlineCommandSharedVectors(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("testdata", "inline_command_vectors.json"))
	require.NoError(t, err)
	var file struct {
		Comment string                `json:"comment"`
		Vectors []inlineCommandVector `json:"vectors"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&file))
	require.NotEmpty(t, file.Vectors, "the shared vector file decoded to nothing; every case below would pass vacuously")

	for _, v := range file.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			t.Parallel()
			verdict := Classify(Skill{Name: "x", Files: []File{{Path: "SKILL.md", Content: []byte(v.Manifest)}}})
			require.Equal(t, v.InlineCommand, slices.Contains(verdict.Capabilities, CapInlineCommand),
				"inline-command for %q (verdict: %s)", v.Manifest, verdict.Describe())
			// Nothing else in a vector can make it executable, so the two must agree:
			// an inline command is exactly what decides whether it needs approval.
			require.Equal(t, v.InlineCommand, verdict.Executable, "executable for %q", v.Manifest)
		})
	}
}
