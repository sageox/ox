package session

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: a hold that is lost, overwritten, or placed where it
// leaks lets an automatic path publish or delete a session the coworker chose
// to keep on this machine (GH #1093).
func TestHoldMarker_WriteReadClear(t *testing.T) {
	dir := t.TempDir()
	assert.False(t, IsHeld(dir), "a fresh folder is not held")

	require.NoError(t, WriteHoldMarker(dir, HoldManualPublishing, "session_stop"))
	assert.True(t, IsHeld(dir))
	first, err := ReadHoldMarker(dir)
	require.NoError(t, err)
	assert.Equal(t, HoldManualPublishing, first.Reason)
	assert.Equal(t, "session_stop", first.Source)

	// A second writer keeps the original hold.
	require.NoError(t, WriteHoldMarker(dir, HoldManualPublishing, "hook"))
	again, err := ReadHoldMarker(dir)
	require.NoError(t, err)
	assert.Equal(t, first, again)

	require.NoError(t, ClearHoldMarker(dir))
	assert.False(t, IsHeld(dir))
	require.NoError(t, ClearHoldMarker(dir), "releasing twice is not an error")
}

func TestIsHeld_FailsClosed(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) string
		want  bool
	}{
		{"missing folder", func(t *testing.T) string { return filepath.Join(t.TempDir(), "gone") }, false},
		{"unparseable marker still holds", func(t *testing.T) string {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, HeldMarkerFile), []byte("{not json"), 0o644))
			return dir
		}, true},
		{"marker that is a directory still holds", func(t *testing.T) string {
			dir := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(dir, HeldMarkerFile), 0o755))
			return dir
		}, true},
		{"parent path is a file", func(t *testing.T) string {
			file := filepath.Join(t.TempDir(), "raw.jsonl")
			require.NoError(t, os.WriteFile(file, nil, 0o644))
			return file
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsHeld(tt.setup(t)))
		})
	}
}

func TestWriteHoldMarker_Refusals(t *testing.T) {
	ledger := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(ledger, ".git"), 0o755))
	shared := filepath.Join(ledger, "sessions", "2026-10-05T10-00-ryan-Ox1234")
	cache := filepath.Join(ledger, ".sageox", "cache", "sessions", "2026-10-05T10-00-ryan-Ox1234")
	require.NoError(t, os.MkdirAll(shared, 0o755))
	require.NoError(t, os.MkdirAll(cache, 0o755))

	err := WriteHoldMarker(shared, HoldManualPublishing, "test")
	require.ErrorIs(t, err, ErrHoldInSharedTree, "a hold in the shared tree would hold the session for every teammate")
	assert.False(t, IsHeld(shared))

	require.NoError(t, WriteHoldMarker(cache, HoldManualPublishing, "test"), "the Ledger cache is not the shared tree")
	assert.True(t, IsHeld(cache))

	require.Error(t, WriteHoldMarker(filepath.Join(ledger, "nope"), HoldManualPublishing, "test"),
		"a hold on a folder that does not exist would hide nothing")
}

// Failure prevented: a hold that silently fails to land, a corrupt marker read
// as valid, or a release that silently fails leaves the coworker believing a
// session is kept (or released) when it is not.
func TestHoldMarker_FailuresAreReported(t *testing.T) {
	t.Run("hold on a file, not a folder", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "raw.jsonl")
		require.NoError(t, os.WriteFile(file, nil, 0o644))
		require.ErrorContains(t, WriteHoldMarker(file, HoldManualPublishing, "test"), "not a directory")
	})
	t.Run("folder that cannot be written", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.Chmod(dir, 0o500))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		// chmod does not stop root and is a no-op on Windows: prove the folder
		// is read-only before relying on it.
		if probe, err := os.CreateTemp(dir, "probe-*"); err == nil {
			_ = probe.Close()
			t.Skip("folder is still writable (root or Windows); cannot inject a write failure")
		}
		require.ErrorContains(t, WriteHoldMarker(dir, HoldManualPublishing, "test"), "write hold marker")
		assert.False(t, IsHeld(dir))
	})
	t.Run("marker that cannot be checked", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.Chmod(dir, 0o600))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		// A folder without search permission hides whether a marker exists.
		// Root and Windows ignore that: prove it before relying on it.
		if _, err := os.Lstat(filepath.Join(dir, HeldMarkerFile)); errors.Is(err, fs.ErrNotExist) {
			t.Skip("folder is still searchable (root or Windows); cannot hide the marker")
		}
		require.ErrorContains(t, WriteHoldMarker(dir, HoldManualPublishing, "test"), "check marker",
			"a hold that cannot be confirmed must not be reported as written")
		assert.True(t, IsHeld(dir), "readers still fail closed")
	})
	t.Run("missing or corrupt marker", func(t *testing.T) {
		dir := t.TempDir()
		_, err := ReadHoldMarker(dir)
		require.ErrorIs(t, err, fs.ErrNotExist)
		require.NoError(t, os.WriteFile(filepath.Join(dir, HeldMarkerFile), []byte("{not json"), 0o644))
		_, err = ReadHoldMarker(dir)
		require.ErrorContains(t, err, "parse hold marker")
	})
	t.Run("release that cannot remove the marker", func(t *testing.T) {
		dir := t.TempDir()
		marker := filepath.Join(dir, HeldMarkerFile)
		require.NoError(t, os.Mkdir(marker, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(marker, "stray"), nil, 0o644))
		require.ErrorContains(t, ClearHoldMarker(dir), "release hold")
		assert.True(t, IsHeld(dir), "a hold that could not be released still holds")
	})
}

// Failure prevented: crash recovery publishes a recording the coworker started
// in manual mode, or holds one started in auto mode.
func TestHoldRecordedManual_HoldsOnlyManualRecordings(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, HoldRecordedManual(&RecordingState{SessionPath: dir, PublishingMode: config.SessionPublishingAuto}, "doctor"))
	assert.False(t, IsHeld(dir), "an auto-mode recording is published as before")
	require.NoError(t, HoldRecordedManual(nil, "doctor"))
	require.NoError(t, HoldRecordedManual(&RecordingState{PublishingMode: config.SessionPublishingManual}, "doctor"),
		"a recording with no folder has nothing to hold")

	require.NoError(t, HoldRecordedManual(&RecordingState{SessionPath: dir, PublishingMode: config.SessionPublishingManual}, "doctor"))
	hold, err := ReadHoldMarker(dir)
	require.NoError(t, err)
	assert.Equal(t, HoldManualPublishing, hold.Reason)
	assert.Equal(t, "doctor", hold.Source)
}

// Failure prevented: a held copy in the XDG cache is missed, so an automatic
// path publishes the session from the Ledger cache; or a hold is looked up in
// the Ledger's shared tree, where anyone with push access could plant one.
func TestHeldSessionDirs_EveryCacheButNotTheSharedTree(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	const repoID = "repo_heldtest"
	ledger := filepath.Join(t.TempDir(), repoID)
	xdgSessions := filepath.Join(paths.SessionCacheDir(repoID), "sessions")

	dirs := HeldSessionDirs(ledger)
	require.GreaterOrEqual(t, len(dirs), 2)
	assert.Equal(t, filepath.Join(ledger, ".sageox", "cache", "sessions"), dirs[0])
	assert.Contains(t, dirs, xdgSessions)
	assert.NotContains(t, dirs, filepath.Join(ledger, "sessions"), "the shared tree never holds a session")
	assert.Equal(t, []string{filepath.Join(".sageox", "cache", "sessions")}, HeldSessionDirs(""),
		"without a Ledger path there is no repo ID, so only the Ledger cache is checked")

	const name = "2026-10-05T10-00-devon-OxDeVn"
	xdgCopy := filepath.Join(xdgSessions, name)
	require.NoError(t, os.MkdirAll(xdgCopy, 0o755))
	assert.False(t, IsHeldInLedger(ledger, name))
	require.NoError(t, WriteHoldMarker(xdgCopy, HoldManualPublishing, "test"))
	assert.True(t, IsHeldInLedger(ledger, name), "a hold on the XDG copy holds the session")
}

func TestHeldAnywhere_MatchesByName(t *testing.T) {
	ledgerCache, xdgCache := t.TempDir(), t.TempDir()
	const name = "2026-10-05T10-00-faridun-OxAbCd"
	require.NoError(t, os.MkdirAll(filepath.Join(xdgCache, name), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerCache, name), 0o755))
	require.NoError(t, WriteHoldMarker(filepath.Join(ledgerCache, name), HoldManualPublishing, "test"))

	assert.True(t, HeldAnywhere(name, xdgCache, ledgerCache), "a hold on one copy holds every copy")
	assert.False(t, HeldAnywhere("2026-10-05T11-00-faridun-OxOther", xdgCache, ledgerCache))
	assert.False(t, HeldAnywhere("", xdgCache, ledgerCache))
}

// Failure prevented: a manual-mode recording that crashes is recovered by a
// process that cannot see the CLI's environment (daemon, doctor), which then
// publishes it. The mode must be recorded at start, with D13 precedence.
func TestStartRecording_RecordsPublishingMode(t *testing.T) {
	tests := []struct {
		name, env, project, want string
	}{
		{"default is auto", "", "", "auto"},
		{"project config manual", "", "manual", "manual"},
		{"env manual over project auto", "manual", "auto", "manual"},
		{"env auto over project manual (D13)", "auto", "manual", "auto"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectRoot := setupRecordingTest(t, t.TempDir())
			t.Setenv("OX_USER_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
			t.Setenv("OX_SESSION_PUBLISHING", tt.env)
			if tt.project != "" {
				cfg := `{"config_version":"2","repo_id":"test-repo-id","session_publishing":"` + tt.project + `"}`
				require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".sageox", "config.json"), []byte(cfg), 0o644))
			}
			sessionFile := filepath.Join(t.TempDir(), "session.jsonl")
			require.NoError(t, os.WriteFile(sessionFile, []byte("{}\n"), 0o644))

			state, err := StartRecording(projectRoot, StartRecordingOptions{
				AgentID: "OxA1b2", AdapterName: "claude-code", SessionFile: sessionFile, Username: "testuser",
			})
			require.NoError(t, err)
			assert.Equal(t, tt.want, state.PublishingMode)
			assert.Equal(t, tt.want == "manual", state.RecordedManualPublishing())

			persisted, err := LoadRecordingStateForAgent(projectRoot, "OxA1b2")
			require.NoError(t, err)
			assert.Equal(t, tt.want, persisted.PublishingMode, "recovery reads the persisted state, not the live one")
		})
	}
}

// Failure prevented: a /clear in the same minute as a manual stop reuses the
// held session's folder name; the new recording truncates the held
// transcript and later publishes the result.
func TestStartRecording_RefusesHeldFolder(t *testing.T) {
	project := setupRecordingTest(t, t.TempDir())
	opts := StartRecordingOptions{AgentID: "OxHeld1", AdapterName: "claude-code", Username: "testuser"}
	// Session names are minute-granular; retry once if the minute rolls over
	// between the two starts, since then they legitimately differ.
	for attempt := 0; attempt < 2; attempt++ {
		first, err := StartRecording(project, opts)
		require.NoError(t, err)
		raw := filepath.Join(first.SessionPath, "raw.jsonl")
		require.NoError(t, os.WriteFile(raw, []byte(`{"type":"user","content":"held work"}`+"\n"), 0o600))
		require.NoError(t, WriteHoldMarker(first.SessionPath, HoldManualPublishing, "session_stop"))
		require.NoError(t, ClearRecordingStateForAgent(project, opts.AgentID))

		_, err = StartRecording(project, opts)
		if err == nil && GetSessionName(first.SessionPath) != GenerateSessionName(opts.AgentID, opts.Username) {
			continue // minute rolled over
		}
		require.ErrorIs(t, err, ErrHeldSessionPath)
		got, readErr := os.ReadFile(raw)
		require.NoError(t, readErr)
		assert.Equal(t, `{"type":"user","content":"held work"}`+"\n", string(got), "the held transcript must be untouched")
		assert.True(t, IsHeld(first.SessionPath))
		return
	}
	t.Fatal("could not start twice within one minute")
}
