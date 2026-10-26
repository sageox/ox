package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// With OX_TEST_IMPORT_ADAPTER=offset this test executable is a Claude Code
// adapter that says which read it was asked for: the whole file, or the
// records from a byte offset.
func init() {
	if os.Getenv("OX_TEST_IMPORT_ADAPTER") != "offset" {
		return
	}
	var result any
	switch {
	case len(os.Args) == 4 && os.Args[1] == "read" && os.Args[2] == "--session-file":
		result = adapterprotocol.ReadResult{Entries: []adapterprotocol.RawEntry{{Role: "user", Content: "the whole file"}}}
	case len(os.Args) == 6 && os.Args[1] == "read-from-offset" && os.Args[2] == "--session-file" && os.Args[4] == "--offset":
		result = adapterprotocol.ReadFromOffsetResult{Entries: []adapterprotocol.RawEntry{{Role: "user", Content: "from byte " + os.Args[5]}}}
	default:
		return
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// wholeFileAdapter can read a session only as a whole file.
type wholeFileAdapter struct{ reads int }

func (a *wholeFileAdapter) Name() string                                           { return "claude-code" }
func (a *wholeFileAdapter) Detect() bool                                           { return true }
func (a *wholeFileAdapter) FindSessionFile(adapters.SessionLookup) (string, error) { return "", nil }
func (a *wholeFileAdapter) ReadMetadata(string) (*adapters.SessionMetadata, error) {
	return nil, nil
}
func (a *wholeFileAdapter) Watch(context.Context, string) (<-chan adapters.RawEntry, error) {
	return nil, nil
}
func (a *wholeFileAdapter) Read(string) ([]adapters.RawEntry, error) {
	a.reads++
	return []adapters.RawEntry{{Role: "user", Content: "the whole file"}}, nil
}

// A resumed transcript is read through the adapter from where the session's
// own records begin, and an adapter that cannot read from an offset refuses it
// rather than reading the whole file.
//
// Failure prevented: the import inspecting a resumed transcript correctly but
// reading it whole, uploading the conversation it continues a second time.
func TestImportNativeAdapter_ReadsAResumedSessionFromWhereItBegins(t *testing.T) {
	path := filepath.Join(t.TempDir(), e2eClaudeA+".jsonl")
	read := func(offset int64) ([]adapters.RawEntry, error) {
		return readNativeWithAdapter(context.Background(), nativeimport.Session{Agent: nativeimport.AgentClaude, Path: path, Offset: offset})
	}

	t.Run("through the adapter's offset read", func(t *testing.T) {
		testExecutableImportAdapter(t, "offset")
		entries, err := read(0)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "the whole file", entries[0].Content, "a transcript that was not resumed is read whole")
		entries, err = read(1234)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "from byte 1234", entries[0].Content)
	})
	t.Run("never whole by an adapter that cannot", func(t *testing.T) {
		adapter := &wholeFileAdapter{}
		swapClaudeAdapter(t, adapter)
		_, err := read(1234)
		require.ErrorContains(t, err, "cannot read a resumed session from where it begins")
		assert.Zero(t, adapter.reads, "the whole file is never read for a resumed session")
		_, err = read(0)
		require.NoError(t, err)
		assert.Equal(t, 1, adapter.reads)
	})
}

// --- Rule: A resumed or forked conversation is imported as its own session

// Devon resumed Monday's session on Wednesday. Claude Code wrote Wednesday's
// work to a new transcript that opens with a copy of Monday's conversation,
// which ox had recorded live. The import uploads Wednesday's session once,
// with only its own turns, and leaves Monday's to its recording. A transcript
// with another session's record after its own is still never uploaded.
//
// Failure prevented: every resumed session refused as belonging to another
// session, so a resume that live capture missed could never reach the Ledger
// (#1398); or, once accepted, Monday's turns uploaded again under Wednesday's
// name, and Monday's recording link in the copy making Wednesday's session
// read as already recorded.
func TestImportE2E_ResumedSessionImportsOnlyItsOwnTurns(t *testing.T) {
	f := newImportFixture(t)
	monday := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	wednesday := monday.Add(48 * time.Hour)
	const (
		mondaySessionID = "ses_01a0e93a-ee60-75c8-9514-c1f09debf2ed"
		mondayRecording = "2026-09-14T09-00-devon-OxPRNT"
		wednesdayReply  = "A stale pointer references an object that was never uploaded; re-uploading it lets the push through."
	)
	mondayPath := f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeB, start: monday, prompt: loginPrompt,
		reply: "The callback handler drops the state cookie on Safari; setting SameSite=Lax fixes the loop.",
		hook:  `<session-context agent_id="OxPRNT" recording="true" url="https://sageox.ai/c/` + mondaySessionID + `">`})
	dir := filepath.Join(f.ledgerPath, "sessions", mondayRecording)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	at := monday.Format(time.RFC3339)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"version":"1.0","session_name":"`+mondayRecording+
		`","session_id":"`+mondaySessionID+`","agent_type":"claude-code","created_at":"`+at+`","title":"Fix the Safari login loop",`+
		`"native_sessions":[{"id":"`+e2eClaudeB+`","source":"hook","first_seen":"`+at+`","last_seen":"`+at+`"}]}`), 0o644))
	runGit(t, f.ledgerPath, "add", "sessions/"+mondayRecording)
	runGit(t, f.ledgerPath, "commit", "--no-verify", "-q", "-m", "session: "+mondayRecording)
	runGit(t, f.ledgerPath, "push", "-q")
	remoteBefore := remoteSessionDirs(t, f.barePath)

	// Wednesday's transcript: Monday's, copied verbatim, then its own turns.
	inherited, err := os.ReadFile(mondayPath)
	require.NoError(t, err)
	turn := func(id, typ string, d time.Duration, message map[string]any) string {
		return jsonLine(t, map[string]any{"type": typ, "sessionId": id, "timestamp": wednesday.Add(d).Format(time.RFC3339Nano),
			"cwd": f.projectRoot, "gitBranch": "main", "message": message})
	}
	own := []string{
		turn(e2eClaudeA, "user", time.Second, map[string]any{"role": "user", "content": pushPrompt}),
		turn(e2eClaudeA, "assistant", time.Minute, map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": wednesdayReply}}}),
	}
	resumedPath := filepath.Join(filepath.Dir(mondayPath), e2eClaudeA+".jsonl")
	require.NoError(t, os.WriteFile(resumedPath, append(append([]byte(nil), inherited...), strings.Join(own, "\n")+"\n"...), 0o600))
	// Another transcript mixes Monday's turns in after its own.
	mixedPath := filepath.Join(filepath.Dir(mondayPath), e2eClaudeC+".jsonl")
	require.NoError(t, os.WriteFile(mixedPath, []byte(turn(e2eClaudeC, "user", time.Hour, map[string]any{"role": "user", "content": tokenPrompt})+"\n"+string(inherited)), 0o600))
	quiet := time.Now().Add(-2 * time.Hour)
	for _, p := range []string{resumedPath, mixedPath} {
		require.NoError(t, os.Chtimes(p, quiet, quiet))
	}
	ownEntries := []adapters.RawEntry{
		{Timestamp: wednesday.Add(time.Second), Role: "user", Content: pushPrompt},
		{Timestamp: wednesday.Add(time.Minute), Role: "assistant", Content: wednesdayReply},
	}
	f.mu.Lock()
	// What the adapter returns read whole, and read from where Wednesday's records begin.
	f.native[resumedPath] = append(append([]adapters.RawEntry(nil), f.native[mondayPath]...), ownEntries...)
	f.nativeFrom[nativeRead{resumedPath, int64(len(inherited))}] = ownEntries
	f.mu.Unlock()

	r := f.run(t, importOptions{yes: true, jsonOut: true})
	require.NoError(t, r.err, r.out)

	s := r.session(t, e2eClaudeA)
	require.Equal(t, "uploaded", s.Outcome, "state %s, covered by %q: %s%s", s.State, s.CoveredBy, s.Reason, s.Detail)
	assert.Equal(t, nativeimport.Name(nativeimport.AgentClaude, e2eClaudeA, wednesday.Add(time.Second)), s.SessionName,
		"it is named for when its own turns began")
	assert.Equal(t, append(remoteBefore, s.SessionName), remoteSessionDirs(t, f.barePath), "only Wednesday's session is added")
	raw := f.uploadedContent(t, s.SessionName, "raw.jsonl")
	assert.Contains(t, raw, pushPrompt)
	assert.Contains(t, raw, wednesdayReply)
	assert.NotContains(t, raw, loginPrompt, "Monday's conversation is not uploaded again")
	meta := remoteMeta(t, f.barePath, s.SessionName)
	assert.Equal(t, e2eTitleFor(pushPrompt), meta.Title)
	assert.Equal(t, 2, meta.EntryCount)
	require.Len(t, meta.NativeSessions, 1)
	assert.Equal(t, e2eClaudeA, meta.NativeSessions[0].ID)
	assert.Equal(t, wednesday.Add(time.Second), meta.NativeSessions[0].FirstSeen.UTC())
	assert.Equal(t, 1, f.summarizer.calls(), "only Wednesday's session is summarized")
	assert.NotContains(t, f.summarizer.prompts[0], loginPrompt, "its summary is not written from Monday's turns")

	monday2 := r.session(t, e2eClaudeB)
	assert.Equal(t, string(stateRecordedLive), monday2.State)
	assert.Equal(t, mondayRecording, monday2.CoveredBy)
	for _, sess := range r.report.Sessions {
		assert.NotEqual(t, e2eClaudeC, sess.NativeID, "a transcript mixing sessions is never offered")
	}
	assert.Equal(t, 1, r.report.Ignored.Unreadable, "the mixed transcript is counted as unreadable")
	gitFsckClean(t, f.barePath)
}
