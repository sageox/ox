package store

import (
	"io"
	"log/slog"
	"sync"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

// maxReadBlobBytes caps blob reads at the search read path. Files larger than
// this are skipped (snippet falls back to empty); they're rare in source trees
// and reading multi-MB blobs to derive a 120-char snippet is wasteful.
const maxReadBlobBytes = 1 << 20

// blobRepoHandle pairs a go-git Repository with its serializing mutex.
// go-git's packfile reader is NOT safe for concurrent access from the same
// handle, so each search-time blob read must hold the per-repo lock.
type blobRepoHandle struct {
	repo *git.Repository
	mu   sync.Mutex
}

// ReadBlob returns blob content by content_hash by scanning the bare repos
// registered in the `repos` table. Lazily opens repo handles on the first call
// and caches them on the Store. Returns nil if the blob is not found in any
// repo, unreadable, larger than maxReadBlobBytes, or if no repos are
// registered. Safe for concurrent callers (per-repo mutex inside).
//
// Used by the search read path (ADR-018 phase 1b) to re-derive code snippets
// + line numbers from source now that Bleve no longer stores the content.
func (s *Store) ReadBlob(contentHash string) []byte {
	s.blobReposOnce.Do(s.initBlobRepos)
	if len(s.blobRepos) == 0 {
		return nil
	}
	oid := plumbing.NewHash(contentHash)
	for i := range s.blobRepos {
		h := &s.blobRepos[i]
		h.mu.Lock()
		content := readBlobFromRepo(h.repo, oid)
		h.mu.Unlock()
		if content != nil {
			return content
		}
	}
	return nil
}

func readBlobFromRepo(r *git.Repository, oid plumbing.Hash) []byte {
	blobObj, err := r.BlobObject(oid)
	if err != nil {
		return nil
	}
	reader, err := blobObj.Reader()
	if err != nil {
		return nil
	}
	defer func() { _ = reader.Close() }()
	content, err := io.ReadAll(io.LimitReader(reader, maxReadBlobBytes+1))
	if err != nil {
		return nil
	}
	if len(content) > maxReadBlobBytes {
		return nil
	}
	return content
}

// initBlobRepos opens bare-repo handles for every path in the `repos` table.
// Called once, lazily, on first ReadBlob. Failures (missing dir, permission)
// log and skip the repo rather than blocking the search path — an unreadable
// repo means we degrade to empty snippets, not an error.
func (s *Store) initBlobRepos() {
	rows, err := s.db.Query("SELECT path FROM repos")
	if err != nil {
		slog.Warn("blob reader: query repos failed", "err", err)
		return
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			continue
		}
		paths = append(paths, p)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("blob reader: iterate repos failed", "err", err)
	}
	_ = rows.Close()

	for _, p := range paths {
		repo, err := git.PlainOpen(p)
		if err != nil {
			slog.Warn("blob reader: open repo failed", "path", p, "err", err)
			continue
		}
		s.blobRepos = append(s.blobRepos, blobRepoHandle{repo: repo})
	}
}

// closeBlobRepos closes all lazily-opened repo handles. Called from Close.
func (s *Store) closeBlobRepos() {
	for i := range s.blobRepos {
		if err := s.blobRepos[i].repo.Close(); err != nil {
			slog.Warn("blob reader: close repo failed", "err", err)
		}
	}
	s.blobRepos = nil
}
