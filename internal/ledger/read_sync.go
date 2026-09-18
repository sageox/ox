package ledger

import (
	"context"
	"crypto/sha1" //nolint:gosec // Git object identity uses Git's SHA-1 blob format.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
)

// ReadSyncOptions identifies one authorized checkout. ReadURL must come from
// authenticated repo discovery; a repository file cannot establish this trust.
type ReadSyncOptions struct {
	RepoID, Endpoint, Path, ReadURL string
}

type ReadCoverage struct {
	Complete bool     `json:"complete"`
	Paths    []string `json:"paths"`
	Files    int      `json:"files"`
	Empty    bool     `json:"empty"`
}

type ReadHydration struct {
	State     string `json:"state"`
	Required  int    `json:"required"`
	Completed int    `json:"completed"`
}

// ReadSyncResult separates local readiness from remote freshness and authority.
// A ready checkout is never itself permission to serve cached data.
type ReadSyncResult struct {
	SchemaVersion      int           `json:"schema_version"`
	RepoID             string        `json:"repo_id"`
	Endpoint           string        `json:"endpoint"`
	Path               string        `json:"path"`
	Head               string        `json:"head"`
	Ready              bool          `json:"ready"`
	LastSuccessfulSync *time.Time    `json:"last_successful_sync"`
	History            string        `json:"history"`
	Coverage           ReadCoverage  `json:"coverage"`
	Hydration          ReadHydration `json:"hydration"`
	ErrorClass         string        `json:"error_class,omitempty"`
	// ErrorDetail says what failed, naming the object when one is identifiable.
	// Additive within schema_version 1; consumers keep matching on ErrorClass.
	ErrorDetail *ReadFailureDetail `json:"error_detail,omitempty"`
	// Skipped accounts for every failure hydration walked past when there was
	// more than one; ErrorDetail describes only the first. Additive within
	// schema_version 1.
	Skipped *ReadSkipped `json:"skipped,omitempty"`
}

// ReadFailureDetail says what failed, naming the object when one is
// identifiable, so an operator can act on it instead of correlating server
// request logs against object storage by hand. Reason tells apart conditions
// that deliberately share one error_class and is always set; every other field
// applies only to some reasons. A batch-level failure carries Reason alone.
//
// Every field is locally computed or a server-supplied status code. No
// credential, signed URL, response body, or subprocess output is carried here.
// There is no server message field: lfs.Client replaces a read route's
// per-object error prose with the status text for its code before it reaches
// this package, so such a field could only restate ServerCode.
type ReadFailureDetail struct {
	Reason       string `json:"reason"`
	Path         string `json:"path,omitempty"`
	OID          string `json:"oid,omitempty"`
	ExpectedOID  string `json:"expected_oid,omitempty"`
	ExpectedSize *int64 `json:"expected_size,omitempty"`
	ActualSize   *int64 `json:"actual_size,omitempty"`
	ServerCode   int    `json:"server_code,omitempty"`
}

// ReadSkipped keeps one cause shared by many objects from reading as one bad
// object. Total counts every failure hydration walked past, including one that
// carries no reason — a failed batch request or a local write failure, which
// ErrorDetail cannot name either. Reasons tallies the failures that do carry
// one, and Sample lists the first readSkipSample of each reason, in the order
// that decides which failure ErrorDetail names.
type ReadSkipped struct {
	Total   int                 `json:"total"`
	Reasons map[string]int      `json:"reasons"`
	Sample  []ReadFailureDetail `json:"sample"`
}

// readSkipSample bounds how many failures of each reason ReadSkipped lists. A
// ledger can skip thousands of objects; Total says how many a sample stands for.
// The bound is per reason so that a rare reason met after a common one has
// filled its share still gets examples.
const readSkipSample = 5

type readReceipt struct {
	ReadSyncResult
	ReadURL string `json:"read_url"`
}

func newReadResult(opts ReadSyncOptions) ReadSyncResult {
	return ReadSyncResult{SchemaVersion: 1, RepoID: opts.RepoID, Endpoint: opts.Endpoint,
		Path: opts.Path, History: "unknown", Coverage: ReadCoverage{Paths: []string{}},
		Hydration: ReadHydration{State: "unknown"}}
}

// ReadSync performs only discovery-authorized Git reads and local materialization.
// No daemon, write notification, Git push, or LFS upload participates.
func ReadSync(ctx context.Context, opts ReadSyncOptions) ReadSyncResult {
	result := newReadResult(opts)
	transport, err := gitserver.NewReadTransport(opts.Endpoint, opts.RepoID, opts.ReadURL)
	if err != nil || !filepath.IsAbs(opts.Path) {
		result.ErrorClass = "invalid_arguments"
		return result
	}
	// Share both existing creation and mutation locks with other ox operations.
	err = gitutil.WithPreCloneLock(ctx, opts.Path, func() error {
		return gitutil.WithRepoLock(ctx, opts.Path, func() error {
			result = readSyncLocked(ctx, opts, transport)
			return nil
		})
	})
	if err != nil {
		result.ErrorClass = "interrupted"
	}
	return result
}

func readSyncLocked(ctx context.Context, opts ReadSyncOptions, transport *gitserver.ReadTransport) ReadSyncResult {
	result := newReadResult(opts)
	dirs := sparseCheckoutDirs()
	result.Coverage.Paths = dirs
	workPath := opts.Path
	staged := false
	if _, err := os.Lstat(opts.Path); os.IsNotExist(err) {
		staged = true
	} else if err != nil || !safeReadDirectory(opts.Path) || !Exists(opts.Path) {
		result.ErrorClass = "interrupted"
		return result
	}
	previous := loadReadReceipt(opts.Path, opts.RepoID, opts.Endpoint)
	clone := false
	if staged {
		if err := os.MkdirAll(filepath.Dir(opts.Path), 0700); err != nil {
			result.ErrorClass = "interrupted"
			return result
		}
		workPath = readStagePath(opts.Path)
		clone = !resumableReadStage(ctx, transport, workPath, opts, dirs)
		if clone {
			if err := replaceReadStage(ctx, transport, workPath, opts); err != nil {
				recordReadFailure(ctx, &result, err)
				return result
			}
		}
	} else {
		// Inspect all local content before fetch or dehydration. Verified hydration
		// is an expected Git diff; any other local edit is preserved and refused.
		if _, err := readFiles(ctx, transport, workPath, dirs, false); err != nil {
			recordReadFailure(ctx, &result, err)
			return result
		}
	}
	if !clone {
		// Durably invalidate before mutating. For a published checkout this is the
		// readiness its readers consult; a resumed stage can carry a ready receipt
		// from an attempt whose publishing rename failed, and it is cleared here.
		if err := publishReadReceipt(workPath, readReceipt{ReadSyncResult: result, ReadURL: opts.ReadURL}, previous); err != nil {
			result.ErrorClass = "interrupted"
			return result
		}
	}

	var observed time.Time
	var remoteHead string
	var syncErr error
	if clone {
		_, syncErr = runReadGit(ctx, transport, true, filepath.Dir(workPath), "clone", "--no-checkout", "--filter=blob:none", "--", opts.ReadURL, workPath)
		if syncErr == nil {
			observed = time.Now().UTC()
			remoteHead, syncErr = runReadGit(ctx, transport, false, workPath, "rev-parse", "--verify", "HEAD")
		}
	} else {
		args := []string{"fetch", "--no-recurse-submodules"}
		shallow, err := runReadGit(ctx, transport, false, workPath, "rev-parse", "--is-shallow-repository")
		if err != nil {
			syncErr = err
		} else {
			if shallow == "true" {
				args = append(args, "--unshallow")
			}
			args = append(args, "--", opts.ReadURL, "+HEAD:refs/ox/read-head", "+refs/heads/*:refs/remotes/origin/*")
			_, syncErr = runReadGit(ctx, transport, true, workPath, args...)
			if syncErr == nil {
				observed = time.Now().UTC()
				remoteHead, syncErr = runReadGit(ctx, transport, false, workPath, "rev-parse", "--verify", "refs/ox/read-head")
			}
		}
		if syncErr == nil {
			// A local commit must already be in authorized remote history. Never
			// reset, rebase, or discard a divergent/local branch to make reads work.
			if _, err := runReadGit(ctx, transport, false, workPath, "merge-base", "--is-ancestor", "HEAD", remoteHead); err != nil {
				syncErr = errors.New("dirty")
			}
		}
		if syncErr == nil {
			syncErr = dehydrateReadFiles(ctx, transport, workPath, remoteHead, dirs)
		}
	}
	if syncErr == nil {
		// Use the same coverage policy as the human/daemon checkout. --cone is
		// explicit so an owned shallow cache can safely establish full coverage.
		args := append([]string{"sparse-checkout", "set", "--cone", "--"}, dirs...)
		_, syncErr = runReadGit(ctx, transport, true, workPath, args...)
	}
	if syncErr == nil {
		_, syncErr = runReadGit(ctx, transport, true, workPath, "checkout", "--no-overwrite-ignore", "--detach", remoteHead)
	}
	if syncErr == nil && clone {
		// A stage is continuable only once it has a worktree, so bind it to this
		// identity here — before hydration, the step that takes the longest.
		if err := publishReadReceipt(workPath, readReceipt{ReadSyncResult: result, ReadURL: opts.ReadURL}, nil); err != nil {
			syncErr = errors.New("interrupted")
		}
	}
	if syncErr == nil {
		syncErr = hydrateReadFiles(ctx, transport, workPath, opts, dirs)
	}
	if staged && syncErr != nil {
		// Keep the stage: it holds every object this attempt transferred, and it
		// stays unpublished until resumableReadStage re-proves it.
		recordReadFailure(ctx, &result, syncErr)
		return result
	}

	// Verification can recover readiness after a remote failure, but only a
	// completed fetch of this exact HEAD may establish new remote evidence.
	result = verifyReadCheckout(ctx, opts, transport, workPath, dirs)
	if previous != nil && previous.Head == result.Head && validReadTime(previous.LastSuccessfulSync) {
		result.LastSuccessfulSync = previous.LastSuccessfulSync
	}
	if result.Ready && syncErr == nil && result.Head == remoteHead && !observed.IsZero() {
		result.LastSuccessfulSync = &observed
	}
	if syncErr != nil {
		recordReadFailure(ctx, &result, syncErr)
	}
	if staged && !result.Ready {
		return result
	}
	if err := publishReadReceipt(workPath, readReceipt{ReadSyncResult: result, ReadURL: opts.ReadURL}, nil); err != nil {
		// Verification or hydration above may have recorded a detail and a skip
		// summary. The failure now being reported is this write, not those
		// objects, so they go with it.
		result.Ready, result.ErrorClass, result.ErrorDetail, result.Skipped = false, "interrupted", nil, nil
		return result
	}
	if staged {
		if err := os.Rename(workPath, opts.Path); err != nil {
			result.Ready, result.ErrorClass = false, "interrupted"
			return result
		}
		if err := syncReadDir(filepath.Dir(opts.Path)); err != nil {
			result.Ready, result.ErrorClass = false, "interrupted"
		}
	}
	return result
}

// readStagePath names the unpublished staging clone for a checkout. It is a
// sibling of path so publishing stays a same-filesystem rename, and it is
// deterministic so a cold clone interrupted during hydration can be continued
// instead of restarting from an empty directory.
func readStagePath(path string) string {
	return filepath.Join(filepath.Dir(path), ".ox-read-clone-"+filepath.Base(path))
}

// replaceReadStage empties the staging path so a fresh clone can use it. The
// path is derived from the checkout name, so content this command never created
// can already be sitting there; ownedReadStage decides, and anything unproven is
// refused rather than deleted.
func replaceReadStage(ctx context.Context, transport *gitserver.ReadTransport, stage string, opts ReadSyncOptions) error {
	if _, err := os.Lstat(stage); err == nil {
		if !safeReadDirectory(stage) || !ownedReadStage(ctx, transport, stage, opts) {
			return errors.New("dirty")
		}
		if err := os.RemoveAll(stage); err != nil {
			return errors.New("interrupted")
		}
	} else if !os.IsNotExist(err) {
		return errors.New("interrupted")
	}
	return os.MkdirAll(stage, 0700)
}

// ownedReadStage reports whether ox can have produced what is at the stage path,
// and is the only thing that authorizes deleting it. A stage ox created is either
// still empty or a clone of this exact read URL: `git clone` records
// remote.origin.url before it fetches any object, so even an attempt interrupted
// mid-clone carries that record and stays replaceable without a human. A Git
// checkout of anything else is someone else's, and its local commits are not
// ox's to discard.
//
// It fails closed, which is what protects a stage from a budget that expires
// mid-attempt: reading the origin needs a Git subprocess, a canceled context
// cannot run one, and an unreadable origin refuses instead of deleting.
func ownedReadStage(ctx context.Context, transport *gitserver.ReadTransport, stage string, opts ReadSyncOptions) bool {
	if !Exists(stage) {
		entries, err := os.ReadDir(stage)
		return err == nil && len(entries) == 0
	}
	origin, err := runReadGit(ctx, transport, false, stage, "config", "--get", "remote.origin.url")
	return err == nil && origin == opts.ReadURL
}

// resumableReadStage reports whether an earlier interrupted cold clone left a
// stage this attempt may continue. The stage must be a Git checkout carrying a
// receipt this same repo identity, endpoint, and read URL wrote after its own
// checkout succeeded, and its worktree must still match HEAD. A stage failing
// any of those holds no published data, dirty file, or local commit — it is this
// command's own scratch space — so the caller replaces it rather than refusing.
func resumableReadStage(ctx context.Context, transport *gitserver.ReadTransport, stage string, opts ReadSyncOptions, dirs []string) bool {
	if !safeReadDirectory(stage) || !Exists(stage) {
		return false
	}
	receipt := loadReadReceiptAt(stage, opts.Path, opts.RepoID, opts.Endpoint)
	if receipt == nil || receipt.ReadURL != opts.ReadURL {
		return false
	}
	_, err := readFiles(ctx, transport, stage, dirs, false)
	return err == nil
}

func validReadTime(t *time.Time) bool {
	return t != nil && !t.IsZero() && !t.After(time.Now().UTC())
}

// readFailure carries object detail on the error that decides error_class, so
// naming the object can never change the failure's category.
type readFailure struct {
	detail ReadFailureDetail
	err    error
}

func (e *readFailure) Error() string { return e.err.Error() }
func (e *readFailure) Unwrap() error { return e.err }

// missingHydration reports an object that cannot be materialized. The class
// stays "missing_hydration"; detail.Reason is what tells the conditions apart.
func missingHydration(detail ReadFailureDetail) error {
	return &readFailure{detail: detail, err: errors.New("missing_hydration")}
}

// readSize boxes a size for ReadFailureDetail. A pointer is what keeps a
// legitimately zero observed size distinguishable from an unset field.
func readSize(n int64) *int64 { return &n }

// recordReadFailure sets the sanitized category together with the object detail
// and skip summary err carries. They move together so a result can never pair
// one failure's class with another failure's objects. It is the only place that
// populates ErrorDetail and Skipped, which is what makes the OID sanitation
// below unskippable.
func recordReadFailure(ctx context.Context, result *ReadSyncResult, err error) {
	result.ErrorClass = readErrorClass(ctx, err)
	result.ErrorDetail, result.Skipped = nil, nil
	// A canceled or expired context classifies as "interrupted" whatever err
	// says, including an object failure raised just before the deadline landed
	// — verification runs Git subprocesses between the two. The operation, not
	// those objects, is what failed, so the detail and summary go with it.
	if result.ErrorClass == "interrupted" {
		return
	}
	var failure *readFailure
	if errors.As(err, &failure) {
		detail := failure.detail
		detail.OID, detail.ExpectedOID = safeReadOID(detail.OID), safeReadOID(detail.ExpectedOID)
		result.ErrorDetail = &detail
	}
	// A single failure walked past is fully described by the class and detail,
	// so a summary starts at the second, and a result with one failure keeps the
	// shape consumers already parse.
	var skips *readSkips
	if errors.As(err, &skips) && skips.total > 1 {
		skipped := ReadSkipped{Total: skips.total, Reasons: skips.reasons, Sample: make([]ReadFailureDetail, 0, len(skips.sample))}
		for _, detail := range skips.sample {
			detail.OID, detail.ExpectedOID = safeReadOID(detail.OID), safeReadOID(detail.ExpectedOID)
			skipped.Sample = append(skipped.Sample, detail)
		}
		result.Skipped = &skipped
	}
}

// safeReadOID returns oid only when it is a canonical bare SHA-256 identifier.
// An unrequested object in a batch response, and an "oid" line in a committed
// pointer, are both arbitrary text that no code validates before it reaches a
// detail. Dropping a non-canonical value keeps the result's redaction rule —
// no credential, credential-bearing URL, or raw server bytes — unconditional.
func safeReadOID(oid string) string {
	if len(oid) != 64 {
		return ""
	}
	for i := range len(oid) {
		if c := oid[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return oid
}

// readInterrupted reports whether the operation itself stopped, rather than one
// object failing. The caller's context is not the only evidence: lfs.Client
// carries its own request deadline, and a request that exceeds it fails with an
// error wrapping context.DeadlineExceeded while ctx is still live. Hydration and
// classification must read that the same way, so both ask here.
func readInterrupted(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func readErrorClass(ctx context.Context, err error) string {
	if readInterrupted(ctx, err) {
		return "interrupted"
	}
	if errors.Is(err, gitserver.ErrReadTokenUnavailable) || errors.Is(err, auth.ErrReadTokenUnavailable) {
		return "denied"
	}
	if errors.Is(err, gitserver.ErrUnsafeReadTransport) {
		return "identity_mismatch"
	}
	var gitErr *exec.ExitError
	if errors.As(err, &gitErr) {
		message := strings.ToLower(string(gitErr.Stderr))
		for _, denied := range []string{"authentication failed", "requested url returned error: 401", "requested url returned error: 403", "could not read username", "could not read password", "told us to quit"} {
			if strings.Contains(message, denied) {
				return "denied"
			}
		}
	}
	var httpErr *lfs.HTTPError
	if errors.As(err, &httpErr) {
		if httpErr.StatusCode == 401 || httpErr.StatusCode == 403 {
			return "denied"
		}
		return "missing_hydration"
	}
	switch err.Error() {
	case "dirty", "missing_hydration", "incomplete_history", "incomplete_coverage", "identity_mismatch", "interrupted":
		return err.Error()
	default:
		return "git_failed"
	}
}

func runReadGit(ctx context.Context, transport *gitserver.ReadTransport, network bool, dir string, args ...string) (string, error) {
	var cmd *exec.Cmd
	var err error
	if network {
		cmd, err = transport.Command(ctx, dir, args...)
	} else {
		cmd, err = transport.LocalCommand(ctx, dir, args...)
	}
	if err != nil {
		return "", err
	}
	// Never include Git/server diagnostics in results: a remote can reflect a
	// credential or inject terminal control sequences into its response.
	data, err := cmd.Output()
	return strings.TrimSpace(string(data)), err
}

type readFile struct {
	path, oid string
	pointer   []byte
	ref       lfs.FileRef
	hydrated  bool
}

// readFiles verifies the index and covered files against Git objects. Hashing
// both plain files and hydrated LFS content detects edits hidden by Git's stat
// cache, assume-unchanged, or an obsolete success receipt.
func readFiles(ctx context.Context, transport *gitserver.ReadTransport, dir string, dirs []string, coverage bool) ([]readFile, error) {
	if _, err := runReadGit(ctx, transport, false, dir, "diff", "--cached", "--quiet", "HEAD", "--"); err != nil {
		return nil, errors.New("dirty")
	}
	if other, err := runReadGit(ctx, transport, false, dir, "ls-files", "--others", "-z"); err != nil {
		return nil, err
	} else {
		for _, p := range strings.Split(other, "\x00") {
			if p != "" && !strings.HasPrefix(p, ".sageox/cache/") {
				return nil, errors.New("dirty")
			}
		}
	}
	tree, err := runReadGit(ctx, transport, false, dir, "ls-tree", "-r", "-z", "--full-tree", "HEAD")
	if err != nil {
		return nil, err
	}
	if coverage {
		for _, required := range baseSparseDirs {
			if !slices.Contains(dirs, required) {
				return nil, errors.New("incomplete_coverage")
			}
		}
		actual, err := runReadGit(ctx, transport, false, dir, "sparse-checkout", "list")
		if err != nil {
			return nil, errors.New("incomplete_coverage")
		}
		paths := strings.Split(actual, "\n")
		for _, required := range dirs {
			if !slices.Contains(paths, strings.TrimSuffix(required, "/")) {
				return nil, errors.New("incomplete_coverage")
			}
		}
	}
	var files []readFile
	for _, entry := range strings.Split(tree, "\x00") {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry == "" {
			continue
		}
		meta, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || strings.HasPrefix(name, ".sageox/cache/") {
			return nil, errors.New("interrupted")
		}
		abs := filepath.Join(dir, filepath.FromSlash(name))
		if !readSparseIncludes(name, dirs) {
			if _, err := os.Lstat(abs); os.IsNotExist(err) {
				continue
			}
		}
		info, err := os.Lstat(abs)
		if err != nil {
			// Missing files in an owned shallow/narrow cache are allowed before
			// materialization only when Git marks them outside its sparse cone.
			if !coverage && os.IsNotExist(err) {
				tag, tagErr := runReadGit(ctx, transport, false, dir, "ls-files", "-t", "--", name)
				if tagErr == nil && strings.HasPrefix(tag, "S ") {
					continue
				}
			}
			return nil, errors.New("dirty")
		}
		if !info.Mode().IsRegular() || fields[1] != "blob" || !safeReadParents(dir, filepath.Dir(abs)) {
			return nil, errors.New("dirty")
		}
		gitOID, lfsOID, size, pointer, err := hashReadFile(ctx, abs, len(fields[2]))
		if err != nil {
			return nil, err
		}
		file := readFile{path: name, oid: fields[2]}
		if len(pointer) != 0 {
			if oid, pointerSize, err := lfs.ParsePointer(string(pointer)); err == nil {
				file.pointer, file.ref = pointer, lfs.FileRef{OID: oid, Size: pointerSize}
			} else if strings.HasPrefix(string(pointer), "version https://git-lfs.github.com/spec/v1\n") {
				return nil, missingHydration(ReadFailureDetail{Reason: "malformed_pointer", Path: name})
			}
		}
		if gitOID != file.oid {
			if len(file.pointer) != 0 {
				// The worktree file is itself a pointer, and not the one HEAD
				// commits. It names some other object, so it is not this file's
				// content and no OID here would be the one worth reporting.
				return nil, missingHydration(ReadFailureDetail{Reason: "nested_stub", Path: name})
			}
			blobSize, err := runReadGit(ctx, transport, false, dir, "cat-file", "-s", file.oid)
			if err != nil {
				return nil, err
			}
			n, err := strconv.ParseInt(blobSize, 10, 64)
			if err != nil || n > 1024 {
				return nil, errors.New("dirty")
			}
			cmd, err := transport.LocalCommand(ctx, dir, "cat-file", "blob", file.oid)
			if err != nil {
				return nil, err
			}
			blob, err := cmd.Output()
			if err != nil {
				return nil, err
			}
			oid, pointerSize, err := lfs.ParsePointer(string(blob))
			if err != nil || size != pointerSize || lfsOID != strings.TrimPrefix(oid, "sha256:") {
				return nil, errors.New("dirty")
			}
			file.pointer = blob
			file.ref, file.hydrated = lfs.FileRef{OID: oid, Size: pointerSize}, true
		}
		files = append(files, file)
	}
	return files, nil
}

func hashReadFile(ctx context.Context, path string, length int) (gitOID, lfsOID string, size int64, pointer []byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", 0, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", "", 0, nil, err
	}
	h := sha1.New() //nolint:gosec // Compatibility with Git's object format; LFS integrity uses SHA-256.
	if length == 64 {
		h = sha256.New()
	}
	fmt.Fprintf(h, "blob %d\x00", info.Size())
	lfsHash := sha256.New()
	buf := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", "", 0, nil, err
		}
		n, err := f.Read(buf)
		size += int64(n)
		_, _ = h.Write(buf[:n])
		_, _ = lfsHash.Write(buf[:n])
		if size <= 1024 {
			pointer = append(pointer, buf[:n]...)
		} else {
			pointer = nil
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", "", 0, nil, err
		}
	}
	if size != info.Size() {
		return "", "", 0, nil, errors.New("dirty")
	}
	return hex.EncodeToString(h.Sum(nil)), hex.EncodeToString(lfsHash.Sum(nil)), size, pointer, nil
}

func readSparseIncludes(name string, dirs []string) bool {
	parent := filepath.ToSlash(filepath.Dir(name))
	if parent == "." {
		return true
	}
	for _, dir := range dirs {
		dir = strings.TrimSuffix(dir, "/")
		if strings.HasPrefix(name, dir+"/") || strings.HasPrefix(dir, parent+"/") {
			return true
		}
	}
	return false
}

func dehydrateReadFiles(ctx context.Context, transport *gitserver.ReadTransport, dir, target string, dirs []string) error {
	files, err := readFiles(ctx, transport, dir, dirs, false)
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.hydrated {
			if target != "" {
				// Git preserves local hydration when the committed pointer is
				// unchanged. Leave those bytes available even if a later object's
				// download fails, and avoid re-downloading them on every refresh.
				next, err := runReadGit(ctx, transport, false, dir, "rev-parse", "--verify", target+":"+f.path)
				if err == nil && next == f.oid {
					continue
				}
			}
			// Changing/deleting a pointer must not destroy the previously read
			// large object. A hard link retains the verified inode before the
			// atomic pointer replacement; no file contents are copied or erased.
			cacheDir := filepath.Join(dir, ".sageox", "cache", "read-sync", "objects")
			if !safeReadParents(dir, cacheDir) {
				return errors.New("interrupted")
			}
			if err := os.MkdirAll(cacheDir, 0700); err != nil {
				return err
			}
			cache := filepath.Join(cacheDir, f.ref.BareOID())
			if err := os.Link(filepath.Join(dir, f.path), cache); err != nil {
				if !os.IsExist(err) {
					return err
				}
				info, err := os.Lstat(cache)
				if err != nil || !info.Mode().IsRegular() {
					return errors.New("interrupted")
				}
				_, oid, size, _, err := hashReadFile(ctx, cache, len(f.oid))
				if err != nil || oid != f.ref.BareOID() || size != f.ref.Size {
					return errors.New("interrupted")
				}
			}
			if err := syncReadDir(cacheDir); err != nil {
				return err
			}
			if err := syncReadDir(filepath.Dir(cacheDir)); err != nil {
				return err
			}
			if err := fileutil.AtomicWriteBytes(filepath.Join(dir, f.path), f.pointer, 0644); err != nil {
				return err
			}
		}
	}
	return nil
}

// readSkips accounts for the failures hydration walked past, and decides which
// failures it may walk past at all. As an error it is the first of them, so that
// failure alone still decides error_class and error_detail; recordReadFailure
// reads the summary of all of them from it.
type readSkips struct {
	first   error
	total   int
	reasons map[string]int
	sample  []ReadFailureDetail
}

func (s *readSkips) Error() string { return s.first.Error() }
func (s *readSkips) Unwrap() error { return s.first }

// err is what hydration returns once every object was attempted. It is nil when
// nothing was walked past, rather than a readSkips holding no failure.
func (s *readSkips) err() error {
	if s.first == nil {
		return nil
	}
	return s
}

// stopsReadHydration reports whether err is about the operation or the grant
// rather than one object. An interrupted operation, an unusable read
// credential, and a 401/403 leave no later object materializable either, so
// hydration stops where they happen — it neither walks past them nor keeps the
// batch's other transfers running behind them.
func stopsReadHydration(ctx context.Context, err error) bool {
	var httpErr *lfs.HTTPError
	return readInterrupted(ctx, err) ||
		errors.Is(err, auth.ErrReadTokenUnavailable) ||
		errors.As(err, &httpErr) && (httpErr.StatusCode == 401 || httpErr.StatusCode == 403)
}

// skip records err and reports whether hydration may continue past it.
// Everything stopsReadHydration does not claim is about a single object, and
// stopping there would leave every later object a stub for as long as the
// condition lasts.
func (s *readSkips) skip(ctx context.Context, err error) bool {
	if stopsReadHydration(ctx, err) {
		return false
	}
	if s.first == nil {
		s.first, s.reasons = err, make(map[string]int)
	}
	s.total++
	var failure *readFailure
	if errors.As(err, &failure) {
		reason := failure.detail.Reason
		s.reasons[reason]++
		if s.reasons[reason] <= readSkipSample {
			s.sample = append(s.sample, failure.detail)
		}
	}
	return true
}

// readHydrationConcurrency bounds how many object downloads are in flight at
// once. Hydration is many small objects from one origin, so it is round-trip
// bound rather than bandwidth bound: transferring them one at a time leaves the
// link idle between objects, and a cold clone of a real ledger cannot finish in
// any budget a headless consumer can schedule (ox #948). 8 is the Git LFS
// client's own default concurrency for this protocol against this class of
// server, and it keeps ox one polite consumer of one origin.
const readHydrationConcurrency = 8

// readRequestAttempts bounds how many times one batch request or object
// download is tried, and readRequestBackoff is the pause before the second
// attempt, doubled before each one after that. A cold clone transfers for many
// minutes, so a single transport blip must not cost the whole sync; the bound
// keeps a request that keeps failing from spending the budget the objects still
// waiting need.
const readRequestAttempts = 3
const readRequestBackoff = 200 * time.Millisecond

// withReadRetry runs request until it succeeds, exhausts readRequestAttempts, or
// fails with something repeating it cannot change.
func withReadRetry(ctx context.Context, request func() error) error {
	backoff := readRequestBackoff
	for attempt := 1; ; attempt++ {
		err := request()
		if err == nil || attempt == readRequestAttempts || !retryReadRequest(ctx, err) {
			return err
		}
		select {
		case <-ctx.Done():
			// Report the failure already in hand rather than the expiry: it is
			// what the attempt observed, and recordReadFailure classifies an
			// expired context as "interrupted" whatever the error says.
			return err
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}

// retryReadRequest reports whether repeating a failed read request could return
// anything different. An interruption, an unusable read credential, content that
// does not hash to the identity it was requested under, and every status except
// a server's own 5xx and 429 are settled answers — repeating one only spends
// budget the objects still waiting need. Everything else is retried: the
// transport failures that dominate a long transfer cannot be enumerated
// reliably, and readRequestAttempts bounds what retrying one in vain costs.
func retryReadRequest(ctx context.Context, err error) bool {
	if readInterrupted(ctx, err) || errors.Is(err, auth.ErrReadTokenUnavailable) ||
		errors.Is(err, lfs.ErrOIDMismatch) || errors.Is(err, lfs.ErrBatchResponseUnusable) {
		return false
	}
	var httpErr *lfs.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode >= 500 || httpErr.StatusCode == 429
	}
	return true
}

// batchReadGrants requests one batch's download grants, retrying under the same
// rule as an object download. A failed batch request costs every object in it,
// and there is no smaller unit of the request to fall back to.
func batchReadGrants(ctx context.Context, client *lfs.Client, batch []lfs.BatchObject) (*lfs.BatchResponse, error) {
	var resp *lfs.BatchResponse
	err := withReadRetry(ctx, func() error {
		var err error
		resp, err = client.BatchDownloadContext(ctx, batch)
		return err
	})
	return resp, err
}

// readGrant pairs one file with the download action its object was granted.
// Two files naming one object share the action.
type readGrant struct {
	action *lfs.Action
	file   readFile
}

// hydrateReadFiles materializes every object it can, then reports the first one
// it could not, carrying a summary of every one so that a failure shared by
// thousands of objects does not read as one bad object (ox #984). A ledger
// accumulates objects for as long as the team works, so an object the server
// will not serve is a steady state rather than an exception; returning at the
// first one left every later object a stub forever, however healthy those
// objects were (ox #947).
//
// Skipping never relaxes readiness. verifyReadCheckout recounts the worktree
// afterwards, so a partial hydration still reports hydration.state "missing"
// and ready false; the returned failure replaces verification's own detail
// because it names why the object was skipped, which the worktree cannot say.
func hydrateReadFiles(ctx context.Context, transport *gitserver.ReadTransport, dir string, opts ReadSyncOptions, dirs []string) error {
	files, err := readFiles(ctx, transport, dir, dirs, true)
	if err != nil {
		return err
	}
	var skips readSkips
	requests := make([]lfs.BatchObject, 0)
	pending := make(map[string][]readFile)
	for _, f := range files {
		if len(f.pointer) == 0 || f.hydrated {
			continue
		}
		if f.ref.Size == 0 {
			// An empty object's content is fully implied by its pointer, and GitLab
			// may never have stored it, so never spend a grant on it: verify the OID is
			// the empty-content hash and materialize the empty file locally.
			if f.ref.BareOID() != lfs.ComputeOID(nil) {
				err := missingHydration(ReadFailureDetail{Reason: "empty_object_oid_mismatch",
					Path: f.path, OID: f.ref.BareOID(), ExpectedOID: lfs.ComputeOID(nil)})
				if !skips.skip(ctx, err) {
					return err
				}
				continue
			}
			if err := materializeEmptyReadObject(filepath.Join(dir, f.path)); err != nil && !skips.skip(ctx, err) {
				return err
			}
			continue
		}
		oid := f.ref.BareOID()
		if same := pending[oid]; len(same) != 0 {
			if same[0].ref.Size != f.ref.Size {
				// At most one of the two pointers can be right, and which one is
				// not knowable until the bytes arrive. Both files stay pending so
				// that each one's own size verification decides whether it may
				// commit: dropping either here would pick the winner by path
				// order and strand the correct pointer when it sorts second.
				//
				// The conflict is a property of the pair, so it names no path.
				// Naming one would have to choose before the bytes settle it, and
				// the file it chose would be the hydrated one half the time — an
				// operator sent to a file that is fine. The losing file names
				// itself later through downloaded_size_mismatch.
				err := missingHydration(ReadFailureDetail{Reason: "shared_object_size_conflict",
					OID: oid, ExpectedSize: readSize(same[0].ref.Size), ActualSize: readSize(f.ref.Size)})
				if !skips.skip(ctx, err) {
					return err
				}
			}
		} else {
			requests = append(requests, lfs.BatchObject{OID: oid, Size: f.ref.Size})
		}
		pending[oid] = append(pending[oid], f)
	}
	if len(requests) == 0 {
		return skips.err()
	}
	client, err := lfs.NewReadClient(opts.Endpoint, opts.RepoID, opts.ReadURL)
	if err != nil {
		return err
	}
	// The read route allows 100 objects and 64 KiB of request JSON. Fixed-size
	// SHA-256 OIDs keep each batch well below that body limit. Materialize one
	// batch at a time so grants stay bounded and failures retain progress.
	const batchSize = 100
	for start := 0; start < len(requests); start += batchSize {
		batch := requests[start:min(start+batchSize, len(requests))]
		resp, err := batchReadGrants(ctx, client, batch)
		if err != nil {
			if !skips.skip(ctx, err) {
				return err
			}
			continue
		}
		// Only a short response is a counting defect. A surplus one is always a
		// specific entry — unrequested or repeated — and the loop below names it;
		// recording the count here first would mask that with a vaguer reason,
		// since the first failure walked past is the one reported.
		if len(resp.Objects) < len(batch) {
			// The count itself is the defect. No OID is identifiable as the one
			// at fault, so this failure names the batch rather than an object.
			// The objects the response does describe are still materialized; the
			// ones it omits keep their stubs and verification counts them.
			err := missingHydration(ReadFailureDetail{Reason: "batch_response_incomplete"})
			if !skips.skip(ctx, err) {
				return err
			}
		}
		// requested maps this batch's OIDs to whether the response has described
		// one yet, so an unrequested or repeated object is identifiable on its own.
		requested := make(map[string]bool, len(batch))
		for _, object := range batch {
			requested[object.OID] = false
		}
		// actions holds only the objects this batch may download. A requested OID
		// absent from it was refused, described incorrectly, or never answered.
		actions := make(map[string]*lfs.Action, len(batch))
		for _, object := range resp.Objects {
			answered, inBatch := requested[object.OID]
			var err error
			switch {
			case !inBatch:
				err = missingHydration(ReadFailureDetail{Reason: "batch_object_unrequested", OID: object.OID})
			case answered:
				err = missingHydration(ReadFailureDetail{Reason: "batch_object_duplicated", OID: object.OID})
			}
			if err != nil {
				if !skips.skip(ctx, err) {
					return err
				}
				continue
			}
			requested[object.OID] = true
			// pending is keyed by the OIDs this batch was built from, so a
			// requested object always has at least one file waiting on it.
			waiting := pending[object.OID]
			file := waiting[0]
			switch {
			case object.Error != nil:
				// Refusal is checked before size. A refused object carries no
				// meaningful size, so checking size first reports a size mismatch
				// for what is really a refusal. The status is wrapped so that a
				// 401/403 — about the grant, not this object — stops hydration
				// under the same rule that classifies it as "denied".
				err = &readFailure{err: &lfs.HTTPError{StatusCode: object.Error.Code},
					detail: ReadFailureDetail{Reason: "object_refused", Path: file.path, OID: object.OID, ServerCode: object.Error.Code}}
			// The grant is accepted when any waiting file declares the granted
			// size, not only the one whose size was requested. When two pointers
			// disagree, a server that reports the object's own size is what says
			// which of them to believe, and the download verifies it again.
			case !slices.ContainsFunc(waiting, func(f readFile) bool { return f.ref.Size == object.Size }):
				err = missingHydration(ReadFailureDetail{Reason: "object_size_mismatch", Path: file.path,
					OID: object.OID, ExpectedSize: readSize(file.ref.Size), ActualSize: readSize(object.Size)})
			case object.Actions == nil:
				err = missingHydration(ReadFailureDetail{Reason: "object_missing_actions", Path: file.path, OID: object.OID})
			case object.Actions.Download == nil:
				err = missingHydration(ReadFailureDetail{Reason: "object_missing_download_action", Path: file.path, OID: object.OID})
			default:
				actions[object.OID] = object.Actions.Download
				continue
			}
			if !skips.skip(ctx, err) {
				return err
			}
		}
		var grants []readGrant
		for _, object := range batch {
			action, granted := actions[object.OID]
			if !granted {
				continue
			}
			for _, f := range pending[object.OID] {
				grants = append(grants, readGrant{action: action, file: f})
			}
		}
		// Downloads run concurrently; their failures are reported in batch order
		// afterwards, so which object error_detail names does not depend on how
		// the transfers happened to interleave. skips stays single-threaded.
		failures := make([]error, len(grants))
		downloads, stopDownloads := context.WithCancel(ctx)
		sem := make(chan struct{}, readHydrationConcurrency)
		var wg sync.WaitGroup
		for i, grant := range grants {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				failures[i] = materializeReadGrant(ctx, downloads, stopDownloads, dir, grant)
			}()
		}
		wg.Wait()
		stopDownloads()
		for _, err := range failures {
			if err != nil && !skips.skip(ctx, err) {
				return err
			}
		}
	}
	return skips.err()
}

// materializeReadGrant downloads one granted file and reports the failure this
// batch must account for.
//
// A failure that stops hydration cancels the batch's other downloads, so a
// grant the server has stopped honoring does not keep requesting objects that
// cannot arrive. A download canceled that way reports nothing: a sibling's
// failure stopped it, not anything about this object, so its file stays a stub
// exactly as it would have with one transfer at a time. The caller's own
// cancellation is not that — ctx carries it too — and every download reports it.
func materializeReadGrant(ctx, downloads context.Context, stop context.CancelFunc, dir string, grant readGrant) error {
	err := materializeReadObject(downloads, grant.action, dir, grant.file.path, grant.file.ref)
	switch {
	case err == nil:
		return nil
	case downloads.Err() != nil && ctx.Err() == nil && errors.Is(err, context.Canceled):
		return nil
	case stopsReadHydration(ctx, err):
		stop()
	}
	return err
}

// downloadReadObject streams one object into f, retrying a transport failure.
func downloadReadObject(ctx context.Context, action *lfs.Action, f *os.File, ref lfs.FileRef) error {
	return withReadRetry(ctx, func() error {
		// An attempt that failed part-way has already written a prefix of the
		// object. Each attempt starts from empty so the next one replaces those
		// bytes instead of appending to them.
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := f.Truncate(0); err != nil {
			return err
		}
		return lfs.DownloadToFileContext(ctx, action, f, true, ref.BareOID())
	})
}

// materializeReadObject downloads one object into rel under dir. rel is the
// repo-relative path a failure names; dir never appears in the detail.
func materializeReadObject(ctx context.Context, action *lfs.Action, dir, rel string, ref lfs.FileRef) error {
	path := filepath.Join(dir, rel)
	f, err := os.CreateTemp(filepath.Dir(path), ".ox-read-object-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := downloadReadObject(ctx, action, f, ref); err != nil {
		// Cancellation and a missing credential are about the operation, not this
		// object, and readErrorClass reports them ahead of any status. Returning
		// them undecorated keeps the class and the detail describing one failure.
		if errors.Is(err, auth.ErrReadTokenUnavailable) || ctx.Err() != nil {
			return err
		}
		var httpErr *lfs.HTTPError
		if errors.As(err, &httpErr) {
			// Wrapping keeps readErrorClass's own HTTPError handling: 401/403
			// stays "denied", every other status stays "missing_hydration".
			return &readFailure{err: err, detail: ReadFailureDetail{Reason: "download_refused",
				Path: rel, OID: ref.BareOID(), ServerCode: httpErr.StatusCode}}
		}
		return missingHydration(ReadFailureDetail{Reason: "download_failed", Path: rel, OID: ref.BareOID()})
	}
	info, err := f.Stat()
	if err != nil {
		return missingHydration(ReadFailureDetail{Reason: "download_stat_failed", Path: rel, OID: ref.BareOID()})
	}
	if info.Size() != ref.Size {
		return missingHydration(ReadFailureDetail{Reason: "downloaded_size_mismatch", Path: rel, OID: ref.BareOID(),
			ExpectedSize: readSize(ref.Size), ActualSize: readSize(info.Size())})
	}
	return commitReadObject(f, path)
}

// materializeEmptyReadObject replaces a size-0 pointer with the empty file it
// describes, through the same durable path as a downloaded object, so a ready
// receipt never covers a replacement that a crash could roll back to the pointer.
func materializeEmptyReadObject(path string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".ox-read-object-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	return commitReadObject(f, path)
}

// commitReadObject makes the fully written temp file f durable at path.
func commitReadObject(f *os.File, path string) error {
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncReadDir(filepath.Dir(path))
}

func verifyReadCheckout(ctx context.Context, opts ReadSyncOptions, transport *gitserver.ReadTransport, dir string, dirs []string) ReadSyncResult {
	result := newReadResult(opts)
	result.Coverage.Paths = dirs
	var err error
	result.Head, err = runReadGit(ctx, transport, false, dir, "rev-parse", "--verify", "HEAD")
	if err != nil {
		result.ErrorClass = "interrupted"
		return result
	}
	shallow, err := runReadGit(ctx, transport, false, dir, "rev-parse", "--is-shallow-repository")
	if err != nil || shallow != "false" {
		result.History, result.ErrorClass = "shallow", "incomplete_history"
		return result
	}
	result.History = "full"
	if _, err := runReadGit(ctx, transport, false, dir, "rev-list", "--count", "HEAD"); err != nil {
		result.History, result.ErrorClass = "unknown", "incomplete_history"
		return result
	}
	files, err := readFiles(ctx, transport, dir, dirs, true)
	if err != nil {
		recordReadFailure(ctx, &result, err)
		return result
	}
	result.Coverage = ReadCoverage{Complete: true, Paths: dirs, Files: len(files), Empty: len(files) == 0}
	result.Hydration.State = "complete"
	for _, f := range files {
		if len(f.pointer) != 0 {
			result.Hydration.Required++
			if f.hydrated {
				result.Hydration.Completed++
			}
		}
	}
	if result.Hydration.Required != result.Hydration.Completed {
		result.Hydration.State = "missing"
		// Routed through recordReadFailure so this detail is sanitized on the
		// same path as every other one.
		missing := errors.New("missing_hydration")
		for _, f := range files {
			if len(f.pointer) != 0 && !f.hydrated {
				missing = missingHydration(ReadFailureDetail{Reason: "object_not_materialized", Path: f.path, OID: f.ref.BareOID()})
				break
			}
		}
		recordReadFailure(ctx, &result, missing)
		return result
	}
	result.Ready = true
	return result
}

// ReadNotReadyError reports that the guard refused a read because the local
// checkout did not verify. Class is one of the sanitized categories published in
// docs/specs/ledger-read-sync.md, so a CLI reader can surface it without
// pattern-matching an error string.
type ReadNotReadyError struct{ Class string }

func (e *ReadNotReadyError) Error() string { return e.Class }

// WithReadCheckout holds the SAME lock as materialization for the entire read.
// The callback must finish all filesystem reads before returning. It must not
// call another locking ledger function. Authorization belongs to the caller.
//
// A refused read returns *ReadNotReadyError. Any other error means the lock
// itself could not be taken.
func WithReadCheckout(ctx context.Context, path, repoID, endpoint string, read func(ReadSyncResult) error) error {
	return gitutil.WithRepoLock(ctx, path, func() error {
		result := checkReadinessLocked(ctx, path, repoID, endpoint)
		if !result.Ready {
			return &ReadNotReadyError{Class: result.ErrorClass}
		}
		return read(result)
	})
}

// CheckReadiness recovers a receipt by verifying local content with all network
// transports disabled. It never creates or advances remote freshness evidence.
func CheckReadiness(ctx context.Context, path, repoID, endpoint string) ReadSyncResult {
	result := newReadResult(ReadSyncOptions{RepoID: repoID, Endpoint: endpoint, Path: path})
	err := gitutil.WithRepoLock(ctx, path, func() error {
		result = checkReadinessLocked(ctx, path, repoID, endpoint)
		return nil
	})
	if err != nil {
		result.ErrorClass = "interrupted"
	}
	return result
}

func checkReadinessLocked(ctx context.Context, path, repoID, endpoint string) ReadSyncResult {
	opts := ReadSyncOptions{RepoID: repoID, Endpoint: endpoint, Path: path}
	result := newReadResult(opts)
	previous := loadReadReceipt(path, repoID, endpoint)
	if previous == nil {
		result.ErrorClass = "interrupted"
		return result
	}
	opts.ReadURL = previous.ReadURL
	transport, err := gitserver.NewReadTransport(endpoint, repoID, opts.ReadURL)
	if err != nil {
		result.ErrorClass = "identity_mismatch"
		return result
	}
	result = verifyReadCheckout(ctx, opts, transport, path, previous.Coverage.Paths)
	if previous.Head == result.Head && validReadTime(previous.LastSuccessfulSync) {
		result.LastSuccessfulSync = previous.LastSuccessfulSync
	}
	if err := publishReadReceipt(path, readReceipt{ReadSyncResult: result, ReadURL: opts.ReadURL}, nil); err != nil {
		// Same as the refresh path: a verification detail cannot outlive it.
		result.Ready, result.ErrorClass, result.ErrorDetail = false, "interrupted", nil
	}
	return result
}

const readReceiptRelative = ".sageox/cache/read-sync/receipt.json"

func loadReadReceipt(path, repoID, endpoint string) *readReceipt {
	return loadReadReceiptAt(path, path, repoID, endpoint)
}

// loadReadReceiptAt reads dir's receipt and accepts it only for the checkout
// path it names. The two differ for a staging clone, which carries the receipt
// of the destination it has not been published to yet.
func loadReadReceiptAt(dir, path, repoID, endpoint string) *readReceipt {
	if !safeReadParents(dir, filepath.Join(dir, ".sageox", "cache", "read-sync")) {
		return nil
	}
	if info, err := os.Lstat(filepath.Join(dir, readReceiptRelative)); err != nil || !info.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(filepath.Join(dir, readReceiptRelative))
	if err != nil {
		return nil
	}
	defer f.Close()
	var receipt readReceipt
	if json.NewDecoder(io.LimitReader(f, 64*1024)).Decode(&receipt) != nil || receipt.SchemaVersion != 1 || receipt.RepoID != repoID || receipt.Endpoint != endpoint || receipt.Path != path {
		return nil
	}
	return &receipt
}

func publishReadReceipt(path string, receipt readReceipt, previous *readReceipt) error {
	if previous != nil {
		// Invalidating retains evidence about the OLD head so a local recovery
		// cannot accidentally stamp the newly checked-out head with old evidence.
		receipt.Head = previous.Head
		receipt.LastSuccessfulSync = previous.LastSuccessfulSync
	}
	dir := filepath.Join(path, ".sageox", "cache", "read-sync")
	if !safeReadParents(path, dir) {
		return errors.New("interrupted")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if info, err := os.Lstat(filepath.Join(path, readReceiptRelative)); err == nil && !info.Mode().IsRegular() || err != nil && !os.IsNotExist(err) {
		return errors.New("interrupted")
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if err := fileutil.AtomicWriteBytes(filepath.Join(path, readReceiptRelative), data, 0600); err != nil {
		return err
	}
	return syncReadDir(dir)
}

func syncReadDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func safeReadDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func safeReadParents(root, dir string) bool {
	for p := dir; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil && !os.IsNotExist(err) || err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return false
		}
		if p == root {
			return true
		}
		if filepath.Dir(p) == p {
			return false
		}
	}
}
