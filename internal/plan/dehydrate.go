package plan

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/sageox/ox/internal/fileutil"
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
//
// An upload only proves the store held the blob at that moment. GitLab
// housekeeping deletes every LFS object no pushed ref points at (every 200
// pushes, no grace period), and the commit carrying the pointer can sit
// unpushed for days behind a busy or wedged Ledger. So PointerizeHTML also keeps
// the exact page in the Ledger recovery cache, the slot the push repair
// (lfs.ReconcileUnpushedPointers) re-uploads a lost blob from. Without that copy
// the swap would leave the page nowhere on this machine, and a pointer nothing
// can satisfy refuses every later push (the 2026-10-24 wedge).

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
// did. The bytes are first kept in the Ledger recovery cache; when they cannot
// be, the plain page is left in place and an error returned, which callers
// treat as a page still awaiting upload. A missing plan.html, or one holding
// any other bytes (a pointer included), is left as it is. It makes no network
// calls.
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
		if err := keepHTMLForRecovery(dir, content); err != nil {
			return false, err
		}
		if err := lfs.WritePointerFile(htmlPath, ref); err != nil {
			return false, fmt.Errorf("write plan.html LFS pointer: %w", err)
		}
		return true, nil
	}
	return false, nil
}

// keepHTMLForRecovery writes page to the recovery cache slot of dir's plan.html.
// A copy of an earlier revision there is replaced: a revision rewrites plan.html
// in place, and only the pointer at the tip ever needs its blob restored,
// because the push repair squashes older ones out of the push.
func keepHTMLForRecovery(dir string, page []byte) error {
	ledger, rel, ok := planHTMLLedgerPath(dir)
	if !ok {
		return fmt.Errorf("plan dir %s is not <ledger>/data/plans/<plan>, so plan.html has no recovery copy and stays plain", dir)
	}
	cachePath := lfs.RecoveryCachePath(ledger, rel)
	if existing, err := os.ReadFile(cachePath); err == nil && bytes.Equal(existing, page) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		return fmt.Errorf("keep plan.html in the Ledger recovery cache: %w", err)
	}
	if err := fileutil.AtomicWriteBytes(cachePath, page, 0o600); err != nil {
		return fmt.Errorf("keep plan.html in the Ledger recovery cache: %w", err)
	}
	return nil
}

// planHTMLLedgerPath splits a plan dir, <ledger>/data/plans/<plan>, into the
// Ledger root and plan.html's ledger-relative path.
func planHTMLLedgerPath(dir string) (ledger, rel string, ok bool) {
	dir = filepath.Clean(dir)
	plans := filepath.Dir(dir)
	data := filepath.Dir(plans)
	name := filepath.Base(dir)
	if filepath.Base(plans) != "plans" || filepath.Base(data) != "data" || name == "." || name == string(filepath.Separator) {
		return "", "", false
	}
	return filepath.Dir(data), path.Join("data", "plans", name, planHTMLFile), true
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
