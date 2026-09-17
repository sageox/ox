package lfs

import (
	"context"
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"sync"

	"github.com/sageox/ox/internal/useragent"
)

type uploadFileSnapshot struct {
	file *os.File
	path string
	info os.FileInfo
	oid  string
}

func openUploadSnapshot(ctx context.Context, path string) (*uploadFileSnapshot, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	snapshot := &uploadFileSnapshot{file: file, path: path}
	ok := false
	defer func() {
		if !ok {
			_ = file.Close()
		}
	}()
	snapshot.info, err = file.Stat()
	if err != nil {
		return nil, err
	}
	if !snapshot.info.Mode().IsRegular() {
		return nil, fmt.Errorf("upload source is not a regular file")
	}
	hash := sha256.New()
	reader := &contextUploadReader{ctx: ctx, reader: io.NewSectionReader(file, 0, snapshot.info.Size())}
	count, err := io.Copy(hash, reader)
	if err != nil {
		return nil, err
	}
	if count != snapshot.info.Size() {
		return nil, fmt.Errorf("upload source truncated while hashing")
	}
	snapshot.oid = fmt.Sprintf("%x", hash.Sum(nil))
	if err := snapshot.checkUnchanged(); err != nil {
		return nil, err
	}
	ok = true
	return snapshot, nil
}

func (s *uploadFileSnapshot) checkUnchanged() error {
	current, err := os.Stat(s.path)
	if err != nil {
		return err
	}
	descriptor, err := s.file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(s.info, current) || !os.SameFile(s.info, descriptor) || current.Size() != s.info.Size() || !current.ModTime().Equal(s.info.ModTime()) || descriptor.Size() != s.info.Size() || !descriptor.ModTime().Equal(s.info.ModTime()) {
		return fmt.Errorf("upload source changed; retry a validated snapshot")
	}
	return nil
}

type contextUploadReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextUploadReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func uploadSnapshot(ctx context.Context, action *Action, snapshot *uploadFileSnapshot) error {
	if err := validateActionHref(action); err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	if err := snapshot.checkUnchanged(); err != nil {
		return err
	}
	body := &uploadDigestReader{reader: io.NewSectionReader(snapshot.file, 0, snapshot.info.Size()), hash: sha256.New()}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, action.Href, body)
	if err != nil {
		return err
	}
	req.ContentLength = snapshot.info.Size()
	req.Header.Set("User-Agent", useragent.String())
	if err := action.setRequestHeaders(req); err != nil {
		return err
	}
	response, err := lfsHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("LFS upload: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Error bodies are diagnostics, never an unbounded second download.
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("LFS upload returned HTTP %d: %s", response.StatusCode, data)
	}
	if body.digest() != snapshot.oid {
		return fmt.Errorf("upload bytes changed or transfer ended before completion")
	}
	return snapshot.checkUnchanged()
}

// A server may send its response before net/http finishes the request body.
// Synchronize the digest check with the transport's reader; early 2xx responses
// must fail verification rather than race the hash or certify partial content.
type uploadDigestReader struct {
	mu     sync.Mutex
	reader io.Reader
	hash   hash.Hash
}

func (r *uploadDigestReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, err := r.reader.Read(p)
	if n > 0 {
		_, _ = r.hash.Write(p[:n])
	}
	return n, err
}
func (r *uploadDigestReader) digest() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return fmt.Sprintf("%x", r.hash.Sum(nil))
}
