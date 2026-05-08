package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

// TestMappingCorruptFromErr_FindsTypedErrorThroughWrapping verifies that the
// daemon's helper unwraps the `codedb initial index failed: open codedb: open
// codedb store: open comment index: ...` error chain and surfaces the typed
// MappingCorruptError so handleMappingCorrupt can act on it.
//
// Failure prevented: the daemon would have to string-parse error messages to
// learn which sub-index failed — brittle and silent if the wrapping changes.
func TestMappingCorruptFromErr_FindsTypedErrorThroughWrapping(t *testing.T) {
	t.Parallel()

	original := &store.MappingCorruptError{Name: "comment", Path: "/tmp/whatever"}
	wrapped := fmt.Errorf("codedb initial index failed: %w",
		fmt.Errorf("open codedb: %w",
			fmt.Errorf("open codedb store: %w",
				fmt.Errorf("open comment index: %w", original))))

	got := mappingCorruptFromErr(wrapped)
	require.NotNil(t, got, "MappingCorruptError must be findable through 4 levels of wrapping")
	require.Equal(t, "comment", got.Name)
	require.Equal(t, "/tmp/whatever", got.Path)

	// negative case: unrelated error
	require.Nil(t, mappingCorruptFromErr(errors.New("unrelated")))
	require.Nil(t, mappingCorruptFromErr(nil))
}

// TestHandleMappingCorrupt_RebuildsAndCapsAttempts verifies the bounded
// auto-rebuild contract: the first observation triggers a rebuild; once the
// per-sub-index counter hits maxBleveRebuildAttempts, further observations
// are no-ops so a chronically-broken disk doesn't become a rebuild loop.
//
// Failure prevented: daemon at 340% CPU rebuilding the same broken sub-index
// every freshness cycle.
func TestHandleMappingCorrupt_RebuildsAndCapsAttempts(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}

	tmp := t.TempDir()
	db, err := codedb.Open(tmp)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	// repeatedly poison the comment mapping and ask the manager to heal.
	mgr := NewCodeDBManager(t.TempDir(), codedbTestLogger(), nil)

	// 1st invocation: should rebuild successfully and increment counter to 1
	poisonCommentMapping(t, tmp)
	mce := &store.MappingCorruptError{Name: "comment", Path: filepath.Join(tmp, "bleve", "comment")}
	mgr.handleMappingCorrupt(mce, tmp)

	// validate Open succeeds after rebuild
	db2, err := codedb.Open(tmp)
	require.NoError(t, err, "Open must succeed after first rebuild")
	require.NoError(t, db2.Close())

	mgr.mu.Lock()
	require.Equal(t, 1, mgr.bleveRebuildAttempts["comment"], "first attempt counted")
	mgr.mu.Unlock()

	// 2nd invocation: still under cap, rebuilds again
	poisonCommentMapping(t, tmp)
	mgr.handleMappingCorrupt(mce, tmp)
	mgr.mu.Lock()
	require.Equal(t, 2, mgr.bleveRebuildAttempts["comment"], "second attempt counted")
	mgr.mu.Unlock()

	// 3rd invocation: at cap (maxBleveRebuildAttempts == 2), MUST NOT rebuild.
	// Poison the mapping again and verify Open still fails (proof the rebuild
	// did not run).
	poisonCommentMapping(t, tmp)
	mgr.handleMappingCorrupt(mce, tmp)

	_, err = codedb.Open(tmp)
	require.Error(t, err, "after cap, rebuild must not run; Open stays broken until ox doctor")
	var mce2 *store.MappingCorruptError
	require.True(t, errors.As(err, &mce2), "broken state still surfaces as MappingCorruptError")

	mgr.mu.Lock()
	require.Equal(t, 2, mgr.bleveRebuildAttempts["comment"], "counter must not advance past cap")
	mgr.mu.Unlock()
}

// TestHandleMappingCorrupt_CodeDiff_DoesNotLoop verifies that when a
// code/diff sub-index is reported corrupt, the daemon does NOT keep
// looping through auto-rebuild — RebuildBleveSubIndex returns
// ErrFullReindexRequired, so we log once per attempt (counter increments)
// and then stop trying after the cap. Without this, a code/diff
// MappingCorruptError combined with the previous "rebuild + return nil"
// behavior would have left search silently empty while the daemon
// reported "auto-rebuilt" every cycle.
//
// Failure prevented: silent broken-heal of code/diff sub-index.
func TestHandleMappingCorrupt_CodeDiff_DoesNotLoop(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}

	tmp := t.TempDir()
	db, err := codedb.Open(tmp)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	mgr := NewCodeDBManager(t.TempDir(), codedbTestLogger(), nil)
	mce := &store.MappingCorruptError{Name: "code", Path: filepath.Join(tmp, "bleve", "code")}

	// before: code bleve dir exists with content
	codeBleveDir := filepath.Join(tmp, "bleve", "code")
	beforeEntries, err := os.ReadDir(codeBleveDir)
	require.NoError(t, err)
	require.NotEmpty(t, beforeEntries, "code bleve must have entries before refusal")

	mgr.handleMappingCorrupt(mce, tmp)

	// after: code bleve dir untouched (refusal does NOT delete on disk)
	afterEntries, err := os.ReadDir(codeBleveDir)
	require.NoError(t, err)
	require.Equal(t, len(beforeEntries), len(afterEntries),
		"ErrFullReindexRequired path must not modify code bleve on disk")

	// counter must increment so the daemon stops after maxBleveRebuildAttempts
	mgr.mu.Lock()
	require.Equal(t, 1, mgr.bleveRebuildAttempts["code"],
		"attempt must count even when rebuild refused — otherwise infinite loop")
	mgr.mu.Unlock()
}

func poisonCommentMapping(t *testing.T, root string) {
	t.Helper()
	boltPath := filepath.Join(root, "bleve", "comment", "store", "root.bolt")
	db, err := bbolt.Open(boltPath, 0600, nil)
	require.NoError(t, err)
	require.NoError(t, db.Update(func(tx *bbolt.Tx) error {
		snaps := tx.Bucket([]byte{'s'})
		require.NotNil(t, snaps)
		var lastKey []byte
		require.NoError(t, snaps.ForEach(func(k, _ []byte) error {
			lastKey = append(lastKey[:0], k...)
			return nil
		}))
		snap := snaps.Bucket(lastKey)
		require.NotNil(t, snap)
		internal := snap.Bucket([]byte{'i'})
		require.NotNil(t, internal)
		return internal.Put([]byte("_mapping"), []byte{})
	}))
	require.NoError(t, db.Close())
}
