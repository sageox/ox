package plan

import (
	"fmt"
	"path/filepath"

	"github.com/sageox/ox/internal/lfs"
)

// DehydrateHTML replaces a large, already-written plain plan.html with an LFS
// pointer — but ONLY after uploading its bytes to the store, so a committed
// pointer never references a blob that isn't there (the GH #810 wedge). Returns
// true when it pointerized, false when it left plan.html plain.
//
// It is deliberately separate from Save. Save is the no-network storage layer
// and always writes plan.html plain; DehydrateHTML runs in the CLI caller, which
// owns credential resolution and the LFS client. Because it goes through
// lfs.UploadBlob → lfs.WritePointerFile (which requires an lfs.UploadedRef), it
// is structurally impossible for this path to mint a pointer for un-uploaded
// content.
//
// Behavior by case:
//   - html at or below htmlLFSThreshold: stays plain so a dehydrated clone reads
//     it directly with no hydration — returns (false, nil).
//   - client == nil (offline / no ledger remote): stays plain — returns
//     (false, nil). Larger in git, but retrievable and pushable; never lost.
//   - upload fails: returns (false, err). The plain plan.html Save wrote is still
//     on disk, so the caller leaves it plain and defers — content is safe and the
//     push is not poisoned.
//   - upload succeeds: overwrites plan.html with a pointer to the now-present
//     blob. The OID matches the plain bytes on disk, so guardPointerOverwrite
//     permits the swap — returns (true, nil).
func DehydrateHTML(dir string, html []byte, client *lfs.Client) (bool, error) {
	if int64(len(html)) <= htmlLFSThreshold {
		return false, nil
	}
	if client == nil {
		return false, nil
	}
	uploaded, err := lfs.UploadBlob(client, html)
	if err != nil {
		return false, fmt.Errorf("upload plan.html blob: %w", err)
	}
	if err := lfs.WritePointerFile(filepath.Join(dir, planHTMLFile), uploaded); err != nil {
		return false, fmt.Errorf("write plan.html LFS pointer: %w", err)
	}
	return true, nil
}
