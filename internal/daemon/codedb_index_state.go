package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/version"
)

// indexStateFileName lives in the codedb data dir, next to the index it
// describes, so wiping or discarding the index takes the state with it.
const indexStateFileName = "index-state.json"

// codeDBIndexState is what survives a daemon restart about the last index run.
// Before it existed, freshness lived only in memory and every restart re-ran
// the whole pipeline, which on a 100k-commit repo meant hours of rework.
type codeDBIndexState struct {
	// OxVersion is the build that recorded Head and Board. A different build may
	// ship new indexing stages, so it re-runs the pipeline once instead of
	// trusting a fingerprint recorded by code that did less. HistoryPendingRefs is
	// exempt: unfinished backfill is owed regardless of which build owes it.
	OxVersion string `json:"ox_version,omitempty"`
	// Head is the readHeadFingerprint ("ref:commit") indexed before the run began.
	Head string `json:"head"`
	// Board is index.BoardFingerprint for this repo's team board posts.
	Board string `json:"board"`
	// HistoryPendingRefs lists the refs whose older commits a depth-limited pass
	// left unindexed and a backfill pass still owes, sorted and without
	// duplicates.
	//
	// It is per ref, not one flag, because the data dir is shared by every
	// worktree of a repo and a backfill walks only the ref its own worktree has
	// checked out: a worktree on a short branch finishing its backfill says
	// nothing about the long branch another worktree is still owed. A worktree
	// owes a backfill only while its own ref is listed.
	//
	// Known residual, deliberately not solved: a worktree that moves from a
	// pending branch to one whose walk stops at already-indexed commits does not
	// inherit the debt. It stays recorded on the original ref and is paid the
	// next time a worktree has that ref checked out.
	HistoryPendingRefs []string `json:"history_pending_refs,omitempty"`
}

// owes reports whether ref still has history to backfill. An unknown ref
// (empty) owes nothing: with no ref there is no walk to blame it on.
func (s codeDBIndexState) owes(ref string) bool {
	return ref != "" && slices.Contains(s.HistoryPendingRefs, ref)
}

// addPending records debt on refs, keeping the list sorted and unique.
func (s *codeDBIndexState) addPending(refs ...string) {
	s.HistoryPendingRefs = normalizeRefs(append(s.HistoryPendingRefs, refs...))
}

// clearPending removes only refs from the debt; every other ref keeps its own.
func (s *codeDBIndexState) clearPending(refs ...string) {
	s.HistoryPendingRefs = normalizeRefs(slices.DeleteFunc(slices.Clone(s.HistoryPendingRefs), func(ref string) bool {
		return slices.Contains(refs, ref)
	}))
}

// normalizeRefs sorts, dedupes and drops empty names; nil when nothing is left,
// so an empty debt is omitted from the file instead of written as [].
func normalizeRefs(refs []string) []string {
	refs = slices.DeleteFunc(slices.Clone(refs), func(ref string) bool { return ref == "" })
	slices.Sort(refs)
	refs = slices.Compact(refs)
	if len(refs) == 0 {
		return nil
	}
	return refs
}

// refOfFingerprint returns the ref of a readHeadFingerprint ("ref:commit"). It
// is the name the indexer gives the worktree's default ref (go-git's resolved
// HEAD, "refs/heads/main", or "HEAD" when detached), so a ref the indexer
// reports as truncated and the ref a later CheckFreshness derives from HEAD
// compare equal. Git ref names cannot contain ':', so the first one splits it.
func refOfFingerprint(fingerprint string) string {
	ref, _, _ := strings.Cut(fingerprint, ":")
	return ref
}

// truncatedRefs collects the refs a pass reports as depth-truncated. The indexer
// calls back from inside its walk, so access is locked.
type truncatedRefs struct {
	mu   sync.Mutex
	refs []string
}

// add records ref and reports whether it is new this attempt.
func (t *truncatedRefs) add(ref string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if slices.Contains(t.refs, ref) {
		return false
	}
	t.refs = append(t.refs, ref)
	return true
}

func (t *truncatedRefs) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refs = nil
}

func (t *truncatedRefs) list() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.refs)
}

func indexStatePath(dataDir string) string {
	return filepath.Join(dataDir, indexStateFileName)
}

// loadIndexState reads the state file. A missing file is the normal first-run
// case; an unreadable or corrupt one is logged. Both return the zero state,
// which reads as "nothing is known, re-run the pipeline", the safe direction.
func loadIndexState(logger *slog.Logger, dataDir string) codeDBIndexState {
	data, err := os.ReadFile(indexStatePath(dataDir))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logger.Warn("codedb index state unreadable, treating as empty", "data_dir", dataDir, "error", err)
		}
		return codeDBIndexState{}
	}
	var state codeDBIndexState
	if err := json.Unmarshal(data, &state); err != nil {
		logger.Warn("codedb index state corrupt, treating as empty", "data_dir", dataDir, "error", err)
		return codeDBIndexState{}
	}
	return state
}

// saveIndexState replaces the state file atomically (temp file + rename) so a
// reader, or a crash, never sees half a write. Two daemons on one repo share
// the file; the last rename wins and both versions are complete.
func saveIndexState(dataDir string, state codeDBIndexState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode index state: %w", err)
	}
	tmp, err := os.CreateTemp(dataDir, indexStateFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create index state temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write index state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close index state temp file: %w", err)
	}
	if err := os.Rename(tmpPath, indexStatePath(dataDir)); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace index state: %w", err)
	}
	return nil
}

// updateIndexState read-modify-writes the state file. The read happens here,
// immediately before the write, never earlier in the run: another daemon on the
// same data dir may have added a ref's debt meanwhile, and writing back an older
// snapshot would erase it. A failed write is logged and dropped: the state only
// saves work on the next restart, so losing it must never fail the index run it
// describes.
func (m *CodeDBManager) updateIndexState(dataDir string, update func(*codeDBIndexState)) {
	state := loadIndexState(m.logger, dataDir)
	update(&state)
	if err := saveIndexState(dataDir, state); err != nil {
		m.logger.Warn("codedb index state not saved, a restart will re-run the pipeline", "data_dir", dataDir, "error", err)
	}
}

// recordIndexed marks head (the fingerprint taken before the run started) and
// the current board fingerprint as indexed, in memory and on disk, and applies
// the run's changes to the history debt: addPending refs are owed, clearPending
// refs are not (nothing can be backfilled there). Debt on any other ref is left
// as found. An empty head means the HEAD could not be read; it is left
// unrecorded so the next check re-runs instead of trusting nothing.
func (m *CodeDBManager) recordIndexed(dataDir, head string, addPending, clearPending []string) {
	m.mu.Lock()
	if head != "" {
		m.lastIndexedHead = head
	}
	board := m.lastIndexedBoard
	m.mu.Unlock()

	m.updateIndexState(dataDir, func(s *codeDBIndexState) {
		s.OxVersion = version.Full()
		s.Head = head
		s.Board = board
		s.clearPending(clearPending...)
		s.addPending(addPending...)
	})
}

// addHistoryDebt persists that ref's older history is owed, leaving the recorded
// head and board alone: they describe the last completed pipeline, not this one.
func (m *CodeDBManager) addHistoryDebt(dataDir, ref string) {
	m.updateIndexState(dataDir, func(s *codeDBIndexState) { s.addPending(ref) })
}

// clearHistoryDebt records that a backfill walked refs to the end. Other refs'
// debt stays: this worktree's walk proves nothing about them.
func (m *CodeDBManager) clearHistoryDebt(dataDir string, refs []string) {
	m.updateIndexState(dataDir, func(s *codeDBIndexState) { s.clearPending(refs...) })
}

// historyOwedFor reports whether the ref in fingerprint (this worktree's HEAD)
// has a backfill owed. It reads the file on each call instead of caching:
// several daemons share one data dir, and whichever finishes a ref's backfill
// clears it for all of them.
func (m *CodeDBManager) historyOwedFor(fingerprint string) bool {
	return loadIndexState(m.logger, m.resolveSharedDataDir()).owes(refOfFingerprint(fingerprint))
}

// seedFreshnessFromDisk fills lastIndexedHead and lastIndexedBoard from the
// state file the first time it is called, so a restarted daemon skips a
// pipeline the previous one already finished. It reports whether it seeded a
// head. In-memory values win over disk, and it runs once per manager: after
// that, memory is the authority and re-seeding could resurrect a fingerprint
// that a failed board stage or a discard cleared on purpose.
//
// The state is distrusted when:
//   - the index directory is gone (nothing to be fresh about);
//   - a self-heal marker is present (the index was emptied and needs a rebuild
//     that a skipped pipeline would never run);
//   - a different ox build recorded it (see codeDBIndexState.OxVersion).
//
// Sharing is safe across worktrees of one repo: the index holds the repo's
// whole object graph, not one worktree's checkout, so "ref:commit" being
// indexed is true no matter which daemon recorded it. A daemon whose HEAD
// differs just runs one idempotent pass. The per-worktree dirty overlay is the
// exception; CheckFreshness refreshes it explicitly when this seeds.
func (m *CodeDBManager) seedFreshnessFromDisk() bool {
	m.mu.Lock()
	if m.freshnessSeeded {
		m.mu.Unlock()
		return false
	}
	m.freshnessSeeded = true
	m.mu.Unlock()

	dataDir := m.resolveSharedDataDir()
	if _, err := os.Stat(dataDir); err != nil {
		return false
	}
	if markers := store.NeedsReindexMarkers(dataDir); len(markers) > 0 {
		m.logger.Info("codedb index state ignored, self-heal markers present", "markers", markers)
		return false
	}
	state := loadIndexState(m.logger, dataDir)
	if state.Head == "" && state.Board == "" {
		return false
	}
	if state.OxVersion != version.Full() {
		m.logger.Info("codedb index state ignored, recorded by a different ox build", "recorded", state.OxVersion, "running", version.Full())
		return false
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	seededHead := false
	if m.lastIndexedHead == "" && state.Head != "" {
		m.lastIndexedHead = state.Head
		seededHead = true
	}
	if m.lastIndexedBoard == "" {
		m.lastIndexedBoard = state.Board
	}
	if seededHead {
		m.logger.Info("codedb freshness restored from disk", "head", state.Head, "history_pending_refs", strings.Join(state.HistoryPendingRefs, ","))
	}
	return seededHead
}
