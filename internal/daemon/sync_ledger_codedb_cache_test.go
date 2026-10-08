package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CodeDB creates its shared index at <ledger>/.sageox/cache/codedb as soon as
// it opens, which can be before the Ledger's first clone lands. The atomic
// swap then failed "file exists" on every retry while `ox sync` kept going.
// These tests drive the real Checkout against a real git server.
//
// Failure prevented: a brand-new Ledger never clones because a cache-only
// directory squats on its path.
func TestSyncScheduler_Checkout_LedgerCloneLandsOverCodeDBCache(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	cacheOnly := func(t *testing.T, repoPath string) {
		require.NoError(t, os.MkdirAll(filepath.Join(repoPath, ".sageox", "cache", "codedb"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(repoPath, ".sageox", "cache", "codedb", "index.db"), []byte("idx"), 0o644))
	}

	tests := []struct {
		name      string
		prepare   func(t *testing.T, repoPath string)
		wantErr   bool
		wantCache bool // CodeDB index survives into the clone
	}{
		{
			name:      "cache appears while the clone runs (CodeDB opens mid-clone)",
			prepare:   cacheOnly,
			wantCache: true,
		},
		{
			name: "cache dir with no codedb content yet",
			prepare: func(t *testing.T, repoPath string) {
				require.NoError(t, os.MkdirAll(filepath.Join(repoPath, ".sageox", "cache"), 0o755))
			},
		},
		{
			name: "target holds unrelated user files: swap still fails loudly",
			prepare: func(t *testing.T, repoPath string) {
				cacheOnly(t, repoPath)
				require.NoError(t, os.WriteFile(filepath.Join(repoPath, "notes.txt"), []byte("mine"), 0o644))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloneURL := setupBareLedgerHTTP(t)
			s := checkoutTestScheduler(t)
			repoPath := filepath.Join(t.TempDir(), "ledger")

			// fires after Checkout's early move-aside logic, i.e. while the
			// clone is in flight — the window CodeDB hit in the field.
			s.onBeforeCloneSem = func() { tt.prepare(t, repoPath) }

			result, err := s.Checkout(CheckoutPayload{CloneURL: cloneURL, RepoPath: repoPath, RepoType: "ledger"}, nil)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.True(t, result.Cloned)
			_, statErr := os.Stat(filepath.Join(repoPath, ".git"))
			require.NoError(t, statErr, "the Ledger clone must land")
			_, statErr = os.Stat(filepath.Join(repoPath, "seed.txt"))
			assert.NoError(t, statErr)
			_, statErr = os.Stat(filepath.Join(repoPath, ".sageox", "cache", "codedb", "index.db"))
			assert.Equal(t, tt.wantCache, statErr == nil, "CodeDB index carry-over")
			assert.Empty(t, tmpCloneSiblings(t, repoPath))
		})
	}
}
