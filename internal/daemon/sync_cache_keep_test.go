package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// sessionCacheFiles is what a recording keeps under the Ledger's local cache:
// the transcript copy (Sacred), the live spool and the stream state.
var sessionCacheFiles = map[string]string{
	"raw.jsonl":         `{"type":"header"}` + "\n" + `{"type":"user","text":"turn one"}` + "\n",
	"spool.jsonl":       `{"type":"user","text":"turn one"}` + "\n",
	"stream-state.json": `{"status":"streaming"}`,
}

const sessionName = "2026-11-18T23-15-person-a-OxAbCd"

func writeSessionCache(t *testing.T, ledgerDir string) string {
	t.Helper()
	dir := filepath.Join(ledgerDir, ".sageox", "cache", "sessions", sessionName)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for name, body := range sessionCacheFiles {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	return dir
}

// requireSessionCacheAt asserts every session file is still at its original
// path with its content, and that no .bak sibling took a copy of the cache
// away with it (a backup is where doctor later deletes things).
func requireSessionCacheAt(t *testing.T, sessionDir, ledgerDir string) {
	t.Helper()
	for name, body := range sessionCacheFiles {
		got, err := os.ReadFile(filepath.Join(sessionDir, name))
		require.NoError(t, err, "%s left its path", name)
		require.Equal(t, body, string(got), name)
	}
	backups, _ := filepath.Glob(ledgerDir + ".bak.*")
	for _, b := range backups {
		require.NoDirExists(t, filepath.Join(b, ".sageox", "cache"), "the moved-aside backup %s holds the session cache", b)
	}
}

// TestCheckout_SelfHealKeepsTheSessionCacheInPlace is the outage BDD's root
// cause: the Ledger never cloned (its host unreachable), recordings filled
// <ledger>/.sageox/cache/sessions/, something else landed in the half-made
// folder, and the next clone retry renamed the WHOLE folder aside, raw.jsonl,
// spool and stream state included. Hooks and the daemon kept writing to the
// old path; live streaming went silent and the recordings sat in a .bak that
// `ox doctor --fix` deletes.
func TestCheckout_SelfHealKeepsTheSessionCacheInPlace(t *testing.T) {
	parent := t.TempDir()
	ledgerDir := filepath.Join(parent, "repo_01a1f0f0-856a-72b7-8381-59022cddc458")
	sessionDir := writeSessionCache(t, ledgerDir)
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "half-written.txt"), []byte("partial"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerDir, ".sageox", "leftover"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, ".sageox", "leftover", "x"), []byte("x"), 0o644))

	scheduler := NewSyncScheduler(DefaultConfig(), slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	_, err := scheduler.Checkout(CheckoutPayload{RepoPath: ledgerDir, CloneURL: "https://example.com/repo.git", RepoType: "ledger"}, nil)
	require.ErrorContains(t, err, "untrusted git host", "the clone itself fails; only the self-heal is under test")

	requireSessionCacheAt(t, sessionDir, ledgerDir)
	backups, _ := filepath.Glob(ledgerDir + ".bak.*")
	require.Len(t, backups, 1, "what was not cache still moves aside")
	require.FileExists(t, filepath.Join(backups[0], "half-written.txt"))
	require.FileExists(t, filepath.Join(backups[0], ".sageox", "leftover", "x"))
	require.NoFileExists(t, filepath.Join(ledgerDir, "half-written.txt"))
}

// TestDoPull_CorruptLedgerKeepsTheSessionCacheInPlace: a Ledger whose .git
// broke is moved aside for a re-clone; its local cache stays where every
// writer expects it.
func TestDoPull_CorruptLedgerKeepsTheSessionCacheInPlace(t *testing.T) {
	if testing.Short() {
		t.Skip("short: runs git to classify the corrupt repo")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerDir, ".git"), 0o755))
	sessionDir := writeSessionCache(t, ledgerDir)

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	scheduler := NewSyncScheduler(cfg, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	require.NoError(t, scheduler.doPull(context.Background(), nil, true, true))

	requireSessionCacheAt(t, sessionDir, ledgerDir)
	require.NoDirExists(t, filepath.Join(ledgerDir, ".git"), "the corrupt clone still moves aside")
	require.False(t, pathIsGitRepo(ledgerDir))
}

// TestBlueGreenGC_SessionLineWrittenDuringTheRecloneSurvives: GC copied the
// cache before a reclone that can take minutes, then deleted the old clone.
// A transcript line a hook appended in between lived only in the old clone's
// cache and went with it.
func TestBlueGreenGC_SessionLineWrittenDuringTheRecloneSurvives(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Ledger clones")
	}
	bare, clone, scheduler := setupClonedLedger(t)
	sessionDir := writeSessionCache(t, clone)
	late := `{"type":"assistant","text":"written while GC recloned"}` + "\n"
	scheduler.gcSwapWindowTestHook = func() {
		f, err := os.OpenFile(filepath.Join(sessionDir, "raw.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
		require.NoError(t, err)
		_, err = f.WriteString(late)
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}

	ws := WorkspaceState{ID: "ledger", Type: WorkspaceTypeLedger, Path: clone, CloneURL: "file://" + bare, Exists: true}
	require.Equal(t, gcSuccess, scheduler.runBlueGreenGC(context.Background(), ws))

	got, err := os.ReadFile(filepath.Join(sessionDir, "raw.jsonl"))
	require.NoError(t, err)
	require.Equal(t, sessionCacheFiles["raw.jsonl"]+late, string(got), "a line written during the reclone was lost")
	for _, name := range []string{"spool.jsonl", "stream-state.json"} {
		require.FileExists(t, filepath.Join(sessionDir, name))
	}
	require.NoDirExists(t, clone+".old")
}
