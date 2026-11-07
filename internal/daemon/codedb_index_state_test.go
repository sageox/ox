package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/version"
)

// --- index-state.json: load / save ---

// TestLoadIndexState: every unreadable shape must read as the zero state ("know
// nothing, re-run"), never as an error that stops indexing, and only the
// shapes that signal trouble may warn.
func TestLoadIndexState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		setup    func(t *testing.T, dataDir string)
		want     codeDBIndexState
		wantWarn bool
	}{
		{
			name:  "missing file is the normal first run",
			setup: func(t *testing.T, dataDir string) {},
		},
		{
			name: "valid state",
			setup: func(t *testing.T, dataDir string) {
				writeStateFile(t, dataDir, `{"ox_version":"v1","head":"refs/heads/main:abc","board":"b1","history_pending":true}`)
			},
			want: codeDBIndexState{OxVersion: "v1", Head: "refs/heads/main:abc", Board: "b1", HistoryPending: true},
		},
		{
			name: "contract shape without the version field",
			setup: func(t *testing.T, dataDir string) {
				writeStateFile(t, dataDir, `{"head":"refs/heads/main:abc","board":"","history_pending":false}`)
			},
			want: codeDBIndexState{Head: "refs/heads/main:abc"},
		},
		{
			name: "unknown fields from a newer build are ignored",
			setup: func(t *testing.T, dataDir string) {
				writeStateFile(t, dataDir, `{"head":"h","future_field":{"x":1}}`)
			},
			want: codeDBIndexState{Head: "h"},
		},
		{
			name:     "empty file",
			setup:    func(t *testing.T, dataDir string) { writeStateFile(t, dataDir, "") },
			wantWarn: true,
		},
		{
			name:     "truncated json (a torn write by an older non-atomic writer)",
			setup:    func(t *testing.T, dataDir string) { writeStateFile(t, dataDir, `{"head":"refs/heads/ma`) },
			wantWarn: true,
		},
		{
			name:     "garbage",
			setup:    func(t *testing.T, dataDir string) { writeStateFile(t, dataDir, "\x00\x01not json") },
			wantWarn: true,
		},
		{
			name: "wrong field type",
			setup: func(t *testing.T, dataDir string) {
				writeStateFile(t, dataDir, `{"head": 5, "history_pending": "yes"}`)
			},
			wantWarn: true,
		},
		{
			name: "directory squatting on the path",
			setup: func(t *testing.T, dataDir string) {
				require.NoError(t, os.MkdirAll(indexStatePath(dataDir), 0o755))
			},
			wantWarn: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dataDir := t.TempDir()
			tt.setup(t, dataDir)
			logger, logs := captureLogger()

			got := loadIndexState(logger, dataDir)

			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantWarn, strings.Contains(logs.String(), "level=WARN"), "logs: %s", logs.String())
		})
	}
}

func writeStateFile(t *testing.T, dataDir, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(indexStatePath(dataDir), []byte(content), 0o600))
}

func TestSaveIndexState(t *testing.T) {
	t.Parallel()

	t.Run("round trip", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		want := codeDBIndexState{OxVersion: "v9", Head: "refs/heads/main:abc", Board: "board-fp", HistoryPending: true}
		require.NoError(t, saveIndexState(dataDir, want))
		assert.Equal(t, want, loadIndexState(codedbTestLogger(), dataDir))
	})

	t.Run("replaces an earlier state completely", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		require.NoError(t, saveIndexState(dataDir, codeDBIndexState{Head: "old", Board: "old", HistoryPending: true}))
		require.NoError(t, saveIndexState(dataDir, codeDBIndexState{Head: "new"}))
		assert.Equal(t, codeDBIndexState{Head: "new"}, loadIndexState(codedbTestLogger(), dataDir))
	})

	t.Run("leaves no temp files behind", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		for i := 0; i < 5; i++ {
			require.NoError(t, saveIndexState(dataDir, codeDBIndexState{Head: "h"}))
		}
		entries, err := os.ReadDir(dataDir)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, indexStateFileName, entries[0].Name())
	})

	t.Run("file permissions are private", func(t *testing.T) {
		t.Parallel()
		if filepath.Separator == '\\' {
			t.Skip("windows: unix mode bits are not reported")
		}
		dataDir := t.TempDir()
		require.NoError(t, saveIndexState(dataDir, codeDBIndexState{Head: "h"}))
		info, err := os.Stat(indexStatePath(dataDir))
		require.NoError(t, err)
		assert.Zero(t, info.Mode().Perm()&0o077, "state must not be group/world accessible")
	})

	t.Run("missing data dir fails without leaving anything", func(t *testing.T) {
		t.Parallel()
		missing := filepath.Join(t.TempDir(), "no-such-dir")
		require.Error(t, saveIndexState(missing, codeDBIndexState{Head: "h"}))
		assert.NoDirExists(t, missing)
	})

	t.Run("failed replace keeps the old state and cleans its temp file", func(t *testing.T) {
		t.Parallel()
		// a non-empty directory at the target makes the final rename fail on every platform
		dataDir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(indexStatePath(dataDir), "keep"), 0o755))
		require.Error(t, saveIndexState(dataDir, codeDBIndexState{Head: "h"}))

		entries, err := os.ReadDir(dataDir)
		require.NoError(t, err)
		require.Len(t, entries, 1, "only the squatting directory remains, no temp file")
		assert.DirExists(t, indexStatePath(dataDir))
	})
}

// TestSaveIndexState_ReadersNeverSeeAPartialWrite is the atomicity claim: while
// writers replace the file as fast as they can, a reader always finds one
// complete state, never a torn one.
// Failure prevented: a daemon starting while another one writes reads half a
// file, and (before corrupt files read as empty) trusted or lost its contents.
func TestSaveIndexState_ReadersNeverSeeAPartialWrite(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	long := strings.Repeat("x", 4096) // large enough that a non-atomic write would span several syscalls
	states := []codeDBIndexState{
		{Head: "refs/heads/a:" + long, Board: "a", HistoryPending: true},
		{Head: "refs/heads/b:" + long, Board: "b"},
	}
	require.NoError(t, saveIndexState(dataDir, states[0]))

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				if err := saveIndexState(dataDir, states[(w+i)%2]); err != nil {
					t.Errorf("save: %v", err)
					return
				}
			}
		}()
	}

	var torn atomic.Int64
	deadline := time.Now().Add(300 * time.Millisecond)
	for reads := 0; time.Now().Before(deadline); reads++ {
		data, err := os.ReadFile(indexStatePath(dataDir))
		if err != nil {
			torn.Add(1)
			continue
		}
		var got codeDBIndexState
		if json.Unmarshal(data, &got) != nil || (got != states[0] && got != states[1]) {
			torn.Add(1)
		}
	}
	close(stop)
	wg.Wait()
	assert.Zero(t, torn.Load(), "readers saw an unreadable or mixed state")
}

// TestUpdateIndexState_WriteFailureIsLoggedNotFatal: the state is an optimization.
func TestUpdateIndexState_WriteFailureIsLoggedNotFatal(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(indexStatePath(dataDir), "keep"), 0o755))
	logger, logs := captureLogger()
	mgr := NewCodeDBManager(t.TempDir(), logger, nil)

	mgr.markHistoryPending(dataDir, true) // must return, not panic or block
	mgr.recordIndexed(dataDir, "refs/heads/main:abc", false)

	assert.Contains(t, logs.String(), "codedb index state not saved")
	mgr.mu.Lock()
	head := mgr.lastIndexedHead
	mgr.mu.Unlock()
	assert.Equal(t, "refs/heads/main:abc", head, "in-memory freshness still advances when the disk write fails")
}

func TestMarkHistoryPending_KeepsRecordedFreshness(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	mgr := NewCodeDBManager(t.TempDir(), codedbTestLogger(), nil)
	require.NoError(t, saveIndexState(dataDir, codeDBIndexState{OxVersion: "v1", Head: "h", Board: "b"}))

	mgr.markHistoryPending(dataDir, true)

	assert.Equal(t, codeDBIndexState{OxVersion: "v1", Head: "h", Board: "b", HistoryPending: true}, loadIndexState(mgr.logger, dataDir),
		"head and board describe the last completed pipeline, not the one that just started")
}

// --- Lazy seeding of lastIndexedHead / lastIndexedBoard ---

func TestSeedFreshnessFromDisk(t *testing.T) {
	t.Parallel()

	current := version.Full()
	tests := []struct {
		name        string
		state       *codeDBIndexState // nil = no file
		rawState    string
		dirMissing  bool
		marker      bool
		inMemory    [2]string // head, board already in memory
		wantSeeded  bool
		wantHeadMem string
		wantBoard   string
	}{
		{
			name:        "restores head and board",
			state:       &codeDBIndexState{OxVersion: current, Head: "h", Board: "b"},
			wantSeeded:  true,
			wantHeadMem: "h",
			wantBoard:   "b",
		},
		{
			name:        "in-memory values win over disk",
			state:       &codeDBIndexState{OxVersion: current, Head: "disk", Board: "disk"},
			inMemory:    [2]string{"mem-head", "mem-board"},
			wantHeadMem: "mem-head",
			wantBoard:   "mem-board",
		},
		{
			name:        "only the empty field is filled",
			state:       &codeDBIndexState{OxVersion: current, Head: "disk", Board: "disk-board"},
			inMemory:    [2]string{"mem-head", ""},
			wantHeadMem: "mem-head",
			wantBoard:   "disk-board",
		},
		{name: "no state file", state: nil},
		{name: "index directory gone", state: &codeDBIndexState{OxVersion: current, Head: "h"}, dirMissing: true},
		{name: "self-heal marker present", state: &codeDBIndexState{OxVersion: current, Head: "h"}, marker: true},
		{name: "recorded by a different ox build", state: &codeDBIndexState{OxVersion: current + "-old", Head: "h", Board: "b"}},
		{name: "recorded before builds were stamped", state: &codeDBIndexState{Head: "h", Board: "b"}},
		{name: "corrupt file", rawState: "{not json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dataDir := t.TempDir()
			mgr := NewCodeDBManager(t.TempDir(), codedbTestLogger(), nil)
			mgr.dataDir = dataDir
			mgr.lastIndexedHead, mgr.lastIndexedBoard = tt.inMemory[0], tt.inMemory[1]
			switch {
			case tt.state != nil:
				require.NoError(t, saveIndexState(dataDir, *tt.state))
			case tt.rawState != "":
				writeStateFile(t, dataDir, tt.rawState)
			}
			if tt.marker {
				require.NoError(t, writeMarkerForTest(dataDir, "code"))
			}
			if tt.dirMissing {
				mgr.dataDir = filepath.Join(dataDir, "never-created")
			}

			seeded := mgr.seedFreshnessFromDisk()

			assert.Equal(t, tt.wantSeeded, seeded)
			assert.Equal(t, tt.wantHeadMem, mgr.lastIndexedHead)
			assert.Equal(t, tt.wantBoard, mgr.lastIndexedBoard)
		})
	}
}

// TestSeedFreshnessFromDisk_OnlyOnce: after the first look memory is the
// authority. Re-seeding would undo a fingerprint the board stage or a discard
// cleared on purpose, and skip the retry that clearing was for.
func TestSeedFreshnessFromDisk_OnlyOnce(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	mgr := NewCodeDBManager(t.TempDir(), codedbTestLogger(), nil)
	mgr.dataDir = dataDir
	require.NoError(t, saveIndexState(dataDir, codeDBIndexState{OxVersion: version.Full(), Head: "h", Board: "b"}))

	require.True(t, mgr.seedFreshnessFromDisk())
	mgr.lastIndexedHead, mgr.lastIndexedBoard = "", "" // a discard / failed board stage clears them

	assert.False(t, mgr.seedFreshnessFromDisk())
	assert.Empty(t, mgr.lastIndexedHead)
	assert.Empty(t, mgr.lastIndexedBoard)
}

// --- Restart: a new daemon on an indexed repo skips the pipeline ---

// TestCheckFreshness_RestartSkipsFinishedPipeline is the headline: before the
// state file, every daemon restart re-ran the whole pipeline (hours on a
// 100k-commit repo) because freshness lived only in memory. Each row is a
// reason a restart must NOT trust the disk.
func TestCheckFreshness_RestartSkipsFinishedPipeline(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: real git + SQLite + Bleve indexing")
	}

	tests := []struct {
		name string
		// whileDown changes the world between the two daemons
		whileDown func(t *testing.T, repoDir, dataDir string)
		wantRuns  int64
	}{
		{
			name:      "nothing moved: skipped",
			whileDown: func(t *testing.T, repoDir, dataDir string) {},
			wantRuns:  0,
		},
		{
			name: "a commit landed while down",
			whileDown: func(t *testing.T, repoDir, dataDir string) {
				backfillCommitFile(t, repoDir, "while_down.go", "package main\n")
			},
			wantRuns: 1,
		},
		{
			name: "history still owed",
			whileDown: func(t *testing.T, repoDir, dataDir string) {
				state := loadIndexState(codedbTestLogger(), dataDir)
				state.HistoryPending = true
				require.NoError(t, saveIndexState(dataDir, state))
			},
			wantRuns: 1,
		},
		{
			name: "recorded by a different ox build",
			whileDown: func(t *testing.T, repoDir, dataDir string) {
				state := loadIndexState(codedbTestLogger(), dataDir)
				state.OxVersion = "0.0.0-previous-build"
				require.NoError(t, saveIndexState(dataDir, state))
			},
			wantRuns: 1,
		},
		{
			name: "self-heal marker written by another process",
			whileDown: func(t *testing.T, repoDir, dataDir string) {
				require.NoError(t, writeMarkerForTest(dataDir, "code"))
			},
			wantRuns: 1,
		},
		{
			name: "index directory wiped",
			whileDown: func(t *testing.T, repoDir, dataDir string) {
				require.NoError(t, os.RemoveAll(dataDir))
			},
			wantRuns: 1,
		},
		{
			name: "state file corrupt",
			whileDown: func(t *testing.T, repoDir, dataDir string) {
				writeStateFile(t, dataDir, "{torn")
			},
			wantRuns: 1,
		},
		{
			name: "state file missing (index from before this feature)",
			whileDown: func(t *testing.T, repoDir, dataDir string) {
				require.NoError(t, os.Remove(indexStatePath(dataDir)))
			},
			wantRuns: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repoDir := seedLinearHistory(t, 3)
			dataDir := t.TempDir()

			// first daemon: a real first-time index through the freshness path
			first := NewCodeDBManager(repoDir, codedbTestLogger(), nil)
			first.dataDir = dataDir
			var firstRuns atomic.Int64
			first.testHook = func() { firstRuns.Add(1) }
			first.CheckFreshness(indexCtx(t))
			waitForIndexingDone(t, first)
			require.Equal(t, int64(1), firstRuns.Load())
			require.Equal(t, readHeadFingerprint(context.Background(), repoDir), loadIndexState(first.logger, dataDir).Head,
				"the first daemon must have persisted what it indexed")

			tt.whileDown(t, repoDir, dataDir)

			// second daemon: same repo and data dir, empty memory
			second := NewCodeDBManager(repoDir, codedbTestLogger(), nil)
			second.dataDir = dataDir
			var secondRuns, overlayRefreshes atomic.Int64
			second.testHook = func() { secondRuns.Add(1) }
			second.dirtyTestHook = func() { overlayRefreshes.Add(1) }
			second.CheckFreshness(indexCtx(t))
			waitForIndexingDone(t, second)

			assert.Equal(t, tt.wantRuns, secondRuns.Load())

			// A skipped pipeline would have rebuilt this worktree's dirty overlay;
			// the restart must still do that, cheaply, on its own.
			if tt.wantRuns == 0 {
				require.Eventually(t, func() bool { return overlayRefreshes.Load() == 1 },
					codedbBuildWait, 10*time.Millisecond, "a skipped restart must still refresh the dirty overlay")
			}
			require.Eventually(t, func() bool {
				second.mu.Lock()
				defer second.mu.Unlock()
				return !second.dirtyRefreshing
			}, codedbBuildWait, 10*time.Millisecond)
			if tt.wantRuns == 1 {
				assert.Zero(t, overlayRefreshes.Load(), "a run of the pipeline already rebuilt the overlay")
			}
		})
	}
}

// TestCheckFreshness_RestartRunsWhenBoardMovedWhileDown: the board fingerprint is
// persisted too, so a teammate's PR that arrived while the daemon was down is
// still indexed after the restart.
func TestCheckFreshness_RestartRunsWhenBoardMovedWhileDown(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: real git + SQLite + Bleve indexing")
	}

	project := newBoardProject(t, "https://github.com/acme/api.git")
	project.writePost(t, "acme/api", 1, "first")
	dataDir := t.TempDir()

	first := NewCodeDBManager(project.root, codedbTestLogger(), nil)
	first.dataDir = dataDir
	first.CheckFreshness(indexCtx(t))
	waitForIndexingDone(t, first)
	require.Equal(t, project.fingerprint("acme/api"), loadIndexState(first.logger, dataDir).Board)

	restart := func(t *testing.T) int64 {
		t.Helper()
		m := NewCodeDBManager(project.root, codedbTestLogger(), nil)
		m.dataDir = dataDir
		var runs atomic.Int64
		m.testHook = func() { runs.Add(1) }
		m.CheckFreshness(indexCtx(t))
		waitForIndexingDone(t, m)
		require.Eventually(t, func() bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			return !m.dirtyRefreshing
		}, codedbBuildWait, 10*time.Millisecond)
		return runs.Load()
	}

	assert.Zero(t, restart(t), "board unchanged: skipped")
	project.writePost(t, "acme/api", 2, "second")
	assert.Equal(t, int64(1), restart(t), "board moved while down: indexed")
}

// TestIndexState_DiscardCorruptIndexRemovesStateFirst: a discard that fails partway must
// not leave a state file vouching for a damaged index. "aaa-stuck" sorts before
// the state file, so a discard that deleted entries in directory order would stop
// there with the state file still in place.
func TestIndexState_DiscardCorruptIndexRemovesStateFirst(t *testing.T) {
	// the stuck entry needs chmod to take effect, which it does not on Windows or as root
	if runtime.GOOS == "windows" {
		t.Skip("windows: chmod cannot make a directory undeletable")
	}
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	t.Parallel()

	dataDir := t.TempDir()
	mgr := NewCodeDBManager(t.TempDir(), codedbTestLogger(), nil)
	mgr.dataDir = dataDir
	require.NoError(t, saveIndexState(dataDir, codeDBIndexState{OxVersion: version.Full(), Head: "h", HistoryPending: true}))

	stuck := filepath.Join(dataDir, "aaa-stuck")
	require.NoError(t, os.MkdirAll(filepath.Join(stuck, "child"), 0o755))
	require.NoError(t, os.Chmod(stuck, 0o500))
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o755) })

	mgr.discardCorruptIndex(dataDir, context.DeadlineExceeded)

	require.DirExists(t, stuck, "precondition: the discard really did stop at the stuck entry")
	assert.NoFileExists(t, indexStatePath(dataDir))
}
