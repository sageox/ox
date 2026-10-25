package plan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/lfs"
)

// A large plan.html is stored as an LFS pointer, and only once its bytes are in
// the store: a committed pointer whose blob was never uploaded loses the render
// and makes the remote reject every later push (the GH #810 wedge). The work is
// split in two because the CLI runs the halves on either side of taking the
// Ledger's repo lock: UploadHTML makes the network calls before it, and
// PointerizeHTML swaps the file for its pointer under it. Because the swap goes
// through lfs.WritePointerFile, which requires an lfs.UploadedRef, this path
// cannot mint a pointer for content that was not uploaded.

// UploadHTML uploads page, the plan.html bytes a save writes, when it is above
// the LFS threshold, and returns the proof PointerizeHTML needs. ok is false,
// with no error, when page is small enough to stay plain (a dehydrated clone
// then reads it with no hydration) or client is nil (no store reachable).
func UploadHTML(client *lfs.Client, page []byte) (ref lfs.UploadedRef, ok bool, err error) {
	if int64(len(page)) <= htmlLFSThreshold || client == nil {
		return lfs.UploadedRef{}, false, nil
	}
	if ref, err = lfs.UploadBlob(client, page); err != nil {
		return lfs.UploadedRef{}, false, fmt.Errorf("upload plan.html blob: %w", err)
	}
	return ref, true, nil
}

// PointerizeHTML replaces dir's plan.html with an LFS pointer when the file
// holds exactly the bytes one of uploaded describes, and reports whether it
// did. A missing plan.html, or one holding any other bytes (a pointer
// included), is left as it is. It makes no network calls.
func PointerizeHTML(dir string, uploaded ...lfs.UploadedRef) (bool, error) {
	htmlPath := filepath.Join(dir, planHTMLFile)
	content, err := os.ReadFile(htmlPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read plan.html: %w", err)
	}
	oid := lfs.ComputeOID(content)
	for _, ref := range uploaded {
		if ref.BareOID() != oid {
			continue
		}
		if err := lfs.WritePointerFile(htmlPath, ref); err != nil {
			return false, fmt.Errorf("write plan.html LFS pointer: %w", err)
		}
		return true, nil
	}
	return false, nil
}

// HasLargePlainHTML reports whether dir holds a plan.html that is a plain file
// above the LFS threshold: the state a failed upload leaves behind. Callers that
// commit a plan dir use it to tell "plain by design" apart from "awaiting upload".
func HasLargePlainHTML(dir string) bool {
	htmlPath := filepath.Join(dir, planHTMLFile)
	info, err := os.Stat(htmlPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= htmlLFSThreshold {
		return false
	}
	return !lfs.IsPointerFile(htmlPath)
}
