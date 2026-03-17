package search

import (
	"context"
	"fmt"
	"testing"

	"github.com/sageox/ox/internal/codedb/store"
)

// BenchmarkExecutePlanSQL measures the full SQL execution path including
// per-row allocation, column mapping, and result construction.
func BenchmarkExecutePlanSQL(b *testing.B) {
	s := openBenchStore(b)
	seedBenchData(b, s, 500) // 500 commits

	q, err := ParseQuery("type:commit author:alice")
	if err != nil {
		b.Fatal(err)
	}
	plan, err := Plan(q)
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results, err := executePlanSQL(ctx, s, plan)
		if err != nil {
			b.Fatal(err)
		}
		if len(results) == 0 {
			b.Fatal("no results")
		}
	}
}

// openBenchStore creates a temporary Store for benchmarking.
func openBenchStore(b *testing.B) *store.Store {
	b.Helper()
	s, err := store.Open(b.TempDir())
	if err != nil {
		b.Fatalf("open bench store: %v", err)
	}
	b.Cleanup(func() { s.Close() })
	return s
}

// seedBenchData inserts n commits with alternating authors.
func seedBenchData(b *testing.B, s *store.Store, n int) {
	b.Helper()
	if _, err := s.Exec(`INSERT INTO repos (id, name, path) VALUES (1, 'bench/repo', '/tmp/bench')`); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < n; i++ {
		author := "alice"
		if i%2 == 1 {
			author = "bob"
		}
		hash := fmt.Sprintf("%040x", i)
		msg := fmt.Sprintf("commit message %d with some text", i)
		if _, err := s.Exec(
			`INSERT INTO commits (repo_id, hash, author, message, timestamp) VALUES (1, ?, ?, ?, ?)`,
			hash, author, msg, 1700000000+i,
		); err != nil {
			b.Fatal(err)
		}
	}
}
