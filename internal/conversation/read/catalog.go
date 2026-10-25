package read

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"time"

	"github.com/sageox/ox/internal/conversation/format"
)

// catalogEntry is one summarized conversation found by walking the
// discussions root rather than by reading INDEX.json.
//
// Why a walk exists next to the INDEX.json-primary read path (D1): INDEX.json
// is a single server-maintained file that is rewritten on every summary and
// restarted from empty whenever the server cannot parse it, so on a real team
// it covers a recent window while summary.json sits in every older folder
// (SageOx Internal, 2026-09-30: 156 index rows, 607 summarized folders back to
// 2026-02). A search that silently skips three quarters of the team's history
// answers "what did we say about X a month ago" wrong. The walk keeps the same
// trust bar the index has: only folders whose summarization completed (a
// summary.json with a valid recording id) are served.
type catalogEntry struct {
	folder     string
	id         *ID
	recordedAt time.Time
	summary    *format.SearchSummary
}

// maxCatalogFolders bounds the walk. A team context far past this is a
// signal to move search server-side, not a reason to read without limit.
const maxCatalogFolders = 20000

// loadCatalog walks the held discussions root and returns every summarized,
// guard-validated conversation, plus a count of folders skipped as
// unreadable. Each folder is validated by name, probed no-follow, and opened
// through the same root descriptor (openDiscussion), so a symlink committed
// into the customer-writable tree is never followed.
//
// keep, when non-nil, pre-filters on the folder name alone so a date-bounded
// search does not read the summaries of folders it would discard anyway.
func loadCatalog(root *os.Root, keep func(folder string) bool) (entries []catalogEntry, unreadable int, err *Error) {
	if root == nil {
		return nil, 0, nil
	}
	dir, openErr := root.Open(".")
	if openErr != nil {
		return nil, 0, newError(ErrCodeReadError, fmt.Sprintf("open discussions root: %v", openErr))
	}
	dirents, readErr := dir.ReadDir(maxCatalogFolders)
	dir.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) { // EOF: an empty root
		return nil, 0, newError(ErrCodeReadError, fmt.Sprintf("list discussions root: %v", readErr))
	}
	// File.ReadDir returns directory order; sort so results are stable.
	sort.Slice(dirents, func(i, j int) bool { return dirents[i].Name() < dirents[j].Name() })
	for _, de := range dirents {
		if !de.IsDir() { // ReadDir reports symlinks as symlinks, never as dirs
			continue
		}
		folder := de.Name()
		if keep != nil && !keep(folder) {
			continue
		}
		droot, derr := openDiscussion(root, folder)
		if derr != nil {
			unreadable++
			continue
		}
		if droot == nil {
			continue
		}
		summary, sErr := format.LoadSearchSummaryIn(droot)
		droot.Close()
		switch {
		case sErr != nil:
			unreadable++
			continue
		case summary == nil:
			continue // not summarized yet: the same bar INDEX.json applies
		}
		// summary.json is customer-writable: accept only a strict rec_
		// UUIDv7, never the links and URIs ParseID also understands.
		rid := summary.RecordingID
		if len(rid) <= len(prefixRecording) || rid[:len(prefixRecording)] != prefixRecording || !isUUIDv7(rid[len(prefixRecording):]) {
			unreadable++
			continue
		}
		id, idErr := ParseID(rid)
		if idErr != nil {
			unreadable++
			continue
		}
		entries = append(entries, catalogEntry{
			folder:     folder,
			id:         id,
			recordedAt: deriveRecordedAt(format.IndexEntry{RecordingID: id.RecordingID, Folder: folder}),
			summary:    summary,
		})
	}
	return entries, unreadable, nil
}

// summarizedFolderResolver is the FolderResolver (D3 seam) that answers an
// INDEX.json miss from the catalog walk. Without it, a conversation that
// search found — anything older than the index window — would come back
// not_indexed the moment an AI coworker tried to read its transcript, and the
// search would be a dead end. It resolves only summarized folders, the same
// bar the index itself applies.
type summarizedFolderResolver struct {
	discussionsRoot string
}

func (s summarizedFolderResolver) ResolveFolder(recordingID string) (string, error) {
	root, err := os.OpenRoot(s.discussionsRoot)
	if err != nil {
		return "", err
	}
	defer root.Close()
	entries, _, cErr := loadCatalog(root, nil)
	if cErr != nil {
		return "", cErr
	}
	for _, e := range entries {
		if e.id.RecordingID == recordingID {
			return e.folder, nil
		}
	}
	return "", fs.ErrNotExist
}
