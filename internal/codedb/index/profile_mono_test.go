//go:build bench_mono

package index

import (
	"context"
	"os"
	"path/filepath"
	"runtime/pprof"
	"testing"

	"github.com/sageox/ox/internal/codedb/store"
)

// TestProfileMonoRepo runs a single index of the monorepo with CPU profiling.
// Output: /tmp/codedb-cpu.prof
// View with: go tool pprof -http=:6060 /tmp/codedb-cpu.prof
func TestProfileMonoRepo(t *testing.T) {
	repoPath := os.Getenv("MONO_REPO_PATH")
	if repoPath == "" {
		home, _ := os.UserHomeDir()
		repoPath = filepath.Join(home, "Code", "sageox", "sageox-mono")
	}
	if _, err := os.Stat(repoPath); err != nil {
		t.Skipf("monorepo not found at %s: %v", repoPath, err)
	}

	f, err := os.Create("/tmp/codedb-cpu.prof")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := pprof.StartCPUProfile(f); err != nil {
		t.Fatal(err)
	}

	codedbDir := t.TempDir()
	s, err := store.Open(codedbDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := IndexLocalRepo(context.Background(), s, repoPath, IndexOptions{}); err != nil {
		t.Fatal(err)
	}

	pprof.StopCPUProfile()
	t.Logf("CPU profile written to /tmp/codedb-cpu.prof")
	t.Logf("View: go tool pprof -http=:6060 /tmp/codedb-cpu.prof")
}
