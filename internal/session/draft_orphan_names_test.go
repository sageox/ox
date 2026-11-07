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

// Ledger directory names arrive from teammates by git. Every name ox
// generates must pass; separators, control characters, and the wildcard and
// pathspec-magic characters generation strips must not.
func TestValidateDraftSessionName_AcceptsGeneratedNamesOnly(t *testing.T) {
	for _, user := range []string{"ryan", "José", "Dana Ñ [dev]", "r.y_an", "a..b"} {
		name := GenerateSessionName("Ox7f3a", user)
		assert.NoError(t, ValidateDraftSessionName(name), name)
	}
	for _, name := range []string{"", ".", "..", "a/b", `a\b`, "*", "victi?", "[v]ictim", ":(glob)x", "a\x00b", "a\nb", "a|b"} {
		assert.Error(t, ValidateDraftSessionName(name), "%q", name)
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
