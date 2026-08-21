// Package read is the policy layer of the ox conversation family: it turns
// "an id and a repo" into windowed, envelope-ready data. It owns active-team
// resolution, INDEX.json-primary id→folder lookup, the single path-join
// guard, disclosure windows, and envelope assembly (plan of record, step 4).
//
// The package reads what is on disk and never pulls (D14): the daemon owns
// sync, last_sync is surfaced from local team-context state, and the whole
// path works logged out. Team-context content is untrusted (customer
// writable): every id is strictly validated and every folder path passes the
// join guard before it touches the filesystem.
package read

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/conversation/format"
)

// DiscussionsDirName is the conversations subtree of a team-context checkout.
const DiscussionsDirName = "discussions"

// FolderResolver is the fallback seam for index misses (D3): when the
// server's resolve endpoint learns to return the storage location, a client
// implementing this interface plugs in here and an index miss falls back to
// it (the only place auth + network are acceptable on the read path).
// Deliberately unimplemented in v1 — until then a miss stays a typed
// not_indexed error, never a local folder scan.
type FolderResolver interface {
	// ResolveFolder maps a rec_ recording id to its discussion folder name.
	// The returned name is untrusted and passes the same path guard as an
	// INDEX.json entry.
	ResolveFolder(recordingID string) (folder string, err error)
}

// Reader serves the five conversation queries against one team's
// discussions/ root (D18: single-team by construction — one root per
// instance, no cross-team fallthrough).
type Reader struct {
	discussionsRoot string
	lastSync        time.Time
	fallback        FolderResolver
	now             func() time.Time
}

// Open resolves the repo's active team context via the canonical helpers
// (config.FindRepoTeamContext, which consults endpoint.GetForProject — never
// a filesystem scan) and returns a Reader over its discussions/ root. When
// no local checkout is resolvable — ephemeral mode, pre-first-sync, or an
// uninitialized repo — the typed no_team_context error is returned (D14).
func Open(projectRoot string) (*Reader, *Error) {
	tc := config.FindRepoTeamContext(projectRoot)
	if tc == nil || tc.Path == "" {
		return nil, newError(ErrCodeNoTeamContext,
			"no local team context for this repo (ephemeral mode, or the daemon has not synced yet); conversation reads need a synced team-context checkout")
	}
	return New(filepath.Join(tc.Path, DiscussionsDirName), tc.LastSync), nil
}

// New builds a Reader directly over a discussions root. Open is the normal
// entry point; New exists for tests and harnesses that stage a root
// themselves.
func New(discussionsRoot string, lastSync time.Time) *Reader {
	return &Reader{
		discussionsRoot: discussionsRoot,
		lastSync:        lastSync,
		now:             time.Now,
	}
}

// SetFallback installs the future index-miss resolver (D3 seam). No-op
// architecture hook in v1: nothing in the ox tree implements FolderResolver
// yet.
func (r *Reader) SetFallback(f FolderResolver) { r.fallback = f }

// row is one live, guard-validated index entry with the derived fields the
// real INDEX.json lacks (recorded_at, has_distillation).
type row struct {
	entry format.IndexEntry
	// recordedAt is derived — UUIDv7 timestamp in recording_id first, index
	// recorded_at next, folder-name date last; zero when nothing yields.
	recordedAt time.Time
	// hasDistillation is stat-ed during the same existence pass that drops
	// phantom entries — no extra enumeration.
	hasDistillation bool
}

// openDiscussionsRoot opens the trusted discussions root once per query.
// Every per-discussion validation and read for that query derives from the
// returned root (Root.Lstat, Root.OpenRoot, root-relative reads) — a folder
// is never re-opened by absolute path after validation, so a folder swapped
// for a symlink cannot redirect a later open outside the root (TOCTOU). A
// missing root is (nil, nil): no discussions tree on disk yet is data, not an
// error.
func (r *Reader) openDiscussionsRoot() (*os.Root, *Error) {
	root, err := os.OpenRoot(r.discussionsRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, newError(ErrCodeReadError, fmt.Sprintf("open discussions root: %v", err))
	}
	return root, nil
}

// loadRows reads INDEX.json through the held discussions root (D1: the
// primary source — no folder enumeration) and returns the live rows: every
// entry passes the name guard and a no-follow folder existence check against
// the same root descriptor; phantom entries (folder deleted after indexing),
// symlinked entries, and guard-rejected entries are dropped. Any other
// filesystem failure — permissions, I/O — is a retryable read_error, never
// conflated with absence. totalIndexed counts the parseable index entries
// before the drop, so callers can report index size honestly. A nil root
// (no discussions tree on disk) has no entries at all.
func (r *Reader) loadRows(root *os.Root) (rows []row, totalIndexed int, err *Error) {
	if root == nil {
		return nil, 0, nil
	}
	entries, _, loadErr := format.LoadIndexIn(root)
	if loadErr != nil {
		return nil, 0, newError(ErrCodeReadError, fmt.Sprintf("load %s: %v", format.IndexFileName, loadErr))
	}
	rows = make([]row, 0, len(entries))
	for _, e := range entries {
		if guardErr := validateFolderName(e.Folder); guardErr != nil {
			continue // hostile or corrupt folder name: never joined, never served
		}
		info, statErr := root.Lstat(e.Folder)
		switch {
		case errors.Is(statErr, fs.ErrNotExist):
			continue // phantom entry (folder deleted after indexing)
		case statErr != nil:
			// Permission or I/O failure is not absence: surface it retryable
			// instead of silently reporting the conversation as unindexed.
			return nil, 0, newError(ErrCodeReadError, fmt.Sprintf("inspect discussion folder %q: %v", truncateID(e.Folder), statErr))
		case !info.IsDir():
			continue // symlinked entry (Lstat reports the link itself) or stray file
		}
		rows = append(rows, row{
			entry:           e,
			recordedAt:      deriveRecordedAt(e),
			hasDistillation: statHasDistillation(root, e.Folder),
		})
	}
	return rows, len(entries), nil
}

// lookup resolves a normalized rec_ id to its live row (D16: INDEX.json keys
// by recording_id) plus an open handle on its discussion folder. The handle
// is derived from the same discussions-root *os.Root the row was validated
// against (never re-opened by absolute path — the TOCTOU guard); the caller
// owns it and must Close it. A miss consults the fallback seam when
// installed, then hard-fails with the typed not_indexed error (D3) — clear
// copy, no local scan crutch.
func (r *Reader) lookup(recordingID string) (row, *os.Root, *Error) {
	root, rootErr := r.openDiscussionsRoot()
	if rootErr != nil {
		return row{}, nil, rootErr
	}
	if root != nil {
		defer root.Close()
	}
	rows, _, err := r.loadRows(root)
	if err != nil {
		return row{}, nil, err
	}
	for _, rw := range rows {
		if rw.entry.RecordingID != recordingID {
			continue
		}
		droot, derr := openDiscussion(root, rw.entry.Folder)
		if derr != nil {
			return row{}, nil, derr
		}
		if droot == nil {
			break // replaced or removed between validation and open: phantom
		}
		return rw, droot, nil
	}
	if r.fallback != nil && root != nil {
		if folder, fbErr := r.fallback.ResolveFolder(recordingID); fbErr == nil {
			// The returned name is untrusted and passes the same guard and
			// derived open as an INDEX.json entry.
			droot, derr := openDiscussion(root, folder)
			if derr != nil {
				return row{}, nil, derr
			}
			if droot != nil {
				return row{
					entry:           format.IndexEntry{Folder: folder, RecordingID: recordingID},
					recordedAt:      deriveRecordedAt(format.IndexEntry{RecordingID: recordingID}),
					hasDistillation: statHasDistillation(root, folder),
				}, droot, nil
			}
		}
	}
	return row{}, nil, newError(ErrCodeNotIndexed,
		fmt.Sprintf("%s is not indexed yet in this team's %s; the index is written when summarization completes — try again after the next sync", recordingID, format.IndexFileName))
}

// statHasDistillation checks distillation/distillation.jsonl under a guarded
// folder, through the discussions root (derived list field — the real index
// has no has_distillation). Root.Stat resolves every component no-follow
// against the root descriptor, so a symlinked path can never point the probe
// outside the discussions tree.
func statHasDistillation(root *os.Root, folder string) bool {
	info, err := root.Stat(filepath.Join(folder, format.DistillationDirName, format.DistillationFileName))
	return err == nil && info.Mode().IsRegular()
}

// deriveRecordedAt derives the recorded_at instant the real INDEX.json lacks:
// the UUIDv7 timestamp embedded in recording_id first, an explicit index
// recorded_at next (some fixture-era indexes carry one), the folder-name
// date prefix last. Zero when nothing yields — callers treat that as
// unknown, not 1970.
func deriveRecordedAt(e format.IndexEntry) time.Time {
	if t, ok := uuidv7Time(e.RecordingID); ok {
		return t
	}
	if e.RecordedAt != "" {
		if t, err := time.Parse(time.RFC3339, e.RecordedAt); err == nil {
			return t.UTC()
		}
	}
	if t, ok := folderNameDate(e.Folder); ok {
		return t
	}
	return time.Time{}
}

// uuidv7Time extracts the 48-bit epoch-millisecond timestamp from a strictly
// valid rec_ UUIDv7 recording id. Untrusted input: the id is fully validated
// before the timestamp is trusted.
func uuidv7Time(recordingID string) (time.Time, bool) {
	if len(recordingID) <= len(prefixRecording) || recordingID[:len(prefixRecording)] != prefixRecording {
		return time.Time{}, false
	}
	u := recordingID[len(prefixRecording):]
	if !isUUIDv7(u) {
		return time.Time{}, false
	}
	// First 48 bits = unix milliseconds: hex digits 0..7 and 9..12
	// ("xxxxxxxx-xxxx-...").
	var ms int64
	for _, c := range []byte(u[:8] + u[9:13]) {
		ms = ms<<4 | int64(hexVal(c))
	}
	return time.UnixMilli(ms).UTC(), true
}

func hexVal(c byte) int {
	if c >= 'a' {
		return int(c-'a') + 10
	}
	return int(c - '0')
}

// folderNameDate parses the conventional "2006-01-02-15-04" prefix of a
// discussion folder name.
func folderNameDate(folder string) (time.Time, bool) {
	const layout = "2006-01-02-15-04"
	if len(folder) < len(layout) {
		return time.Time{}, false
	}
	t, err := time.Parse(layout, folder[:len(layout)])
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}
