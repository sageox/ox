package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	idxRepoID   = "repo_0198a3c4-5e6f-7a8b-9c0d-1e2f3a4b5c6d"
	idxNativeID = "3f9a1c2b-7d4e-4a01-9c55-2b8e0f6d1a73"
)

func importGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

// newIndexLedger commits one meta.json per entry into a fresh Ledger repo.
func newIndexLedger(t *testing.T, metas map[string]lfs.SessionMeta) string {
	t.Helper()
	ledger := t.TempDir()
	importGit(t, ledger, "init", "-q", "-b", "main")
	for name, meta := range metas {
		dir := filepath.Join(ledger, "sessions", name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		data, err := json.Marshal(meta)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644))
	}
	importGit(t, ledger, "add", "-A")
	importGit(t, ledger, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "seed")
	return ledger
}

func nativeSession(id, source string) []lfs.NativeSession {
	at := time.Date(2026, 9, 12, 14, 3, 0, 0, time.UTC)
	return []lfs.NativeSession{{ID: id, Source: source, FirstSeen: at, LastSeen: at}}
}

func candidate(markers ...nativeimport.Marker) nativeimport.Session {
	return candidateFor(idxNativeID, markers...)
}

func candidateFor(id string, markers ...nativeimport.Marker) nativeimport.Session {
	start := time.Date(2026, 9, 12, 14, 3, 10, 0, time.UTC)
	return nativeimport.Session{
		Agent: nativeimport.AgentClaude, NativeID: id, Path: "/native/" + id + ".jsonl",
		StartedAt: start, LastActivity: start.Add(2 * time.Hour), ModTime: start.Add(2 * time.Hour),
		Prompts: 3, Replies: 3, Markers: markers,
	}
}

// Failure prevented: uploading a native session a second time, because it
// was imported before (under any name) or recorded live by any ox version.
// Every case reads one shared Ledger, as a real run does; each case owns its
// native session ID and agent ID, so a match can only come from its own row.
func TestImportIndexClassify(t *testing.T) {
	id := func(n int) string { return fmt.Sprintf("3f9a1c2b-7d4e-4a01-9c55-2b8e0f6d1a%02d", n) }
	inHead := nativeimport.Name(nativeimport.AgentClaude, id(2), candidateFor(id(2)).StartedAt)
	ledger := newIndexLedger(t, map[string]lfs.SessionMeta{
		"2026-09-01T10-00-lau-OxAAAA": {AgentType: "claude-code"},
		inHead:                        {AgentType: "claude-code"},
		"2026-09-12T14-04-import-claude-" + id(3): {AgentType: "claude-code", NativeSessions: nativeSession(id(3), "import")},
		"2026-09-12T14-03-lau-OxLIVE":             {AgentType: "claude", NativeSessions: nativeSession(id(4), "startup")},
		"2026-09-12T14-03-lau-OxDRFT":             {AgentType: "claude-code", Draft: true, NativeSessions: nativeSession(id(5), "import")},
		"2026-08-20T10-00-lau-OxOLD1":             {AgentType: "claude-code", SessionID: "ses_01a0e93a-ee60-75c8-9514-c1f09debf2ed"},
		"2026-04-01T10-00-lau-OxOLD2":             {AgentType: "claude-code"},
		"2026-09-12T15-30-lau-OxLATE":             {AgentType: "claude-code"},
		"2026-05-01T09-00-sam-OxEARL":             {AgentType: "claude-code"},
		"2026-09-12T14-03-lau-OxCDX1":             {AgentType: "codex", NativeSessions: nativeSession(id(10), "startup")},
	})
	idx, err := buildImportIndex(context.Background(), t.TempDir(), ledger, idxRepoID)
	require.NoError(t, err)

	later := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		session   nativeimport.Session
		wantState importState
		wantMatch string
	}{
		{"nothing in the Ledger", candidateFor(id(1)), stateReady, ""},
		{"the deterministic name is already in HEAD", candidateFor(id(2)), stateAlreadyImported, inHead},
		{"imported earlier under another name", candidateFor(id(3)), stateAlreadyImported, "2026-09-12T14-04-import-claude-" + id(3)},
		{"recorded live since ox 0.17, with the agent spelled claude", candidateFor(id(4)), stateRecordedLive, "2026-09-12T14-03-lau-OxLIVE"},
		{"a draft of a live recording covers it too", candidateFor(id(5)), stateRecordedLive, "2026-09-12T14-03-lau-OxDRFT"},
		{
			"an older recording matched by the ses_ link in ox's marker",
			candidateFor(id(6), nativeimport.Marker{AgentID: "OxOLD1", URL: "https://sageox.ai/c/ses_01a0e93a-ee60-75c8-9514-c1f09debf2ed"}),
			stateRecordedLive, "2026-08-20T10-00-lau-OxOLD1",
		},
		{
			"an older recording matched by the name link in ox's marker",
			candidateFor(id(7), nativeimport.Marker{AgentID: "OxOLD2", URL: "https://sageox.ai/repo/r/sessions/2026-04-01T10-00-lau-OxOLD2/view"}),
			stateRecordedLive, "2026-04-01T10-00-lau-OxOLD2",
		},
		{
			"a marker with only an agent ID matches a recording that starts inside the session",
			candidateFor(id(8), nativeimport.Marker{AgentID: "OxLATE"}), stateRecordedLive, "2026-09-12T15-30-lau-OxLATE",
		},
		{
			"an agent ID whose recording starts outside the session is not a match",
			candidateFor(id(9), nativeimport.Marker{AgentID: "OxEARL"}), stateReady, "",
		},
		{"a Codex recording of the same UUID does not cover a Claude session", candidateFor(id(10)), stateReady, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, match := idx.classify(tt.session, later)
			assert.Equal(t, tt.wantState, state)
			assert.Equal(t, tt.wantMatch, match)
		})
	}
}

func TestImportIndexInProgress(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	idx := newImportIndex()
	idx.active[keyFor(nativeimport.AgentClaude, "active-id")] = "2026-09-30T11-00-lau-OxACTV"
	idx.activeFiles["/native/recorded.jsonl"] = "2026-09-30T11-00-lau-OxFILE"

	quiet := candidate()
	quiet.ModTime = now.Add(-time.Hour)
	tests := []struct {
		name   string
		mutate func(*nativeimport.Session)
		want   importState
	}{
		{"quiet and unknown", func(*nativeimport.Session) {}, stateReady},
		{"a Codex turn in flight", func(s *nativeimport.Session) { s.InFlight = true }, stateInProgress},
		{"a Codex turn open but quiet for 13 hours was interrupted", func(s *nativeimport.Session) {
			s.InFlight, s.ModTime = true, now.Add(-13*time.Hour)
		}, stateReady},
		{"written in the last 30 minutes", func(s *nativeimport.Session) { s.ModTime = now.Add(-5 * time.Minute) }, stateInProgress},
		{"ox is recording it", func(s *nativeimport.Session) { s.NativeID = "active-id" }, stateInProgress},
		{"ox is recording its file", func(s *nativeimport.Session) { s.Path = "/native/recorded.jsonl" }, stateInProgress},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := quiet
			tt.mutate(&s)
			state, _ := idx.classify(s, now)
			assert.Equal(t, tt.want, state)
		})
	}
}

func TestImportIndexReadsPendingCaptures(t *testing.T) {
	ledger := newIndexLedger(t, nil)
	name := "2026-09-12T14-03-lau-OxPEND"
	dir := filepath.Join(ledger, ".sageox", "cache", "sessions", name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	header := `{"type":"header","metadata":{"version":"1.0","created_at":"2026-09-12T14:03:00Z","agent_type":"claude-code","session_id":"ses_01a0e93a-ee60-75c8-9514-c1f09debf2ee"}}`
	entry := `{"type":"user","content":"hi","ts":"2026-09-12T14:03:05Z"}`
	footer := `{"type":"footer","native_sessions":[{"id":"` + idxNativeID + `","source":"startup","first_seen":"2026-09-12T14:03:00Z","last_seen":"2026-09-12T14:03:00Z"}]}`
	// Footers merge field by field: a later one without native_sessions hides none.
	later := `{"type":"footer","trace_capture":{"status":"none"}}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(header+"\n"+entry+"\n"+footer+"\n"+later+"\n"), 0o600))

	uploaded := filepath.Join(ledger, ".sageox", "cache", "sessions", "2026-09-10T08-00-lau-OxDONE")
	require.NoError(t, os.MkdirAll(uploaded, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(uploaded, "raw.jsonl"),
		[]byte(lfs.FormatPointer("sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393", 10)), 0o600))

	idx, err := buildImportIndex(context.Background(), t.TempDir(), ledger, idxRepoID)
	require.NoError(t, err)
	state, match := idx.classify(candidate(), time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC))
	assert.Equal(t, stateRecordedLive, state, "a capture waiting to upload already covers the session")
	assert.Equal(t, name, match)
}

// Failure prevented: an upload nobody confirmed, or a confirmation prompt in
// the middle of what should be one JSON document.
func TestImportMayUploadOnlyWithConfirmation(t *testing.T) {
	tests := []struct {
		name        string
		opts        importOptions
		interactive bool
		want        bool
	}{
		{"a dry run never uploads, even with --yes", importOptions{dryRun: true, yes: true}, true, false},
		{"--yes is the confirmation", importOptions{yes: true}, false, true},
		{"a coworker at a terminal is asked", importOptions{}, true, true},
		{"no terminal and no --yes is a preview", importOptions{}, false, false},
		{"an AI coworker needs --yes", importOptions{agentCtx: true, jsonOut: true}, true, false},
		{"JSON output needs --yes", importOptions{jsonOut: true}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, importMayUpload(tt.opts, tt.interactive))
		})
	}
}

// Failure prevented: a session recorded before 2026-03-31, when ox named
// recordings in local time, uploaded a second time because its agent-ID
// marker was compared against the wrong hour.
func TestImportIndexAgentMarkerUsesTheRecordingsStart(t *testing.T) {
	name := "2026-03-20T10-00-lau-OxPACF" // named at 10:00 Pacific, 17:00 UTC
	ledger := newIndexLedger(t, map[string]lfs.SessionMeta{
		name: {AgentType: "claude-code", CreatedAt: time.Date(2026, 3, 20, 17, 0, 5, 0, time.UTC)},
	})
	idx, err := buildImportIndex(context.Background(), t.TempDir(), ledger, idxRepoID)
	require.NoError(t, err)

	s := candidateFor(idxNativeID, nativeimport.Marker{AgentID: "OxPACF"})
	s.StartedAt = time.Date(2026, 3, 20, 16, 58, 0, 0, time.UTC)
	s.LastActivity, s.ModTime = s.StartedAt.Add(2*time.Hour), s.StartedAt.Add(2*time.Hour)
	state, match := idx.classify(s, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC))
	assert.Equal(t, stateRecordedLive, state)
	assert.Equal(t, name, match)
}

// Failure prevented: one corrupt session record in the Ledger stopping every
// import, or hiding the sessions the readable records cover.
func TestImportIndexToleratesAnUnreadableMeta(t *testing.T) {
	ledger := newIndexLedger(t, map[string]lfs.SessionMeta{
		"2026-09-12T14-03-lau-OxLIVE": {AgentType: "claude-code", NativeSessions: nativeSession(idxNativeID, "startup")},
	})
	bad := filepath.Join(ledger, "sessions", "2026-09-13T10-00-lau-OxBAD1")
	require.NoError(t, os.MkdirAll(bad, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bad, "meta.json"), []byte("{not json"), 0o644))
	importGit(t, ledger, "add", "-A")
	importGit(t, ledger, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "-m", "a corrupt record")

	idx, err := buildImportIndex(context.Background(), t.TempDir(), ledger, idxRepoID)
	require.NoError(t, err)
	state, match := idx.classify(candidate(), time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC))
	assert.Equal(t, stateRecordedLive, state)
	assert.Equal(t, "2026-09-12T14-03-lau-OxLIVE", match)
}

// Failure prevented: a freshly cloned, still empty Ledger refused as
// unreadable instead of accepting its first import.
func TestImportIndexOfAnEmptyLedger(t *testing.T) {
	ledger := t.TempDir()
	importGit(t, ledger, "init", "-q")
	idx, err := buildImportIndex(context.Background(), t.TempDir(), ledger, idxRepoID)
	require.NoError(t, err)
	state, _ := idx.classify(candidate(), time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC))
	assert.Equal(t, stateReady, state)
}

// Failure prevented: a preview that calls a session fully in the Ledger when
// the Ledger holds only part of it: a session continued after its import, or
// one ox began recording on a resume.
func TestImportIndexSaysWhatTheLedgerLacks(t *testing.T) {
	start := candidate().StartedAt
	importName := nativeimport.Name(nativeimport.AgentClaude, idxNativeID, start)
	imported := lfs.SessionMeta{AgentType: "claude-code", NativeSessions: []lfs.NativeSession{
		{ID: idxNativeID, Source: nativeimport.NativeSourceImport, FirstSeen: start, LastSeen: start.Add(2 * time.Hour)},
	}}
	recordedFrom := func(at time.Time) lfs.SessionMeta {
		return lfs.SessionMeta{AgentType: "claude-code", NativeSessions: []lfs.NativeSession{
			{ID: idxNativeID, Source: "resume", FirstSeen: at, LastSeen: at.Add(time.Hour)},
		}}
	}
	continued := candidate()
	continued.LastActivity = start.Add(5 * time.Hour)
	tests := []struct {
		name    string
		metas   map[string]lfs.SessionMeta
		session nativeimport.Session
		state   importState
		want    string
	}{
		{"imported and untouched since", map[string]lfs.SessionMeta{importName: imported}, candidate(), stateAlreadyImported, ""},
		{"continued after its import, unrecorded", map[string]lfs.SessionMeta{importName: imported}, continued,
			stateAlreadyImported, "continued after it was imported; the later part is not in the Ledger"},
		{"continued after its import, recorded by ox from a later time", map[string]lfs.SessionMeta{
			importName: imported, "2026-09-12T19-00-lau-OxRSME": recordedFrom(start.Add(5 * time.Hour)),
		}, continued, stateAlreadyImported, "continued after it was imported; ox recorded it from " +
			start.Add(5*time.Hour).Local().Format("2006-01-02 15:04 MST") + " as 2026-09-12T19-00-lau-OxRSME"},
		{"continued after its import, recorded by ox at an unknown time", map[string]lfs.SessionMeta{
			importName: imported, "2026-09-12T19-00-lau-OxRSME": {AgentType: "claude-code",
				NativeSessions: []lfs.NativeSession{{ID: idxNativeID, Source: "resume"}}},
		}, continued, stateAlreadyImported, "continued after it was imported; ox also recorded it as 2026-09-12T19-00-lau-OxRSME"},
		{"recorded from its start", map[string]lfs.SessionMeta{"2026-09-12T14-03-lau-OxLIVE": recordedFrom(start)},
			candidate(), stateRecordedLive, ""},
		{"recorded only from a resume", map[string]lfs.SessionMeta{"2026-09-15T09-00-lau-OxRSME": recordedFrom(start.Add(72 * time.Hour))},
			candidate(), stateRecordedLive, "the part before that is not in the Ledger"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, err := buildImportIndex(context.Background(), t.TempDir(), newIndexLedger(t, tt.metas), idxRepoID)
			require.NoError(t, err)
			state, _ := idx.classify(tt.session, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC))
			require.Equal(t, tt.state, state)
			note := idx.coverageNote(tt.session, state)
			if tt.want == "" {
				assert.Empty(t, note)
				return
			}
			assert.Contains(t, note, tt.want)
		})
	}
}
