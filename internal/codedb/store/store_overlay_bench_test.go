package store

import (
	"fmt"
	"testing"

	"github.com/blevesearch/bleve/v2"
)

// benchDoc represents a document indexed during benchmarks.
type benchDoc struct {
	Content  string `json:"content"`
	FilePath string `json:"filepath"`
	Language string `json:"language"`
}

// seedIndex populates a Bleve index with n documents containing searchable content.
// prefix differentiates documents across indexes; shared injects a common term
// into every document so cross-index queries can match everywhere.
func seedIndex(b *testing.B, idx bleve.Index, prefix string, n int, shared string) {
	b.Helper()
	for i := 0; i < n; i++ {
		doc := benchDoc{
			Content:  fmt.Sprintf("%s document number %d with %s searchable content", prefix, i, shared),
			FilePath: fmt.Sprintf("src/%s/file_%d.go", prefix, i),
			Language: "go",
		}
		if err := idx.Index(fmt.Sprintf("%s_%d", prefix, i), doc); err != nil {
			b.Fatalf("index doc %s_%d: %v", prefix, i, err)
		}
	}
}

// buildOverlays creates n in-memory Bleve indexes, each with docsPerOverlay documents,
// attaches them to the store, and rebuilds the combined alias.
func buildOverlays(b *testing.B, s *Store, n, docsPerOverlay int) {
	b.Helper()
	for i := 0; i < n; i++ {
		mapping := bleve.NewIndexMapping()
		idx, err := bleve.NewMemOnly(mapping)
		if err != nil {
			b.Fatalf("create overlay %d: %v", i, err)
		}
		prefix := fmt.Sprintf("overlay%d", i)
		seedIndex(b, idx, prefix, docsPerOverlay, "benchmark")
		s.dirtyCodeIndexes[prefix] = idx
	}
	s.rebuildCombinedIndex()
}

// BenchmarkSearchWithOverlays measures full-text search latency as overlay count increases.
// Each overlay has ~100 indexed documents to simulate a realistic dirty worktree.
// This answers: "does N-way IndexAlias merge degrade query performance?"
//
// Run: go test -bench=BenchmarkSearchWithOverlays -benchmem ./internal/codedb/store/...
func BenchmarkSearchWithOverlays(b *testing.B) {
	for _, n := range []int{0, 1, 3, 5, 10} {
		b.Run(fmt.Sprintf("overlays_%d", n), func(b *testing.B) {
			s := openBenchStore(b)

			// seed baseline with 500 docs
			seedIndex(b, s.CodeIndex, "baseline", 500, "benchmark")

			// attach N overlays, each with 100 docs
			buildOverlays(b, s, n, 100)

			// query that matches docs across baseline AND overlays
			q := bleve.NewMatchQuery("benchmark")
			req := bleve.NewSearchRequest(q)
			req.Size = 10

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err := s.CombinedCodeIndex.Search(req)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkSearchWithOverlays_OverlayOnlyHit measures queries that match
// content exclusively in overlay indexes (not baseline). This isolates the
// merge cost when results come only from overlays.
func BenchmarkSearchWithOverlays_OverlayOnlyHit(b *testing.B) {
	for _, n := range []int{1, 3, 5, 10} {
		b.Run(fmt.Sprintf("overlays_%d", n), func(b *testing.B) {
			s := openBenchStore(b)

			// baseline has 500 docs without the overlay-specific term
			seedIndex(b, s.CodeIndex, "baseline", 500, "common")

			// overlays use a unique term not present in baseline
			for i := 0; i < n; i++ {
				mapping := bleve.NewIndexMapping()
				idx, err := bleve.NewMemOnly(mapping)
				if err != nil {
					b.Fatalf("create overlay %d: %v", i, err)
				}
				prefix := fmt.Sprintf("dirty%d", i)
				seedIndex(b, idx, prefix, 100, "overlayonly")
				s.dirtyCodeIndexes[prefix] = idx
			}
			s.rebuildCombinedIndex()

			q := bleve.NewMatchQuery("overlayonly")
			req := bleve.NewSearchRequest(q)
			req.Size = 10

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err := s.CombinedCodeIndex.Search(req)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkSearchBaselineOnly measures search against just the baseline index
// (no overlays). This is the control measurement.
func BenchmarkSearchBaselineOnly(b *testing.B) {
	s := openBenchStore(b)
	seedIndex(b, s.CodeIndex, "baseline", 500, "benchmark")

	q := bleve.NewMatchQuery("benchmark")
	req := bleve.NewSearchRequest(q)
	req.Size = 10

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := s.CodeIndex.Search(req)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSearchOverlayOnly measures search against a single in-memory overlay
// (no baseline). This isolates overlay search cost.
func BenchmarkSearchOverlayOnly(b *testing.B) {
	mapping := bleve.NewIndexMapping()
	idx, err := bleve.NewMemOnly(mapping)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = idx.Close() }()

	seedIndex(b, idx, "overlay", 100, "benchmark")

	q := bleve.NewMatchQuery("benchmark")
	req := bleve.NewSearchRequest(q)
	req.Size = 10

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := idx.Search(req)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// openBenchStore opens a Store in a temp directory for benchmarks.
func openBenchStore(b *testing.B) *Store {
	b.Helper()
	tmp := b.TempDir()
	s, err := Open(tmp)
	if err != nil {
		b.Fatalf("Open(%s): %v", tmp, err)
	}
	b.Cleanup(func() { _ = s.Close() })
	return s
}
