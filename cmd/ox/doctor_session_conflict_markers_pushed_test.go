package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Markers already pushed to the shared Ledger (#1258) ---
//
// A teammate's clone committed and pushed meta.json files holding autostash-pop conflict blocks.
// They sit at HEAD on every clone, never appear in @{u}..HEAD, and the daemon skips each session on
// every scan. Every conflict is in summary bookkeeping fields, as on the real Ledgers.

const (
	pushedTie       = "sessions/2026-06-24T09-00-avery-OxAAAA/meta.json" // same state and attempts: the stash side wins
	pushedAttempts  = "sessions/2026-08-02T14-00-sam-OxSSSS/meta.json"   // same state: the upstream side has more attempts
	pushedRecovered = "sessions/2026-09-05T11-00-devon-OxDDDD/meta.json" // a finished summary beats more failed attempts
	pushedNested    = "sessions/2026-09-29T16-00-quinn-OxQQQQ/meta.json" // a second stash pop over markers
	pushedClean     = "sessions/2026-09-30T08-00-riley-OxLLLL/meta.json" // never conflicted
)

var allPushed = []string{pushedTie, pushedAttempts, pushedRecovered, pushedNested} // sorted, as the report lists them

const (
	tieConflict = "{\n  \"session_id\": \"ses_tie\",\n  \"summary_attempts\": 2,\n" +
		"<<<<<<< Updated upstream\n  \"summary_status\": \"failed_validation\",\n  \"validation_error\": \"empty title\"\n" +
		"=======\n  \"summary_status\": \"failed_validation\",\n  \"validation_error\": \"title too short\"\n>>>>>>> Stashed changes\n}\n"
	tieResolved = "{\n  \"session_id\": \"ses_tie\",\n  \"summary_attempts\": 2,\n" +
		"  \"summary_status\": \"failed_validation\",\n  \"validation_error\": \"title too short\"\n}\n"

	attemptsConflict = "{\n  \"session_id\": \"ses_attempts\",\n" +
		"<<<<<<< Updated upstream\n  \"summary_attempts\": 3,\n=======\n  \"summary_attempts\": 1,\n>>>>>>> Stashed changes\n" +
		"  \"summary_status\": \"failed_validation\"\n}\n"
	attemptsResolved = "{\n  \"session_id\": \"ses_attempts\",\n  \"summary_attempts\": 3,\n  \"summary_status\": \"failed_validation\"\n}\n"

	// a repair reset summary_attempts to 0 when it recovered the title; the older failure has 3
	recoveredConflict = "{\n  \"session_id\": \"ses_recovered\",\n" +
		"<<<<<<< Updated upstream\n  \"title\": \"Ledger repair\",\n  \"summary_status\": \"ok\"\n" +
		"=======\n  \"summary_attempts\": 3,\n  \"summary_status\": \"unrecoverable\"\n>>>>>>> Stashed changes\n}\n"
	recoveredResolved = "{\n  \"session_id\": \"ses_recovered\",\n  \"title\": \"Ledger repair\",\n  \"summary_status\": \"ok\"\n}\n"

	nestedConflict = "{\n  \"session_id\": \"ses_nested\",\n" +
		"<<<<<<< Updated upstream\n" +
		"<<<<<<< Updated upstream\n  \"summary_attempts\": 2,\n=======\n  \"summary_attempts\": 1,\n>>>>>>> Stashed changes\n" +
		"  \"summary_status\": \"failed_validation\"\n" +
		"=======\n  \"summary_attempts\": 3,\n  \"summary_status\": \"failed_validation\"\n>>>>>>> Stashed changes\n}\n"
	nestedResolved = "{\n  \"session_id\": \"ses_nested\",\n  \"summary_attempts\": 3,\n  \"summary_status\": \"failed_validation\"\n}\n"

	cleanPushedMeta = "{\n  \"session_id\": \"ses_clean\",\n  \"summary_status\": \"ok\"\n}\n"
)

// newPushedMarkedLedger builds a bare remote that already holds marker-bearing meta.json files, the
// state a swept autostash commit leaves once it is pushed, and returns a clone of it.
func newPushedMarkedLedger(t *testing.T) (remote, ledger string) {
	t.Helper()
	skipIntegration(t)
	base := t.TempDir()
	remote = filepath.Join(base, "remote.git")
	seed := filepath.Join(base, "seed")
	initBareRemote(t, remote, "--initial-branch=main")
	require.NoError(t, os.MkdirAll(seed, 0o755))
	mustRunGit(t, seed, "init", "--initial-branch=main")
	mustRunGit(t, seed, "config", "commit.gpgsign", "false")
	writeLedgerFile(t, seed, pushedTie, tieConflict)
	writeLedgerFile(t, seed, pushedAttempts, attemptsConflict)
	writeLedgerFile(t, seed, pushedRecovered, recoveredConflict)
	writeLedgerFile(t, seed, pushedNested, nestedConflict)
	writeLedgerFile(t, seed, pushedClean, cleanPushedMeta)
	mustRunGit(t, seed, "add", "-A")
	mustRunGit(t, seed, "commit", "-m", "Update sessions 2026-09-29")
	mustRunGit(t, seed, "remote", "add", "origin", remote)
	mustRunGit(t, seed, "push", "origin", "main")
	return remote, cloneLedger(t, remote, filepath.Join(base, "ledger"))
}

func cloneLedger(t *testing.T, remote, dir string) string {
	t.Helper()
	mustRunGit(t, filepath.Dir(dir), "clone", remote, dir)
	mustRunGit(t, dir, "config", "user.name", "Test")
	mustRunGit(t, dir, "config", "user.email", "test@example.com")
	mustRunGit(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

// sessionReadable is the daemon's gate: an error here means finalize skips the session.
func sessionReadable(ledger, metaPath string) error {
	_, _, err := lfs.PreservedSessionIDAndDraft(filepath.Join(ledger, filepath.Dir(filepath.FromSlash(metaPath))))
	return err
}

// Failure prevented: doctor reports a clean Ledger while every teammate's daemon skips these
// sessions forever, because the scan only read unpushed commits.
func TestResolveCommittedConflictMarkers_AlreadyPushed(t *testing.T) {
	ctx := context.Background()
	remote, ledger := newPushedMarkedLedger(t)
	require.ErrorContains(t, sessionReadable(ledger, pushedTie), "conflict markers")

	report, err := resolveCommittedConflictMarkers(ctx, ledger, false)
	require.NoError(t, err)
	assert.Equal(t, allPushed, report.Marked, "markers already on the remote must be found")
	assert.Equal(t, allPushed, report.Pushed)

	markedTip, _ := runIsolatedGit(t, ledger, "rev-parse", "HEAD")
	report, err = resolveCommittedConflictMarkers(ctx, ledger, true)
	require.NoError(t, err)
	assert.Equal(t, allPushed, report.Resolved)
	assert.Empty(t, report.Unrepairable)
	assert.True(t, report.Committed)
	assert.NoError(t, report.Remaining)

	assert.Equal(t, tieResolved, ledgerFile(t, ledger, "HEAD:"+pushedTie), "a tie keeps the local (stash) side")
	assert.Equal(t, attemptsResolved, ledgerFile(t, ledger, "HEAD:"+pushedAttempts), "within one state, more summary_attempts marks the later write")
	assert.Equal(t, recoveredResolved, ledgerFile(t, ledger, "HEAD:"+pushedRecovered), "a recovered summary must not lose to an older failure")
	assert.Equal(t, nestedResolved, ledgerFile(t, ledger, "HEAD:"+pushedNested))
	assert.Equal(t, cleanPushedMeta, ledgerFile(t, ledger, "HEAD:"+pushedClean))

	// the marked commit stays in history, and the repair names every blob it replaced
	parent, _ := runIsolatedGit(t, ledger, "rev-parse", "HEAD~1")
	assert.Equal(t, markedTip, parent)
	body, _ := runIsolatedGit(t, ledger, "log", "-1", "--format=%B")
	for _, path := range allPushed {
		blob, err := runIsolatedGit(t, ledger, "rev-parse", "HEAD~1:"+path)
		require.NoError(t, err)
		assert.Contains(t, body, blob+" "+path)
		replaced, _ := runIsolatedGit(t, ledger, "cat-file", "-p", blob)
		assert.Contains(t, replaced, "<<<<<<< Updated upstream", "the replaced bytes stay recoverable")
	}

	// one teammate's repair heals the team: a fresh clone reads every session
	mustRunGit(t, ledger, "push", "origin", "main")
	fresh := cloneLedger(t, remote, filepath.Join(filepath.Dir(ledger), "fresh"))
	report, err = resolveCommittedConflictMarkers(ctx, fresh, false)
	require.NoError(t, err)
	assert.Empty(t, report.Marked)
	for _, path := range append(allPushed, pushedClean) {
		assert.NoError(t, sessionReadable(fresh, path), path)
	}
}

// Failure prevented: a clone whose sparse checkout left sessions/ out (a manifest-driven checkout
// that stopped at .sageox/) reports every pushed file as an "uncommitted local edit" and repairs
// nothing. A file deleted by hand, which is not sparse-excluded, must still be refused.
func TestResolveCommittedConflictMarkers_SparseExcludedSessions(t *testing.T) {
	ctx := context.Background()
	remote, ledger := newPushedMarkedLedger(t)
	mustRunGit(t, ledger, "sparse-checkout", "set", "--no-cone", "/.sageox/")
	_, err := os.Stat(filepath.Join(ledger, filepath.FromSlash(pushedTie)))
	require.ErrorIs(t, err, os.ErrNotExist, "the fixture must leave sessions/ out of the working tree")

	report, err := resolveCommittedConflictMarkers(ctx, ledger, true)

	require.NoError(t, err)
	assert.Equal(t, allPushed, report.Resolved)
	assert.Empty(t, report.Unrepairable)
	assert.True(t, report.Committed)
	assert.NoError(t, report.Remaining)
	assert.Equal(t, recoveredResolved, ledgerFile(t, ledger, "HEAD:"+pushedRecovered))
	assert.Equal(t, cleanPushedMeta, ledgerFile(t, ledger, "HEAD:"+pushedClean), "an unmarked sparse file stays as committed")

	mustRunGit(t, ledger, "push", "origin", "main")
	fresh := cloneLedger(t, remote, filepath.Join(filepath.Dir(ledger), "fresh"))
	for _, path := range allPushed {
		assert.NoError(t, sessionReadable(fresh, path), path)
	}

	t.Run("a file deleted by hand is still refused", func(t *testing.T) {
		_, deleted := newPushedMarkedLedger(t)
		require.NoError(t, os.Remove(filepath.Join(deleted, filepath.FromSlash(pushedTie))))

		report, err := resolveCommittedConflictMarkers(ctx, deleted, true)

		require.NoError(t, err)
		require.Len(t, report.Unrepairable, 1)
		assert.Equal(t, pushedTie, report.Unrepairable[0].Path)
		assert.Contains(t, report.Unrepairable[0].Reason, "uncommitted local edit")
	})
}

// Failure prevented: two teammates repair the same pushed markers before either sees the other's
// commit, and the second push conflicts or the clones settle on different content.
func TestResolveCommittedConflictMarkers_ConcurrentRepairsConverge(t *testing.T) {
	ctx := context.Background()
	remote, first := newPushedMarkedLedger(t)
	second := cloneLedger(t, remote, filepath.Join(filepath.Dir(first), "second"))

	for _, ledger := range []string{first, second} {
		report, err := resolveCommittedConflictMarkers(ctx, ledger, true)
		require.NoError(t, err)
		require.True(t, report.Committed)
	}
	mustRunGit(t, first, "push", "origin", "main")

	// plain git, with none of the Ledger's sessions/ auto-resolve: identical repairs must merge alone
	out, err := runIsolatedGit(t, second, "pull", "--rebase", "origin", "main")
	require.NoError(t, err, out)
	out, err = runIsolatedGit(t, second, "push", "origin", "main")
	require.NoError(t, err, out)

	firstTree, _ := runIsolatedGit(t, first, "rev-parse", "HEAD^{tree}")
	secondTree, _ := runIsolatedGit(t, second, "rev-parse", "HEAD^{tree}")
	assert.Equal(t, firstTree, secondTree)
	report, err := resolveCommittedConflictMarkers(ctx, second, false)
	require.NoError(t, err)
	assert.Empty(t, report.Marked)
}

// Failure prevented: a report that tells a teammate their pushes are refused when the real cost is
// that the whole team cannot read these sessions.
func TestMarkedSummary(t *testing.T) {
	tests := []struct {
		name   string
		report conflictMarkerReport
		want   string
	}{
		{"unpushed only", conflictMarkerReport{Marked: []string{"a", "b"}},
			"2 session file(s) in unpushed commits hold conflict markers; pushes are refused"},
		{"pushed only", conflictMarkerReport{Marked: []string{"a"}, Pushed: []string{"a"}},
			"1 session file(s) hold conflict markers; 1 already pushed, unreadable on every teammate's clone"},
		{"both", conflictMarkerReport{Marked: []string{"a", "b", "c"}, Pushed: []string{"a"}},
			"3 session file(s) hold conflict markers; 1 already pushed, unreadable on every teammate's clone; 2 unpushed, pushes are refused"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, markedSummary(tt.report))
		})
	}
}

// Failure prevented: a repair that keeps a side the commit validator refuses (wedging the commit),
// or that picks differently on two clones.
func TestPickMetaSide(t *testing.T) {
	const path = "sessions/x/meta.json"
	tests := []struct {
		name            string
		upstream, stash string
		want            string
		ok              bool
	}{
		{"tie keeps the stash side", `{"summary_status":"failed_validation"}`, `{"summary_status":"failed_validation","validation_error":"x"}`,
			`{"summary_status":"failed_validation","validation_error":"x"}`, true},
		{"same state, more upstream attempts wins", `{"summary_attempts":3}`, `{"summary_attempts":1}`, `{"summary_attempts":3}`, true},
		{"same state, more stash attempts wins", `{"summary_attempts":1}`, `{"summary_attempts":2}`, `{"summary_attempts":2}`, true},
		// a title repair resets summary_attempts to 0; the older failure must not win on its count
		{"a finished summary beats more failed attempts", `{"summary_status":"ok","title":"t"}`, `{"summary_status":"unrecoverable","summary_attempts":3}`,
			`{"summary_status":"ok","title":"t"}`, true},
		{"a pre-status summary counts as finished", `{"summary":"done"}`, `{"summary_status":"failed_validation","summary_attempts":2}`,
			`{"summary":"done"}`, true},
		// a re-arm clears status and attempts so the daemon retries; unrecoverable would stop it
		{"a re-armed session beats unrecoverable", `{"summary_status":"unrecoverable","summary_attempts":3}`, `{}`, `{}`, true},
		{"a failed attempt beats unrecoverable", `{"summary_status":"failed_validation","summary_attempts":1}`, `{"summary_status":"unrecoverable","summary_attempts":3}`,
			`{"summary_status":"failed_validation","summary_attempts":1}`, true},
		{"only upstream parses", `{"a":1}`, `{"a":`, `{"a":1}`, true},
		{"only stash parses", `{"a":`, `{"a":2}`, `{"a":2}`, true},
		{"a non-object is not a meta.json", `[1]`, `null`, "", false},
		// the stash side would win the tie, but the daemon cannot read a string counter
		{"the daemon must be able to read the winner", `{"summary_attempts":0}`, `{"summary_attempts":"9"}`, `{"summary_attempts":0}`, true},
		{"an unsafe files entry is refused", `{"a":1}`, `{"files":{"../escape":{}}}`, `{"a":1}`, true},
		{"neither parses", `{`, `}`, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := pickMetaSide(path, []byte(tt.upstream), []byte(tt.stash))
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

// Failure prevented: a nested or diff3 block read wrong, leaving marker lines or the base section in
// a resolved file.
func TestConflictSides(t *testing.T) {
	tests := []struct {
		name            string
		in              string
		upstream, stash string
		ok              bool
	}{
		{"two-way", autostashConflict, "{\n\"title\":\"kept\"\n}\n", "{\n\"title\":\"stash side\"\n}\n", true},
		{"diff3 base belongs to neither side",
			"{\n<<<<<<< Updated upstream\n\"a\":1\n||||||| base\n\"a\":0\n=======\n\"a\":2\n>>>>>>> Stashed changes\n}\n",
			"{\n\"a\":1\n}\n", "{\n\"a\":2\n}\n", true},
		{"nested block resolves with its parent", nestedConflict,
			"{\n  \"session_id\": \"ses_nested\",\n  \"summary_attempts\": 2,\n  \"summary_status\": \"failed_validation\"\n}\n",
			nestedResolved, true},
		{"nested block inside the stash half",
			"<<<<<<< Updated upstream\nA\n=======\n<<<<<<< Updated upstream\nB\n=======\nC\n>>>>>>> Stashed changes\n>>>>>>> Stashed changes\n",
			"A\n", "C\n", true},
		{"separator inside the stash half is content",
			"<<<<<<< Updated upstream\nA\n=======\n=======\n>>>>>>> Stashed changes\n", "A\n", "=======\n", true},
		{"orphaned tail", "{}\n=======\n>>>>>>> Stashed changes\n", "", "", false},
		{"unterminated nested start", "<<<<<<< Updated upstream\n<<<<<<< again\n=======\n>>>>>>> Stashed changes\n", "", "", false},
		{"unterminated block", "<<<<<<< Updated upstream\n{}\n=======\n", "", "", false},
		{"end before separator", "<<<<<<< Updated upstream\n{}\n>>>>>>> Stashed changes\n", "", "", false},
		{"crlf separator", "<<<<<<< Updated upstream\r\nA\r\n=======\r\nB\r\n>>>>>>> Stashed changes\r\n", "A\r\n", "B\r\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream, stash, ok := conflictSides([]byte(tt.in))
			require.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.upstream, string(upstream))
			assert.Equal(t, tt.stash, string(stash))
			assert.False(t, strings.Contains(string(upstream)+string(stash), "<<<<<<<"))
		})
	}
}
