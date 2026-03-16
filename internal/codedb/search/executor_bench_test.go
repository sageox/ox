package search

import (
	"context"
	"fmt"
	"strconv"
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

// BenchmarkDedupKeyFmtSprintf measures the current fmt.Sprintf dedup key construction.
func BenchmarkDedupKeyFmtSprintf(b *testing.B) {
	filePath := "internal/codedb/search/executor.go"
	line := 42
	content := "func Execute(ctx context.Context, s *store.Store, query *ParsedQuery) ([]Result, error) {"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = fmt.Sprintf("%s:%d:%s", filePath, line, content)
	}
}

// BenchmarkDedupKeyStrconv measures the optimized string concat dedup key construction.
func BenchmarkDedupKeyStrconv(b *testing.B) {
	filePath := "internal/codedb/search/executor.go"
	line := 42
	content := "func Execute(ctx context.Context, s *store.Store, query *ParsedQuery) ([]Result, error) {"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = filePath + ":" + strconv.Itoa(line) + ":" + content
	}
}

// BenchmarkSscanfInt measures fmt.Sscanf for integer parsing (current code).
func BenchmarkSscanfInt(b *testing.B) {
	val := "12345"
	var n int
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fmt.Sscanf(val, "%d", &n)
	}
}

// BenchmarkStrconvAtoi measures strconv.Atoi for integer parsing (optimized).
func BenchmarkStrconvAtoi(b *testing.B) {
	val := "12345"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n, _ := strconv.Atoi(val)
		_ = n
	}
}

// BenchmarkSscanfFloat measures fmt.Sscanf for float parsing (current code).
func BenchmarkSscanfFloat(b *testing.B) {
	val := "3.14159"
	var f float64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fmt.Sscanf(val, "%f", &f)
	}
}

// BenchmarkStrconvParseFloat measures strconv.ParseFloat for float parsing (optimized).
func BenchmarkStrconvParseFloat(b *testing.B) {
	val := "3.14159"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, _ := strconv.ParseFloat(val, 64)
		_ = f
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
