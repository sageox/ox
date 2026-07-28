package ledger

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- Sparse checkout integration tests ---

func TestConfigureSparseCheckout_IncludesMurmurPaths(t *testing.T) {
	tempDir := t.TempDir()

	initCmd := exec.Command("git", "init", tempDir)
	if err := initCmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("ConfigureSparseCheckout: %v", err)
	}

	cmd := exec.Command("git", "-C", tempDir, "sparse-checkout", "list")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("sparse-checkout list: %v", err)
	}

	outputStr := string(output)

	// base dirs must still be present
	for _, dir := range []string{".sync", "sessions", "audit"} {
		if !strings.Contains(outputStr, dir) {
			t.Errorf("sparse checkout missing base dir %q", dir)
		}
	}

	// current hour murmur path must be present
	now := time.Now().UTC()
	currentMurmurDir := MurmurDateHourDir(now)
	if !strings.Contains(outputStr, currentMurmurDir) {
		t.Errorf("sparse checkout missing current murmur hour path %q in output:\n%s", currentMurmurDir, outputStr)
	}

	// today's GitHub data path must still be present
	todayGitHub := fmt.Sprintf("data/github/%d/%02d/%02d", now.Year(), now.Month(), now.Day())
	if !strings.Contains(outputStr, todayGitHub) {
		t.Errorf("sparse checkout missing today's GitHub data path %q", todayGitHub)
	}
}

func TestConfigureSparseCheckout_MurmurPathCount(t *testing.T) {
	tempDir := t.TempDir()

	initCmd := exec.Command("git", "init", tempDir)
	if err := initCmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("ConfigureSparseCheckout: %v", err)
	}

	cmd := exec.Command("git", "-C", tempDir, "sparse-checkout", "list")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("sparse-checkout list: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	murmurCount := 0
	for _, line := range lines {
		if strings.Contains(line, "data/murmurs/") {
			murmurCount++
		}
	}

	if murmurCount != DefaultMurmurWindowHours {
		t.Errorf("expected %d murmur paths in sparse checkout, got %d", DefaultMurmurWindowHours, murmurCount)
	}
}

func TestConfigureSparseCheckout_MurmurDoesNotBreakExisting(t *testing.T) {
	// verify that adding murmur paths doesn't remove or corrupt existing
	// sparse checkout entries (GitHub data, base dirs)
	tempDir := t.TempDir()

	initCmd := exec.Command("git", "init", tempDir)
	if err := initCmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("ConfigureSparseCheckout: %v", err)
	}

	cmd := exec.Command("git", "-C", tempDir, "sparse-checkout", "list")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("sparse-checkout list: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")

	githubCount := 0
	for _, line := range lines {
		if strings.Contains(line, "data/github/") {
			githubCount++
		}
	}

	if githubCount != DefaultGitHubDataWindowDays {
		t.Errorf("expected %d GitHub data paths, got %d (murmur integration may have broken GitHub paths)", DefaultGitHubDataWindowDays, githubCount)
	}

	// total should be base dirs + GitHub + murmur. Derived from baseSparseDirs
	// rather than hardcoded: adding a base dir is a legitimate change, and what
	// this assertion actually guards is the windows not clobbering the bases.
	expectedTotal := len(baseSparseDirs) + DefaultGitHubDataWindowDays + DefaultMurmurWindowHours
	if len(lines) != expectedTotal {
		t.Errorf("expected %d total sparse checkout entries (%d base + %d GitHub + %d murmur), got %d",
			expectedTotal, len(baseSparseDirs), DefaultGitHubDataWindowDays, DefaultMurmurWindowHours, len(lines))
	}
}

// Regression test: repeated ConfigureSparseCheckout must not delete untracked
// files under .sageox/cache/. Before the fix, "sparse-checkout init --cone"
// ran on every call and wiped the codedb directory.
func TestConfigureSparseCheckout_IdempotentPreservesSageoxCache(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	if err := exec.Command("git", "init", tempDir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	// First call: initializes sparse checkout
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("first ConfigureSparseCheckout: %v", err)
	}

	// Create a sentinel file simulating the codedb cache
	sentinel := filepath.Join(tempDir, ".sageox", "cache", "codedb", "sentinel.db")
	if err := os.MkdirAll(filepath.Dir(sentinel), 0o755); err != nil {
		t.Fatalf("mkdir sentinel dir: %v", err)
	}
	if err := os.WriteFile(sentinel, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	// Second call: must NOT destroy the sentinel
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("second ConfigureSparseCheckout: %v", err)
	}

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("sentinel under .sageox/cache/codedb/ must survive repeated ConfigureSparseCheckout: %v", err)
	}
}

// Regression test: repeated ConfigureSparseCheckout must preserve ALL categories
// of local files — not just codedb. Users may have pending commits, bleve indexes,
// whisper DBs, or other local state. Before the fix, "sparse-checkout init --cone"
// ran on every 60s sync cycle and wiped everything outside the cone.
func TestConfigureSparseCheckout_PreservesAllLocalFileCategories(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	if err := exec.Command("git", "init", tempDir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	// first call: initializes sparse checkout
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("first ConfigureSparseCheckout: %v", err)
	}

	// create files across every known local-only category
	localFiles := map[string]string{
		// codedb SQLite + bleve indexes (nested)
		".sageox/cache/codedb/index.db":               "sqlite-data",
		".sageox/cache/codedb/bleve/store/segment.vx": "bleve-segment",
		".sageox/cache/codedb/bleve/index_meta.json":  "bleve-meta",
		// whisper cache
		".sageox/cache/whisper/whisper.db": "whisper-data",
		// github sync state
		".sageox/cache/github_sync/state.json": "sync-state",
		// telemetry queue
		".sageox/cache/telemetry.jsonl": "event-data",
		// health metadata
		".sageox/cache/health.json": "health-data",
		// arbitrary user file outside .sageox (simulates pending commit)
		"user-notes.txt": "user-data",
	}

	for relPath, content := range localFiles {
		absPath := filepath.Join(tempDir, relPath)
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", relPath, err)
		}
		if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", relPath, err)
		}
	}

	// simulate 3 scheduler cycles (initial + 2 refreshes)
	for i := 2; i <= 4; i++ {
		if err := ConfigureSparseCheckout(tempDir); err != nil {
			t.Fatalf("ConfigureSparseCheckout call %d: %v", i, err)
		}
	}

	// every file must survive
	for relPath := range localFiles {
		absPath := filepath.Join(tempDir, relPath)
		if _, err := os.Stat(absPath); err != nil {
			t.Errorf("file %q destroyed by repeated ConfigureSparseCheckout: %v", relPath, err)
		}
	}
}

// Verify that sparse-checkout list output is identical whether init --cone runs
// (first call) or is skipped (subsequent calls). Catches regressions where the
// init guard inadvertently changes sparse behavior.
func TestConfigureSparseCheckout_IdempotentSparseSet(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	if err := exec.Command("git", "init", tempDir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	// first call: runs init --cone
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("first ConfigureSparseCheckout: %v", err)
	}

	firstOutput, err := exec.Command("git", "-C", tempDir, "sparse-checkout", "list").Output()
	if err != nil {
		t.Fatalf("sparse-checkout list after first call: %v", err)
	}

	// second call: skips init --cone
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("second ConfigureSparseCheckout: %v", err)
	}

	secondOutput, err := exec.Command("git", "-C", tempDir, "sparse-checkout", "list").Output()
	if err != nil {
		t.Fatalf("sparse-checkout list after second call: %v", err)
	}

	if string(firstOutput) != string(secondOutput) {
		t.Errorf("sparse-checkout list changed between calls:\nfirst:\n%s\nsecond:\n%s",
			string(firstOutput), string(secondOutput))
	}

	// third call: verify stability beyond two calls
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("third ConfigureSparseCheckout: %v", err)
	}

	thirdOutput, err := exec.Command("git", "-C", tempDir, "sparse-checkout", "list").Output()
	if err != nil {
		t.Fatalf("sparse-checkout list after third call: %v", err)
	}

	if string(firstOutput) != string(thirdOutput) {
		t.Errorf("sparse-checkout list drifted after third call:\nfirst:\n%s\nthird:\n%s",
			string(firstOutput), string(thirdOutput))
	}
}

// Verify that .sageox/ is accessible after ConfigureSparseCheckout. The .sageox/
// directory hosts the gitignored cache/ tree; even though it's not in the cone dirs
// list, untracked/gitignored files must remain intact across sparse operations.
func TestConfigureSparseCheckout_SageoxCacheAccessible(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	if err := exec.Command("git", "init", tempDir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("ConfigureSparseCheckout: %v", err)
	}

	// create .sageox/cache/ hierarchy (simulating what daemon creates at runtime)
	cacheDir := filepath.Join(tempDir, ".sageox", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir .sageox/cache: %v", err)
	}

	// verify we can create and read files in the cache directory
	testFile := filepath.Join(cacheDir, "test.json")
	if err := os.WriteFile(testFile, []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatalf("write to .sageox/cache after sparse checkout: %v", err)
	}

	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read from .sageox/cache after sparse checkout: %v", err)
	}
	if string(data) != `{"ok":true}` {
		t.Errorf("unexpected content in .sageox/cache file: %s", string(data))
	}
}

// Regression test: staged files in directories outside the computed cone must
// survive ConfigureSparseCheckout. Without the dirtyDirsOutsideCone guard,
// "sparse-checkout set" removes tracked files outside the cone from the working
// tree — destroying data the CLI has staged but not yet committed.
func TestConfigureSparseCheckout_PreservesStagedFilesOutsideCone(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	if err := exec.Command("git", "init", "-b", "main", tempDir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	// configure git user for commits in test dir
	if err := exec.Command("git", "-C", tempDir, "config", "user.email", "test@test.com").Run(); err != nil {
		t.Fatalf("git config user.email: %v", err)
	}
	if err := exec.Command("git", "-C", tempDir, "config", "user.name", "Test").Run(); err != nil {
		t.Fatalf("git config user.name: %v", err)
	}

	// first call: initialize sparse checkout + create initial commit so HEAD exists
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("first ConfigureSparseCheckout: %v", err)
	}

	// create and commit a file so HEAD is valid (required for staging to work)
	readmePath := filepath.Join(tempDir, "sessions", "README")
	if err := os.MkdirAll(filepath.Dir(readmePath), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.WriteFile(readmePath, []byte("init"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := exec.Command("git", "-C", tempDir, "add", "--sparse", "sessions/README").Run(); err != nil {
		t.Fatalf("git add sessions/README: %v", err)
	}
	if err := exec.Command("git", "-C", tempDir, "commit", "-m", "init").Run(); err != nil {
		t.Fatalf("git commit init: %v", err)
	}

	// simulate CLI staging files in a directory NOT in the cone
	// (e.g., "data/linear/", "data/custom/", or any non-standard import path)
	customDir := filepath.Join(tempDir, "data", "custom")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatalf("mkdir data/custom: %v", err)
	}
	stagedFile := filepath.Join(customDir, "report.csv")
	stagedContent := "user,score\nalice,100\n"
	if err := os.WriteFile(stagedFile, []byte(stagedContent), 0o644); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	if err := exec.Command("git", "-C", tempDir, "add", "--sparse", "data/custom/report.csv").Run(); err != nil {
		t.Fatalf("git add --sparse: %v", err)
	}

	// second ConfigureSparseCheckout (simulating the 60s sync scheduler)
	// this calls "sparse-checkout set" which, without the fix, would delete
	// data/custom/report.csv from disk
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("second ConfigureSparseCheckout: %v", err)
	}

	// the staged file must survive on disk
	content, err := os.ReadFile(stagedFile)
	if err != nil {
		t.Fatalf("staged file data/custom/report.csv destroyed by ConfigureSparseCheckout: %v", err)
	}
	if string(content) != stagedContent {
		t.Errorf("staged file content changed: got %q, want %q", string(content), stagedContent)
	}
}

// Regression test: modified tracked files outside the rolling window must
// survive ConfigureSparseCheckout. A committed file in data/github/2025/01/01/
// (outside the 30-day window) that has local modifications should not be
// deleted from the working tree.
func TestConfigureSparseCheckout_PreservesModifiedFilesOutsideWindow(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	if err := exec.Command("git", "init", "-b", "main", tempDir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := exec.Command("git", "-C", tempDir, "config", "user.email", "test@test.com").Run(); err != nil {
		t.Fatalf("git config user.email: %v", err)
	}
	if err := exec.Command("git", "-C", tempDir, "config", "user.name", "Test").Run(); err != nil {
		t.Fatalf("git config user.name: %v", err)
	}

	// initialize sparse checkout with a wide cone that includes old data
	if err := exec.Command("git", "-C", tempDir, "sparse-checkout", "init", "--cone").Run(); err != nil {
		t.Fatalf("sparse-checkout init: %v", err)
	}
	if err := exec.Command("git", "-C", tempDir, "sparse-checkout", "set", "sessions", "data/github/2025/01/01").Run(); err != nil {
		t.Fatalf("sparse-checkout set: %v", err)
	}

	// create and commit a file in old data path
	oldDataDir := filepath.Join(tempDir, "data", "github", "2025", "01", "01")
	if err := os.MkdirAll(oldDataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	oldFile := filepath.Join(oldDataDir, "prs.json")
	if err := os.WriteFile(oldFile, []byte("original"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	sessDir := filepath.Join(tempDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, ".gitkeep"), []byte(""), 0o644); err != nil {
		t.Fatalf("write .gitkeep: %v", err)
	}
	if err := exec.Command("git", "-C", tempDir, "add", "--sparse", ".").Run(); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := exec.Command("git", "-C", tempDir, "commit", "-m", "init with old data").Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	// modify the old data file (simulates user editing committed data)
	modifiedContent := "modified-by-user"
	if err := os.WriteFile(oldFile, []byte(modifiedContent), 0o644); err != nil {
		t.Fatalf("modify: %v", err)
	}

	// ConfigureSparseCheckout won't include data/github/2025/01/01 (outside 30-day window),
	// but the modified file should be protected by dirtyDirsOutsideCone
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("ConfigureSparseCheckout: %v", err)
	}

	content, err := os.ReadFile(oldFile)
	if err != nil {
		t.Fatalf("modified file destroyed by ConfigureSparseCheckout: %v", err)
	}
	if string(content) != modifiedContent {
		t.Errorf("content changed: got %q, want %q", string(content), modifiedContent)
	}
}

// Integration test: renamed files produce two NUL-separated tokens in
// "git status --porcelain -z" output. The second token (orig path) is a bare
// path without the "XY " status prefix. dirtyDirsOutsideCone must handle both
// tokens correctly, protecting both source and destination directories.
func TestConfigureSparseCheckout_PreservesRenamedFilesOutsideCone(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	if err := exec.Command("git", "init", "-b", "main", tempDir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := exec.Command("git", "-C", tempDir, "config", "user.email", "test@test.com").Run(); err != nil {
		t.Fatalf("git config user.email: %v", err)
	}
	if err := exec.Command("git", "-C", tempDir, "config", "user.name", "Test").Run(); err != nil {
		t.Fatalf("git config user.name: %v", err)
	}

	// initialize sparse checkout
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("first ConfigureSparseCheckout: %v", err)
	}

	// create and commit a file in a directory outside the default cone
	origDir := filepath.Join(tempDir, "imports", "batch1")
	if err := os.MkdirAll(origDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	origFile := filepath.Join(origDir, "data.csv")
	origContent := "id,value\n1,hello\n"
	if err := os.WriteFile(origFile, []byte(origContent), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// need sessions dir so sparse-checkout set has something in the cone
	sessDir := filepath.Join(tempDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, ".gitkeep"), []byte(""), 0o644); err != nil {
		t.Fatalf("write .gitkeep: %v", err)
	}

	if err := exec.Command("git", "-C", tempDir, "add", "--sparse", ".").Run(); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := exec.Command("git", "-C", tempDir, "commit", "-m", "init with imports").Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	// rename the file to a different outside-cone directory via git mv
	destDir := filepath.Join(tempDir, "archive", "batch1")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("mkdir dest: %v", err)
	}
	destFile := filepath.Join(destDir, "data.csv")
	mvCmd := exec.Command("git", "-C", tempDir, "mv", "imports/batch1/data.csv", "archive/batch1/data.csv")
	if mvOut, err := mvCmd.CombinedOutput(); err != nil {
		// if git mv fails (e.g. sparse-checkout restrictions), simulate
		// the rename manually: remove from index + add new path
		if err := os.Rename(origFile, destFile); err != nil {
			t.Fatalf("rename file: %v", err)
		}
		if err := exec.Command("git", "-C", tempDir, "rm", "--sparse", "--cached", "imports/batch1/data.csv").Run(); err != nil {
			t.Fatalf("git rm --cached: %v (git mv output: %s)", err, mvOut)
		}
		if err := exec.Command("git", "-C", tempDir, "add", "--sparse", "archive/batch1/data.csv").Run(); err != nil {
			t.Fatalf("git add --sparse after rename: %v (git mv output: %s)", err, mvOut)
		}
	}

	// verify rename is staged
	statusOut, _ := exec.Command("git", "-C", tempDir, "status", "--porcelain", "-z").CombinedOutput()
	statusStr := string(statusOut)
	if !strings.Contains(statusStr, "archive/batch1/data.csv") {
		t.Fatalf("rename not detected in git status: %s", statusStr)
	}

	// reconfigure sparse checkout — must protect both source and dest dirs
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("ConfigureSparseCheckout after rename: %v", err)
	}

	// the renamed file must survive on disk at its new location
	content, err := os.ReadFile(destFile)
	if err != nil {
		t.Fatalf("renamed file destroyed by ConfigureSparseCheckout: %v", err)
	}
	if string(content) != origContent {
		t.Errorf("content changed: got %q, want %q", string(content), origContent)
	}
}

// Regression test: ConfigureSparseCheckout must not wipe .sageox/cache/codedb/
// when .sageox was not previously in the sparse-checkout cone.
//
// Production failure sequence that motivated this test:
//  1. Ledger cloned with sparse-checkout cone not including .sageox/
//  2. Daemon starts, creates .sageox/cache/codedb/bleve/code/store/root.bolt
//  3. File watcher fires, triggers pullChanges → ConfigureSparseCheckout
//  4. "git sparse-checkout set" without .sageox in cone deletes entire .sageox/ dir
//  5. Bleve segment write fails: open .../store/000000000002.zap: no such file or directory
//  6. Index fails, stats never update, CheckFreshness retries → infinite loop
//
// The fix (adding .sageox to the cone in ConfigureSparseCheckout) ensures step 4
// never deletes the cache. This test would have caught the regression before it shipped.
func TestConfigureSparseCheckout_PreservesBleveCacheWhenSageoxNotPreviouslyInCone(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	if err := exec.Command("git", "init", tempDir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	// Set up sparse-checkout WITHOUT .sageox — simulates a ledger cloned before
	// the .sageox cone fix, where the daemon has been running for a while.
	if err := exec.Command("git", "-C", tempDir, "sparse-checkout", "init", "--cone").Run(); err != nil {
		t.Fatalf("sparse-checkout init: %v", err)
	}
	if err := exec.Command("git", "-C", tempDir, "sparse-checkout", "set", ".sync", "sessions", "audit").Run(); err != nil {
		t.Fatalf("sparse-checkout set without .sageox: %v", err)
	}

	// Simulate the bleve store that codedb creates mid-index: the bolt file
	// exists and segment files are being written. This is the exact structure
	// that was getting wiped.
	storeDir := filepath.Join(tempDir, ".sageox", "cache", "codedb", "bleve", "code", "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatalf("create bleve store dir: %v", err)
	}
	boltFile := filepath.Join(storeDir, "root.bolt")
	if err := os.WriteFile(boltFile, []byte("fake-bolt"), 0o644); err != nil {
		t.Fatalf("write root.bolt: %v", err)
	}
	zapFile := filepath.Join(storeDir, "000000000001.zap")
	if err := os.WriteFile(zapFile, []byte("fake-segment"), 0o644); err != nil {
		t.Fatalf("write .zap segment: %v", err)
	}

	// ConfigureSparseCheckout adds .sageox to the cone — this must not delete the store.
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("ConfigureSparseCheckout: %v", err)
	}

	for _, path := range []string{boltFile, zapFile, storeDir} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("bleve store path must survive ConfigureSparseCheckout when .sageox was not previously in cone: %s: %v", path, err)
		}
	}
}

// Regression test: ConfigureSparseCheckout must preserve the bleve store even
// when called repeatedly from the 60s sync scheduler while indexing is active.
// Each call runs "git sparse-checkout set" — the cache must survive all of them.
func TestConfigureSparseCheckout_RepeatedCallsPreserveBleveStore(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	if err := exec.Command("git", "init", tempDir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	storeDir := filepath.Join(tempDir, ".sageox", "cache", "codedb", "bleve", "code", "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatalf("create bleve store dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "root.bolt"), []byte("bolt"), 0o644); err != nil {
		t.Fatalf("write root.bolt: %v", err)
	}

	// Call 10 times — matches the scheduler calling pullChanges every ~60s over ~10 minutes.
	for i := range 10 {
		if err := ConfigureSparseCheckout(tempDir); err != nil {
			t.Fatalf("ConfigureSparseCheckout call %d: %v", i+1, err)
		}
		if _, err := os.Stat(storeDir); err != nil {
			t.Fatalf("bleve store deleted on ConfigureSparseCheckout call %d: %v", i+1, err)
		}
	}
}
