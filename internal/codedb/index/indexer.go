package index

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/blevesearch/bleve/v2"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/utils/merkletrie"

	"github.com/sageox/ox/internal/codedb/comments"
	"github.com/sageox/ox/internal/codedb/language"
	codedbsqlc "github.com/sageox/ox/internal/codedb/sqlc"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/codedb/symbols"
)

// ProgressFunc is called with status messages during indexing.
type ProgressFunc func(msg string)

// IndexOptions configures the indexing process.
type IndexOptions struct {
	MaxHistoryDepth int // 0 = unlimited
	Progress        ProgressFunc
	SkipDirs        map[string]bool // directories to skip in worktree indexing; nil = use defaultSkipDirs
	TreeCacheLimit  int             // max tree snapshots cached during commit walking; 0 = default (128)
}

// defaultTreeCacheLimit is the default max entries in the tree cache.
// Each entry stores a map[string]plumbing.Hash (path → blob OID) for one tree.
// Memory cost per entry ≈ numFiles × 76 bytes (path + hash + map overhead).
// At 128 entries: ~1MB for small repos (<100 files), ~10MB for 1K files, ~95MB for 10K files.
const defaultTreeCacheLimit = 128

// BleveCodeDoc is the document indexed into the code Bleve index.
type BleveCodeDoc struct {
	Content string `json:"content"`
}

// BleveDiffDoc is the document indexed into the diff Bleve index.
type BleveDiffDoc struct {
	Content string `json:"content"`
}

// commitData holds parsed commit information for indexing.
type commitData struct {
	oid       plumbing.Hash
	author    string
	message   string
	timestamp int64
	treeHash  plumbing.Hash
	parentIDs []plumbing.Hash
}

// refInfo holds a resolved ref name and its tip commit hash.
type refInfo struct {
	name   string
	tipOID plumbing.Hash
}

const bleveBatchSize = 500

// checkpointEvery is the number of commits between SQL+Bleve checkpoint flushes.
// Each checkpoint commits the current transaction and starts a new one, making
// recently-indexed commits searchable without waiting for the full index run.
// At 50 commits, first results become searchable within seconds of indexing starting.
const checkpointEvery = 50

// defaultSkipDirs is the default set of directories to skip when indexing a working tree.
// Override via IndexOptions.SkipDirs.
var defaultSkipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	".sageox":      true,
	"vendor":       true,
}

// defaultBranchFallbacks lists ref names to try when HEAD cannot be resolved.
var defaultBranchFallbacks = []string{
	"refs/heads/main",
	"refs/heads/master",
	"refs/remotes/origin/main",
	"refs/remotes/origin/master",
}

// indexState holds mutable state shared across indexing sub-operations.
type indexState struct {
	tx              *sql.Tx
	store           *store.Store
	repo            *git.Repository
	repoID          int64
	codeBatch       *bleve.Batch
	diffBatch       *bleve.Batch
	codeBatchN      int
	diffBatchN      int
	diffIndexFailed bool // set on first diff Bleve write failure; skips further diff writes
	knownCommits    map[string]bool
	treeCache       map[plumbing.Hash]map[string]plumbing.Hash
	treeOrder       []plumbing.Hash // FIFO insertion order for per-entry eviction
	treeCacheLimit  int
	blobIDCache     map[string]int64 // content_hash -> blob DB ID; avoids repeated SQL lookups
	commitIDCache   map[string]int64 // commit hash -> commit DB ID; used in insertParentLinks
	newCommits      int
	newBlobs        int
	report          func(string)
}

// flushCodeBatch commits the code batch if it has reached the threshold.
func (st *indexState) flushCodeBatch(force bool) error {
	if st.codeBatchN == 0 {
		return nil
	}
	if !force && st.codeBatchN < bleveBatchSize {
		return nil
	}
	if err := st.store.CodeIndex.Batch(st.codeBatch); err != nil {
		return fmt.Errorf("flush code batch: %w", err)
	}
	st.codeBatch = st.store.CodeIndex.NewBatch()
	st.codeBatchN = 0
	return nil
}

// flushDiffBatch commits the diff batch if it has reached the threshold.
// Diff index failures are non-fatal: SQL commits/blobs are the source of truth.
// A diff Bleve failure means type:diff searches return incomplete results but
// the core index (commits, blobs, symbols) is unaffected. On first failure the
// run logs a warning and skips further diff writes to avoid cascading errors.
func (st *indexState) flushDiffBatch(force bool) error {
	if st.diffIndexFailed || st.diffBatchN == 0 {
		return nil
	}
	if !force && st.diffBatchN < bleveBatchSize {
		return nil
	}
	if err := st.store.DiffIndex.Batch(st.diffBatch); err != nil {
		st.diffIndexFailed = true
		slog.Warn("codedb: diff index write failed, type:diff search will be incomplete for this run", "err", err)
		st.diffBatch = st.store.DiffIndex.NewBatch()
		st.diffBatchN = 0
		return nil // non-fatal: SQL data is preserved
	}
	st.diffBatch = st.store.DiffIndex.NewBatch()
	st.diffBatchN = 0
	return nil
}

// checkpointFlush commits the current SQL transaction and flushes Bleve batches,
// then starts a fresh transaction. This makes indexed data searchable mid-run
// without waiting for the entire commit history to be processed.
// The caller's deferred st.tx.Rollback() will correctly target the new transaction
// if anything fails after this point.
func (st *indexState) checkpointFlush(ctx context.Context) error {
	if err := st.flushCodeBatch(true); err != nil {
		return fmt.Errorf("checkpoint: flush code batch: %w", err)
	}
	if err := st.flushDiffBatch(true); err != nil {
		return fmt.Errorf("checkpoint: flush diff batch: %w", err)
	}
	if err := st.tx.Commit(); err != nil {
		return fmt.Errorf("checkpoint: commit: %w", err)
	}
	newTx, err := st.store.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("checkpoint: begin new tx: %w", err)
	}
	st.tx = newTx
	return nil
}

// IndexRepo indexes a git repository into the store.
func IndexRepo(ctx context.Context, s *store.Store, url string, opts IndexOptions) error {
	report := func(msg string) {
		if opts.Progress != nil {
			opts.Progress(msg)
		}
	}

	// 1. Clone or fetch
	t0 := time.Now()
	report("Cloning/fetching repository...")
	dirName, err := RepoDirFromURL(url)
	if err != nil {
		return fmt.Errorf("derive repo dir from URL: %w", err)
	}
	repoPath := filepath.Join(s.ReposDir(), dirName)
	repo, err := CloneOrFetch(url, repoPath)
	if err != nil {
		return fmt.Errorf("clone/fetch %s: %w", url, err)
	}
	defer repo.Close()
	report(fmt.Sprintf("  clone/fetch: %s", time.Since(t0).Round(time.Millisecond)))

	// 2. Upsert repo record
	t1 := time.Now()
	repoName, err := RepoNameFromURL(url)
	if err != nil {
		return fmt.Errorf("derive repo name from URL: %w", err)
	}
	repoID, err := upsertRepo(ctx, s, repoName, repoPath)
	if err != nil {
		return fmt.Errorf("upsert repo record: %w", err)
	}

	// 3. Load known commits
	knownCommits, err := loadKnownCommits(ctx, s, repoID)
	if err != nil {
		return fmt.Errorf("load known commits: %w", err)
	}

	// 4. Resolve default branch
	defaultRef, err := resolveDefaultBranch(repo)
	if err != nil {
		return fmt.Errorf("resolve default branch: %w", err)
	}
	report(fmt.Sprintf("  setup: %s, branch %s, %d known commits",
		time.Since(t1).Round(time.Millisecond), defaultRef.name, len(knownCommits)))

	treeCacheLimit := opts.TreeCacheLimit
	if treeCacheLimit <= 0 {
		treeCacheLimit = defaultTreeCacheLimit
	}

	// 5. Begin transaction
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	st := &indexState{
		tx:             tx,
		store:          s,
		repo:           repo,
		repoID:         repoID,
		codeBatch:      s.CodeIndex.NewBatch(),
		diffBatch:      s.DiffIndex.NewBatch(),
		knownCommits:   knownCommits,
		treeCache:      make(map[plumbing.Hash]map[string]plumbing.Hash),
		treeOrder:      make([]plumbing.Hash, 0, treeCacheLimit),
		treeCacheLimit: treeCacheLimit,
		blobIDCache:    make(map[string]int64),
		commitIDCache:  make(map[string]int64),
		report:         report,
	}
	// defer on st.tx (not the original tx pointer) so checkpointFlush's
	// new transactions are also rolled back on failure.
	defer func() { st.tx.Rollback() }()

	// 6. Check if default branch tip has changed
	existingRefs, err := loadExistingRefs(ctx, s, repoID)
	if err != nil {
		return fmt.Errorf("load existing refs: %w", err)
	}

	t2 := time.Now()
	if prev, ok := existingRefs[defaultRef.name]; ok && prev == defaultRef.tipOID.String() {
		report(fmt.Sprintf("  default branch unchanged, skipping (%s)", time.Since(t2).Round(time.Millisecond)))
	} else {
		if err := st.processRef(ctx, defaultRef, 0, 1, opts.MaxHistoryDepth, existingRefs); err != nil {
			return fmt.Errorf("process default branch: %w", err)
		}
		report(fmt.Sprintf("  indexed default branch: %s (%s)",
			time.Since(t2).Round(time.Millisecond), defaultRef.name))
	}
	report(fmt.Sprintf("Indexing complete: %d new commits, %d new blobs.", st.newCommits, st.newBlobs))

	// 8. Commit SQL transaction first, then flush Bleve batches.
	// This ordering ensures Bleve never contains documents without
	// corresponding SQL rows (split-brain). If SQL commit fails,
	// Bleve is unchanged. If Bleve flush fails after SQL commit,
	// a re-index will re-populate Bleve from the committed SQL data.
	t3 := time.Now()
	if err := st.tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	if err := st.flushCodeBatch(true); err != nil {
		return fmt.Errorf("flush code index after commit: %w", err)
	}
	if err := st.flushDiffBatch(true); err != nil {
		return fmt.Errorf("flush diff index after commit: %w", err)
	}
	report(fmt.Sprintf("  flush+commit: %s", time.Since(t3).Round(time.Millisecond)))
	report(fmt.Sprintf("  total: %s", time.Since(t0).Round(time.Millisecond)))

	return nil
}

// IndexLocalRepo indexes a local git repository in-place (no clone).
// It indexes all committed history AND the current working tree, including
// uncommitted (dirty) files.
func IndexLocalRepo(ctx context.Context, s *store.Store, localPath string, opts IndexOptions) error {
	report := func(msg string) {
		if opts.Progress != nil {
			opts.Progress(msg)
		}
	}

	t0 := time.Now()

	// 1. Open the repo — for linked worktrees, open the main repo so
	// go-git can access the shared object store (packfiles, loose objects).
	report("Opening local repository...")
	repoOpenPath, isWorktree := resolveGitDir(localPath)
	repo, err := plainOpenTolerant(repoOpenPath)
	if err != nil {
		return fmt.Errorf("open local repo %s: %w", localPath, err)
	}
	defer repo.Close()

	// 2. Upsert repo record — use the local path as both name and path
	repoName := filepath.Base(localPath)
	repoID, err := upsertRepo(ctx, s, repoName, localPath)
	if err != nil {
		return fmt.Errorf("upsert repo record: %w", err)
	}

	// 3. Load known commits
	knownCommits, err := loadKnownCommits(ctx, s, repoID)
	if err != nil {
		return fmt.Errorf("load known commits: %w", err)
	}

	// 4. Resolve default branch — for linked worktrees, always use git CLI
	// since go-git opened the main repo (which has a different HEAD)
	var defaultRef refInfo
	if isWorktree {
		defaultRef, err = resolveDefaultBranchGit(localPath)
	} else {
		defaultRef, err = resolveDefaultBranchWithPath(repo, localPath)
	}
	if err != nil {
		return fmt.Errorf("resolve default branch: %w", err)
	}
	report(fmt.Sprintf("  branch %s, %d known commits", defaultRef.name, len(knownCommits)))

	treeCacheLimit := opts.TreeCacheLimit
	if treeCacheLimit <= 0 {
		treeCacheLimit = defaultTreeCacheLimit
	}

	// 5. Begin transaction
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	st := &indexState{
		tx:             tx,
		store:          s,
		repo:           repo,
		repoID:         repoID,
		codeBatch:      s.CodeIndex.NewBatch(),
		diffBatch:      s.DiffIndex.NewBatch(),
		knownCommits:   knownCommits,
		treeCache:      make(map[plumbing.Hash]map[string]plumbing.Hash),
		treeOrder:      make([]plumbing.Hash, 0, treeCacheLimit),
		treeCacheLimit: treeCacheLimit,
		blobIDCache:    make(map[string]int64),
		commitIDCache:  make(map[string]int64),
		report:         report,
	}
	// defer on st.tx (not the original tx pointer) so checkpointFlush's
	// new transactions are also rolled back on failure.
	defer func() { st.tx.Rollback() }()

	// 6. Check if default branch tip has changed
	existingRefs, err := loadExistingRefs(ctx, s, repoID)
	if err != nil {
		return fmt.Errorf("load existing refs: %w", err)
	}

	t2 := time.Now()
	if prev, ok := existingRefs[defaultRef.name]; ok && prev == defaultRef.tipOID.String() {
		report(fmt.Sprintf("  default branch unchanged (%s)", time.Since(t2).Round(time.Millisecond)))
	} else {
		if err := st.processRef(ctx, defaultRef, 0, 1, opts.MaxHistoryDepth, existingRefs); err != nil {
			return fmt.Errorf("process default branch: %w", err)
		}
		report(fmt.Sprintf("  indexed default branch: %s (%s)",
			time.Since(t2).Round(time.Millisecond), defaultRef.name))
	}

	report(fmt.Sprintf("Indexing complete: %d new commits, %d new blobs.", st.newCommits, st.newBlobs))

	// 9. Commit SQL transaction first, then flush Bleve batches.
	t3 := time.Now()
	if err := st.tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	if err := st.flushCodeBatch(true); err != nil {
		return fmt.Errorf("flush code index after commit: %w", err)
	}
	if err := st.flushDiffBatch(true); err != nil {
		return fmt.Errorf("flush diff index after commit: %w", err)
	}
	report(fmt.Sprintf("  flush+commit: %s", time.Since(t3).Round(time.Millisecond)))
	report(fmt.Sprintf("  total: %s", time.Since(t0).Round(time.Millisecond)))

	return nil
}

// DirtyIndexPath returns the on-disk path for a worktree's dirty overlay index.
// The path is deterministic based on the worktree's absolute path, allowing the
// CLI to find the daemon-built dirty index without IPC.
func DirtyIndexPath(codedbDir, worktreePath string) string {
	h := sha256.Sum256([]byte(worktreePath))
	return filepath.Join(codedbDir, "bleve", "dirty", hex.EncodeToString(h[:8]))
}

// BuildDirtyIndex creates an on-disk Bleve index of dirty (uncommitted) files.
// Uses git status to identify dirty files (fast) rather than walking the full tree.
// The dirty index at dirtyPath is fully rebuilt on each call via atomic swap
// (write to .tmp, then rename) so CLI readers never see a partially-written index.
// Files are indexed as "dirty_<relPath>" for enrichment without SQL.
func BuildDirtyIndex(ctx context.Context, localPath, dirtyPath string, opts IndexOptions) (int, error) {
	dirtyFiles, err := gitStatusDirtyFiles(ctx, localPath)
	if err != nil {
		return 0, fmt.Errorf("git status: %w", err)
	}

	if len(dirtyFiles) == 0 {
		// no dirty files — remove stale index if it exists
		_ = os.RemoveAll(dirtyPath)
		return 0, nil
	}

	// build into a temp path, then atomic-swap to dirtyPath
	tmpPath := dirtyPath + ".tmp"
	_ = os.RemoveAll(tmpPath)

	mapping := bleve.NewIndexMapping()
	dirtyIdx, err := bleve.New(tmpPath, mapping)
	if err != nil {
		return 0, fmt.Errorf("create dirty index: %w", err)
	}

	indexed := 0
	batch := dirtyIdx.NewBatch()

	for _, relPath := range dirtyFiles {
		if ctx.Err() != nil {
			break
		}

		lang := language.Detect(relPath)
		if lang == "" {
			continue
		}

		absPath := filepath.Join(localPath, relPath)
		content, rErr := os.ReadFile(absPath)
		if rErr != nil || !utf8.Valid(content) || len(content) == 0 || len(content) > 1<<20 {
			continue
		}

		batch.Index("dirty_"+relPath, BleveCodeDoc{Content: string(content)})
		indexed++
	}

	if indexed > 0 {
		if err := dirtyIdx.Batch(batch); err != nil {
			dirtyIdx.Close()
			_ = os.RemoveAll(tmpPath)
			return 0, fmt.Errorf("batch dirty index: %w", err)
		}
	}
	dirtyIdx.Close()

	// atomic swap: remove old, rename tmp into place
	_ = os.RemoveAll(dirtyPath)
	if err := os.Rename(tmpPath, dirtyPath); err != nil {
		_ = os.RemoveAll(tmpPath)
		return 0, fmt.Errorf("swap dirty index: %w", err)
	}

	// write manifest so GCDirtyIndexes can reverse the hash back to the path
	absPath, err := filepath.Abs(localPath)
	if err != nil {
		_ = os.RemoveAll(dirtyPath)
		return 0, fmt.Errorf("abs dirty worktree path: %w", err)
	}
	if err := os.WriteFile(dirtyPath+".manifest", []byte(absPath), 0o644); err != nil {
		_ = os.RemoveAll(dirtyPath)
		return 0, fmt.Errorf("write dirty manifest: %w", err)
	}

	if opts.Progress != nil {
		opts.Progress(fmt.Sprintf("indexed %d dirty files", indexed))
	}

	return indexed, nil
}

// GCDirtyIndexes removes dirty overlay directories whose worktrees no longer exist.
// Each overlay is paired with a "<dir>.manifest" file written by BuildDirtyIndex that
// records the original worktree path. Overlays without a manifest (written by an older
// build) are left alone to avoid removing indexes whose provenance is unknown.
//
// Returns the number of overlays removed.
func GCDirtyIndexes(codedbDir string) (int, error) {
	dirtyDir := filepath.Join(codedbDir, "bleve", "dirty")
	entries, err := os.ReadDir(dirtyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read dirty index dir: %w", err)
	}

	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dirPath := filepath.Join(dirtyDir, e.Name())
		manifestPath := dirPath + ".manifest"

		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			if os.IsNotExist(err) {
				// no manifest — written by an older build; leave it alone
				continue
			}
			return 0, fmt.Errorf("read dirty manifest %s: %w", manifestPath, err)
		}
		worktreePath := string(raw)
		if _, statErr := os.Stat(worktreePath); os.IsNotExist(statErr) {
			// worktree is gone — remove overlay + manifest
			if err := os.RemoveAll(dirPath); err != nil {
				return removed, fmt.Errorf("remove dirty overlay %s: %w", dirPath, err)
			}
			if err := os.Remove(manifestPath); err != nil && !os.IsNotExist(err) {
				return removed, fmt.Errorf("remove dirty manifest %s: %w", manifestPath, err)
			}
			removed++
		}
	}
	return removed, nil
}

// AttachAllDirtyIndexes scans the dirty index directory for manifest files and
// attaches all valid dirty overlays by worktree ID. Returns the number of
// overlays successfully attached. Overlays whose worktrees no longer exist are skipped.
func AttachAllDirtyIndexes(s *store.Store) int {
	dirtyDir := filepath.Join(s.Root, "bleve", "dirty")
	entries, err := os.ReadDir(dirtyDir)
	if err != nil {
		return 0
	}

	attached := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dirPath := filepath.Join(dirtyDir, e.Name())
		manifestPath := dirPath + ".manifest"

		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			continue // no manifest — skip
		}
		worktreePath := string(raw)
		if _, statErr := os.Stat(worktreePath); os.IsNotExist(statErr) {
			continue // worktree gone — skip
		}

		if err := s.AttachDirtyIndexByID(e.Name(), dirPath); err != nil {
			slog.Debug("skip dirty overlay", "id", e.Name(), "err", err)
			continue
		}
		attached++
	}
	return attached
}

// gitStatusDirtyFiles returns relative paths of dirty files using git status -z.
// The -z flag uses NUL delimiters, avoiding all quoting/escaping issues with
// filenames containing spaces, non-ASCII, or special characters.
func gitStatusDirtyFiles(ctx context.Context, repoPath string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain", "-z")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	if len(out) == 0 {
		return nil, nil
	}

	var files []string
	entries := bytes.Split(out, []byte{0})

	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) < 4 {
			continue
		}

		xy := entry[:2]
		path := string(entry[3:])

		// skip deleted files (no content to index)
		if xy[0] == 'D' || xy[1] == 'D' {
			continue
		}

		// renames/copies: with -z, the "from" path is in the next NUL-separated entry
		if xy[0] == 'R' || xy[0] == 'C' {
			i++ // skip the "from" path
		}

		files = append(files, path)
	}

	return files, nil
}

// upsertRepo inserts or updates a repo record and returns its ID.
func upsertRepo(ctx context.Context, s *store.Store, name, path string) (int64, error) {
	q := s.Queries()
	if err := q.UpsertRepo(ctx, codedbsqlc.UpsertRepoParams{Name: name, Path: path}); err != nil {
		return 0, fmt.Errorf("upsert repo: %w", err)
	}
	id, err := q.GetRepoIDByName(ctx, name)
	if err != nil {
		return 0, fmt.Errorf("get repo id: %w", err)
	}
	return id, nil
}

// loadKnownCommits returns a set of commit hashes already indexed for a repo.
func loadKnownCommits(ctx context.Context, s *store.Store, repoID int64) (map[string]bool, error) {
	hashes, err := s.Queries().ListCommitHashesByRepo(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("query known commits: %w", err)
	}
	known := make(map[string]bool, len(hashes))
	for _, h := range hashes {
		known[h] = true
	}
	return known, nil
}

// loadExistingRefs returns a map of ref name -> tip commit hash for a repo.
func loadExistingRefs(ctx context.Context, s *store.Store, repoID int64) (map[string]string, error) {
	rows, err := s.Queries().ListRefsByRepo(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("load existing refs: %w", err)
	}
	refs := make(map[string]string, len(rows))
	for _, row := range rows {
		refs[row.Name] = row.Hash
	}
	return refs, nil
}

// resolveDefaultBranch returns the single default branch ref (main/master).
// It checks HEAD first, then falls back to common default branch names.
// For linked worktrees (where .git is a file), go-git may fail to resolve HEAD,
// so we fall back to the git CLI.
func resolveDefaultBranch(repo *git.Repository) (refInfo, error) {
	// Try HEAD — in bare repos this points to the default branch
	headRef, err := repo.Head()
	if err == nil {
		return refInfo{
			name:   headRef.Name().String(),
			tipOID: headRef.Hash(),
		}, nil
	}

	// Fallback: try common default branch names
	for _, name := range defaultBranchFallbacks {
		ref, err := repo.Reference(plumbing.ReferenceName(name), true)
		if err == nil {
			return refInfo{
				name:   ref.Name().String(),
				tipOID: ref.Hash(),
			}, nil
		}
	}

	return refInfo{}, fmt.Errorf("no default branch found (tried HEAD, main, master)")
}

// resolveDefaultBranchWithPath resolves the default branch, falling back to
// the git CLI for linked worktrees where go-git can't resolve HEAD.
func resolveDefaultBranchWithPath(repo *git.Repository, localPath string) (refInfo, error) {
	ref, err := resolveDefaultBranch(repo)
	if err == nil {
		return ref, nil
	}

	// go-git fails on linked worktrees — fall back to git CLI
	return resolveDefaultBranchGit(localPath)
}

// resolveDefaultBranchGit uses the git CLI to resolve HEAD when go-git can't
// (e.g., linked worktrees where .git is a file).
func resolveDefaultBranchGit(repoPath string) (refInfo, error) {
	// get the symbolic ref name (e.g., refs/heads/main)
	nameCmd := exec.Command("git", "symbolic-ref", "HEAD")
	nameCmd.Dir = repoPath
	nameOut, err := nameCmd.Output()
	if err != nil {
		return refInfo{}, fmt.Errorf("git symbolic-ref HEAD: %w", err)
	}
	refName := strings.TrimSpace(string(nameOut))

	// get the commit hash
	hashCmd := exec.Command("git", "rev-parse", "HEAD")
	hashCmd.Dir = repoPath
	hashOut, err := hashCmd.Output()
	if err != nil {
		return refInfo{}, fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	hashStr := strings.TrimSpace(string(hashOut))

	return refInfo{
		name:   refName,
		tipOID: plumbing.NewHash(hashStr),
	}, nil
}

// resolveGitDir returns the path to open with go-git and whether it's a linked
// worktree. For linked worktrees (where .git is a file containing "gitdir: ..."),
// it follows the pointer to the main repo's .git directory so go-git can access
// the shared object store. For normal repos, returns the path unchanged.
func resolveGitDir(repoPath string) (string, bool) {
	dotGit := filepath.Join(repoPath, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil || info.IsDir() {
		return repoPath, false // normal repo or no .git
	}

	// .git is a file → linked worktree, read "gitdir: <path>"
	content, err := os.ReadFile(dotGit)
	if err != nil {
		return repoPath, false
	}
	line := strings.TrimSpace(string(content))
	if !strings.HasPrefix(line, "gitdir: ") {
		return repoPath, false
	}
	worktreeGitDir := strings.TrimPrefix(line, "gitdir: ")
	if !filepath.IsAbs(worktreeGitDir) {
		worktreeGitDir = filepath.Join(repoPath, worktreeGitDir)
	}

	// read commondir to find the shared .git (e.g., "../.." → main .git)
	commondirFile := filepath.Join(worktreeGitDir, "commondir")
	commondirBytes, err := os.ReadFile(commondirFile)
	if err != nil {
		return repoPath, false
	}
	commondir := strings.TrimSpace(string(commondirBytes))
	if !filepath.IsAbs(commondir) {
		commondir = filepath.Join(worktreeGitDir, commondir)
	}

	// commondir points to the main .git dir; the repo root is its parent
	mainRepoRoot := filepath.Dir(commondir)
	return mainRepoRoot, true
}

// walkNewCommits discovers commits reachable from tip that aren't in knownCommits.
// Returns commits in oldest-first (topological) order. Respects maxDepth (0 = unlimited).
// Uses BFS so that after reversal, parents always appear before children even with merges.
func walkNewCommits(repo *git.Repository, tip plumbing.Hash, knownCommits map[string]bool, maxDepth int) ([]commitData, bool) {
	var newCommits []commitData
	visited := make(map[plumbing.Hash]bool)
	queue := []plumbing.Hash{tip}
	depthTruncated := false

	for len(queue) > 0 {
		if maxDepth > 0 && len(newCommits) >= maxDepth {
			depthTruncated = true
			break
		}
		oid := queue[0]
		queue = queue[1:]

		oidHex := oid.String()
		if knownCommits[oidHex] || visited[oid] {
			continue
		}
		visited[oid] = true

		commitObj, err := repo.CommitObject(oid)
		if err != nil {
			continue
		}

		var parentIDs []plumbing.Hash
		for _, p := range commitObj.ParentHashes {
			parentIDs = append(parentIDs, p)
			queue = append(queue, p)
		}

		newCommits = append(newCommits, commitData{
			oid:       oid,
			author:    commitObj.Author.Name,
			message:   commitObj.Message,
			timestamp: commitObj.Author.When.Unix(),
			treeHash:  commitObj.TreeHash,
			parentIDs: parentIDs,
		})
	}

	// Reverse for oldest-first (topological) processing
	for i, j := 0, len(newCommits)-1; i < j; i, j = i+1, j-1 {
		newCommits[i], newCommits[j] = newCommits[j], newCommits[i]
	}

	return newCommits, depthTruncated
}

// processRef walks a single ref's history and indexes new commits, diffs, and file_revs.
func (st *indexState) processRef(ctx context.Context, ri refInfo, refIdx, totalRefs, maxDepth int, existingRefs map[string]string) error {
	newCommits, depthTruncated := walkNewCommits(st.repo, ri.tipOID, st.knownCommits, maxDepth)

	if len(newCommits) > 0 {
		st.report(fmt.Sprintf("Ref %d/%d: %s — %d new commits",
			refIdx+1, totalRefs, ri.name, len(newCommits)))
	}
	if depthTruncated {
		st.report(fmt.Sprintf("Warning: history depth limit (%d) reached for ref %s.",
			maxDepth, ri.name))
	}

	for i, cd := range newCommits {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := st.indexCommit(ctx, cd); err != nil {
			return err
		}
		// Checkpoint every N commits so the index is searchable mid-run.
		if i > 0 && i%checkpointEvery == 0 {
			st.report(fmt.Sprintf("Checkpoint: %d/%d commits indexed and searchable", i, len(newCommits)))
			if err := st.checkpointFlush(ctx); err != nil {
				return fmt.Errorf("checkpoint flush at commit %d: %w", i, err)
			}
		}
	}

	// Skip file_revs rebuild if tip hasn't changed
	if prev, ok := existingRefs[ri.name]; ok && prev == ri.tipOID.String() {
		return nil
	}

	// Build file_revs for tip
	return st.buildTipFileRevs(ri)
}

// indexCommit inserts a single commit and its diffs into the database and Bleve.
func (st *indexState) indexCommit(ctx context.Context, cd commitData) error {
	oidHex := cd.oid.String()
	q := codedbsqlc.New(st.tx)

	if err := q.InsertCommit(ctx, codedbsqlc.InsertCommitParams{
		RepoID:    st.repoID,
		Hash:      oidHex,
		Author:    sql.NullString{String: cd.author, Valid: cd.author != ""},
		Message:   sql.NullString{String: cd.message, Valid: cd.message != ""},
		Timestamp: sql.NullInt64{Int64: cd.timestamp, Valid: true},
	}); err != nil {
		return fmt.Errorf("insert commit: %w", err)
	}

	// Use LastInsertId to skip a SELECT; each commit hash is processed once per run.
	// Fall back to SELECT only when RowsAffected==0 (commit already existed).
	commitDBID, err := q.GetCommitIDByHash(ctx, oidHex)
	if err != nil {
		return fmt.Errorf("get commit id: %w", err)
	}
	st.commitIDCache[oidHex] = commitDBID

	st.newCommits++
	if st.newCommits%500 == 0 {
		st.report(fmt.Sprintf("Processed %d commits, %d new blobs...", st.newCommits, st.newBlobs))
	}

	// Insert parent links
	if err := st.insertParentLinks(commitDBID, cd.parentIDs); err != nil {
		return err
	}

	// Use DiffTree for O(changed_files) tree reads instead of O(all_files)×2.
	// DiffTree uses merkle-trie hash comparison: only descends into subtrees
	// whose hash changed, identical to git diff-tree semantics.
	childTree, err := st.repo.TreeObject(cd.treeHash)
	if err != nil {
		return fmt.Errorf("get child tree: %w", err)
	}

	var parentTree *object.Tree
	if len(cd.parentIDs) > 0 {
		parentCommit, pErr := st.repo.CommitObject(cd.parentIDs[0])
		if pErr == nil {
			parentTree, _ = st.repo.TreeObject(parentCommit.TreeHash)
		}
	}

	changes, err := object.DiffTreeContext(ctx, parentTree, childTree)
	if err != nil {
		return fmt.Errorf("diff tree: %w", err)
	}

	for _, change := range changes {
		action, aErr := change.Action()
		if aErr != nil {
			continue
		}
		switch action {
		case merkletrie.Insert:
			if !change.To.TreeEntry.Mode.IsFile() {
				continue // skip submodules and directories
			}
			if err := st.indexChangeEntry(commitDBID, change.To.Name, change.To.TreeEntry.Hash, plumbing.ZeroHash, false); err != nil {
				return err
			}
		case merkletrie.Modify:
			if !change.To.TreeEntry.Mode.IsFile() {
				continue // skip submodules and directories
			}
			if err := st.indexChangeEntry(commitDBID, change.To.Name, change.To.TreeEntry.Hash, change.From.TreeEntry.Hash, true); err != nil {
				return err
			}
		case merkletrie.Delete:
			if !change.From.TreeEntry.Mode.IsFile() {
				continue // skip submodules and directories
			}
			if err := st.indexDeletionEntry(commitDBID, change.From.Name, change.From.TreeEntry.Hash); err != nil {
				return err
			}
		}
	}

	st.knownCommits[oidHex] = true
	return nil
}

// insertParentLinks records commit parent relationships.
func (st *indexState) insertParentLinks(commitDBID int64, parentIDs []plumbing.Hash) error {
	ctx := context.Background()
	q := codedbsqlc.New(st.tx)
	for _, parentOID := range parentIDs {
		parentHex := parentOID.String()
		// Use cache to avoid a SELECT per parent; cache is populated by indexCommit.
		parentDBID, ok := st.commitIDCache[parentHex]
		if !ok {
			// Parent was indexed in a previous run — look up from DB.
			var err error
			parentDBID, err = q.GetCommitIDByHash(ctx, parentHex)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					continue // parent not yet in DB; skip link
				}
				return fmt.Errorf("get parent commit id %s: %w", parentHex, err)
			}
			st.commitIDCache[parentHex] = parentDBID
		}
		if err := q.InsertCommitParent(ctx, codedbsqlc.InsertCommitParentParams{
			CommitID: commitDBID,
			ParentID: parentDBID,
		}); err != nil {
			return fmt.Errorf("insert commit parent: %w", err)
		}
	}
	return nil
}

// indexChangedFiles processes files that were added or modified in a commit.
func (st *indexState) indexChangedFiles(commitDBID int64, childEntries, parentEntries map[string]plumbing.Hash) error {
	for path, childBlobOID := range childEntries {
		parentBlobOID, existsInParent := parentEntries[path]
		if existsInParent && parentBlobOID == childBlobOID {
			continue // unchanged
		}

		// ensureBlob returns the content it read for Bleve; reuse it in generateDiffText
		// to avoid reading the same git object twice per changed file.
		newBlobDBID, newText, indexed, err := st.ensureBlob(childBlobOID, path)
		if err != nil {
			return err
		}
		if indexed {
			st.newBlobs++
		}

		var oldBlobDBID sql.NullInt64
		var oldText string
		if existsInParent {
			id, text, idx, err := st.ensureBlob(parentBlobOID, path)
			if err != nil {
				return err
			}
			if idx {
				st.newBlobs++
			}
			oldBlobDBID = sql.NullInt64{Int64: id, Valid: true}
			oldText = text
		}

		if err := st.insertDiff(commitDBID, path, oldBlobDBID, newBlobDBID, parentBlobOID, childBlobOID, existsInParent, true, oldText, newText); err != nil {
			return err
		}
	}
	return nil
}

// indexDeletedFiles processes files that were removed in a commit.
func (st *indexState) indexDeletedFiles(commitDBID int64, childEntries, parentEntries map[string]plumbing.Hash) error {
	for path, parentBlobOID := range parentEntries {
		if _, exists := childEntries[path]; exists {
			continue
		}
		oldBlobDBID, oldText, indexed, err := st.ensureBlob(parentBlobOID, path)
		if err != nil {
			return err
		}
		if indexed {
			st.newBlobs++
		}

		if err := st.insertDiff(commitDBID, path, sql.NullInt64{Int64: oldBlobDBID, Valid: true}, 0, parentBlobOID, plumbing.ZeroHash, true, false, oldText, ""); err != nil {
			return err
		}
	}
	return nil
}

// indexChangeEntry processes one added or modified file from a DiffTree change.
// hasOld=false for insertions (initial commit or new file); hasOld=true for modifications.
func (st *indexState) indexChangeEntry(commitDBID int64, path string, newOID, oldOID plumbing.Hash, hasOld bool) error {
	newBlobDBID, newText, indexed, err := st.ensureBlob(newOID, path)
	if err != nil {
		return err
	}
	if indexed {
		st.newBlobs++
	}

	var oldBlobDBID sql.NullInt64
	var oldText string
	if hasOld {
		id, text, idx, err := st.ensureBlob(oldOID, path)
		if err != nil {
			return err
		}
		if idx {
			st.newBlobs++
		}
		oldBlobDBID = sql.NullInt64{Int64: id, Valid: true}
		oldText = text
	}

	return st.insertDiff(commitDBID, path, oldBlobDBID, newBlobDBID, oldOID, newOID, hasOld, true, oldText, newText)
}

// indexDeletionEntry processes one deleted file from a DiffTree change.
func (st *indexState) indexDeletionEntry(commitDBID int64, path string, oldOID plumbing.Hash) error {
	oldBlobDBID, oldText, indexed, err := st.ensureBlob(oldOID, path)
	if err != nil {
		return err
	}
	if indexed {
		st.newBlobs++
	}
	return st.insertDiff(commitDBID, path, sql.NullInt64{Int64: oldBlobDBID, Valid: true}, 0, oldOID, plumbing.ZeroHash, true, false, oldText, "")
}

// insertDiff inserts a diff record and indexes the diff text in Bleve.
// oldText/newText are pre-read blob contents from ensureBlob; when non-empty they
// avoid a second git object read inside generateDiffText.
func (st *indexState) insertDiff(commitDBID int64, path string, oldBlobDBID sql.NullInt64, newBlobDBID int64, oldOID, newOID plumbing.Hash, hasOld, hasNew bool, oldText, newText string) error {
	ctx := context.Background()
	q := codedbsqlc.New(st.tx)

	var newBlobNull sql.NullInt64
	if hasNew {
		newBlobNull = sql.NullInt64{Int64: newBlobDBID, Valid: true}
	}

	if err := q.InsertDiff(ctx, codedbsqlc.InsertDiffParams{
		CommitID:  commitDBID,
		Path:      path,
		OldBlobID: oldBlobDBID,
		NewBlobID: newBlobNull,
	}); err != nil {
		return fmt.Errorf("insert diff: %w", err)
	}

	diffDBID, err := q.GetDiffIDByCommitPath(ctx, codedbsqlc.GetDiffIDByCommitPathParams{
		CommitID: commitDBID,
		Path:     path,
	})
	if err != nil {
		return fmt.Errorf("get diff id: %w", err)
	}

	diffText := generateDiffText(st.repo, path, oldOID, newOID, hasOld, hasNew, oldText, newText)
	if diffText != "" {
		st.diffBatch.Index("diff_"+strconv.FormatInt(diffDBID, 10), BleveDiffDoc{Content: diffText})
		st.diffBatchN++
		if err := st.flushDiffBatch(false); err != nil {
			return err
		}
	}
	return nil
}

// buildTipFileRevs rebuilds the file_revs table for a ref's tip commit.
func (st *indexState) buildTipFileRevs(ri refInfo) error {
	ctx := context.Background()
	q := codedbsqlc.New(st.tx)

	tipHex := ri.tipOID.String()
	tipCommitDBID, err := q.GetCommitIDByHash(ctx, tipHex)
	if err != nil {
		return nil // skip if tip not indexed
	}

	if err := q.DeleteFileRevsByCommit(ctx, tipCommitDBID); err != nil {
		return fmt.Errorf("delete file_revs: %w", err)
	}

	var tipEntries map[string]plumbing.Hash
	tipCommit, tErr := st.repo.CommitObject(ri.tipOID)
	if tErr == nil {
		tipEntries, _ = st.getTree(tipCommit.TreeHash)
	}
	if tipEntries == nil {
		tipEntries = make(map[string]plumbing.Hash)
	}

	for path, blobOID := range tipEntries {
		blobDBID, _, indexed, err := st.ensureBlob(blobOID, path)
		if err != nil {
			continue
		}
		if indexed {
			st.newBlobs++
		}
		if err := q.InsertFileRev(ctx, codedbsqlc.InsertFileRevParams{
			CommitID: tipCommitDBID,
			Path:     path,
			BlobID:   blobDBID,
		}); err != nil {
			return fmt.Errorf("insert file_rev: %w", err)
		}
	}

	// upsert ref
	if err := q.UpsertRef(ctx, codedbsqlc.UpsertRefParams{
		RepoID:   st.repoID,
		Name:     ri.name,
		CommitID: tipCommitDBID,
	}); err != nil {
		return fmt.Errorf("upsert ref: %w", err)
	}

	return nil
}

// getTree wraps getTreeEntries with FIFO cache eviction. Instead of wiping the
// entire cache when full (which evicts recently-needed parent trees), it removes
// only the oldest entry, preserving sequential access locality.
// Memory bound: treeCacheLimit × (numFiles × 76 bytes per entry).
func (st *indexState) getTree(hash plumbing.Hash) (map[string]plumbing.Hash, error) {
	_, alreadyCached := st.treeCache[hash]
	if !alreadyCached && len(st.treeCache) >= st.treeCacheLimit {
		// Evict the oldest (first-inserted) entry.
		for len(st.treeOrder) > 0 {
			oldest := st.treeOrder[0]
			st.treeOrder = st.treeOrder[1:]
			if _, stillPresent := st.treeCache[oldest]; stillPresent {
				delete(st.treeCache, oldest)
				break
			}
		}
	}
	entries, err := getTreeEntries(st.repo, hash, st.treeCache)
	if err != nil {
		return nil, err
	}
	if !alreadyCached {
		st.treeOrder = append(st.treeOrder, hash)
	}
	return entries, nil
}

// getTreeEntries returns a map of filepath -> blob hash for all blobs in a tree.
func getTreeEntries(repo *git.Repository, treeHash plumbing.Hash, cache map[plumbing.Hash]map[string]plumbing.Hash) (map[string]plumbing.Hash, error) {
	if treeHash == (plumbing.Hash{}) {
		return nil, fmt.Errorf("zero hash")
	}
	if cached, ok := cache[treeHash]; ok {
		return cached, nil
	}

	tree, err := repo.TreeObject(treeHash)
	if err != nil {
		return nil, err
	}

	entries := make(map[string]plumbing.Hash)
	tree.Files().ForEach(func(f *object.File) error {
		entries[f.Name] = f.Hash
		return nil
	})

	cache[treeHash] = entries
	return entries, nil
}

// ensureBlob inserts a blob record if not already present and indexes its content
// in Bleve only for newly inserted blobs. Uses an in-memory cache to avoid
// repeated SQL lookups for the same content_hash within a single indexing run.
// Returns (blobDBID, blobText, indexedInBleve, error).
// blobText is the decoded UTF-8 content when the blob was read for Bleve indexing;
// callers can reuse it to avoid a second git object read (e.g. in generateDiffText).
func (st *indexState) ensureBlob(blobOID plumbing.Hash, path string) (int64, string, bool, error) {
	contentHash := blobOID.String()

	// fast path: check in-memory cache first (avoids SQL roundtrip)
	if cachedID, ok := st.blobIDCache[contentHash]; ok {
		return cachedID, "", false, nil
	}

	// check SQL
	q := codedbsqlc.New(st.tx)
	ctx := context.Background()
	blobDBID, err := q.GetBlobIDByHash(ctx, contentHash)
	if err == nil {
		st.blobIDCache[contentHash] = blobDBID
		return blobDBID, "", false, nil
	}

	// new blob — insert and index in Bleve
	lang := language.Detect(path)
	if err := q.InsertBlob(ctx, codedbsqlc.InsertBlobParams{
		ContentHash: contentHash,
		Language:    toNullString(lang),
	}); err != nil {
		return 0, "", false, fmt.Errorf("insert blob: %w", err)
	}

	blobDBID, err = q.GetBlobIDByHash(ctx, contentHash)
	if err != nil {
		return 0, "", false, fmt.Errorf("get blob id: %w", err)
	}

	st.blobIDCache[contentHash] = blobDBID

	var blobText string
	indexed := false
	blobObj, bErr := st.repo.BlobObject(blobOID)
	if bErr == nil {
		reader, rErr := blobObj.Reader()
		if rErr == nil {
			content, readErr := io.ReadAll(reader)
			reader.Close()
			if readErr == nil && utf8.Valid(content) && len(content) > 0 {
				blobText = string(content)
				st.codeBatch.Index("blob_"+strconv.FormatInt(blobDBID, 10), BleveCodeDoc{Content: blobText})
				st.codeBatchN++
				indexed = true
				if err := st.flushCodeBatch(false); err != nil {
					return 0, "", false, err
				}
			}
		}
	}

	return blobDBID, blobText, indexed, nil
}

// generateDiffText creates simple diff text for full-text search indexing.
// Each side is truncated to 100 lines to keep the index manageable.
// oldText/newText are pre-read blob contents from ensureBlob; when non-empty they
// avoid a redundant git object read (saves one BlobObject+ReadAll per side).
func generateDiffText(repo *git.Repository, path string, oldOID, newOID plumbing.Hash, hasOld, hasNew bool, oldText, newText string) string {
	const maxLines = 100

	var b strings.Builder
	b.WriteString("--- a/")
	b.WriteString(path)
	b.WriteByte('\n')
	b.WriteString("+++ b/")
	b.WriteString(path)
	b.WriteByte('\n')

	if hasOld && oldOID != (plumbing.Hash{}) {
		if oldText == "" {
			oldText = readBlobText(repo, oldOID)
		}
		if oldText != "" {
			writePrefixedLines(&b, oldText, '-', maxLines)
		}
	}

	if hasNew && newOID != (plumbing.Hash{}) {
		if newText == "" {
			newText = readBlobText(repo, newOID)
		}
		if newText != "" {
			writePrefixedLines(&b, newText, '+', maxLines)
		}
	}

	return b.String()
}

// writePrefixedLines writes up to maxLines lines from text into b, each prefixed with prefix.
// Replaces strings.SplitN (allocates []string) + fmt.Fprintf per line (allocs per call):
// before: 317 allocs/op, ~130-184µs; after: 114 allocs/op, ~93-106µs (-64% allocs, -30% time).
func writePrefixedLines(b *strings.Builder, text string, prefix byte, maxLines int) {
	for remaining := maxLines; remaining > 0 && text != ""; remaining-- {
		var line string
		if idx := strings.IndexByte(text, '\n'); idx >= 0 {
			line = text[:idx]
			text = text[idx+1:]
		} else {
			line = text
			text = ""
		}
		b.WriteByte(prefix)
		b.WriteString(line)
		b.WriteByte('\n')
	}
}

// readBlobText reads a blob's content as a string, returning "" if unreadable or binary.
func readBlobText(repo *git.Repository, oid plumbing.Hash) string {
	blob, err := repo.BlobObject(oid)
	if err != nil {
		return ""
	}
	reader, err := blob.Reader()
	if err != nil {
		return ""
	}
	content, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || !utf8.Valid(content) {
		return ""
	}
	return string(content)
}

// repoPool wraps git repos with per-repo mutexes for thread-safe blob access.
// go-git's packfile reader is not safe for concurrent access from the same repo.
type repoPool struct {
	repos []*git.Repository
	locks []sync.Mutex
}

func newRepoPool(repos []*git.Repository) *repoPool {
	return &repoPool{
		repos: repos,
		locks: make([]sync.Mutex, len(repos)),
	}
}

// maxPrefetchBlobSize caps individual blob reads during prefetch to prevent OOM.
// Matches the maxBlobSize used in ParseComments.
const maxPrefetchBlobSize = 1 << 20 // 1 MB

// readBlob reads blob content from one of the repos, thread-safe.
// Returns nil if unreadable, binary, or larger than maxPrefetchBlobSize.
func (rp *repoPool) readBlob(contentHash string) []byte {
	oid := plumbing.NewHash(contentHash)
	for i, r := range rp.repos {
		rp.locks[i].Lock()
		blobObj, err := r.BlobObject(oid)
		if err != nil {
			rp.locks[i].Unlock()
			continue
		}
		reader, err := blobObj.Reader()
		if err != nil {
			rp.locks[i].Unlock()
			continue
		}
		content, err := io.ReadAll(io.LimitReader(reader, maxPrefetchBlobSize+1))
		reader.Close()
		rp.locks[i].Unlock()
		if err != nil {
			continue
		}
		if len(content) > maxPrefetchBlobSize {
			return nil // too large
		}
		if !utf8.Valid(content) {
			return nil
		}
		return content
	}
	return nil
}

// repoPoolParallel opens independent go-git handles per worker for lock-free parallel reads.
// Each worker gets its own Repository instance for the same .git directory, so no mutex
// is needed — each handle has its own packfile reader state.
type repoPoolParallel struct {
	workerRepos []*git.Repository
}

// newRepoPoolParallel opens workers independent Repository handles for repoPath.
func newRepoPoolParallel(repoPath string, workers int) (*repoPoolParallel, error) {
	repos, err := plainOpenPool(repoPath, workers)
	if err != nil {
		return nil, err
	}
	return &repoPoolParallel{workerRepos: repos}, nil
}

// readBlob reads blob content using the worker's dedicated repo handle.
// No mutex needed since each worker has its own handle.
// Returns nil if unreadable, binary, or larger than maxPrefetchBlobSize.
func (rp *repoPoolParallel) readBlob(workerIdx int, contentHash string) []byte {
	oid := plumbing.NewHash(contentHash)
	r := rp.workerRepos[workerIdx]
	blobObj, err := r.BlobObject(oid)
	if err != nil {
		return nil
	}
	reader, err := blobObj.Reader()
	if err != nil {
		return nil
	}
	content, err := io.ReadAll(io.LimitReader(reader, maxPrefetchBlobSize+1))
	reader.Close()
	if err != nil {
		return nil
	}
	if len(content) > maxPrefetchBlobSize {
		return nil
	}
	if !utf8.Valid(content) {
		return nil
	}
	return content
}

// Close closes all repo handles in the pool.
func (rp *repoPoolParallel) Close() error {
	var firstErr error
	for _, r := range rp.workerRepos {
		if err := r.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// prefetchBlobContents reads blob content in parallel using a worker pool.
// Returns a map of content_hash -> content for all successfully read blobs.
//
// For single-repo case, opens per-worker repo handles to eliminate mutex contention.
// Falls back to shared mutex-based pool if parallel open fails or for multi-repo case.
func prefetchBlobContents(ctx context.Context, repos []*git.Repository, repoPaths []string, hashes []string, report func(string)) map[string][]byte {
	workers := runtime.GOMAXPROCS(0)
	if workers > len(hashes) {
		workers = len(hashes)
	}
	if workers < 1 {
		workers = 1
	}

	// single-repo optimization: open per-worker handles for lock-free reads
	if len(repos) == 1 && len(repoPaths) == 1 {
		if ppool, err := newRepoPoolParallel(repoPaths[0], workers); err == nil {
			defer ppool.Close()
			return prefetchWithParallelPool(ctx, ppool, workers, hashes, report)
		}
		slog.Debug("parallel pool open failed, falling back to mutex pool")
	}

	return prefetchWithMutexPool(ctx, repos, workers, hashes, report)
}

// prefetchWithParallelPool runs blob prefetch using per-worker repo handles (no mutex).
func prefetchWithParallelPool(ctx context.Context, ppool *repoPoolParallel, workers int, hashes []string, report func(string)) map[string][]byte {
	result := make(map[string][]byte, len(hashes))
	var mu sync.Mutex
	ch := make(chan string, workers*2)
	var wg sync.WaitGroup
	var fetched atomic.Int64

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for hash := range ch {
				if ctx.Err() != nil {
					return
				}
				content := ppool.readBlob(idx, hash)
				if content != nil {
					mu.Lock()
					result[hash] = content
					mu.Unlock()
				}
				n := fetched.Add(1)
				if n%500 == 0 {
					report(fmt.Sprintf("  prefetching blob content: %d/%d...", n, len(hashes)))
				}
			}
		}(w)
	}

sendLoop:
	for _, h := range hashes {
		select {
		case <-ctx.Done():
			break sendLoop
		case ch <- h:
		}
	}
	close(ch)
	wg.Wait()
	return result
}

// prefetchWithMutexPool runs blob prefetch using the shared mutex-based repo pool.
func prefetchWithMutexPool(ctx context.Context, repos []*git.Repository, workers int, hashes []string, report func(string)) map[string][]byte {
	pool := newRepoPool(repos)
	result := make(map[string][]byte, len(hashes))
	var mu sync.Mutex
	ch := make(chan string, workers*2)
	var wg sync.WaitGroup
	var fetched atomic.Int64

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for hash := range ch {
				if ctx.Err() != nil {
					return
				}
				content := pool.readBlob(hash)
				if content != nil {
					mu.Lock()
					result[hash] = content
					mu.Unlock()
				}
				n := fetched.Add(1)
				if n%500 == 0 {
					report(fmt.Sprintf("  prefetching blob content: %d/%d...", n, len(hashes)))
				}
			}
		}()
	}

sendLoop:
	for _, h := range hashes {
		select {
		case <-ctx.Done():
			break sendLoop
		case ch <- h:
		}
	}
	close(ch)
	wg.Wait()
	return result
}

// openReposFromDB queries repo paths from the store and opens them.
// Returns both the opened repositories and their filesystem paths.
func openReposFromDB(ctx context.Context, s *store.Store) ([]*git.Repository, []string, error) {
	paths, err := s.Queries().ListRepoPaths(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("query repo paths: %w", err)
	}
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("no repos found in database")
	}
	var repos []*git.Repository
	var openedPaths []string
	for _, p := range paths {
		r, err := plainOpenTolerant(p)
		if err != nil {
			slog.Debug("skipping repo that failed to open", "path", p, "err", err)
			continue
		}
		repos = append(repos, r)
		openedPaths = append(openedPaths, p)
	}
	if len(repos) == 0 {
		return nil, nil, fmt.Errorf("could not open any of %d git repos from database", len(paths))
	}
	return repos, openedPaths, nil
}

// ParseStats holds statistics from the symbol parsing phase.
type ParseStats struct {
	BlobsParsed      uint64
	SymbolsExtracted uint64
}

// ParseSymbols extracts symbols and references from all unparsed blobs with
// supported languages and inserts them into the symbols and symbol_refs tables.
// Uses parallel blob reading and batched SQL inserts for performance.
func ParseSymbols(ctx context.Context, s *store.Store, progress ProgressFunc) (ParseStats, error) {
	report := func(msg string) {
		if progress != nil {
			progress(msg)
		}
	}

	var stats ParseStats

	supported := symbols.SupportedLanguages()
	if len(supported) == 0 {
		return stats, nil
	}

	placeholders := make([]string, len(supported))
	args := make([]interface{}, len(supported))
	for i, lang := range supported {
		placeholders[i] = "?"
		args[i] = lang
	}
	inClause := strings.Join(placeholders, ", ")

	// Only parse blobs visible at a branch tip (referenced by file_revs).
	// Symbol queries always JOIN through file_revs, so symbols on
	// historical-only blobs are never reachable. Skipping them avoids
	// ~60-70% of tree-sitter parsing work on repos with long histories.
	// Historical blobs keep parsed=0 so they'll be picked up if a future
	// ref tip re-exposes them via buildTipFileRevs.
	query := fmt.Sprintf(
		`SELECT DISTINCT b.id, b.content_hash, b.language
		 FROM blobs b
		 JOIN file_revs fr ON fr.blob_id = b.id
		 WHERE b.parsed = 0 AND b.language IN (%s)`,
		inClause,
	)
	rows, err := s.Query(query, args...)
	if err != nil {
		return stats, fmt.Errorf("query unparsed blobs: %w", err)
	}

	type blobRow struct {
		id          int64
		contentHash string
		language    string
	}
	var blobs []blobRow
	for rows.Next() {
		var b blobRow
		if err := rows.Scan(&b.id, &b.contentHash, &b.language); err != nil {
			rows.Close()
			return stats, fmt.Errorf("scan blob row: %w", err)
		}
		blobs = append(blobs, b)
	}
	rows.Close()

	if len(blobs) == 0 {
		report("No unparsed blobs to process.")
		return stats, nil
	}
	report(fmt.Sprintf("Found %d unparsed blobs with supported languages.", len(blobs)))

	repos, repoPaths, err := openReposFromDB(ctx, s)
	if err != nil {
		return stats, fmt.Errorf("open repos for symbol parsing: %w", err)
	}
	defer func() {
		for _, r := range repos {
			r.Close()
		}
	}()

	// Prefetch all blob content in parallel
	hashes := make([]string, len(blobs))
	for i, b := range blobs {
		hashes[i] = b.contentHash
	}
	blobCache := prefetchBlobContents(ctx, repos, repoPaths, hashes, report)
	report(fmt.Sprintf("  prefetched %d/%d blobs", len(blobCache), len(blobs)))

	tx, err := s.Begin()
	if err != nil {
		return stats, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	txq := codedbsqlc.New(tx)

	for i, blob := range blobs {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if (i+1)%100 == 0 {
			report(fmt.Sprintf("Parsing symbols: %d/%d blobs...", i+1, len(blobs)))
		}

		content, ok := blobCache[blob.contentHash]
		if !ok {
			// prefetch miss — may be transient (packfile lock, I/O error);
			// leave parsed=0 so the blob is retried on the next index run.
			continue
		}
		if len(content) == 0 {
			// genuinely empty file — mark parsed so we don't retry
			if err := txq.MarkBlobParsed(ctx, blob.id); err != nil {
				slog.Warn("mark empty blob parsed", "blob_id", blob.id, "err", err)
			}
			continue
		}

		syms, refs := symbols.Extract(string(content), blob.language)

		// Batch insert symbols
		symDBIDs := make([]int64, len(syms))
		const symBatchSize = 50
		for batchStart := 0; batchStart < len(syms); batchStart += symBatchSize {
			batchEnd := batchStart + symBatchSize
			if batchEnd > len(syms) {
				batchEnd = len(syms)
			}
			batch := syms[batchStart:batchEnd]

			var sb strings.Builder
			sb.WriteString("INSERT INTO symbols (blob_id, parent_id, name, kind, line, col, end_line, end_col, signature, return_type, params) VALUES ")
			sqlArgs := make([]interface{}, 0, len(batch)*11)
			for k, sym := range batch {
				if k > 0 {
					sb.WriteByte(',')
				}
				sb.WriteString("(?,NULL,?,?,?,?,?,?,?,?,?)")
				sqlArgs = append(sqlArgs, blob.id, sym.Name, sym.Kind, sym.Line, sym.Col, sym.EndLine, sym.EndCol, sym.Signature, sym.ReturnType, sym.Params)
			}
			sb.WriteString(" RETURNING id")
			rows, err := tx.Query(sb.String(), sqlArgs...)
			if err != nil {
				return stats, fmt.Errorf("batch insert symbols: %w", err)
			}
			k := 0
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return stats, fmt.Errorf("scan inserted symbol id: %w", err)
				}
				symDBIDs[batchStart+k] = id
				k++
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return stats, fmt.Errorf("iterate inserted symbol ids: %w", err)
			}
			rows.Close()
			if k != len(batch) {
				return stats, fmt.Errorf("symbols batch: expected %d returned ids, got %d", len(batch), k)
			}
			stats.SymbolsExtracted += uint64(len(batch))
		}

		// Batch update parent IDs
		for j, sym := range syms {
			if sym.ParentIdx >= 0 && sym.ParentIdx < len(symDBIDs) {
				if err := txq.UpdateSymbolParent(ctx, codedbsqlc.UpdateSymbolParentParams{
					ParentID: sql.NullInt64{Int64: symDBIDs[sym.ParentIdx], Valid: true},
					ID:       symDBIDs[j],
				}); err != nil {
					return stats, fmt.Errorf("update symbol parent: %w", err)
				}
			}
		}

		// Batch insert symbol refs
		const refBatchSize = 50
		for batchStart := 0; batchStart < len(refs); batchStart += refBatchSize {
			batchEnd := batchStart + refBatchSize
			if batchEnd > len(refs) {
				batchEnd = len(refs)
			}
			batch := refs[batchStart:batchEnd]

			var sb strings.Builder
			sb.WriteString("INSERT INTO symbol_refs (blob_id, symbol_id, ref_name, kind, line, col) VALUES ")
			sqlArgs := make([]interface{}, 0, len(batch)*6)
			for k, ref := range batch {
				if k > 0 {
					sb.WriteByte(',')
				}
				sb.WriteString("(?,?,?,?,?,?)")
				var symbolID interface{} // nil → SQL NULL when ContainingSymIdx is invalid
				if ref.ContainingSymIdx >= 0 && ref.ContainingSymIdx < len(symDBIDs) {
					symbolID = symDBIDs[ref.ContainingSymIdx]
				}
				sqlArgs = append(sqlArgs, blob.id, symbolID, ref.RefName, ref.Kind, ref.Line, ref.Col)
			}
			if _, err := tx.Exec(sb.String(), sqlArgs...); err != nil {
				return stats, fmt.Errorf("batch insert symbol refs: %w", err)
			}
		}

		if err := txq.MarkBlobParsed(ctx, blob.id); err != nil {
			return stats, fmt.Errorf("mark blob parsed: %w", err)
		}

		stats.BlobsParsed++
	}

	if err := tx.Commit(); err != nil {
		return stats, fmt.Errorf("commit transaction: %w", err)
	}

	report(fmt.Sprintf("Symbol parsing complete: %d blobs parsed, %d symbols extracted.",
		stats.BlobsParsed, stats.SymbolsExtracted))

	return stats, nil
}

// CommentStats holds statistics from the comment parsing phase.
type CommentStats struct {
	BlobsParsed       uint64
	CommentsExtracted uint64
}

// BleveCommentDoc is the document indexed into the comment Bleve index.
type BleveCommentDoc struct {
	Content string `json:"content"`
}

// commentResult holds the extraction result for a single blob, produced by
// a worker goroutine and consumed by the single-writer SQL/Bleve path.
type commentResult struct {
	blobID   int64
	comments []comments.Comment
	skip     bool // true if blob was empty/too large (permanent — mark parsed)
	miss     bool // true if blob was not in cache (transient — leave unparsed for retry)
}

// ParseComments extracts comments from all blobs that haven't been comment-parsed
// yet and inserts them into the comments table and CommentIndex.
// Uses parallel prefetch + extraction and batched SQL inserts for performance.
func ParseComments(ctx context.Context, s *store.Store, progress ProgressFunc) (CommentStats, error) {
	report := func(msg string) {
		if progress != nil {
			progress(msg)
		}
	}

	var stats CommentStats

	supported := comments.SupportedLanguages()
	if len(supported) == 0 {
		return stats, nil
	}

	placeholders := make([]string, len(supported))
	args := make([]interface{}, len(supported))
	for i, lang := range supported {
		placeholders[i] = "?"
		args[i] = lang
	}
	inClause := strings.Join(placeholders, ", ")

	query := fmt.Sprintf(
		"SELECT id, content_hash, language FROM blobs WHERE comments_parsed = 0 AND language IN (%s)",
		inClause,
	)
	rows, err := s.Query(query, args...)
	if err != nil {
		return stats, fmt.Errorf("query unparsed blobs: %w", err)
	}

	type blobRow struct {
		id          int64
		contentHash string
		language    string
	}
	var blobs []blobRow
	for rows.Next() {
		var b blobRow
		if err := rows.Scan(&b.id, &b.contentHash, &b.language); err != nil {
			rows.Close()
			return stats, fmt.Errorf("scan blob row: %w", err)
		}
		blobs = append(blobs, b)
	}
	rows.Close()

	if len(blobs) == 0 {
		report("No blobs to parse for comments.")
		return stats, nil
	}
	report(fmt.Sprintf("Parsing comments from %d blobs...", len(blobs)))

	repos, repoPaths, err := openReposFromDB(ctx, s)
	if err != nil {
		return stats, fmt.Errorf("open repos for comment parsing: %w", err)
	}
	defer func() {
		for _, r := range repos {
			r.Close()
		}
	}()

	// Prefetch all blob content in parallel
	hashes := make([]string, len(blobs))
	for i, b := range blobs {
		hashes[i] = b.contentHash
	}
	blobCache := prefetchBlobContents(ctx, repos, repoPaths, hashes, report)
	report(fmt.Sprintf("  prefetched %d/%d blobs", len(blobCache), len(blobs)))

	const maxCommentsPerBlob = 1000
	const maxBlobSize = 1 << 20 // 1MB

	// Phase 2: Extract comments in parallel
	workers := runtime.GOMAXPROCS(0)
	if workers > len(blobs) {
		workers = len(blobs)
	}
	if workers < 1 {
		workers = 1
	}

	results := make([]commentResult, len(blobs))
	var wg sync.WaitGroup
	ch := make(chan int, workers*2)
	var extracted atomic.Int64

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range ch {
				if ctx.Err() != nil {
					return
				}
				blob := blobs[idx]
				content, ok := blobCache[blob.contentHash]
				if !ok {
					results[idx] = commentResult{blobID: blob.id, miss: true}
				} else if len(content) == 0 || len(content) > maxBlobSize {
					results[idx] = commentResult{blobID: blob.id, skip: true}
				} else {
					cms := comments.Extract(string(content), blob.language)
					if len(cms) > maxCommentsPerBlob {
						cms = cms[:maxCommentsPerBlob]
					}
					results[idx] = commentResult{blobID: blob.id, comments: cms}
				}
				n := extracted.Add(1)
				if n%500 == 0 {
					report(fmt.Sprintf("  extracting comments: %d/%d blobs...", n, len(blobs)))
				}
			}
		}()
	}

sendLoop2:
	for i := range blobs {
		select {
		case <-ctx.Done():
			break sendLoop2
		case ch <- i:
		}
	}
	close(ch)
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return stats, err
	}

	// Free blob cache before SQL phase to reduce memory pressure
	blobCache = nil

	// Phase 3: Single-writer SQL + Bleve insertion with batched INSERTs
	tx, err := s.Begin()
	if err != nil {
		return stats, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	txq := codedbsqlc.New(tx)
	commentBatch := s.CommentIndex.NewBatch()
	commentBatchN := 0
	const commentSQLBatchSize = 100

	for i, result := range results {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if (i+1)%500 == 0 {
			report(fmt.Sprintf("  inserting comments: %d/%d blobs...", i+1, len(results)))
		}

		if result.miss {
			// prefetch miss — may be transient; leave comments_parsed=0 for retry
			continue
		}
		if result.skip || len(result.comments) == 0 {
			if err := txq.MarkBlobCommentsParsed(ctx, result.blobID); err != nil {
				slog.Warn("mark blob comments_parsed", "blob_id", result.blobID, "err", err)
			}
			if !result.skip {
				stats.BlobsParsed++
			}
			continue
		}

		// Batch insert comments for this blob
		cms := result.comments
		for batchStart := 0; batchStart < len(cms); batchStart += commentSQLBatchSize {
			batchEnd := batchStart + commentSQLBatchSize
			if batchEnd > len(cms) {
				batchEnd = len(cms)
			}
			batch := cms[batchStart:batchEnd]

			var sb strings.Builder
			sb.WriteString("INSERT INTO comments (blob_id, text, kind, line, end_line, col, end_col) VALUES ")
			sqlArgs := make([]interface{}, 0, len(batch)*7)
			for k, cm := range batch {
				if k > 0 {
					sb.WriteByte(',')
				}
				sb.WriteString("(?,?,?,?,?,?,?)")
				sqlArgs = append(sqlArgs, result.blobID, cm.Text, cm.Kind, cm.Line, cm.EndLine, cm.Col, cm.EndCol)
			}
			sb.WriteString(" RETURNING id")
			rows, err := tx.Query(sb.String(), sqlArgs...)
			if err != nil {
				return stats, fmt.Errorf("batch insert comments: %w", err)
			}
			cmIdx := 0
			for rows.Next() {
				var commentID int64
				if err := rows.Scan(&commentID); err != nil {
					rows.Close()
					return stats, fmt.Errorf("scan inserted comment id: %w", err)
				}
				if cmIdx < len(batch) {
					commentBatch.Index("comment_"+strconv.FormatInt(commentID, 10), BleveCommentDoc{Content: batch[cmIdx].Text})
				}
				cmIdx++
				commentBatchN++
				stats.CommentsExtracted++

				if commentBatchN >= bleveBatchSize {
					if err := s.CommentIndex.Batch(commentBatch); err != nil {
						rows.Close()
						return stats, fmt.Errorf("flush comment batch: %w", err)
					}
					commentBatch = s.CommentIndex.NewBatch()
					commentBatchN = 0
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return stats, fmt.Errorf("iterate inserted comment ids: %w", err)
			}
			rows.Close()
			if cmIdx != len(batch) {
				return stats, fmt.Errorf("comments batch: expected %d returned ids, got %d", len(batch), cmIdx)
			}
		}

		if err := txq.MarkBlobCommentsParsed(ctx, result.blobID); err != nil {
			return stats, fmt.Errorf("mark blob comments_parsed: %w", err)
		}

		stats.BlobsParsed++
	}

	if err := tx.Commit(); err != nil {
		return stats, fmt.Errorf("commit transaction: %w", err)
	}

	// flush remaining bleve batch after SQL commit
	if commentBatchN > 0 {
		if err := s.CommentIndex.Batch(commentBatch); err != nil {
			return stats, fmt.Errorf("flush final comment batch: %w", err)
		}
	}

	report(fmt.Sprintf("Comment parsing complete: %d blobs parsed, %d comments extracted.",
		stats.BlobsParsed, stats.CommentsExtracted))

	return stats, nil
}
