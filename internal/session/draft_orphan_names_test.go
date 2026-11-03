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

// Ledger directory names arrive from teammates by git; only generated-name
// characters may reach a draft git pathspec or RemoveAll.
func TestValidateDraftSessionName_AllowsOnlyGeneratedNameCharacters(t *testing.T) {
	require.NoError(t, ValidateDraftSessionName(GenerateSessionName("Ox7f3a", "ryan")))
	require.NoError(t, ValidateDraftSessionName("2026-01-06T14-32-r.y_an-Ox7f3a"))
	for _, name := range []string{"*", "victi?", "[v]ictim", ":(glob)x", "caf\u00e9", "a b"} {
		assert.Error(t, ValidateDraftSessionName(name), name)
	}
}

func TestFindOrphanedDrafts_SkipsNonconformingNames(t *testing.T) {
	ledger := t.TempDir()
	aged := time.Now().Add(-2 * OrphanedDraftAge).UTC()
	for _, name := range []string{"*", "[v]ictim", "ordinary"} {
		dir := filepath.Join(ledger, "sessions", name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
			Version: "1.0", SessionName: name, SessionID: "ses_orphan_test", AgentID: "OxOrphan",
			CreatedAt: aged, Draft: true, UpdatedAt: &aged, Files: map[string]lfs.FileRef{},
		}))
	}

	orphans, err := FindOrphanedDrafts(ledger, nil)

	require.NoError(t, err)
	assert.Equal(t, []string{"ordinary"}, orphans)
}
