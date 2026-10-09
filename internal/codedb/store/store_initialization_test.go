package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/require"
)

func TestAbsentCreation_RespectsHealLock(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "code")
	lock := flock.New(subIndexHealLockPath(path))
	held, err := lock.TryLock()
	require.NoError(t, err)
	require.True(t, held)
	defer func() { _ = lock.Unlock() }()
	priorTimeout, priorPoll := subIndexHealLockTimeout, subIndexHealLockPoll
	subIndexHealLockTimeout, subIndexHealLockPoll = 100*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { subIndexHealLockTimeout, subIndexHealLockPoll = priorTimeout, priorPoll })

	idx, err := openOrCreateBleveIndex(root, path, "code")
	if idx != nil {
		defer func() { _ = idx.Close() }()
	}
	require.Error(t, err, "an absent path during another healer's locked recreation must defer; it must not create concurrently")
	require.Nil(t, idx)
	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr), "contending opener must leave the paused healer's absent directory untouched")
}

func TestMissingMetadata_HealthyBoltRecovers(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "code")
	idx, err := openOrCreateBleveIndex(root, path, "code")
	require.NoError(t, err)
	require.NoError(t, idx.Close())
	require.NoError(t, os.Remove(filepath.Join(path, "index_meta.json")))
	require.False(t, isBleveIndexCorrupt(filepath.Join(path, "store", "root.bolt")))

	recovered, openErr := openOrCreateBleveIndex(root, path, "code")
	require.NoError(t, openErr, "missing metadata must recover on this open rather than defer forever")
	require.NotNil(t, recovered)
	require.NoError(t, recovered.Close())
	require.True(t, HasNeedsReindexMarker(root, "code"))
}

func TestMissingMetadata_LiveWriterIsPreserved(t *testing.T) {
	installFastCorruptionProbes(t)
	root := t.TempDir()
	path := filepath.Join(root, "code")
	idx, err := openOrCreateBleveIndex(root, path, "code")
	require.NoError(t, err)
	defer func() { _ = idx.Close() }()
	require.NoError(t, idx.Index("preserved", map[string]string{"text": "original"}))
	require.NoError(t, os.Remove(filepath.Join(path, "index_meta.json")))
	second, err := openOrCreateBleveIndex(root, path, "code")
	require.Error(t, err)
	require.Nil(t, second)
	require.False(t, HasNeedsReindexMarker(root, "code"))
	count, err := idx.DocCount()
	require.NoError(t, err)
	require.Equal(t, uint64(1), count, "a held writer must retain its indexed data")
}

func TestUnreadableMetadata_IsPreserved(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "code")
	idx, err := openOrCreateBleveIndex(root, path, "code")
	require.NoError(t, err)
	require.NoError(t, idx.Index("preserved", map[string]string{"text": "original"}))
	require.NoError(t, idx.Close())
	meta := filepath.Join(path, "index_meta.json")
	original, err := os.ReadFile(meta)
	require.NoError(t, err)
	require.NoError(t, os.Remove(meta))
	// A directory causes an actual metadata read error even under root.
	require.NoError(t, os.Mkdir(meta, 0o700))
	_, err = os.ReadFile(meta)
	require.Error(t, err, "fault must prevent reading metadata")
	second, err := openOrCreateBleveIndex(root, path, "code")
	require.Error(t, err)
	require.Nil(t, second)
	require.False(t, HasNeedsReindexMarker(root, "code"))
	require.NoError(t, os.Remove(meta))
	require.NoError(t, os.WriteFile(meta, original, 0o600))
	recovered, err := openOrCreateBleveIndex(root, path, "code")
	require.NoError(t, err)
	defer func() { _ = recovered.Close() }()
	count, err := recovered.DocCount()
	require.NoError(t, err)
	require.Equal(t, uint64(1), count)
}

func TestAbsentCreation_AdoptsIndexCreatedWhileWaiting(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "code")
	originalHook := subIndexHealLockAcquiredHook
	t.Cleanup(func() { subIndexHealLockAcquiredHook = originalHook })
	created := false
	subIndexHealLockAcquiredHook = func(string) {
		created = true
		idx, err := createBleveSubIndex(path, "code")
		require.NoError(t, err)
		require.NoError(t, idx.Index("preserved", map[string]string{"text": "original"}))
		require.NoError(t, idx.Close())
	}
	idx, err := openOrCreateBleveIndex(root, path, "code")
	require.NoError(t, err)
	defer func() { _ = idx.Close() }()
	require.True(t, created, "the first opener must take the shared lock")
	count, err := idx.DocCount()
	require.NoError(t, err)
	require.Equal(t, uint64(1), count)
	require.False(t, HasNeedsReindexMarker(root, "code"))
}
