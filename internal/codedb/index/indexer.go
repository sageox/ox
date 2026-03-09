package index

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/blevesearch/bleve/v2"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/sageox/ox/internal/codedb/language"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/codedb/symbols"
)

// ProgressFunc is called with status messages during indexing.
type ProgressFunc func(msg string)

// IndexOptions configures the indexing process.
type IndexOptions struct {
	MaxHistoryDepth int // 0 = unlimited
	Progress        ProgressFunc
}

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

const bleveBatchSize = 200

// indexState holds mutable state shared across indexing sub-operations.
type indexState struct {
	tx           *sql.Tx
	store        *store.Store
	repo         *git.Repository
	repoID       int64
	codeBatch    *bleve.Batch
	diffBatch    *bleve.Batch
	codeBatchN   int
	diffBatchN   int
	knownCommits map[string]bool
	treeCache    map[plumbing.Hash]map[string]plumbing.Hash
	newCommits   int
	newBlobs     int
	report       func(string)
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
func (st *indexState) flushDiffBatch(force bool) error {
	if st.diffBatchN == 0 {
		return nil
	}
	if !force && st.diffBatchN < bleveBatchSize {
		return nil
	}
	if err := st.store.DiffIndex.Batch(st.diffBatch); err != nil {
		return fmt.Errorf("flush diff batch: %w", err)
	}
	st.diffBatch = st.store.DiffIndex.NewBatch()
	st.diffBatchN = 0
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
	report("Cloning/fetching repository...")
	dirName, err := RepoDirFromURL(url)
	if err != nil {
		return err
	}
	repoPath := filepath.Join(s.ReposDir(), dirName)
	repo, err := CloneOrFetch(url, repoPath)
	if err != nil {
		return fmt.Errorf("clone/fetch %s: %w", url, err)
	}

	// 2. Upsert repo record
	repoName, err := RepoNameFromURL(url)
	if err != nil {
		return err
	}
	repoID, err := upsertRepo(s, repoName, repoPath)
	if err != nil {
		return err
	}

	// 3. Load known commits
	knownCommits, err := loadKnownCommits(s, repoID)
	if err != nil {
		return err
	}

	// 4. List refs
	report("Listing refs...")
	refList, err := listResolvedRefs(repo)
	if err != nil {
		return err
	}
	report(fmt.Sprintf("Found %d refs.", len(refList)))

	// 5. Begin transaction
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	st := &indexState{
		tx:           tx,
		store:        s,
		repo:         repo,
		repoID:       repoID,
		codeBatch:    s.CodeIndex.NewBatch(),
		diffBatch:    s.DiffIndex.NewBatch(),
		knownCommits: knownCommits,
		treeCache:    make(map[plumbing.Hash]map[string]plumbing.Hash),
		report:       report,
	}

	// 6. Process each ref
	for refIdx, ri := range refList {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := st.processRef(ctx, ri, refIdx, len(refList), opts.MaxHistoryDepth); err != nil {
			return err
		}
	}

	report(fmt.Sprintf("Indexing complete: %d new commits, %d new blobs.", st.newCommits, st.newBlobs))

	// 7. Flush remaining batches and commit transaction
	report("Committing indexes...")
	if err := st.flushCodeBatch(true); err != nil {
		return err
	}
	if err := st.flushDiffBatch(true); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

// upsertRepo inserts or updates a repo record and returns its ID.
func upsertRepo(s *store.Store, name, path string) (int64, error) {
	_, err := s.Exec(
		`INSERT INTO repos (name, path) VALUES (?, ?)
		 ON CONFLICT(name) DO UPDATE SET path = excluded.path`,
		name, path,
	)
	if err != nil {
		return 0, fmt.Errorf("upsert repo: %w", err)
	}
	var id int64
	err = s.QueryRow("SELECT id FROM repos WHERE name = ?", name).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("get repo id: %w", err)
	}
	return id, nil
}

// loadKnownCommits returns a set of commit hashes already indexed for a repo.
func loadKnownCommits(s *store.Store, repoID int64) (map[string]bool, error) {
	known := make(map[string]bool)
	rows, err := s.Query("SELECT hash FROM commits WHERE repo_id = ?", repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		known[h] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return known, nil
}

// listResolvedRefs returns all refs with symbolic references resolved to their target.
func listResolvedRefs(repo *git.Repository) ([]refInfo, error) {
	var refList []refInfo
	refs, err := repo.References()
	if err != nil {
		return nil, fmt.Errorf("list refs: %w", err)
	}
	refs.ForEach(func(ref *plumbing.Reference) error {
		resolved := ref
		if ref.Type() == plumbing.SymbolicReference {
			r, err := repo.Reference(ref.Name(), true)
			if err != nil {
				return nil // skip broken refs
			}
			resolved = r
		}
		refList = append(refList, refInfo{
			name:   ref.Name().String(),
			tipOID: resolved.Hash(),
		})
		return nil
	})
	return refList, nil
}

// walkNewCommits discovers commits reachable from tip that aren't in knownCommits.
// Returns commits in oldest-first order. Respects maxDepth (0 = unlimited).
func walkNewCommits(repo *git.Repository, tip plumbing.Hash, knownCommits map[string]bool, maxDepth int) ([]commitData, bool) {
	var newCommits []commitData
	visited := make(map[plumbing.Hash]bool)
	walkStack := []plumbing.Hash{tip}
	depthTruncated := false

	for len(walkStack) > 0 {
		if maxDepth > 0 && len(newCommits) >= maxDepth {
			depthTruncated = true
			break
		}
		oid := walkStack[len(walkStack)-1]
		walkStack = walkStack[:len(walkStack)-1]

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
			walkStack = append(walkStack, p)
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

	// Reverse for oldest-first processing
	for i, j := 0, len(newCommits)-1; i < j; i, j = i+1, j-1 {
		newCommits[i], newCommits[j] = newCommits[j], newCommits[i]
	}

	return newCommits, depthTruncated
}

// processRef walks a single ref's history and indexes new commits, diffs, and file_revs.
func (st *indexState) processRef(ctx context.Context, ri refInfo, refIdx, totalRefs, maxDepth int) error {
	newCommits, depthTruncated := walkNewCommits(st.repo, ri.tipOID, st.knownCommits, maxDepth)

	if len(newCommits) > 0 {
		st.report(fmt.Sprintf("Ref %d/%d: %s — %d new commits",
			refIdx+1, totalRefs, ri.name, len(newCommits)))
	}
	if depthTruncated {
		st.report(fmt.Sprintf("Warning: history depth limit (%d) reached for ref %s.",
			maxDepth, ri.name))
	}

	for _, cd := range newCommits {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := st.indexCommit(cd); err != nil {
			return err
		}
	}

	// Build file_revs for tip
	return st.buildTipFileRevs(ri)
}

// indexCommit inserts a single commit and its diffs into the database and Bleve.
func (st *indexState) indexCommit(cd commitData) error {
	oidHex := cd.oid.String()

	_, err := st.tx.Exec(
		`INSERT OR IGNORE INTO commits (repo_id, hash, author, message, timestamp)
		 VALUES (?, ?, ?, ?, ?)`,
		st.repoID, oidHex, cd.author, cd.message, cd.timestamp,
	)
	if err != nil {
		return fmt.Errorf("insert commit: %w", err)
	}

	var commitDBID int64
	err = st.tx.QueryRow("SELECT id FROM commits WHERE hash = ?", oidHex).Scan(&commitDBID)
	if err != nil {
		return fmt.Errorf("get commit id: %w", err)
	}

	st.newCommits++
	if st.newCommits%500 == 0 {
		st.report(fmt.Sprintf("Processed %d commits, %d new blobs...", st.newCommits, st.newBlobs))
	}

	// Insert parent links
	if err := st.insertParentLinks(commitDBID, cd.parentIDs); err != nil {
		return err
	}

	// Evict tree cache periodically to bound memory usage.
	// 32 entries ~ 32 full tree snapshots; keeps memory reasonable for large repos
	// while avoiding re-parsing recently seen trees.
	if len(st.treeCache) >= 32 {
		st.treeCache = make(map[plumbing.Hash]map[string]plumbing.Hash)
	}

	childEntries, err := getTreeEntries(st.repo, cd.treeHash, st.treeCache)
	if err != nil {
		return fmt.Errorf("get tree entries: %w", err)
	}

	parentEntries := make(map[string]plumbing.Hash)
	if len(cd.parentIDs) > 0 {
		parentCommit, pErr := st.repo.CommitObject(cd.parentIDs[0])
		if pErr == nil {
			if pe, peErr := getTreeEntries(st.repo, parentCommit.TreeHash, st.treeCache); peErr == nil {
				parentEntries = pe
			}
		}
	}

	// Index changed and new files
	if err := st.indexChangedFiles(commitDBID, childEntries, parentEntries); err != nil {
		return err
	}

	// Index deleted files
	if err := st.indexDeletedFiles(commitDBID, childEntries, parentEntries); err != nil {
		return err
	}

	st.knownCommits[oidHex] = true
	return nil
}

// insertParentLinks records commit parent relationships.
func (st *indexState) insertParentLinks(commitDBID int64, parentIDs []plumbing.Hash) error {
	for _, parentOID := range parentIDs {
		var parentDBID int64
		err := st.tx.QueryRow("SELECT id FROM commits WHERE hash = ?", parentOID.String()).Scan(&parentDBID)
		if err == nil {
			if _, err := st.tx.Exec("INSERT OR IGNORE INTO commit_parents (commit_id, parent_id) VALUES (?, ?)",
				commitDBID, parentDBID); err != nil {
				return fmt.Errorf("insert commit parent: %w", err)
			}
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

		newBlobDBID, indexed, err := st.ensureBlob(childBlobOID, path)
		if err != nil {
			return err
		}
		if indexed {
			st.newBlobs++
		}

		var oldBlobDBID sql.NullInt64
		if existsInParent {
			id, indexed, err := st.ensureBlob(parentBlobOID, path)
			if err != nil {
				return err
			}
			if indexed {
				st.newBlobs++
			}
			oldBlobDBID = sql.NullInt64{Int64: id, Valid: true}
		}

		if err := st.insertDiff(commitDBID, path, oldBlobDBID, newBlobDBID, parentBlobOID, childBlobOID, existsInParent, true); err != nil {
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
		oldBlobDBID, indexed, err := st.ensureBlob(parentBlobOID, path)
		if err != nil {
			return err
		}
		if indexed {
			st.newBlobs++
		}

		nullBlob := sql.NullInt64{}
		if err := st.insertDiff(commitDBID, path, sql.NullInt64{Int64: oldBlobDBID, Valid: true}, nullBlob.Int64, parentBlobOID, plumbing.ZeroHash, true, false); err != nil {
			return err
		}
	}
	return nil
}

// insertDiff inserts a diff record and indexes the diff text in Bleve.
func (st *indexState) insertDiff(commitDBID int64, path string, oldBlobDBID sql.NullInt64, newBlobDBID int64, oldOID, newOID plumbing.Hash, hasOld, hasNew bool) error {
	var newBlobPtr interface{}
	if hasNew {
		newBlobPtr = newBlobDBID
	}

	_, err := st.tx.Exec(
		`INSERT OR IGNORE INTO diffs (commit_id, path, old_blob_id, new_blob_id)
		 VALUES (?, ?, ?, ?)`,
		commitDBID, path, oldBlobDBID, newBlobPtr,
	)
	if err != nil {
		return fmt.Errorf("insert diff: %w", err)
	}

	var diffDBID int64
	err = st.tx.QueryRow("SELECT id FROM diffs WHERE commit_id = ? AND path = ?",
		commitDBID, path).Scan(&diffDBID)
	if err != nil {
		return nil // non-fatal: skip Bleve indexing for this diff
	}

	diffText := generateDiffText(st.repo, path, oldOID, newOID, hasOld, hasNew)
	if diffText != "" {
		st.diffBatch.Index(fmt.Sprintf("diff_%d", diffDBID), BleveDiffDoc{Content: diffText})
		st.diffBatchN++
		if err := st.flushDiffBatch(false); err != nil {
			return err
		}
	}
	return nil
}

// buildTipFileRevs rebuilds the file_revs table for a ref's tip commit.
func (st *indexState) buildTipFileRevs(ri refInfo) error {
	tipHex := ri.tipOID.String()
	var tipCommitDBID int64
	err := st.tx.QueryRow("SELECT id FROM commits WHERE hash = ?", tipHex).Scan(&tipCommitDBID)
	if err != nil {
		return nil // skip if tip not indexed
	}

	if _, err := st.tx.Exec("DELETE FROM file_revs WHERE commit_id = ?", tipCommitDBID); err != nil {
		return fmt.Errorf("delete file_revs: %w", err)
	}

	var tipEntries map[string]plumbing.Hash
	tipCommit, tErr := st.repo.CommitObject(ri.tipOID)
	if tErr == nil {
		tipEntries, _ = getTreeEntries(st.repo, tipCommit.TreeHash, st.treeCache)
	}
	if tipEntries == nil {
		tipEntries = make(map[string]plumbing.Hash)
	}

	for path, blobOID := range tipEntries {
		blobDBID, indexed, err := st.ensureBlob(blobOID, path)
		if err != nil {
			continue
		}
		if indexed {
			st.newBlobs++
		}
		if _, err := st.tx.Exec("INSERT OR IGNORE INTO file_revs (commit_id, path, blob_id) VALUES (?, ?, ?)",
			tipCommitDBID, path, blobDBID); err != nil {
			return fmt.Errorf("insert file_rev: %w", err)
		}
	}

	// Upsert ref
	if _, err := st.tx.Exec(
		`INSERT INTO refs (repo_id, name, commit_id) VALUES (?, ?, ?)
		 ON CONFLICT(repo_id, name) DO UPDATE SET commit_id = excluded.commit_id`,
		st.repoID, ri.name, tipCommitDBID,
	); err != nil {
		return fmt.Errorf("upsert ref: %w", err)
	}

	return nil
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
// in Bleve if it hasn't been indexed yet (parsed == 0).
// Returns (blobDBID, indexedInBleve, error).
func (st *indexState) ensureBlob(blobOID plumbing.Hash, path string) (int64, bool, error) {
	contentHash := blobOID.String()
	lang := language.Detect(path)

	var langPtr *string
	if lang != "" {
		langPtr = &lang
	}

	_, err := st.tx.Exec(
		"INSERT OR IGNORE INTO blobs (content_hash, language) VALUES (?, ?)",
		contentHash, langPtr,
	)
	if err != nil {
		return 0, false, fmt.Errorf("insert blob: %w", err)
	}

	var blobDBID int64
	err = st.tx.QueryRow("SELECT id FROM blobs WHERE content_hash = ?", contentHash).Scan(&blobDBID)
	if err != nil {
		return 0, false, fmt.Errorf("get blob id: %w", err)
	}

	var parsed int
	if err := st.tx.QueryRow("SELECT parsed FROM blobs WHERE id = ?", blobDBID).Scan(&parsed); err != nil && err != sql.ErrNoRows {
		return 0, false, fmt.Errorf("check parsed status: %w", err)
	}

	indexed := false
	blobObj, bErr := st.repo.BlobObject(blobOID)
	if bErr == nil && parsed == 0 {
		reader, rErr := blobObj.Reader()
		if rErr == nil {
			content, readErr := io.ReadAll(reader)
			reader.Close()
			if readErr == nil && utf8.Valid(content) && len(content) > 0 {
				st.codeBatch.Index(fmt.Sprintf("blob_%d", blobDBID), BleveCodeDoc{Content: string(content)})
				st.codeBatchN++
				indexed = true
				if err := st.flushCodeBatch(false); err != nil {
					return 0, false, err
				}
			}
		}
	}

	return blobDBID, indexed, nil
}

// generateDiffText creates simple diff text for full-text search indexing.
// Each side is truncated to 100 lines to keep the index manageable.
func generateDiffText(repo *git.Repository, path string, oldOID, newOID plumbing.Hash, hasOld, hasNew bool) string {
	const maxLines = 100

	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", path, path)

	if hasOld && oldOID != (plumbing.Hash{}) {
		if text := readBlobText(repo, oldOID); text != "" {
			lines := strings.SplitN(text, "\n", maxLines+1)
			for i, line := range lines {
				if i >= maxLines {
					break
				}
				fmt.Fprintf(&b, "-%s\n", line)
			}
		}
	}

	if hasNew && newOID != (plumbing.Hash{}) {
		if text := readBlobText(repo, newOID); text != "" {
			lines := strings.SplitN(text, "\n", maxLines+1)
			for i, line := range lines {
				if i >= maxLines {
					break
				}
				fmt.Fprintf(&b, "+%s\n", line)
			}
		}
	}

	return b.String()
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

// ParseStats holds statistics from the symbol parsing phase.
type ParseStats struct {
	BlobsParsed      uint64
	SymbolsExtracted uint64
}

// ParseSymbols extracts symbols and references from all unparsed blobs with
// supported languages and inserts them into the symbols and symbol_refs tables.
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

	query := fmt.Sprintf(
		"SELECT id, content_hash, language FROM blobs WHERE parsed = 0 AND language IN (%s)",
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

	repoRows, err := s.Query("SELECT path FROM repos")
	if err != nil {
		return stats, fmt.Errorf("query repo paths: %w", err)
	}
	var repoPaths []string
	for repoRows.Next() {
		var p string
		if err := repoRows.Scan(&p); err != nil {
			repoRows.Close()
			return stats, fmt.Errorf("scan repo path: %w", err)
		}
		repoPaths = append(repoPaths, p)
	}
	repoRows.Close()

	if len(repoPaths) == 0 {
		return stats, fmt.Errorf("no repos found in database")
	}

	var repos []*git.Repository
	for _, rp := range repoPaths {
		r, err := git.PlainOpen(rp)
		if err != nil {
			continue
		}
		repos = append(repos, r)
	}
	if len(repos) == 0 {
		return stats, fmt.Errorf("could not open any git repos")
	}

	tx, err := s.Begin()
	if err != nil {
		return stats, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	for i, blob := range blobs {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if (i+1)%100 == 0 {
			report(fmt.Sprintf("Parsing symbols: %d/%d blobs...", i+1, len(blobs)))
		}

		oid := plumbing.NewHash(blob.contentHash)
		var content []byte
		for _, r := range repos {
			blobObj, bErr := r.BlobObject(oid)
			if bErr != nil {
				continue
			}
			reader, rErr := blobObj.Reader()
			if rErr != nil {
				continue
			}
			var readErr error
			content, readErr = io.ReadAll(reader)
			reader.Close()
			if readErr != nil {
				content = nil
				continue
			}
			break
		}
		if content == nil || !utf8.Valid(content) {
			tx.Exec("UPDATE blobs SET parsed = 1 WHERE id = ?", blob.id)
			continue
		}

		syms, refs := symbols.Extract(string(content), blob.language)

		symDBIDs := make([]int64, len(syms))
		for j, sym := range syms {
			res, err := tx.Exec(
				`INSERT INTO symbols (blob_id, parent_id, name, kind, line, col, end_line, end_col, signature, return_type, params) VALUES (?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				blob.id, sym.Name, sym.Kind, sym.Line, sym.Col, sym.EndLine, sym.EndCol, sym.Signature, sym.ReturnType, sym.Params,
			)
			if err != nil {
				return stats, fmt.Errorf("insert symbol: %w", err)
			}
			symDBIDs[j], _ = res.LastInsertId()
			stats.SymbolsExtracted++
		}

		for j, sym := range syms {
			if sym.ParentIdx >= 0 && sym.ParentIdx < len(symDBIDs) {
				_, err := tx.Exec("UPDATE symbols SET parent_id = ? WHERE id = ?",
					symDBIDs[sym.ParentIdx], symDBIDs[j])
				if err != nil {
					return stats, fmt.Errorf("update symbol parent: %w", err)
				}
			}
		}

		for _, ref := range refs {
			var symbolID int64
			if ref.ContainingSymIdx >= 0 && ref.ContainingSymIdx < len(symDBIDs) {
				symbolID = symDBIDs[ref.ContainingSymIdx]
			}
			_, err := tx.Exec(
				`INSERT INTO symbol_refs (blob_id, symbol_id, ref_name, kind, line, col) VALUES (?, ?, ?, ?, ?, ?)`,
				blob.id, symbolID, ref.RefName, ref.Kind, ref.Line, ref.Col,
			)
			if err != nil {
				return stats, fmt.Errorf("insert symbol ref: %w", err)
			}
		}

		_, err = tx.Exec("UPDATE blobs SET parsed = 1 WHERE id = ?", blob.id)
		if err != nil {
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
