package lfs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUploadBlob_AlreadyPresentYieldsUsableRef: when the server reports a blob is
// already in the store (batch response carries no upload action), UploadBlob must
// still return a usable UploadedRef — "already present" is a successful upload
// outcome, which is all UploadedRef attests.
func TestUploadBlob_AlreadyPresentYieldsUsableRef(t *testing.T) {
	content := []byte("already in the store")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Objects []struct {
				OID  string `json:"oid"`
				Size int64  `json:"size"`
			} `json:"objects"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		// respond with objects and NO actions → server says they already exist.
		type obj struct {
			OID  string `json:"oid"`
			Size int64  `json:"size"`
		}
		resp := struct {
			Transfer string `json:"transfer"`
			Objects  []obj  `json:"objects"`
		}{Transfer: "basic"}
		for _, o := range req.Objects {
			resp.Objects = append(resp.Objects, obj{OID: o.OID, Size: o.Size})
		}
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	ref, err := UploadBlob(NewClient(srv.URL, "oauth2", "token"), content)
	require.NoError(t, err, "already-present blob must be treated as a successful upload")
	require.Equal(t, ComputeOID(content), ref.BareOID())
}

// TestNoAssertUploadedOfFreshFileRef closes the one residual hole in the
// UploadedRef guarantee. The type makes the proof field unforgeable OUTSIDE this
// package, but AssertUploaded is exported, so `AssertUploaded(NewFileRef(x))`
// re-expresses GH #810 (a pointer for content that was never uploaded) while
// type-checking. Every legitimate AssertUploaded site wraps a ref whose blob a
// PRIOR upload already stored; NONE should wrap a freshly-computed NewFileRef.
//
// This scans production source (non-test) for that adjacency and fails if it
// reappears — the enforcement backstop the compile-time type can't provide.
func TestNoAssertUploadedOfFreshFileRef(t *testing.T) {
	root := repoRootForTest(t)
	// AssertUploaded( ... NewFileRef(  — allowing whitespace and an optional lfs. qualifier.
	bad := regexp.MustCompile(`AssertUploaded\(\s*(lfs\.)?NewFileRef\(`)

	var offenders []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		// Propagate traversal errors — an unwalkable subtree could hide a
		// prohibited call, so failing loud beats scanning a partial tree.
		if err != nil {
			return fmt.Errorf("walk %s: %w", path, err)
		}
		if d.IsDir() {
			if name := d.Name(); name == "vendor" || name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			// An unreadable production file must fail the scan, not pass it
			// vacuously — it could contain the very call this test forbids.
			return fmt.Errorf("read %s: %w", path, rerr)
		}
		if bad.Match(data) {
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, rel)
		}
		return nil
	})
	require.NoError(t, err)
	require.Empty(t, offenders,
		"AssertUploaded(NewFileRef(...)) mints pointer proof for un-uploaded content — GH #810. "+
			"Upload via UploadBlob/UploadSessionFiles instead: %v", offenders)
}

func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from test dir")
		}
		dir = parent
	}
}
