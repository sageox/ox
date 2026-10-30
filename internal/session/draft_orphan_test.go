package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A finished session can keep draft=true beside its committed transcript and
// summary (#1648). The reaper treated such folders as abandoned placeholders and
// deleted them from the shared Ledger; the bare placeholder is the control.
func TestFindOrphanedDrafts_IgnoresDraftFlagBesideSessionContent(t *testing.T) {
	ledger := t.TempDir()
	stale := time.Now().Add(-2 * OrphanedDraftAge)
	for _, name := range []string{"placeholder", "finished"} {
		dir := filepath.Join(ledger, "sessions", name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
			Version: "1.0", SessionName: name, Draft: true,
			CreatedAt: stale, UpdatedAt: &stale, Files: map[string]lfs.FileRef{},
		}))
	}
	require.NoError(t, os.WriteFile(filepath.Join(ledger, "sessions", "finished", "summary.json"), []byte(`{"title":"done"}`), 0o644))

	orphans, err := FindOrphanedDrafts(ledger, nil)

	require.NoError(t, err)
	assert.Equal(t, []string{"placeholder"}, orphans)
}
