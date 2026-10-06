package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	teammateRaw = "sessions/2026-09-17T10-00-tess-OxTTTT/raw.jsonl"
	ownRaw      = "sessions/2026-10-06T11-00-ryan-OxRRRR/raw.jsonl"
	unknownRaw  = "sessions/2026-10-06T12-00-cy-OxUUUU/raw.jsonl"
)

var (
	teammateContent = "{\"role\":\"user\",\"who\":\"teammate\"}\n"
	ownContent      = "{\"role\":\"user\",\"who\":\"own\"}\n"
	unknownContent2 = "{\"role\":\"user\",\"who\":\"nobody knows\"}\n"
)

func writeLedgerFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// newWedgedLedger builds a Ledger one unpushed commit ahead of a real bare remote, the shape #1174
// left on a coworker's machine: the remote holds pointers, the local commit holds hydrated content.
//   - teammate: remote has a pointer; the local commit holds the same bytes (origin pointer repairs it)
//   - own: never pushed; its meta.json carries the OID (manifest repairs it)
//   - unknown (optional): raw content no pointer or manifest names
func newWedgedLedger(t *testing.T, withUnknown bool) (ledger string) {
	t.Helper()
	skipIntegration(t)
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	seed := filepath.Join(base, "seed")
	ledger = filepath.Join(base, "ledger")
	require.NoError(t, os.MkdirAll(remote, 0o755))
	mustRunGit(t, remote, "init", "--bare", "--initial-branch=main")

	require.NoError(t, os.MkdirAll(seed, 0o755))
	mustRunGit(t, seed, "init", "--initial-branch=main")
	mustRunGit(t, seed, "config", "commit.gpgsign", "false")
	teammateRef := lfs.NewFileRef([]byte(teammateContent))
	writeLedgerFile(t, seed, teammateRaw, lfs.FormatPointer(teammateRef.OID, teammateRef.Size))
	writeLedgerFile(t, seed, "sessions/2026-09-17T10-00-tess-OxTTTT/meta.json", `{"title":"teammate"}`+"\n")
	mustRunGit(t, seed, "add", "-A")
	mustRunGit(t, seed, "commit", "-m", "teammate session")
	mustRunGit(t, seed, "remote", "add", "origin", remote)
	mustRunGit(t, seed, "push", "origin", "main")

	mustRunGit(t, base, "clone", remote, ledger)
	mustRunGit(t, ledger, "config", "user.name", "Test")
	mustRunGit(t, ledger, "config", "user.email", "test@example.com")
	mustRunGit(t, ledger, "config", "commit.gpgsign", "false")

	writeLedgerFile(t, ledger, teammateRaw, teammateContent)
	ownRef := lfs.NewFileRef([]byte(ownContent))
	writeLedgerFile(t, ledger, ownRaw, ownContent)
	writeLedgerFile(t, ledger, "sessions/2026-10-06T11-00-ryan-OxRRRR/meta.json",
		`{"title":"own","files":{"raw.jsonl":{"storage":"lfs","oid":"`+ownRef.OID+`","size":`+strconv.FormatInt(ownRef.Size, 10)+`}}}`+"\n")
	if withUnknown {
		writeLedgerFile(t, ledger, unknownRaw, unknownContent2)
		writeLedgerFile(t, ledger, "sessions/2026-10-06T12-00-cy-OxUUUU/meta.json", `{"title":"unknown"}`+"\n")
	}
	mustRunGit(t, ledger, "add", "-A")
	mustRunGit(t, ledger, "commit", "-m", "Update sessions 2026-10-06")
	return ledger
}

func ledgerFile(t *testing.T, ledger, spec string) string {
	t.Helper()
	out, err := runIsolatedGit(t, ledger, "show", spec)
	require.NoError(t, err, out)
	return out + "\n"
}

// TestRestoreUnpushedSessionPointers_WedgedLedger covers the repair for a Ledger that already carries
// hydrated session content in unpushed commits (#1174). Without it, push validation refuses forever and
// the only way out is hand-editing history.
func TestRestoreUnpushedSessionPointers_WedgedLedger(t *testing.T) {
	ctx := context.Background()

	t.Run("repairs what it can, names what it cannot, never rewrites history", func(t *testing.T) {
		ledger := newWedgedLedger(t, true)
		userCommit, _ := runIsolatedGit(t, ledger, "rev-parse", "HEAD")
		require.Error(t, lfs.ValidateUnpushedTip(ctx, ledger, "origin/main"), "the validator must refuse the wedged tip")

		report, err := restoreUnpushedSessionPointers(ctx, ledger, true)

		require.NoError(t, err)
		assert.ElementsMatch(t, []string{teammateRaw, ownRaw}, report.Restored)
		require.Len(t, report.Unrepairable, 1)
		assert.Equal(t, unknownRaw, report.Unrepairable[0].Path)
		assert.True(t, report.Committed)

		subject, _ := runIsolatedGit(t, ledger, "log", "-1", "--format=%s")
		assert.Equal(t, "doctor: restore LFS pointers for 2 session artifacts", subject)
		parent, _ := runIsolatedGit(t, ledger, "rev-parse", "HEAD~1")
		assert.Equal(t, userCommit, parent, "the existing commit must be left exactly as it was")

		teammateRef := lfs.NewFileRef([]byte(teammateContent))
		ownRef := lfs.NewFileRef([]byte(ownContent))
		assert.Equal(t, lfs.FormatPointer(teammateRef.OID, teammateRef.Size), ledgerFile(t, ledger, "HEAD:"+teammateRaw))
		assert.Equal(t, lfs.FormatPointer(ownRef.OID, ownRef.Size), ledgerFile(t, ledger, "HEAD:"+ownRaw))
		assert.Equal(t, unknownContent2, ledgerFile(t, ledger, "HEAD:"+unknownRaw), "the unknown file stays as committed")
		onDisk, err := os.ReadFile(filepath.Join(ledger, filepath.FromSlash(unknownRaw)))
		require.NoError(t, err)
		assert.Equal(t, unknownContent2, string(onDisk), "the unknown file is left untouched on disk")
		cached, err := os.ReadFile(filepath.Join(ledger, ".sageox", "cache", filepath.FromSlash(ownRaw)))
		require.NoError(t, err, "restoring a pointer must keep the hydrated copy in the cache")
		assert.Equal(t, ownContent, string(cached))

		require.Error(t, report.Remaining)
		assert.Contains(t, report.Remaining.Error(), unknownRaw, "the validator error must name the file that still blocks")
	})

	t.Run("a fully repairable ledger passes the push validator and pushes", func(t *testing.T) {
		ledger := newWedgedLedger(t, false)
		require.Error(t, lfs.ValidateUnpushedTip(ctx, ledger, "origin/main"))

		report, err := restoreUnpushedSessionPointers(ctx, ledger, true)

		require.NoError(t, err)
		assert.Empty(t, report.Unrepairable)
		assert.NoError(t, report.Remaining)
		require.NoError(t, lfs.ValidateUnpushedTip(ctx, ledger, "origin/main"))
		mustRunGit(t, ledger, "push", "origin", "main")
		head, _ := runIsolatedGit(t, ledger, "rev-parse", "HEAD")
		remoteHead, _ := runIsolatedGit(t, ledger, "ls-remote", "origin", "refs/heads/main")
		assert.Contains(t, remoteHead, head)
	})

	t.Run("without fix it reports and changes nothing", func(t *testing.T) {
		ledger := newWedgedLedger(t, true)
		before, _ := runIsolatedGit(t, ledger, "rev-parse", "HEAD")

		report, err := restoreUnpushedSessionPointers(ctx, ledger, false)

		require.NoError(t, err)
		assert.ElementsMatch(t, []string{teammateRaw, ownRaw, unknownRaw}, report.Raw)
		assert.False(t, report.Committed)
		after, _ := runIsolatedGit(t, ledger, "rev-parse", "HEAD")
		assert.Equal(t, before, after)
	})

	t.Run("a clean ledger is a no-op", func(t *testing.T) {
		ledger := newWedgedLedger(t, false)
		mustRunGit(t, ledger, "reset", "--hard", "origin/main")

		report, err := restoreUnpushedSessionPointers(ctx, ledger, true)

		require.NoError(t, err)
		assert.Empty(t, report.Raw)
		assert.False(t, report.Committed)
	})
}
