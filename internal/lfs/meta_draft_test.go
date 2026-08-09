package lfs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testDraftSessionID = "ses_01950000-0000-7000-8000-000000000001"

func draftInputFixture(name string) DraftInput {
	return DraftInput{
		SessionName: name,
		SessionID:   testDraftSessionID,
		Username:    "Test Coworker",
		UserID:      "usr_1",
		RepoID:      "repo_1",
		AgentID:     "OxTest",
		AgentType:   "claude-code",
		Model:       "claude-opus-5",
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		TurnCount:   2,
		Now:         time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC),
	}
}

// readRawMeta returns the meta.json BYTES, deliberately not the parsed struct.
// Several invariants here are about what is or is not present in the file — a
// struct round-trip cannot see an `"draft": false` key that should have been
// omitted.
func readRawMeta(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	return raw
}

// TestDraftFlagOmitempty pins the on-disk shape, not the struct shape.
//
// Catches: a `json:"draft"` tag without omitempty. That would stamp
// `"draft": false` onto every finalized meta.json in every ledger — a schema
// diff on every session, and a brand-new merge-conflict surface on a file that
// two writers already race for.
func TestDraftFlagOmitempty(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, WriteSessionMetaOnly(dir, &SessionMeta{
		SessionName: "s", CreatedAt: time.Now(),
		Files: map[string]FileRef{"raw.jsonl": {OID: "sha256:abc", Size: 1}},
	}))
	raw := readRawMeta(t, dir)
	assert.NotContains(t, raw, "draft", "a non-draft meta must not carry a draft key at all")
	assert.NotContains(t, raw, "turn_count")
	assert.NotContains(t, raw, "updated_at")

	require.NoError(t, WriteDraftSessionMeta(context.Background(), dir+"2", draftInputFixture("s2")))
	raw2 := readRawMeta(t, dir+"2")
	assert.Equal(t, true, raw2["draft"], "a draft must carry draft:true on disk")
}

// TestIsDraft_LegacyAndMalformed pins the fail-safe DIRECTION.
//
// "Not a draft" is the safe default and "is a draft" is the destructive one:
// treating an unreadable meta as a draft makes doctor's orphan sweep skip a
// session whose only transcript copy is in the cache, and makes abort willing
// to delete a directory it cannot classify.
func TestIsDraft_LegacyAndMalformed(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"legacy meta with no draft key", `{"session_name":"s","created_at":"2026-01-01T00:00:00Z"}`},
		{"empty object", `{}`},
		{"draft as string not bool", `{"draft":"true"}`},
		{"truncated json", `{"draft":tr`},
		{"zero bytes", ``},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(tc.body), 0644))

			meta, err := ReadSessionMeta(dir)
			if err != nil {
				// unreadable -> nil meta -> not a draft, and no panic
				assert.False(t, meta.IsDraft())
				return
			}
			assert.False(t, meta.IsDraft(), "%s must not read as a draft", tc.name)
		})
	}

	var nilMeta *SessionMeta
	assert.False(t, nilMeta.IsDraft(), "nil meta must be nil-safe and report not-a-draft")
}

// TestValidateDraftShape_RejectsFilesManifest is the writer-side guard on the
// LFS invariant.
//
// Catches: any future path that stamps draft:true onto a meta with a populated
// manifest. That meta claims LFS OIDs which were never uploaded, and committing
// the reference makes the ledger's pre-receive hook reject every subsequent
// push — for the whole team, not just the author.
func TestValidateDraftShape_RejectsFilesManifest(t *testing.T) {
	dir := t.TempDir()
	err := WriteSessionMetaOnly(dir, &SessionMeta{
		SessionName: "s", CreatedAt: time.Now(), Draft: true,
		Files: map[string]FileRef{"raw.jsonl": {OID: "sha256:abc", Size: 1}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not name artifacts")
	assert.NoFileExists(t, filepath.Join(dir, "meta.json"), "a rejected write must not land on disk")
}

// TestValidateDraftShape_RejectsSummary guards the "summary still owed" signal.
//
// The ABSENCE of summary artifacts is what IsStubSummary and the daemon's
// anti-entropy both key on. A draft that carries summary text makes every
// downstream consumer believe a zero-turn session was already summarized.
func TestValidateDraftShape_RejectsSummary(t *testing.T) {
	for _, tc := range []struct {
		name string
		meta *SessionMeta
		want string
	}{
		{"summary text", &SessionMeta{SessionName: "s", Draft: true, Summary: "did some work"}, "must not carry summary text"},
		{"summary status", &SessionMeta{SessionName: "s", Draft: true, SummaryStatus: "pending"}, "must not carry summary_status"},
		{"whitespace-padded summary is still a summary", &SessionMeta{SessionName: "s", Draft: true, Summary: "   x  "}, "must not carry summary text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := WriteSessionMetaOnly(t.TempDir(), tc.meta)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestValidateDraftShape_AllowsNonDraftWithFiles is the negative control.
// Without it, the two tests above pass with validateDraftShape rewritten to
// "always return an error".
func TestValidateDraftShape_AllowsNonDraftWithFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, WriteSessionMetaOnly(dir, &SessionMeta{
		SessionName: "s", CreatedAt: time.Now(),
		Summary:       "a real summary",
		SummaryStatus: "ok",
		Files:         map[string]FileRef{"raw.jsonl": {OID: "sha256:abc", Size: 1}},
	}))
}

// TestClearDraft_PreservesEveryOtherField is the daemon-finalize contract.
//
// The daemon's finalize does `next := current` to preserve fields it does not
// own. ClearDraft must remove exactly the three draft markers and nothing else
// — a ClearDraft that reset the struct would silently erase redactions,
// produced_commits, and linkage from the SHARED ledger history, because
// sessions/ conflicts auto-resolve to the local side.
func TestClearDraft_PreservesEveryOtherField(t *testing.T) {
	updated := time.Now().UTC()
	meta := &SessionMeta{
		SessionName: "s", SessionID: testDraftSessionID, AgentID: "OxTest",
		Draft: true, TurnCount: 7, UpdatedAt: &updated,
		Redactions:      []RedactionPass{{PassID: "p1"}},
		ProducedCommits: []string{"abc123"},
		LinkedPRs:       []string{"o/r#1"},
		LinkageStatus:   LinkageStatusStaged,
		EntryCount:      42,
		Title:           "real title",
	}
	meta.ClearDraft()

	assert.False(t, meta.Draft)
	assert.Zero(t, meta.TurnCount)
	assert.Nil(t, meta.UpdatedAt)

	assert.Equal(t, testDraftSessionID, meta.SessionID)
	assert.Len(t, meta.Redactions, 1)
	assert.Equal(t, []string{"abc123"}, meta.ProducedCommits)
	assert.Equal(t, []string{"o/r#1"}, meta.LinkedPRs)
	assert.Equal(t, LinkageStatusStaged, meta.LinkageStatus)
	assert.Equal(t, 42, meta.EntryCount)
	assert.Equal(t, "real title", meta.Title)

	var nilMeta *SessionMeta
	assert.NotPanics(t, func() { nilMeta.ClearDraft() })
}

// TestWriteDraftSessionMeta_RefusesFinalizedSession stops a draft from
// downgrading a real session — the finalize-landed-mid-decision race, and a
// session-name collision with a finalized session pulled from the remote.
func TestWriteDraftSessionMeta_RefusesFinalizedSession(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, WriteSessionMetaOnly(dir, &SessionMeta{
		SessionName: "s", SessionID: testDraftSessionID, CreatedAt: time.Now(),
		Title: "finalized", EntryCount: 99,
	}))

	err := WriteDraftSessionMeta(context.Background(), dir, draftInputFixture("s"))
	require.ErrorIs(t, err, ErrNotDraft)

	meta, readErr := ReadSessionMeta(dir)
	require.NoError(t, readErr)
	assert.Equal(t, "finalized", meta.Title, "the finalized meta must be untouched")
	assert.Equal(t, 99, meta.EntryCount)
	assert.False(t, meta.IsDraft())
}

// TestWriteDraftSessionMeta_RefusesDirWithTranscript is the second half of the
// LFS guard: never label a directory that already holds real turn data as a
// zero-turn draft.
func TestWriteDraftSessionMeta_RefusesDirWithTranscript(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte("real bytes"), 0644))

	err := WriteDraftSessionMeta(context.Background(), dir, draftInputFixture("s"))
	require.ErrorIs(t, err, ErrDraftDirNotEmpty)
	assert.NoFileExists(t, filepath.Join(dir, "meta.json"))
}

// TestWriteDraftSessionMeta_ServerArtifactsDoNotBlockRefresh is the negative
// control for the guard above, and it encodes a real design decision.
//
// The SageOx server may summarize a zero-turn draft and push summary.md /
// summary.json into the directory; a finalize-time `git pull --rebase` folds
// those into our tree. That is anticipated and handled by the purge. If the
// guard covered all of ContentFiles instead of just raw.jsonl, the server doing
// exactly what we expect would silently freeze the draft's counters for the
// rest of the session — server-visible progress would stop, which is the very
// symptom drafts exist to fix.
func TestWriteDraftSessionMeta_ServerArtifactsDoNotBlockRefresh(t *testing.T) {
	for _, name := range []string{"summary.md", "session.md", "plan.md", "context-trace.jsonl", "summary.json"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, WriteDraftSessionMeta(context.Background(), dir, draftInputFixture("s")))
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("server authored"), 0644))

			refresh := draftInputFixture("s")
			refresh.TurnCount = 12
			require.NoError(t, WriteDraftSessionMeta(context.Background(), dir, refresh),
				"a server-authored %s must not block a counter refresh", name)

			meta, err := ReadSessionMeta(dir)
			require.NoError(t, err)
			assert.Equal(t, 12, meta.TurnCount)
		})
	}
}

// TestWriteDraftSessionMeta_PreservesPublishedSessionID is the /c/ link
// stability contract.
//
// Catches: a refresh that rebuilds the meta from the caller's current recording
// state. If the state was rebuilt (binary upgrade mid-recording, crash
// recovery) its SessionID may differ, and adopting it would rotate a link
// already pasted into a PR body.
func TestWriteDraftSessionMeta_PreservesPublishedSessionID(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, WriteDraftSessionMeta(context.Background(), dir, draftInputFixture("s")))

	drifted := draftInputFixture("s")
	drifted.SessionID = "ses_01950000-0000-7000-8000-00000000ffff"
	drifted.TurnCount = 12
	require.NoError(t, WriteDraftSessionMeta(context.Background(), dir, drifted))

	meta, err := ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, testDraftSessionID, meta.SessionID, "the published id wins over the caller's")
	assert.Equal(t, 12, meta.TurnCount, "counters still advance")
}

// TestWriteDraftSessionMeta_CountersAreMonotonic.
//
// RecordingState is an unlocked load-modify-save, so a lost increment is
// possible. Server-visible progress walking backwards reads as the session
// un-doing work, and would make a "is it still alive?" check unreliable.
func TestWriteDraftSessionMeta_CountersAreMonotonic(t *testing.T) {
	dir := t.TempDir()
	ahead := draftInputFixture("s")
	ahead.TurnCount = 20
	ahead.EntryCount = 200
	require.NoError(t, WriteDraftSessionMeta(context.Background(), dir, ahead))

	behind := draftInputFixture("s")
	behind.TurnCount = 3
	behind.EntryCount = 5
	require.NoError(t, WriteDraftSessionMeta(context.Background(), dir, behind))

	meta, err := ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, 20, meta.TurnCount)
	assert.Equal(t, 200, meta.EntryCount)
}

// TestWriteDraftSessionMeta_RequiresValidSessionID.
//
// A draft whose id is missing or malformed produces a /c/ URL that will never
// match the finalized session's, silently splitting one recording into two
// server-side records.
func TestWriteDraftSessionMeta_RequiresValidSessionID(t *testing.T) {
	for _, tc := range []struct{ name, id string }{
		{"empty", ""},
		{"missing ses_ prefix", "01950000-0000-7000-8000-000000000001"},
		{"not a uuid", "ses_nope"},
		{"uppercase uuid rejected so the URL byte-matches meta.json", "ses_01950000-0000-7000-8000-00000000000A"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			in := draftInputFixture("s")
			in.SessionID = tc.id
			require.Error(t, WriteDraftSessionMeta(context.Background(), dir, in))
			assert.NoFileExists(t, filepath.Join(dir, "meta.json"))
		})
	}
}

// TestDraftInput_CarriesNoTranscriptDerivedText is a structural privacy check.
//
// The privacy guarantee is meant to be type-level: a caller must not be able to
// pass turn content into a draft. RecordingState carries a Title derived from
// the user's FIRST USER MESSAGE, and "make the draft look nicer in the UI" is
// the obvious change that would leak it into a pushed, git-tracked, shared file
// at turn 2 — before the user has any indication the session is public.
//
// This asserts on the field set itself so adding such a field fails here rather
// than in review.
func TestDraftInput_CarriesNoTranscriptDerivedText(t *testing.T) {
	allowed := map[string]bool{
		"SessionName": true, "SessionID": true, "Username": true, "UserID": true,
		"RepoID": true, "AgentID": true, "AgentType": true, "Model": true,
		"CreatedAt": true, "TurnCount": true, "EntryCount": true, "Now": true,
	}
	typ := reflectTypeOfDraftInput()
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		assert.True(t, allowed[name],
			"new DraftInput field %q: a draft must carry identity and counters only. "+
				"If this field can hold transcript-derived text (a title, a summary, a preview, "+
				"a prompt excerpt) it leaks into a shared git repo at turn 2 — see the type doc.", name)
	}
}

// TestDraftMetaHasNoTranscriptFields asserts the same property one layer down,
// on the bytes actually committed.
func TestDraftMetaHasNoTranscriptFields(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, WriteDraftSessionMeta(context.Background(), dir, draftInputFixture("s")))

	raw := readRawMeta(t, dir)
	for _, forbidden := range []string{"title", "summary", "summary_status", "validation_error", "produced_commits", "linked_prs", "linked_issues", "produced_plans"} {
		assert.NotContains(t, raw, forbidden, "draft meta.json must not carry %q", forbidden)
	}
	files, ok := raw["files"].(map[string]any)
	require.True(t, ok, "files must be present as a well-formed empty manifest, not null")
	assert.Empty(t, files)
}

// reflectTypeOfDraftInput is isolated so the reflect import stays confined to
// the one structural test that needs it.
func reflectTypeOfDraftInput() reflect.Type { return reflect.TypeOf(DraftInput{}) }
