//go:build memprofile

package index

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/stretchr/testify/require"
)

// TestBleveStoredContentCost measures how much of the code Bleve index is the
// stored field content (kept only to produce highlight fragments) vs the
// inverted index needed for matching. Quantifies the win of switching the
// content field to index-only (Store=false, no term vectors).
//
//	go test -tags=memprofile -run TestBleveStoredContentCost -v ./internal/codedb/index/
func TestBleveStoredContentCost(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)
	root := findRepoRoot(cwd)
	if root == "" {
		t.Skip("no repo found")
	}

	var docs []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(p, ".go") && info.Size() < (1<<20) {
			if b, e := os.ReadFile(p); e == nil {
				docs = append(docs, string(b))
			}
		}
		return nil
	})
	t.Logf("corpus: %d .go files", len(docs))

	idxDir := func(t *testing.T, m mapping.IndexMapping) uint64 {
		dir := filepath.Join(t.TempDir(), "idx")
		idx, err := bleve.New(dir, m)
		require.NoError(t, err)
		batch := idx.NewBatch()
		for i, d := range docs {
			require.NoError(t, batch.Index(strconv.Itoa(i), BleveCodeDoc{Content: d}))
			if (i+1)%500 == 0 {
				require.NoError(t, idx.Batch(batch))
				batch = idx.NewBatch()
			}
		}
		require.NoError(t, idx.Batch(batch))
		require.NoError(t, idx.Close())
		return dirSize(dir)
	}

	stored := idxDir(t, bleve.NewIndexMapping())

	m := bleve.NewIndexMapping()
	ft := bleve.NewTextFieldMapping()
	ft.Store = false
	ft.IncludeTermVectors = false
	dm := bleve.NewDocumentMapping()
	dm.AddFieldMappingsAt("content", ft)
	m.DefaultMapping = dm
	indexOnly := idxDir(t, m)

	t.Logf("stored(default)=%.1f MB  index-only(Store=false,noTV)=%.1f MB  saving=%.0f%%",
		mb(stored), mb(indexOnly), 100*(1-float64(indexOnly)/float64(stored)))
}
