package gitutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lfsPointer builds a spec-shaped LFS pointer for a fake OID.
func lfsPointer(oid string, size int) string {
	return lfsPointerPrefix + "\noid sha256:" + oid + "\nsize " + itoa(size) + "\n"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestPointerWins_HydratedNeverBeatsPointer is the regression test for the
// wedge that blocked a real ledger for 13 days with 281 conflicts.
//
// Failure prevented: every mixed session-artifact conflict on that ledger was
// local=hydrated / remote=pointer. The positional rule (`checkout --theirs`)
// resolves to the LOCAL side during a rebase, so enabling auto-resolve for
// sessions/ without this guard would commit hydrated bytes over the remote LFS
// pointer. Per .claude/rules/cache-only-design.md that breaks the LFS linkage
// and every subsequent push is rejected with "LFS objects are missing" — a
// permanent, repo-wide wedge strictly worse than the one being fixed.
func TestPointerWins_HydratedNeverBeatsPointer(t *testing.T) {
	t.Parallel()
	const (
		sessionFile = "sessions/2026-07-01T00-00-ryan-Oxtest/session.md"
		pointerOID  = "3bdb5928692a4ce4bb0ec3ad2a2f0e1d9c8b7a65432109876543210fedcba9876"
	)
	pointer := lfsPointer(pointerOID, 4096)
	hydrated := "# Agent Session\n\nReal hydrated markdown that must NOT be committed.\n"

	t.Run("pointer on the remote side wins over local hydrated bytes", func(t *testing.T) {
		// setupDivergentRepos puts `oursContent` on the LOCAL replayed commit
		// and `theirsContent` on the branch being rebased onto.
		_, local := setupDivergentRepos(t, sessionFile, hydrated, pointer)

		err := ResolveRebaseAcceptTheirs(context.Background(), local, []string{"sessions/"})
		require.NoError(t, err, "sessions/ conflicts must auto-resolve, not wedge")

		got, readErr := os.ReadFile(filepath.Join(local, sessionFile))
		require.NoError(t, readErr)
		assert.Equal(t, pointer, string(got),
			"the LFS pointer must survive; committing hydrated bytes breaks every future push")
	})

	t.Run("pointer on the local side also wins", func(t *testing.T) {
		// Mirror image. Pointer-wins must be commutative — that is the property
		// that makes two replicas resolving independently converge instead of
		// ping-ponging forever.
		_, local := setupDivergentRepos(t, sessionFile, pointer, hydrated)

		err := ResolveRebaseAcceptTheirs(context.Background(), local, []string{"sessions/"})
		require.NoError(t, err)

		got, readErr := os.ReadFile(filepath.Join(local, sessionFile))
		require.NoError(t, readErr)
		assert.Equal(t, pointer, string(got),
			"pointer must win from either side, or replicas cannot converge")
	})

	t.Run("two non-pointer sides fall back to the positional rule", func(t *testing.T) {
		// meta.json is never a pointer. Both sides are plain content, so the
		// guard has no opinion and the existing last-writer behavior applies.
		// This is the accepted tradeoff: summaries are best-effort.
		const metaFile = "sessions/2026-07-01T00-00-ryan-Oxtest/meta.json"
		localMeta := `{"summary_status":"unrecoverable"}`
		cloudMeta := `{"summary_status":"ok","title":"Real summary"}`

		_, local := setupDivergentRepos(t, metaFile, localMeta, cloudMeta)

		err := ResolveRebaseAcceptTheirs(context.Background(), local, []string{"sessions/"})
		require.NoError(t, err, "must resolve rather than wedge")

		got, readErr := os.ReadFile(filepath.Join(local, metaFile))
		require.NoError(t, readErr)
		assert.Equal(t, localMeta, string(got),
			"positional rule keeps the replayed (local) side when neither is a pointer")
	})
}

// TestPointerWinsStage_Classification pins the decision function itself, so a
// future refactor cannot silently invert it.
func TestPointerWinsStage_Classification(t *testing.T) {
	t.Parallel()
	const f = "sessions/s1/session.md"
	pointer := lfsPointer("abc123def4567890abc123def4567890abc123def4567890abc123def4567890", 100)
	content := "# not a pointer\n"

	t.Run("stage 2 pointer, stage 3 content", func(t *testing.T) {
		_, local := setupDivergentRepos(t, f, content, pointer)
		assert.Equal(t, 2, pointerWinsStage(context.Background(), local, f))
	})

	t.Run("stage 3 pointer, stage 2 content", func(t *testing.T) {
		_, local := setupDivergentRepos(t, f, pointer, content)
		assert.Equal(t, 3, pointerWinsStage(context.Background(), local, f))
	})

	t.Run("both content: no opinion", func(t *testing.T) {
		_, local := setupDivergentRepos(t, f, "local text\n", "cloud text\n")
		assert.Equal(t, 0, pointerWinsStage(context.Background(), local, f))
	})

	t.Run("both pointers with different OIDs: no opinion", func(t *testing.T) {
		p1 := lfsPointer("1111111111111111111111111111111111111111111111111111111111111111", 10)
		p2 := lfsPointer("2222222222222222222222222222222222222222222222222222222222222222", 20)
		_, local := setupDivergentRepos(t, f, p1, p2)
		assert.Equal(t, 0, pointerWinsStage(context.Background(), local, f),
			"neither side risks the LFS linkage, so the guard must not interfere")
	})
}

func TestIsLFSPointerBlob(t *testing.T) {
	t.Parallel()
	valid := lfsPointer("deadbeef00000000000000000000000000000000000000000000000000000000", 42)

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"canonical pointer", valid, true},
		{"markdown", "# Agent Session\n\nsome notes\n", false},
		{"empty", "", false},
		{"json meta", `{"session_name":"x","entry_count":2}`, false},
		// A file that merely mentions the spec URL deep in its body is content,
		// not a pointer — the marker must be a prefix.
		{"mentions spec url but is content", "see version https://git-lfs.github.com/spec/v1 for details", false},
		// Oversized input can't be a pointer even with the right prefix; this
		// stops a hydrated file that happens to begin with the marker from being
		// misclassified as safe to keep.
		{"prefix but oversized", valid + string(make([]byte, maxLFSPointerSize)), false},
		// A malformed pointer must NOT win a conflict. Downstream LFS parsing
		// rejects it, so treating it as a pointer would commit an unusable blob
		// over valid content — a data-loss dressed up as a safety guard.
		{"truncated: version line only", lfsPointerPrefix + "\n", false},
		{"missing size", lfsPointerPrefix + "\noid sha256:abc123\n", false},
		{"missing oid", lfsPointerPrefix + "\nsize 100\n", false},
		{"empty oid digest", lfsPointerPrefix + "\noid sha256:\nsize 100\n", false},
		{"oid without algorithm prefix", lfsPointerPrefix + "\noid abc123\nsize 100\n", false},
		{"non-numeric size", lfsPointerPrefix + "\noid sha256:abc123\nsize huge\n", false},
		{"zero size", lfsPointerPrefix + "\noid sha256:abc123\nsize 0\n", false},
		{"negative size", lfsPointerPrefix + "\noid sha256:abc123\nsize -5\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isLFSPointerBlob(tt.content))
		})
	}
}
