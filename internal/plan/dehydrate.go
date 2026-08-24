package plan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/lfs"
)

// DehydrateHTML replaces a large, already-written plain plan.html with an LFS
// pointer — but ONLY after uploading its bytes to the store, so a committed
// pointer never references a blob that isn't there (the GH #810 wedge). Returns
// true when it pointerized, false when it left plan.html plain.
//
// It reads the bytes it is about to pointerize straight off disk, NOT from a
// caller-supplied slice. This is load-bearing: Save applies StampHTMLMeta and
// writes the STAMPED bytes to plan.html, so uploading a caller's pre-stamp copy
// would produce an OID that disagrees with the on-disk file — guardPointerOverwrite
// would then refuse the swap, and every real render (all carry a <head>) would
// silently never dehydrate while leaking an orphaned blob per save. Reading the
// file makes the upload OID equal the on-disk OID by construction, so the guard
// always permits the swap.
//
// It is deliberately separate from Save. Save is the no-network storage layer and
// always writes plan.html plain; DehydrateHTML runs in the CLI caller, which owns
// credential resolution and the LFS client. Because it goes through
// lfs.UploadBlob → lfs.WritePointerFile (which requires an lfs.UploadedRef), it is
// structurally impossible for this path to mint a pointer for un-uploaded content.
//
// Behavior by case:
//   - no plan.html, or already a pointer: nothing to do — returns (false, nil).
//   - on-disk file at or below htmlLFSThreshold: stays plain so a dehydrated clone
//     reads it directly with no hydration — returns (false, nil).
//   - client == nil (offline / no ledger remote): stays plain — returns
//     (false, nil). Larger in git, but retrievable and pushable; never lost.
//   - upload fails: returns (false, err). The plain plan.html on disk is untouched
//     (content safe) and no poisoned pointer reaches the remote.
//   - upload succeeds: overwrites plan.html with a pointer to the now-present blob.
func DehydrateHTML(dir string, client *lfs.Client) (bool, error) {
	htmlPath := filepath.Join(dir, planHTMLFile)

	if lfs.IsPointerFile(htmlPath) {
		return false, nil // already dehydrated
	}

	// Read the file FIRST and gate on what we actually read — never on a separate
	// os.Stat. A concurrent Save can rewrite plan.html between a stat and a read,
	// so sizing one snapshot and uploading another risks pointerizing a now-small
	// file (or skipping a now-large one). The uploaded blob's OID must describe the
	// exact bytes we sized.
	content, err := os.ReadFile(htmlPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil // no render on disk — nothing to dehydrate
		}
		return false, fmt.Errorf("read plan.html for dehydration: %w", err)
	}
	if int64(len(content)) <= htmlLFSThreshold {
		return false, nil // small enough to keep plain
	}
	if client == nil {
		return false, nil // no store reachable — stays plain, safe
	}

	uploaded, err := lfs.UploadBlob(client, content)
	if err != nil {
		return false, fmt.Errorf("upload plan.html blob: %w", err)
	}
	if err := lfs.WritePointerFile(htmlPath, uploaded); err != nil {
		return false, fmt.Errorf("write plan.html LFS pointer: %w", err)
	}
	return true, nil
}
