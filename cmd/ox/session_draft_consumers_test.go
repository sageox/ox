package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for the CONSUMERS that had to learn about drafts — the ones that used
// to treat "sessions/<name>/meta.json exists" as "this session is real":
// doctor's orphan sweep, lfs.RecoverEmptyTitleMeta, preserveComputedFields,
// loadUploadedKeys, and the session-list merge.
//
// makeXDGCacheSession writes a crashed-but-substantive recording into the XDG
// cache — the shape doctor's orphan sweep exists to recover.
func makeXDGCacheSession(t *testing.T, projectRoot, sessionName string) string {
	t.Helper()
	repoID := getRepoIDOrDefault(projectRoot)
	contextPath := session.GetContextPath(repoID)
	require.NotEmpty(t, contextPath, "XDG context path must resolve for this fixture")

	dir := filepath.Join(contextPath, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(dir, 0755))
	raw := `{"type":"header","metadata":{"version":"1.0","created_at":"2026-01-01T00:00:00Z","agent_id":"OxOrph","session_id":"` +
		draftTestSessionID + `"}}` + "\n" +
		`{"ts":"2026-01-01T00:01:00Z","type":"user","content":"real work that must not be lost"}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(raw), 0644))
	return dir
}

// TestDoctorUploadRetry_DraftDoesNotHideOrphanedSession is the data-loss test.
// Run it first when triaging this feature.
//
// findOrphanedSessions skipped any session whose ledger meta.json existed. A
// draft published at turn 2 makes that true, so a session that later crashed
// AND failed to upload would be skipped forever — and this check runs at
// FixLevelAuto, so the only copy of the transcript would rot in the cache until
// pruned, with no error anywhere.
//
// Red-first check: delete the IsDraft() branch in findOrphanedSessions and the
// first sub-case returns zero orphans.
func TestDoctorUploadRetry_DraftDoesNotHideOrphanedSession(t *testing.T) {
	setTestCfg(t)

	t.Run("draft in ledger: session IS still an orphan", func(t *testing.T) {
		projectRoot, ledgerPath := draftReaperFixture(t)
		const sessionName = "2026-01-01T00-00-testuser-OxOrph"
		makeXDGCacheSession(t, projectRoot, sessionName)
		draftLedgerSession(t, ledgerPath, sessionName)

		orphans, err := findOrphanedSessions(projectRoot, ledgerPath)
		require.NoError(t, err)
		names := orphanNames(orphans)
		assert.Contains(t, names, sessionName,
			"a draft placeholder must not hide a crashed session from recovery")
	})

	t.Run("finalized in ledger: session is NOT an orphan", func(t *testing.T) {
		projectRoot, ledgerPath := draftReaperFixture(t)
		const sessionName = "2026-01-01T00-00-testuser-OxDone"
		makeXDGCacheSession(t, projectRoot, sessionName)
		finalizedLedgerSession(t, ledgerPath, sessionName)

		orphans, err := findOrphanedSessions(projectRoot, ledgerPath)
		require.NoError(t, err)
		assert.NotContains(t, orphanNames(orphans), sessionName,
			"negative control: an uploaded session must still be skipped")
	})

	t.Run("unreadable ledger meta fails safe toward skip", func(t *testing.T) {
		projectRoot, ledgerPath := draftReaperFixture(t)
		const sessionName = "2026-01-01T00-00-testuser-OxCrpt"
		makeXDGCacheSession(t, projectRoot, sessionName)

		dir := filepath.Join(ledgerPath, "sessions", sessionName)
		require.NoError(t, os.MkdirAll(dir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"draft":tr`), 0644))

		orphans, err := findOrphanedSessions(projectRoot, ledgerPath)
		require.NoError(t, err)
		assert.NotContains(t, orphanNames(orphans), sessionName,
			"retrying an upload we cannot classify risks clobbering a finalized session; "+
				"skipping only defers recovery to a human")
	})
}

func orphanNames(orphans []orphanedSession) []string {
	names := make([]string, 0, len(orphans))
	for _, o := range orphans {
		names = append(names, o.SessionName)
	}
	return names
}

// treeHash fingerprints a directory tree (paths + contents) so a test can prove
// a check mutated NOTHING. Asserting only on a check's returned Status is
// theater: a check that rewrote meta.json and returned Passed sails through.
func treeHash(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.Contains(path, string(os.PathSeparator)+".git"+string(os.PathSeparator)) {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	require.NoError(t, err)
	sort.Strings(paths)
	for _, p := range paths {
		body, readErr := os.ReadFile(p)
		require.NoError(t, readErr)
		rel, _ := filepath.Rel(root, p)
		fmt.Fprintf(h, "%s\x00%x\x00", rel, sha256.Sum256(body))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestMetaRepair_NoOpOnDraft.
//
// A draft legitimately has no title. Without a skip, every autofix tick treats
// that as a summarization fault, bumps SummaryAttempts, and at
// MaxSummaryAttempts stamps summary_status="unrecoverable" into a session that
// is still recording. The daemon's finalize is a preserve-unowned-fields RMW,
// so that stamp would then be carried into the FINISHED session — permanently
// marking real work as unsummarizable. It would also dirty the ledger worktree
// mid-recording, which doctor's uncommitted-sessions fix would then commit.
//
// Red-first check: delete the IsDraft() branch in RecoverEmptyTitleMeta and the
// no-mutation assertion fails.
func TestMetaRepair_NoOpOnDraft(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
		SessionName: "2026-01-01T00-00-testuser-OxRep", SessionID: draftTestSessionID,
		CreatedAt: time.Now(), Draft: true, TurnCount: 2, Files: map[string]lfs.FileRef{},
	}))
	before := treeHash(t, dir)

	// Repeat past MaxSummaryAttempts — the bug only manifests after N ticks.
	for i := 0; i < lfs.MaxSummaryAttempts+2; i++ {
		out := lfs.RecoverEmptyTitleMeta(dir, false)
		assert.True(t, out.Skipped, "tick %d: a draft must be skipped, not repaired", i)
		assert.Empty(t, out.Error)
	}

	assert.Equal(t, before, treeHash(t, dir), "repair must not mutate a draft at all")

	meta, err := lfs.ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Zero(t, meta.SummaryAttempts)
	assert.Empty(t, meta.SummaryStatus,
		"a draft must never be stamped unrecoverable — the daemon would carry that into the real session")
	assert.True(t, meta.IsDraft())
}

// TestMetaRepair_StillRepairsNonDraft is the negative control. Without it,
// TestMetaRepair_NoOpOnDraft passes with RecoverEmptyTitleMeta gutted to
// "always skip".
func TestMetaRepair_StillRepairsNonDraft(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
		SessionName: "2026-01-01T00-00-testuser-OxReal", SessionID: draftTestSessionID,
		CreatedAt: time.Now(),
		Files:     map[string]lfs.FileRef{"raw.jsonl": {OID: "sha256:abc", Size: 10}},
	}))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.json"),
		[]byte(`{"title":"Recovered title"}`), 0644))

	out := lfs.RecoverEmptyTitleMeta(dir, false)
	assert.False(t, out.Skipped, "a real session with an empty title must still be repaired")
	assert.True(t, out.RecoveredFromJSON)

	meta, err := lfs.ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, "Recovered title", meta.Title)
}

// TestPreserveComputedFields_RejectsDraftEraSummary.
//
// While meta.draft is true, any summary.json in the directory was authored
// against a ZERO-TURN placeholder — by the SageOx server, or pulled in by a
// finalize-time rebase — so its files_changed and chapters describe nothing.
// Carrying them forward stamps a fabricated file list onto the real summary.
func TestPreserveComputedFields_RejectsDraftEraSummary(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
		SessionName: "s", SessionID: draftTestSessionID, CreatedAt: time.Now(),
		Draft: true, Files: map[string]lfs.FileRef{},
	}))
	summaryPath := filepath.Join(dir, "summary.json")
	require.NoError(t, os.WriteFile(summaryPath,
		[]byte(`{"title":"Zero-turn session","files_changed":[{"path":"SERVER.md"}],"chapters":[{"title":"server chapter"}]}`), 0644))

	incoming := &session.SummarizeResponse{Title: "Real work"}
	preserveComputedFields(summaryPath, incoming)

	assert.Empty(t, incoming.FilesChanged, "draft-era files_changed must not be carried forward")
	assert.Empty(t, incoming.Chapters, "draft-era chapters must not be carried forward")
}

// TestPreserveComputedFields_StillCarriesRealStopTimeSummary is the negative
// control, and it is what stops the fix from degenerating into "delete
// preserveComputedFields" — which would regress the ox-0pxt class where
// legitimately computed fields are lost because raw.jsonl is an LFS stub by the
// time push-summary runs.
func TestPreserveComputedFields_StillCarriesRealStopTimeSummary(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
		SessionName: "s", SessionID: draftTestSessionID, CreatedAt: time.Now(),
		Files: map[string]lfs.FileRef{"raw.jsonl": {OID: "sha256:abc", Size: 10}},
	}))
	summaryPath := filepath.Join(dir, "summary.json")
	require.NoError(t, os.WriteFile(summaryPath,
		[]byte(`{"title":"stop-time","files_changed":[{"path":"real.go"}],"chapters":[{"title":"real chapter"}]}`), 0644))

	incoming := &session.SummarizeResponse{Title: "Regenerated"}
	preserveComputedFields(summaryPath, incoming)

	require.Len(t, incoming.FilesChanged, 1, "a legitimate stop-time summary's computed fields must still be carried forward")
	assert.Equal(t, "real.go", incoming.FilesChanged[0].Path)
	assert.Len(t, incoming.Chapters, 1)
}

// TestLoadUploadedKeys_ExcludesDrafts.
//
// shouldPrune returns false for StatusUploaded. If a draft counted as uploaded,
// the local cache copy of every drafted session would become permanently
// unprunable — including after the session was aborted.
func TestLoadUploadedKeys_ExcludesDrafts(t *testing.T) {
	setTestCfg(t)
	_, ledgerPath := draftReaperFixture(t)

	draftLedgerSession(t, ledgerPath, "2026-01-01T00-00-testuser-OxPrD")
	finalizedLedgerSession(t, ledgerPath, "2026-01-01T00-00-testuser-OxPrF")

	keys, err := loadUploadedKeys(ledgerPath)
	require.NoError(t, err)
	assert.NotContains(t, keys, "2026-01-01T00-00-testuser-OxPrD", "a draft is not an upload")
	assert.Contains(t, keys, "2026-01-01T00-00-testuser-OxPrF", "negative control")
}

// TestMergeSessionSources_NonDraftReplacesDraft.
//
// `ox session list` merges the git-tracked ledger as PRIMARY, ahead of the
// ledger cache where live recordings live. The old first-key-wins merge meant a
// draft row (no title, Recording=false) shadowed its own live recording, so
// every active session rendered as finished. That is the most user-visible
// regression this feature could ship.
func TestMergeSessionSources_NonDraftReplacesDraft(t *testing.T) {
	const name = "2026-01-01T00-00-testuser-OxMrg"

	draftRow := session.SessionInfo{SessionName: name, Draft: true, CreatedAt: time.Now()}
	liveRow := session.SessionInfo{SessionName: name, Recording: true, EntryCount: 17,
		Title: "live work", CreatedAt: time.Now()}

	merged := mergeSessionSources([]session.SessionInfo{draftRow}, []session.SessionInfo{liveRow})
	require.Len(t, merged, 1, "the two rows describe one session and must collapse")
	assert.False(t, merged[0].Draft)
	assert.True(t, merged[0].Recording)
	assert.Equal(t, 17, merged[0].EntryCount)
	assert.Equal(t, "live work", merged[0].Title)

	// A draft must NOT replace a non-draft in the other direction.
	merged2 := mergeSessionSources([]session.SessionInfo{liveRow}, []session.SessionInfo{draftRow})
	require.Len(t, merged2, 1)
	assert.True(t, merged2[0].Recording, "primary non-draft must win over an additional draft")

	// Non-draft vs non-draft keeps the historical primary-wins behavior.
	a := session.SessionInfo{SessionName: name, Title: "primary", CreatedAt: time.Now()}
	b := session.SessionInfo{SessionName: name, Title: "additional", CreatedAt: time.Now()}
	merged3 := mergeSessionSources([]session.SessionInfo{a}, []session.SessionInfo{b})
	require.Len(t, merged3, 1)
	assert.Equal(t, "primary", merged3[0].Title)
}

// TestResolveOrphanSessionID_UsesDraftPreservedID.
//
// The draftPreservedID parameter exists because retrySessionUpload PURGES the
// draft before this runs, so lfs.PreservedSessionID finds nothing — the caller
// has to read the id first and hand it in. Every pre-existing call site passes
// "", so the branch it was added for was never taken by a test.
//
// The case that matters is a recording whose raw.jsonl header predates the
// SessionID field: the purged draft was the ONLY carrier, and losing it mints a
// fresh id that 404s a /c/ link already published in a PR body.
func TestResolveOrphanSessionID_UsesDraftPreservedID(t *testing.T) {
	const draftID = "ses_01950000-0000-7000-8000-0000000000dd"

	t.Run("no meta, no header: the purged draft's id wins over a fresh mint", func(t *testing.T) {
		sessionDir := t.TempDir() // purged: no meta.json
		orphan := orphanedSession{SessionName: "s", Meta: &session.StoreMeta{}}

		got, err := resolveOrphanSessionID(sessionDir, orphan, draftID)
		require.NoError(t, err)
		assert.Equal(t, draftID, got,
			"without this the id rotates and every circulated /c/ link breaks")
	})

	t.Run("the draft id outranks the raw-header id", func(t *testing.T) {
		const headerID = "ses_01950000-0000-7000-8000-0000000000ee"
		sessionDir := t.TempDir()
		orphan := orphanedSession{SessionName: "s", Meta: &session.StoreMeta{SessionID: headerID}}

		got, err := resolveOrphanSessionID(sessionDir, orphan, draftID)
		require.NoError(t, err)
		assert.Equal(t, draftID, got,
			"ResolveSessionID's documented precedence is preserved-beats-start-minted, and a "+
				"draft's meta.json is a PUBLISHED id — it is already on the remote and already "+
				"in circulation via /c/ links. The header id is start-minted. If the two ever "+
				"disagree, the published one is the one teammates and PR bodies point at.")
	})

	t.Run("an on-disk meta still outranks everything", func(t *testing.T) {
		const metaID = "ses_01950000-0000-7000-8000-0000000000ff"
		sessionDir := t.TempDir()
		require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, &lfs.SessionMeta{
			SessionName: "s", SessionID: metaID, CreatedAt: time.Now(),
		}))
		orphan := orphanedSession{SessionName: "s", Meta: &session.StoreMeta{}}

		got, err := resolveOrphanSessionID(sessionDir, orphan, draftID)
		require.NoError(t, err)
		assert.Equal(t, metaID, got, "a surviving meta.json is the highest-precedence carrier")
	})

	t.Run("nothing anywhere still mints exactly one valid id", func(t *testing.T) {
		sessionDir := t.TempDir()
		orphan := orphanedSession{SessionName: "s", Meta: &session.StoreMeta{}}

		got, err := resolveOrphanSessionID(sessionDir, orphan, "")
		require.NoError(t, err)
		assert.True(t, sessionid.IsValidSessionID(got), "a fresh mint must still be well-formed")
	})
}
