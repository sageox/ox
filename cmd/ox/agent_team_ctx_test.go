package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Failure prevented: missing or damaged metadata hides a discussion or prevents
// healthy discussions from appearing in the team context listing.
func TestTeamContextDiscussionLabelsSurviveInvalidMetadata(t *testing.T) {
	for _, tc := range []struct {
		name        string
		metadata    string
		isDirectory bool
		wantLabel   string
	}{
		{"valid title", `{"title":"Release planning","recording_id":"rec_123"}`, false, "Release planning"},
		{"missing file", "", false, "2026-09-09-current"},
		{"unreadable file", "", true, "2026-09-09-current"},
		{"malformed JSON", `{"title":`, false, "2026-09-09-current"},
		{"missing title", `{"recording_id":"rec_123"}`, false, "2026-09-09-current"},
		{"empty title", `{"title":""}`, false, "2026-09-09-current"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			current := filepath.Join(dir, "2026-09-09-current")
			previous := filepath.Join(dir, "2026-09-08-previous")
			require.NoError(t, os.MkdirAll(current, 0o755))
			require.NoError(t, os.MkdirAll(previous, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(previous, "metadata.json"), []byte(`{"title":"Architecture review"}`), 0o644))
			metadataPath := filepath.Join(current, "metadata.json")
			if tc.isDirectory {
				require.NoError(t, os.Mkdir(metadataPath, 0o755))
			} else if tc.metadata != "" {
				require.NoError(t, os.WriteFile(metadataPath, []byte(tc.metadata), 0o644))
			}

			var out bytes.Buffer
			require.True(t, listRecentDiscussions(&out, dir))
			require.Equal(t, fmt.Sprintf("## Recent Discussions\n\nRecent discussions (2 shown, read files in each dir for detail):\n\n- %s — %s\n- Architecture review — %s\n\n", tc.wantLabel, current, previous), out.String())
		})
	}
}
