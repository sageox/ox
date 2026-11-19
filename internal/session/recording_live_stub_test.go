package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCleanupOrphanedStubsInDir_KeepsAnUnfinishedLiveStream: once a session is
// published, the cache prune keeps only its live-stream files until the
// stream ends (the daemon's live tick scans that cache to finish it). Such a
// folder has no raw.jsonl, no meta.json and no recording marker, so after the
// one-hour grace the orphan sweep took it for a phantom and removed it. A close
// still owed after an outage longer than an hour was stranded, and the session
// read as live on SageOx for good.
func TestCleanupOrphanedStubsInDir_KeepsAnUnfinishedLiveStream(t *testing.T) {
	old := time.Now().Add(-2 * orphanStubGracePeriod)
	cases := []struct {
		name     string
		state    string
		markers  []string
		wantKept bool
	}{
		{name: "close still owed", state: `{"status":"closing","conversation_id":"cnv_x"}`, wantKept: true},
		{name: "never opened", state: `{"status":"pending"}`, wantKept: true},
		{name: "streaming", state: `{"status":"streaming"}`, markers: []string{"close-requested"}, wantKept: true},
		{name: "unreadable state fails closed", state: `{not json`, wantKept: true},
		{name: "closed: a finished stream is reclaimed", state: `{"status":"closed"}`, wantKept: false},
		{name: "abort requested: the delete is queued outside the folder", state: `{"status":"streaming"}`, markers: []string{"delete-requested"}, wantKept: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "2026-11-18T23-15-person-a-OxAbCd")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "stream-state.json"), []byte(tc.state), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "spool.jsonl"), []byte(`{"type":"user"}`+"\n"), 0o600))
			for _, m := range tc.markers {
				require.NoError(t, os.WriteFile(filepath.Join(dir, m), nil, 0o600))
			}
			require.NoError(t, os.Chtimes(dir, old, old))

			res := CleanupOrphanedStubsInDir(root)

			if tc.wantKept {
				assert.Zero(t, res.Removed)
				assert.FileExists(t, filepath.Join(dir, "stream-state.json"), "an unfinished live stream lost its folder")
				return
			}
			assert.Equal(t, 1, res.Removed, "control: a stub with no stream left to finish is still reclaimed")
			assert.NoDirExists(t, dir)
		})
	}
}

// TestGhostCleanup_KeepsAnUnfinishedLiveStream: the agent died before stop, so
// the header-only recording with a dead parent is a ghost. If its live stream
// was already open on SageOx, removing the folder took the stream state with
// it and nothing ever closed the server's copy.
func TestGhostCleanup_KeepsAnUnfinishedLiveStream(t *testing.T) {
	base := t.TempDir()
	live := filepath.Join(base, "2026-11-18T23-15-person-a-OxLIVE")
	ghostState(t, live)
	require.NoError(t, os.WriteFile(filepath.Join(live, "raw.jsonl"), []byte(`{"metadata":{},"type":"header"}`+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(live, "stream-state.json"), []byte(`{"status":"streaming"}`), 0o600))
	ghost := filepath.Join(base, "2026-11-18T23-15-person-a-OxGHST")
	ghostState(t, ghost)

	res := CleanupGhostSessionsInDir(base)

	assert.Equal(t, 1, res.Removed, "control: the ghost without a stream is still reclaimed")
	assert.NoDirExists(t, ghost)
	assert.FileExists(t, filepath.Join(live, "stream-state.json"), "an unfinished live stream lost its folder")
}
