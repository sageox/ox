package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/ledger"
	"github.com/stretchr/testify/require"
)

// makeOutboxMurmur builds a MurmurPayload whose RelPath/MurmurJSON match what the
// CLI would queue, stamped at ts.
func makeOutboxMurmur(t *testing.T, targetDir, content string, ts time.Time) MurmurPayload {
	t.Helper()
	id, err := uuid.NewV7()
	require.NoError(t, err)
	mf := ledger.MurmurFile{
		SchemaVersion: "1",
		ID:            id.String(),
		Timestamp:     ts,
		Topic:         "wip",
		Importance:    "normal",
		Content:       content,
	}
	raw, err := json.Marshal(mf)
	require.NoError(t, err)
	return MurmurPayload{
		TargetDir:  targetDir,
		RelPath:    ledger.MurmurFilePath(ts, id.String()),
		Content:    content,
		MurmurJSON: raw,
	}
}

// TestMurmurOutbox_RoundTrip verifies a queued murmur can be written, read back
// intact, and removed. Failure prevented: a serialization or path bug silently
// drops queued murmurs the daemon was supposed to drain.
func TestMurmurOutbox_RoundTrip(t *testing.T) {
	t.Parallel()
	target := t.TempDir()
	p := makeOutboxMurmur(t, target, "hello outbox", time.Now().UTC())

	require.NoError(t, WriteOutboxMurmur(target, p))

	entries, err := ReadOutboxMurmurs(target)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	got := entries[0].payload
	require.Equal(t, p.RelPath, got.RelPath)
	require.Equal(t, p.Content, got.Content)
	require.JSONEq(t, string(p.MurmurJSON), string(got.MurmurJSON))

	// file lives under the gitignored ledger cache, named "<id>.json"
	require.Equal(t, filepath.Base(p.RelPath), filepath.Base(entries[0].path))
	require.Contains(t, entries[0].path, filepath.Join(".sageox", "cache", "murmur-outbox"))

	require.NoError(t, RemoveOutboxMurmur(entries[0].path))
	entries, err = ReadOutboxMurmurs(target)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// TestMurmurOutbox_ReadMissingDirIsEmpty verifies an absent outbox dir is not an
// error — the common case where the daemon never missed a murmur.
func TestMurmurOutbox_ReadMissingDirIsEmpty(t *testing.T) {
	t.Parallel()
	entries, err := ReadOutboxMurmurs(t.TempDir())
	require.NoError(t, err)
	require.Empty(t, entries)
}

// ledgerWithMurmurDir returns a git repo (with an initial commit) whose
// data/murmurs/ tree exists, registered as a ledger workspace in a fresh
// registry — the minimum a SyncScheduler needs to drain.
func newDrainScheduler(t *testing.T, ledgerDir string) *SyncScheduler {
	t.Helper()
	reg := NewWorkspaceRegistry("", "")
	reg.workspaces["ledger"] = &WorkspaceState{
		ID:     "ledger",
		Type:   WorkspaceTypeLedger,
		Path:   ledgerDir,
		Exists: true,
	}
	return &SyncScheduler{logger: slog.Default(), workspaceRegistry: reg}
}

func gitLog(t *testing.T, dir, pathspec string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "log", "--oneline", "--", pathspec).CombinedOutput()
	require.NoError(t, err, "git log failed: %s", string(out))
	return string(out)
}

// TestDrainMurmurOutbox_CommitsAndRemoves is the core regression test: a murmur
// queued while the daemon was down must be written, committed to data/murmurs/,
// and removed from the outbox once the daemon drains. Failure prevented: the
// original bug where a daemon-down murmur was lost forever.
func TestDrainMurmurOutbox_CommitsAndRemoves(t *testing.T) {
	if testing.Short() {
		t.Skip("short: shells git")
	}
	ledgerDir, _ := initGitRepoWithCommit(t)
	p := makeOutboxMurmur(t, ledgerDir, "fix routing bug", time.Now().UTC())
	require.NoError(t, WriteOutboxMurmur(ledgerDir, p))

	s := newDrainScheduler(t, ledgerDir)
	s.drainMurmurOutbox(context.Background())

	// murmur file written at its partitioned path
	require.FileExists(t, filepath.Join(ledgerDir, p.RelPath))

	// committed, scoped to data/murmurs/
	require.Contains(t, gitLog(t, ledgerDir, "data/murmurs/"), "murmur: fix routing bug")

	// outbox drained
	entries, err := ReadOutboxMurmurs(ledgerDir)
	require.NoError(t, err)
	require.Empty(t, entries, "outbox file must be removed after a confirmed commit")
}

// TestDrainMurmurOutbox_DropsStale verifies murmurs older than the 24h window are
// discarded (deleted, never committed) so day-old WIP never resurfaces.
func TestDrainMurmurOutbox_DropsStale(t *testing.T) {
	if testing.Short() {
		t.Skip("short: shells git")
	}
	ledgerDir, _ := initGitRepoWithCommit(t)
	stale := time.Now().UTC().Add(-time.Duration(ledger.MaxMurmurWindowHours+1) * time.Hour)
	p := makeOutboxMurmur(t, ledgerDir, "ancient murmur", stale)
	require.NoError(t, WriteOutboxMurmur(ledgerDir, p))

	s := newDrainScheduler(t, ledgerDir)
	s.drainMurmurOutbox(context.Background())

	require.NoFileExists(t, filepath.Join(ledgerDir, p.RelPath), "stale murmur must not be committed")
	require.Empty(t, gitLog(t, ledgerDir, "data/murmurs/"), "stale murmur must not produce a commit")
	entries, err := ReadOutboxMurmurs(ledgerDir)
	require.NoError(t, err)
	require.Empty(t, entries, "stale murmur must be dropped from the outbox")
}

// TestDrainMurmurOutbox_IgnoresUnregisteredTarget verifies the drain only touches
// workspaces in the registry allow-list, and uses the registry path (not the
// file's stored TargetDir) as the commit target. Failure prevented: a crafted
// outbox file redirecting a commit outside the workspace.
func TestDrainMurmurOutbox_IgnoresUnregisteredTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("short: shells git")
	}
	ledgerDir, _ := initGitRepoWithCommit(t)
	rogueDir := t.TempDir()

	// Queue under the registered ledger, but point the stored TargetDir at a
	// directory the registry does not know about.
	p := makeOutboxMurmur(t, ledgerDir, "trusted target wins", time.Now().UTC())
	p.TargetDir = rogueDir
	require.NoError(t, WriteOutboxMurmur(ledgerDir, p))

	s := newDrainScheduler(t, ledgerDir)
	s.drainMurmurOutbox(context.Background())

	// committed into the registry's ledger, never the rogue dir
	require.FileExists(t, filepath.Join(ledgerDir, p.RelPath))
	require.NoDirExists(t, filepath.Join(rogueDir, "data", "murmurs"))
}

// TestDrainMurmurOutbox_DropsMalformedRelPath verifies a traversal-escaping
// RelPath is rejected and removed rather than committed.
func TestDrainMurmurOutbox_DropsMalformedRelPath(t *testing.T) {
	if testing.Short() {
		t.Skip("short: shells git")
	}
	ledgerDir, _ := initGitRepoWithCommit(t)
	p := makeOutboxMurmur(t, ledgerDir, "escape attempt", time.Now().UTC())
	p.RelPath = filepath.Join("..", "..", "escape.json")
	require.NoError(t, WriteOutboxMurmur(ledgerDir, p))

	s := newDrainScheduler(t, ledgerDir)
	s.drainMurmurOutbox(context.Background())

	require.Empty(t, strings.TrimSpace(gitLog(t, ledgerDir, "data/murmurs/")))
	entries, err := ReadOutboxMurmurs(ledgerDir)
	require.NoError(t, err)
	require.Empty(t, entries, "malformed murmur must be dropped from the outbox")
}
