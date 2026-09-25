package gitutil

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/lfs/pointer"
	"github.com/sageox/ox/internal/session/pipeline"
)

// ConflictMarkerStart is git's textual conflict marker. We look for it as a
// line prefix (not merely a substring) so legitimate content that happens to
// contain the literal characters mid-line — a quoted diff in a session
// transcript, for instance — doesn't false-positive.
const ConflictMarkerStart = "<<<<<<<"

// conflictMarkerSeparator and conflictMarkerEnd finish a conflict hunk. Either
// line can appear alone in real content, so the scan only trusts them in order.
const (
	conflictMarkerSeparator = "======="
	conflictMarkerEnd       = ">>>>>>>"
)

// ErrConflictProbeFailed marks an error as "we could not determine whether the
// index holds conflicts", which is a different fact from "the index holds
// conflicts". The distinction is load-bearing: a context deadline or a canceled
// daemon kills `git ls-files --unmerged` before it produces any output, so the
// resulting error carries ZERO information about index state. Reporting such an
// error as a conflict suspended sync on provably clean clones and minted a
// RequiresConfirm issue nothing could clear (#962).
//
// It exists to keep the MESSAGE honest — the wrapper must not assert a
// conclusion the probe never reached. It is NOT a reliable transient/durable
// discriminator, and callers must not use it as one: an unreadable .git/index
// carries it too and fails identically forever. A caller that has to know the
// index state must re-read it (HasUnmergedEntries) rather than classify this
// error, because the error text cannot distinguish the two.
var ErrConflictProbeFailed = errors.New("could not determine index state")

// HasConflictMarkers reports whether the file at path contains an unresolved
// git conflict marker.
//
// This exists because `git add` does not refuse a conflicted path — it takes
// the file's current working-tree content, markers included, and stages it
// as "resolved." A caller that adds a conflicted path alongside genuinely
// clean changes and then commits bakes the markers permanently into history.
// Callers that stage files for an unattended/automatic commit MUST check
// this first and exclude (or refuse on) any match; git will not do it for
// them.
func HasConflictMarkers(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return HasConflictMarkersBytes(data), nil
}

// HasConflictMarkersBytes is the scan behind HasConflictMarkers, taking raw
// content directly. Use this when the content under inspection isn't (or
// might not be) the working-tree file — e.g. a staged git blob read via
// `git show :<path>`, which can differ from what's currently on disk.
//
// It also catches an orphaned tail whose opening marker is gone: a ">>>>>>>"
// line anywhere below a line that is exactly "=======".
func HasConflictMarkersBytes(data []byte) bool {
	sawSeparator := false
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, ConflictMarkerStart):
			return true
		case strings.TrimSuffix(line, "\r") == conflictMarkerSeparator:
			sawSeparator = true
		case sawSeparator && strings.HasPrefix(line, conflictMarkerEnd):
			return true
		}
	}
	return false
}

// ValidateLedgerBlob rejects bytes that an automatic Ledger writer must never
// publish. Conflict markers are invalid in every Ledger artifact; session
// metadata additionally has a structural JSON contract that Git cannot enforce.
// Canonical LFS artifact names must contain pointers. Storage=git exceptions
// are applied only by the tree validator after reading the committed manifest.
//
// Keep this check content-only so both staged-index validators and immutable
// tree commits can enforce the same invariant without re-reading the worktree.
func ValidateLedgerBlob(path string, data []byte) error {
	return validateLedgerBlob(path, data, "")
}

// storage must come from meta.json in the immutable tree being validated.
func validateLedgerBlob(path string, data []byte, storage string) error {
	if HasConflictMarkersBytes(data) {
		return fmt.Errorf("%s contains an unresolved conflict", path)
	}
	if isSessionContentPath(path) && storage != "git" {
		if _, _, err := pointer.Parse(string(data)); err != nil {
			return fmt.Errorf("%s must contain an LFS pointer (upload content before committing): %w", path, err)
		}
	}
	if isSessionMetaPath(path) {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil || object == nil {
			return fmt.Errorf("%s contains invalid JSON object", path)
		}
	}
	return nil
}

// Git index entry modes. A tree entry is one of these four; anything else is
// corrupt.
const (
	gitModeFile       = "100644"
	gitModeExecutable = "100755"
	gitModeSymlink    = "120000"
	gitModeGitlink    = "160000"
)

// ValidateLedgerEntryMode rejects index entry types no ox writer produces
// under sessions/. Content validation alone is not enough there: Git stores a
// symbolic link's TARGET as the blob, so a link whose target text is a valid
// JSON object passes ValidateLedgerBlob and the ledger publishes
// sessions/<name>/meta.json as a symlink. A gitlink has no blob at all.
//
// Scoped to the session tree deliberately. ValidateStagedLedgerCommit also
// vets LFS reconcile's squash of hand-made unpushed history, and refusing a
// human's symlink elsewhere in the ledger would wedge push repair.
func ValidateLedgerEntryMode(path, mode string) error {
	if !isSessionPath(path) {
		return nil
	}
	switch mode {
	case gitModeFile, gitModeExecutable:
		return nil
	case gitModeSymlink:
		return fmt.Errorf("%s is a symbolic link, not a regular file", path)
	case gitModeGitlink:
		return fmt.Errorf("%s is a submodule (gitlink), not a regular file", path)
	default:
		return fmt.Errorf("%s has unsupported index mode %q", path, mode)
	}
}

// ValidateStagedLedgerCommit validates the exact blobs currently staged for an
// automatic Ledger commit. The caller MUST hold WithRepoLock from before its
// first git add through the subsequent commit; otherwise another ox process
// could replace an already-validated index entry before commit.
//
// The index is snapshotted with write-tree and the resulting tree is compared
// to HEAD, so validation reads immutable objects rather than the mutable index
// or worktree. A live unmerged index fails at write-tree before git add can
// accidentally mark a conflicted path resolved. When pathspecs are supplied,
// only blobs that the path-scoped commit can publish are scanned; the
// unmerged-index check remains global because git refuses every commit while
// any index stage is unresolved.
//
// Validation alone leaves a window: `git commit -- <pathspec>` re-reads the
// WORKTREE at commit time, not the index just validated. Callers that commit
// must use CommitLedgerSnapshot, which validates and commits one immutable
// tree, instead of pairing this function with a porcelain commit.
func ValidateStagedLedgerCommit(ctx context.Context, repoPath string, pathspecs ...string) error {
	if err := IsSafeForGitOps(repoPath); err != nil {
		return fmt.Errorf("unsafe Ledger commit: %w", err)
	}
	tree, err := writeIndexTree(ctx, repoPath, nil)
	if err != nil {
		return err
	}
	parent, err := currentBranchTip(ctx, repoPath)
	if err != nil {
		return err
	}
	return validateLedgerTree(ctx, repoPath, parent, tree, pathspecs...)
}

// validateLedgerTree validates every entry that tree adds or changes relative
// to parent (every entry, on an unborn branch where parent is ""), limited to
// pathspecs when given. Deletions have no blob and are always allowed here;
// the sacred mass-deletion guard is a separate check.
func validateLedgerTree(ctx context.Context, repoPath, parent, tree string, pathspecs ...string) error {
	entries, err := changedTreeEntries(ctx, repoPath, parent, tree, pathspecs...)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ValidateLedgerEntryMode(entry.path, entry.mode); err != nil {
			return fmt.Errorf("refusing automatic Ledger commit: %w", err)
		}
		blob, err := cleanGitOutput(ctx, repoPath, "cat-file", "blob", entry.oid)
		if err != nil {
			return fmt.Errorf("inspect staged Ledger blob %s: %w", entry.path, err)
		}
		storage := ""
		if isSessionContentPath(entry.path) {
			storage, err = ledgerContentStorage(ctx, repoPath, tree, entry.path)
			if err != nil {
				return fmt.Errorf("inspect staged Ledger storage for %s: %w", entry.path, err)
			}
		}
		if err := validateLedgerBlob(entry.path, blob, storage); err != nil {
			return fmt.Errorf("refusing automatic Ledger commit: %w", err)
		}
	}
	return nil
}

// ledgerContentStorage reads the sibling manifest from the exact commit tree.
// Looking at the worktree (or even the mutable index) here would let an
// uncommitted storage=git declaration bypass the content guard.
func ledgerContentStorage(ctx context.Context, repoPath, tree, contentPath string) (string, error) {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(contentPath)), "/")
	metaPath := strings.Join(parts[:2], "/") + "/meta.json"
	raw, err := cleanGitOutput(ctx, repoPath, "ls-tree", "-z", tree, "--", metaPath)
	if err != nil {
		return "", err
	}
	if len(raw) == 0 {
		return "", nil
	}
	fields := strings.Fields(strings.SplitN(string(raw), "\t", 2)[0])
	if len(fields) != 3 {
		return "", fmt.Errorf("invalid metadata tree entry")
	}
	if err := ValidateLedgerEntryMode(metaPath, fields[0]); err != nil {
		return "", err
	}
	data, err := cleanGitOutput(ctx, repoPath, "cat-file", "blob", fields[2])
	if err != nil {
		return "", err
	}
	if err := ValidateLedgerBlob(metaPath, data); err != nil {
		return "", err
	}
	var meta struct {
		Files map[string]struct {
			Storage string `json:"storage"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", err
	}
	return meta.Files[strings.Join(parts[2:], "/")].Storage, nil
}

func isSessionContentPath(path string) bool {
	return isSessionPath(path) && slices.Contains(pipeline.LedgerContentFiles, filepath.Base(path))
}

// treeEntry is one non-deleted path in a tree delta: its destination mode and
// object id.
type treeEntry struct {
	path string
	mode string
	oid  string
}

// changedTreeEntries lists the entries tree adds or modifies relative to
// parent, skipping deletions. With parent == "" (unborn branch) every entry in
// tree is returned. Rename detection is off so every record is one path.
func changedTreeEntries(ctx context.Context, repoPath, parent, tree string, pathspecs ...string) ([]treeEntry, error) {
	if parent == "" {
		args := append([]string{"ls-tree", "-r", "-z", tree, "--"}, pathspecs...)
		raw, err := cleanGitOutput(ctx, repoPath, args...)
		if err != nil {
			return nil, fmt.Errorf("list Ledger tree: %w", err)
		}
		var entries []treeEntry
		// ls-tree -z: "<mode> SP <type> SP <oid> TAB <path>" per record.
		for _, rec := range splitNUL(raw) {
			tab := strings.IndexByte(rec, '\t')
			if tab < 0 {
				continue
			}
			fields := strings.Fields(rec[:tab])
			if len(fields) < 3 {
				return nil, fmt.Errorf("unexpected ls-tree record %q", rec)
			}
			entries = append(entries, treeEntry{path: rec[tab+1:], mode: fields[0], oid: fields[2]})
		}
		return entries, nil
	}
	args := append([]string{"diff-tree", "-r", "-z", "--no-renames", parent, tree, "--"}, pathspecs...)
	raw, err := cleanGitOutput(ctx, repoPath, args...)
	if err != nil {
		return nil, fmt.Errorf("diff Ledger trees: %w", err)
	}
	// diff-tree -r -z: ":<srcmode> <dstmode> <srcoid> <dstoid> <status>" NUL
	// "<path>" NUL per record.
	toks := splitNUL(raw)
	var entries []treeEntry
	for i := 0; i+1 < len(toks); i += 2 {
		fields := strings.Fields(strings.TrimPrefix(toks[i], ":"))
		if len(fields) < 5 {
			return nil, fmt.Errorf("unexpected diff-tree record %q", toks[i])
		}
		if strings.HasPrefix(fields[4], "D") {
			continue
		}
		entries = append(entries, treeEntry{path: toks[i+1], mode: fields[1], oid: fields[3]})
	}
	return entries, nil
}

// writeIndexTree snapshots an index into an immutable tree and returns its
// id. extraEnv selects an alternate index (GIT_INDEX_FILE); nil means the
// repository's own. write-tree refuses unmerged entries, so a live conflict
// fails closed here.
func writeIndexTree(ctx context.Context, repoPath string, extraEnv []string) (string, error) {
	out, err := runPlumbing(ctx, repoPath, nil, extraEnv, "write-tree")
	if err != nil {
		return "", fmt.Errorf("snapshot Ledger index (unresolved conflict in index?): %w", err)
	}
	tree := strings.TrimSpace(string(out))
	if tree == "" {
		return "", errors.New("git write-tree returned an empty tree id")
	}
	return tree, nil
}

// currentBranchTip returns the commit HEAD resolves to, or "" on an unborn
// branch. Managed Ledgers normally have a HEAD; supporting an unborn clone
// keeps the guard fail-closed without making initial bootstrap a special case.
func currentBranchTip(ctx context.Context, repoPath string) (string, error) {
	out, err := cleanGitOutput(ctx, repoPath, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		if _, headErr := cleanGitOutput(ctx, repoPath, "symbolic-ref", "--quiet", "HEAD"); headErr == nil {
			return "", nil // a branch name with no commit yet
		}
		return "", fmt.Errorf("resolve HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func isSessionMetaPath(path string) bool {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	return len(parts) == 3 && parts[0] == "sessions" && parts[1] != "" && parts[2] == "meta.json"
}

// isSessionPath reports whether path lives inside a session directory — the
// tree ox writers own exclusively and always populate with regular files.
func isSessionPath(path string) bool {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	return len(parts) >= 3 && parts[0] == "sessions" && parts[1] != ""
}

func splitNUL(data []byte) []string {
	trimmed := bytes.TrimRight(data, "\x00")
	if len(trimmed) == 0 {
		return nil
	}
	parts := bytes.Split(trimmed, []byte{0})
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		paths = append(paths, string(part))
	}
	return paths
}

// cleanGitOutput runs local Git plumbing without RunGit's output sanitization;
// staged JSON bytes and object ids must round-trip byte-for-byte.
func cleanGitOutput(ctx context.Context, repoPath string, args ...string) ([]byte, error) {
	return runPlumbing(ctx, repoPath, nil, nil, args...)
}

// runPlumbing is cleanGitOutput with optional stdin and extra environment, for
// commands such as update-index --index-info against an alternate index.
// Signing is disabled explicitly: commit-tree honors commit.gpgsign and an
// unattended daemon can never satisfy a passphrase prompt.
func runPlumbing(ctx context.Context, repoPath string, stdin []byte, extraEnv []string, args ...string) ([]byte, error) {
	full := []string{"-C", repoPath, "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "LANG=C")
	cmd.Env = append(cmd.Env, extraEnv...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// ResolveAutostashConflicts clears formatting-only session metadata conflicts
// left by pull --autostash, which can exit successfully with an unmerged index.
// It preserves every field from both sides and refuses differing values —
// except for the small allowlist of ox-owned bookkeeping fields in
// sessionMetaBookkeepingMerges, whose merge is mechanical rather than a choice
// between two authors' content. Deletions, other paths, active git operations,
// and edits made after the conflict are always refused.
// The caller must hold WithRepoLock. No commits are made or stashes removed.
func ResolveAutostashConflicts(ctx context.Context, repoPath string, safePrefixes, denyPrefixes []string) (bool, error) {
	entries, err := listUnmergedEntries(ctx, repoPath)
	if err != nil {
		// Deliberately unwrapped: listUnmergedEntries already tags this with
		// ErrConflictProbeFailed and names the command, and any prefix added
		// here would re-assert the conclusion the probe never reached.
		return false, err
	}
	if len(entries) == 0 {
		return false, nil
	}
	for _, marker := range []string{"rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		if _, err := os.Stat(filepath.Join(repoPath, ".git", marker)); !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("cannot recover autostash while %s is present or unreadable", marker)
		}
	}
	tmp, err := os.MkdirTemp("", "ox-autostash-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	type repair struct {
		path     string
		original []byte
		merged   []byte
		mode     os.FileMode
	}
	var repairs []repair
	for path, stages := range groupByPath(entries) {
		parts := strings.Split(path, "/")
		if len(parts) != 3 || parts[0] != "sessions" || parts[2] != "meta.json" ||
			!matchesSafePrefix(path, safePrefixes, denyPrefixes) || !stages[1] || !stages[2] || !stages[3] {
			return false, fmt.Errorf("unresolved conflict in %s requires manual resolution", path)
		}
		var fields [4]map[string]any
		var files [4]string
		for stage := 1; stage <= 3; stage++ {
			// Read raw blobs: RunGit sanitizes output and must not rewrite metadata.
			data, err := exec.CommandContext(ctx, "git", "-C", repoPath, "show", fmt.Sprintf(":%d:%s", stage, path)).Output()
			if err != nil {
				return false, fmt.Errorf("read conflict stage for %s: %w", path, err)
			}
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.UseNumber() // preserve large IDs and sizes exactly
			if !json.Valid(data) || decoder.Decode(&fields[stage]) != nil || fields[stage] == nil {
				return false, fmt.Errorf("invalid JSON in conflict for %s", path)
			}
			files[stage] = filepath.Join(tmp, fmt.Sprint(stage))
			if err := os.WriteFile(files[stage], data, 0o600); err != nil {
				return false, err
			}
		}
		for key := range fields[1] {
			_, ours := fields[2][key]
			_, theirs := fields[3][key]
			if !ours || !theirs {
				return false, fmt.Errorf("field %s was deleted in %s; manual resolution required", key, path)
			}
		}
		for key, value := range fields[3] {
			if ours, exists := fields[2][key]; exists && !reflect.DeepEqual(ours, value) {
				// Differing values refuse by default. The narrow exception is
				// ox's own bookkeeping, where the merge is mechanical rather
				// than a choice between two authors' content — see
				// sessionMetaBookkeepingMerges. Reached only for a path the
				// guard above already confined to sessions/<name>/meta.json
				// inside safePrefixes, so the policy cannot escape that tree.
				merged, ok := mergeSessionMetaBookkeeping(key, ours, value)
				if !ok {
					return false, fmt.Errorf("field %s differs in %s; manual resolution required", key, path)
				}
				fields[2][key] = merged
				continue
			}
			fields[2][key] = value
		}
		fullPath := filepath.Join(repoPath, path)
		info, err := os.Lstat(fullPath)
		if err != nil || !info.Mode().IsRegular() {
			return false, fmt.Errorf("conflicted metadata is missing or not a regular file: %s", path)
		}
		original, err := os.ReadFile(fullPath)
		if err != nil {
			return false, err
		}
		merged, err := json.MarshalIndent(fields[2], "", "  ")
		if err != nil {
			return false, err
		}
		merged = append(merged, '\n')
		// Reproduce git's stash conflict before replacing it. Otherwise a human's
		// edits outside the markers could be lost by rebuilding from index stages.
		cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "merge-file", "-p",
			"-L", "Updated upstream", "-L", "Stash base", "-L", "Stashed changes", files[2], files[1], files[3])
		expected, mergeErr := cmd.Output()
		var exitErr *exec.ExitError
		// An earlier pass may have written the repair but failed to stage it.
		if !bytes.Equal(original, merged) && (!errors.As(mergeErr, &exitErr) || exitErr.ExitCode() < 1 || exitErr.ExitCode() > 127 || !bytes.Equal(original, expected)) {
			return false, fmt.Errorf("conflict in %s has additional edits or is not an autostash conflict; manual resolution required", path)
		}
		repairs = append(repairs, repair{path, original, merged, info.Mode().Perm()})
	}
	// Validate the whole conflict set before touching the index or worktree.
	for _, r := range repairs {
		fullPath := filepath.Join(repoPath, r.path)
		if err := fileutil.WithFileLock(ctx, fullPath, func() error {
			current, err := os.ReadFile(fullPath)
			if err != nil {
				return err
			}
			if !bytes.Equal(current, r.original) {
				return fmt.Errorf("conflicted metadata changed during recovery: %s", r.path)
			}
			if err := fileutil.AtomicWriteBytes(fullPath, r.merged, r.mode); err != nil {
				return err
			}
			_, err = RunGit(ctx, repoPath, "add", "--sparse", "--", r.path)
			return err
		}); err != nil {
			return false, fmt.Errorf("resolve %s: %w", r.path, err)
		}
	}
	return true, nil
}

// sessionMetaBookkeepingMerges is the complete allowlist of sessions/*/meta.json
// fields whose two sides ox reconciles itself instead of refusing. It is
// deliberately tiny and must stay that way: the blanket refusal above is what
// stops a real content field — a summary body, a validation_error string, a
// title — from being silently half-discarded, so this is not a general
// "pick a side" heuristic and must never be widened into one.
//
// A field earns a place here only when a merge rule is mechanically correct
// from the field's own semantics, not merely convenient.
//
// Deliberately absent: summary_status. #897 named it daemon-written bookkeeping
// alongside summary_attempts, but it is a state label with no total order —
// nothing picks between "failed" and "pending" without inventing policy — so it
// keeps refusing.
var sessionMetaBookkeepingMerges = map[string]func(ours, theirs any) (any, bool){
	// summary_attempts is ox's own retry counter. Both sides of a
	// pull --autostash have been counting the same session's attempts, which
	// makes this the one conflict class ox reliably generates against itself
	// (#956) — and, before this rule, the one class auto-resolve refused,
	// wedging the index until a human ran git by hand in the ledger clone.
	//
	// max is a safe LOWER BOUND on the attempts made, not their true total.
	// Every ledger clone runs autofix over the whole sessions/ tree
	// (daemon checkSessionMetaTitles), so two clones retry the SAME session
	// independently and their bumps are distinct attempts, not two views of
	// one serialized counter: from a common base of 1, ours=2 with theirs=3
	// is four attempts recorded as three.
	//
	// That undercount is chosen, not overlooked. It is bounded by the number
	// of clones racing one session, it buys at most a few extra LLM calls
	// before MaxSummaryAttempts bites, and checkSessionInlineSummaryRetry
	// re-arms the cap daily regardless. The opposite error is far more
	// expensive: a rule that can overshoot flips a still-summarizable session
	// to "unrecoverable", which permanently demotes a teammate-visible title
	// to the session-name slug. Spend tokens; do not discard summaries.
	// TestAutostashRecoveryMergesBookkeepingCounters pins the undercount with
	// "divergent clones undercount: max is a lower bound, not the total", so
	// changing this rule is a deliberate policy change, not a bug fix.
	//
	// An exact rule does exist and is deliberately not used: stage 1 is the
	// stash base, so ours+theirs-base would count both sides' bumps. It is
	// exact only while stage 1 really is the common ancestor of both counters,
	// and wherever that assumption slips the error flips to overcounting — the
	// expensive direction above.
	//
	// Resets are a different hazard, and they are structurally out of reach
	// twice over. summary_attempts is omitempty, so resetting it to 0 DELETES
	// the key (meta_repair.go patches it exactly that way), and the deletion
	// guard above refuses any base key missing from either side. Independently,
	// all three resetting writers — session_finalize.go on a successful
	// summarization, meta_repair.go RecoverEmptyTitleMeta on a recovered title,
	// and ResetInlineSummaryEligible on a re-arm — write the counter atomically
	// alongside summary_status and/or validation_error and/or title, none of
	// which have a merge rule, so the loop above refuses on that key first.
	// Both guards matter, because across a reset max is actively WRONG: the
	// lower side is the LATER observation, and taking the higher resurrects a
	// stale count that re-trips MaxSummaryAttempts a try early. The test cases
	// "deleted counter refuses before any merge rule applies" and "a counter
	// RESET paired with its status write still refuses" pin one guard each.
	//
	// Writers that BUMP the counter alone already exist — RecoverEmptyTitleMeta
	// below the cap, and `ox session repair-meta-summary` on a session already
	// marked failed_validation — which is precisely why this merge is reachable
	// at all. A writer that RESETS it alone would invalidate the analysis
	// above; give the counter a reset-aware merge (or an episode id) first.
	"summary_attempts": mergeMonotonicCounter,
}

// mergeSessionMetaBookkeeping resolves one differing session-metadata field
// when an ox-owned merge rule covers it. ok is false for every field outside
// the allowlist and for any value the rule does not recognize, which returns
// the caller to its refusal.
func mergeSessionMetaBookkeeping(key string, ours, theirs any) (any, bool) {
	merge, covered := sessionMetaBookkeepingMerges[key]
	if !covered {
		return nil, false
	}
	return merge(ours, theirs)
}

// mergeMonotonicCounter resolves two sides of a counter that never decreases
// within one failure episode to the larger. The two sides may be independent
// counts rather than two readings of one count, so the result is a lower bound
// on the total — see sessionMetaBookkeepingMerges for why that direction is the
// one worth erring in. The winning side is returned as-is (a json.Number from
// the stage that produced it) so the merged file re-encodes the original
// literal rather than a value round-tripped through float64.
//
// Anything that is not a non-negative JSON integer — a float, a string, a null
// from an older writer — is not a counter this rule understands. Refusing there
// costs only a manual resolution; guessing could overwrite a field that merely
// shares the name.
func mergeMonotonicCounter(ours, theirs any) (any, bool) {
	left, ok := counterValue(ours)
	if !ok {
		return nil, false
	}
	right, ok := counterValue(theirs)
	if !ok {
		return nil, false
	}
	if right > left {
		return theirs, true
	}
	return ours, true
}

// counterValue reads a counter from a conflict stage decoded with
// json.Decoder.UseNumber, so an integral value arrives as json.Number and a
// non-integral one is rejected by Int64 rather than silently truncated.
func counterValue(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	n, err := number.Int64()
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
