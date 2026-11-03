package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShouldPrune(t *testing.T) {
	tests := []struct {
		name     string
		status   session.SessionStatus
		pruneAll bool
		want     bool
	}{
		// default mode — only StatusLocal is pruned
		{"default_local", session.StatusLocal, false, true},
		{"default_paused", session.StatusPaused, false, false},
		{"default_canceled", session.StatusCanceled, false, false},
		{"default_ghost", session.StatusGhost, false, false},
		{"default_orphan", session.StatusOrphan, false, false},
		{"default_uploaded", session.StatusUploaded, false, false},
		{"default_recording", session.StatusRecording, false, false},
		{"default_suspended", session.StatusSuspended, false, false},
		{"default_held", session.StatusHeld, false, false},

		// --all mode — every non-uploaded, non-recording status is pruned
		{"all_local", session.StatusLocal, true, true},
		{"all_paused", session.StatusPaused, true, true},
		{"all_canceled", session.StatusCanceled, true, true},
		{"all_ghost", session.StatusGhost, true, true},
		{"all_orphan", session.StatusOrphan, true, true},
		{"all_uploaded", session.StatusUploaded, true, false},
		{"all_recording", session.StatusRecording, true, false},
		{"all_suspended", session.StatusSuspended, true, false},
		// a held session is the coworker's to remove, by name (GH #1093)
		{"all_held", session.StatusHeld, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldPrune(tt.status, tt.pruneAll)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestCollectPruneCandidates_SameSessionAcrossStoresDeletesBothCopies guards
// against the dedup regression where a not-yet-uploaded session staged in
// both the per-user store and the ledger cache only had its first-seen copy
// scheduled for deletion, silently leaving the other behind.
func TestCollectPruneCandidates_SameSessionAcrossStoresDeletesBothCopies(t *testing.T) {
	localStore, err := session.NewStore(t.TempDir())
	require.NoError(t, err)
	cacheStore, err := session.NewStore(t.TempDir())
	require.NoError(t, err)

	const name = "2026-01-05T10-30-user1-Oxa7b3"
	for _, s := range []*session.Store{localStore, cacheStore} {
		w, err := s.CreateRaw(name)
		require.NoError(t, err)
		require.NoError(t, w.Close())
	}

	stores := []prunableStore{
		{store: localStore, origin: "local"},
		{store: cacheStore, origin: "ledger-cache"},
	}

	candidates, skipped, err := collectPruneCandidates(stores, map[string]bool{}, false)
	require.NoError(t, err)
	assert.Equal(t, 0, skipped)
	require.Len(t, candidates, 1)
	assert.Len(t, candidates[0].locations, 2)
	assert.Equal(t, "local+ledger-cache", candidates[0].originLabel())
}

// TestDeletePruneCandidates_PartialFailureStillDeletesOtherLocations covers
// the actual delete loop (not just candidate collection): when one location
// of a multi-location candidate has already vanished — e.g. a race with a
// concurrent prune or cleanup — the candidate must not count as removed, but
// its surviving location must still be deleted rather than left behind.
func TestDeletePruneCandidates_PartialFailureStillDeletesOtherLocations(t *testing.T) {
	storeA, err := session.NewStore(t.TempDir())
	require.NoError(t, err)
	storeB, err := session.NewStore(t.TempDir())
	require.NoError(t, err)
	storeC, err := session.NewStore(t.TempDir())
	require.NoError(t, err)

	mustCreate := func(s *session.Store, name string) {
		t.Helper()
		w, err := s.CreateRaw(name)
		require.NoError(t, err)
		require.NoError(t, w.Close())
	}

	const cleanName = "2026-01-05T10-30-user1-Oxaaa1"
	mustCreate(storeA, cleanName)

	// partial candidate: two locations, one is deleted out from under the
	// store before the delete loop runs, simulating a race.
	const partialName = "2026-01-05T10-31-user1-Oxbbb2"
	mustCreate(storeB, partialName)
	mustCreate(storeC, partialName)
	require.NoError(t, os.RemoveAll(storeB.GetSessionPath(partialName)))

	candidates := []pruneCandidate{
		{name: cleanName, locations: []prunableStore{{store: storeA, origin: "local"}}},
		{name: partialName, locations: []prunableStore{
			{store: storeB, origin: "local"},
			{store: storeC, origin: "ledger-cache"},
		}},
	}

	removed := deletePruneCandidates(candidates)

	assert.Equal(t, 1, removed, "only the fully-clean candidate should count as removed")
	_, statErr := os.Stat(storeA.GetSessionPath(cleanName))
	assert.True(t, os.IsNotExist(statErr), "clean candidate's location should be deleted")
	_, statErr = os.Stat(storeC.GetSessionPath(partialName))
	assert.True(t, os.IsNotExist(statErr), "surviving location must still be deleted even though its sibling location already failed")
}

// A downloaded .jsonl directory must never resolve to the cache root when
// its ledger entry disappears and default prune selects the cached copy.
func TestPrune_HydratedJSONLDirectoryPreservesCache(t *testing.T) {
	raw := []byte("{\"type\":\"user\",\"content\":\"session transcript\"}\n")
	ref := lfs.NewFileRef(raw)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ledger.git/info/lfs/objects/batch":
			w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
			_ = json.NewEncoder(w).Encode(lfs.BatchResponse{Transfer: "basic", Objects: []lfs.BatchResponseObject{{
				OID: ref.BareOID(), Size: ref.Size,
				Actions: &lfs.Actions{Download: &lfs.Action{Href: server.URL + "/download"}},
			}}})
		case "/download":
			_, _ = w.Write(raw)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	ledgerPath := t.TempDir()
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	const name = ".jsonl"
	sessionDir := filepath.Join(sessionsDir, name)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	meta := lfs.NewSessionMeta(name, "teammate", "OxTEST", "claude-code", time.Now().UTC()).Build()
	meta.Files = map[string]lfs.FileRef{"raw.jsonl": ref}
	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, meta))
	require.NoError(t, lfs.WritePointerFile(filepath.Join(sessionDir, "raw.jsonl"), lfs.AssertUploaded(ref)))

	// Hydrate through the cache helper with a local LFS server, then exercise
	// the command's resolver/status check without requiring real credentials.
	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", name)
	require.NoError(t, lfs.MarkCacheDownload(cacheDir))
	cachedRaw, err := lfs.HydrateRawToCacheErr(lfs.NewClient(server.URL+"/ledger.git", "user", "token"), sessionDir, ledgerPath)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(cacheDir, "raw.jsonl"), cachedRaw)
	require.NoError(t, hydrateFromLedgerContext(t.Context(), t.TempDir(), sessionsDir, name, true))

	localStore, err := session.NewStore(t.TempDir())
	require.NoError(t, err)
	stores := buildPruneStores(localStore, ledgerPath)
	require.Len(t, stores, 2)
	cacheStore := stores[1].store

	// Default prune must preserve both live work and an orphaned transcript
	// that is still recoverable, as well as a sibling still in the ledger.
	preserved := make(map[string][]byte)
	for _, sibling := range []string{"active", "recoverable", "uploaded"} {
		dir := filepath.Join(cacheStore.BasePath(), sibling)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		rawPath := filepath.Join(dir, "raw.jsonl")
		require.NoError(t, os.WriteFile(rawPath, raw, 0o600))
		preserved[rawPath] = raw
		if sibling == "uploaded" {
			ledgerDir := filepath.Join(sessionsDir, sibling)
			require.NoError(t, os.MkdirAll(ledgerDir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "raw.jsonl"), raw, 0o600))
			continue
		}
		state := session.RecordingState{StartedAt: time.Now().Add(-time.Hour)}
		if sibling == "active" {
			state.ParentPID = os.Getpid()
		}
		data, err := json.Marshal(state)
		require.NoError(t, err)
		recordingPath := filepath.Join(dir, ".recording.json")
		require.NoError(t, os.WriteFile(recordingPath, data, 0o600))
		preserved[recordingPath] = data
	}

	require.NoError(t, os.RemoveAll(sessionDir))
	uploaded, err := loadUploadedKeys(ledgerPath)
	require.NoError(t, err)
	candidates, skipped, err := collectPruneCandidates(stores, uploaded, false)
	require.NoError(t, err)
	require.Equal(t, 1, skipped)
	require.Len(t, candidates, 1)
	require.Equal(t, name, candidates[0].name)
	require.Equal(t, session.StatusLocal, candidates[0].status)

	assert.Equal(t, 1, deletePruneCandidates(candidates))
	assert.NoDirExists(t, cacheDir)
	assert.DirExists(t, cacheStore.BasePath())
	for path, want := range preserved {
		got, err := os.ReadFile(path)
		require.NoError(t, err, "prune removed protected sibling data: %s", path)
		assert.Equal(t, want, got, path)
	}
	assert.FileExists(t, filepath.Join(sessionsDir, "uploaded", "raw.jsonl"))
}

func TestPrunePartialFailureError(t *testing.T) {
	tests := []struct {
		name           string
		removed, total int
		wantErr        bool
	}{
		{"all_removed", 3, 3, false},
		{"nothing_to_remove", 0, 0, false},
		{"partial_failure", 2, 3, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := prunePartialFailureError(tt.removed, tt.total)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
