package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWriteMurmurRaw_RoundTrip verifies the core contract: data written via
// WriteMurmurRaw is byte-identical when read back, and FindMurmur locates it.
func TestWriteMurmurRaw_RoundTrip(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	now := time.Now().UTC()
	murmurID := "test-roundtrip-001"
	relPath := MurmurFilePath(now, murmurID)

	m := MurmurFile{
		SchemaVersion: "1",
		ID:            murmurID,
		Timestamp:     now,
		Topic:         "wip",
		Content:       "implementing auth module",
		AgentID:       "agent-abc",
	}
	data, err := json.Marshal(m)
	require.NoError(t, err)

	// write
	err = WriteMurmurRaw(baseDir, relPath, data)
	require.NoError(t, err)

	// read back directly
	got, err := os.ReadFile(filepath.Join(baseDir, relPath))
	require.NoError(t, err)
	assert.Equal(t, data, got, "file content must be byte-identical to what was written")

	// find by ID
	foundPath, err := FindMurmur(baseDir, murmurID)
	require.NoError(t, err)
	assert.Equal(t, relPath, foundPath)

	// Re-publishing a regular file remains supported.
	data = append(data, '\n')
	require.NoError(t, WriteMurmurRaw(baseDir, relPath, data))
	got, err = os.ReadFile(filepath.Join(baseDir, relPath))
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestWriteMurmur_RejectsSymlinkComponents(t *testing.T) {
	for _, raw := range []bool{false, true} {
		for _, outside := range []bool{false, true} {
			for component := 0; component < 5; component++ {
				t.Run(fmt.Sprintf("raw=%t/outside=%t/component=%d", raw, outside, component), func(t *testing.T) {
					base := t.TempDir()
					mf := MurmurFile{ID: "test", Timestamp: time.Now().UTC(), Content: "replacement"}
					rel := MurmurFilePath(mf.Timestamp, mf.ID)
					parts := strings.Split(rel, string(filepath.Separator))
					link := filepath.Join(base, filepath.Join(parts[:component+1]...))
					target := filepath.Join(base, ".git")
					if outside {
						target = t.TempDir()
					}
					victim := filepath.Join(target, filepath.Join(parts[component+1:]...))
					if component == len(parts)-1 {
						target = filepath.Join(target, "config")
						victim = target
					}
					require.NoError(t, os.MkdirAll(filepath.Dir(victim), 0o755))
					require.NoError(t, os.WriteFile(victim, []byte("unchanged"), 0o644))
					require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
					if err := os.Symlink(target, link); err != nil {
						t.Skipf("symlinks unavailable: %v", err)
					}
					var err error
					if raw {
						err = WriteMurmurRaw(base, rel, []byte("replacement"))
					} else {
						_, err = WriteMurmur(base, mf)
					}
					require.Error(t, err)
					got, err := os.ReadFile(victim)
					require.NoError(t, err)
					require.Equal(t, "unchanged", string(got))
				})
			}
		}
	}
}

func TestWriteMurmurRaw_RejectsNonMurmurPaths(t *testing.T) {
	base := t.TempDir()
	for _, rel := range []string{"", "../escape", ".git/config", "data/murmurs/../../../escape", filepath.Join(base, "absolute")} {
		require.Error(t, WriteMurmurRaw(base, rel, []byte("replacement")), "path %q", rel)
	}
}

func TestWriteMurmurRaw_RejectsDanglingSymlink(t *testing.T) {
	base := t.TempDir()
	rel := MurmurFilePath(time.Now().UTC(), "dangling")
	link := filepath.Join(base, rel)
	victim := filepath.Join(t.TempDir(), "missing")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	if err := os.Symlink(victim, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	require.Error(t, WriteMurmurRaw(base, rel, []byte("replacement")))
	_, err := os.Lstat(victim)
	require.ErrorIs(t, err, os.ErrNotExist)
}

// TestWriteMurmurRaw_DeletedDir verifies that writing to a deleted base dir
// returns a descriptive error rather than panicking.
func TestWriteMurmurRaw_DeletedDir(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	require.NoError(t, os.RemoveAll(baseDir))

	now := time.Now().UTC()
	relPath := MurmurFilePath(now, "orphan-murmur")

	err := WriteMurmurRaw(baseDir, relPath, []byte(`{"id":"orphan"}`))
	// on most systems MkdirAll can recreate the path; the key assertion is
	// no panic. If the OS prevents recreation (permissions), we want an error.
	if err != nil {
		assert.Contains(t, err.Error(), "murmur", "error should mention murmur context")
	}
}

// TestFindMurmur_NonExistentID verifies that searching for a murmur ID that
// was never written returns an error and an empty path — not a panic.
func TestFindMurmur_NonExistentID(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()

	path, err := FindMurmur(baseDir, "does-not-exist-xyz")
	assert.Error(t, err, "must return error for non-existent murmur")
	assert.Empty(t, path, "path must be empty when murmur not found")
	assert.Contains(t, err.Error(), "not found")
}

// TestFindMurmur_TimeWindow verifies that FindMurmur searches the correct
// hourly directory structure and finds murmurs written in the current hour.
func TestFindMurmur_TimeWindow(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	now := time.Now().UTC()
	murmurID := "time-window-test"
	relPath := MurmurFilePath(now, murmurID)

	err := WriteMurmurRaw(baseDir, relPath, []byte(`{"id":"time-window-test"}`))
	require.NoError(t, err)

	// verify the hourly directory structure is correct
	expectedDir := MurmurDateHourDir(now)
	assert.DirExists(t, filepath.Join(baseDir, expectedDir))

	// FindMurmur should locate it
	foundPath, err := FindMurmur(baseDir, murmurID)
	require.NoError(t, err)
	assert.Equal(t, relPath, foundPath)
}

// TestWriteMurmurRaw_ConcurrentWrites verifies that 10 goroutines writing
// different murmurs simultaneously produce no corruption or lost writes.
func TestWriteMurmurRaw_ConcurrentWrites(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	now := time.Now().UTC()
	const n = 10

	var wg sync.WaitGroup
	errs := make([]error, n)

	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id := "concurrent-" + string(rune('a'+idx))
			relPath := MurmurFilePath(now, id)
			data := []byte(`{"id":"` + id + `"}`)
			errs[idx] = WriteMurmurRaw(baseDir, relPath, data)
		}(i)
	}
	wg.Wait()

	// all writes must succeed
	for i, err := range errs {
		assert.NoError(t, err, "write %d failed", i)
	}

	// all files must be findable
	for i := range n {
		id := "concurrent-" + string(rune('a'+i))
		path, err := FindMurmur(baseDir, id)
		assert.NoError(t, err, "find %s failed", id)
		assert.NotEmpty(t, path)
	}
}
