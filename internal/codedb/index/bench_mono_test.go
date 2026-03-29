//go:build bench_mono

package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/codedb/store"
)

// BenchmarkIndexMonoRepo benchmarks indexing a large real-world monorepo.
// Run with: go test -tags=bench_mono -bench=BenchmarkIndexMonoRepo -benchtime=1x -v ./internal/codedb/index/
// Set MONO_REPO_PATH to the repo to index (default: ~/Code/sageox/sageox-mono).
func BenchmarkIndexMonoRepo(b *testing.B) {
	repoPath := os.Getenv("MONO_REPO_PATH")
	if repoPath == "" {
		home, _ := os.UserHomeDir()
		repoPath = filepath.Join(home, "Code", "sageox", "sageox-mono")
	}
	if _, err := os.Stat(repoPath); err != nil {
		b.Skipf("monorepo not found at %s: %v", repoPath, err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		codedbDir := b.TempDir()
		s, err := store.Open(codedbDir)
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()

		err = IndexLocalRepo(context.Background(), s, repoPath, IndexOptions{})

		b.StopTimer()
		if closeErr := s.Close(); closeErr != nil {
			b.Fatal(closeErr)
		}
		if err != nil {
			b.Fatal(err)
		}
	}
}
