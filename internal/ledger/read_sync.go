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
}

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
	fresh := false
	if _, err := os.Lstat(opts.Path); os.IsNotExist(err) {
		fresh = true
	} else if err != nil || !safeReadDirectory(opts.Path) || !Exists(opts.Path) {
		result.ErrorClass = "interrupted"
		return result
	}
	previous := loadReadReceipt(opts.Path, opts.RepoID, opts.Endpoint)
	if !fresh {
		// Inspect all local content before fetch or dehydration. Verified hydration
		// is an expected Git diff; any other local edit is preserved and refused.
		if _, err := readFiles(ctx, transport, workPath, dirs, false); err != nil {
			result.ErrorClass = readErrorClass(ctx, err)
			return result
		}
		if err := publishReadReceipt(opts.Path, readReceipt{ReadSyncResult: result, ReadURL: opts.ReadURL}, previous); err != nil {
			result.ErrorClass = "interrupted"
			return result
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(opts.Path), 0700); err != nil {
			result.ErrorClass = "interrupted"
			return result
		}
		stage, err := os.MkdirTemp(filepath.Dir(opts.Path), ".ox-read-clone-*")
		if err != nil {
			result.ErrorClass = "interrupted"
			return result
		}
		workPath = stage
		defer os.RemoveAll(stage) // only our unpublished, newly created staging area
	}

	var observed time.Time
	var remoteHead string
	var syncErr error
	if fresh {
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
	if syncErr == nil {
		syncErr = hydrateReadFiles(ctx, transport, workPath, opts, dirs)
	}
	if fresh && syncErr != nil {
		result.ErrorClass = readErrorClass(ctx, syncErr)
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
		result.ErrorClass = readErrorClass(ctx, syncErr)
	}
	if fresh && !result.Ready {
		return result
	}
	if err := publishReadReceipt(workPath, readReceipt{ReadSyncResult: result, ReadURL: opts.ReadURL}, nil); err != nil {
		result.Ready, result.ErrorClass = false, "interrupted"
		return result
	}
	if fresh {
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

func validReadTime(t *time.Time) bool {
	return t != nil && !t.IsZero() && !t.After(time.Now().UTC())
}

func readErrorClass(ctx context.Context, err error) string {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
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
				return nil, errors.New("missing_hydration")
			}
		}
		if gitOID != file.oid {
			if len(file.pointer) != 0 {
				return nil, errors.New("missing_hydration") // nested stub is not reader content
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

func hydrateReadFiles(ctx context.Context, transport *gitserver.ReadTransport, dir string, opts ReadSyncOptions, dirs []string) error {
	files, err := readFiles(ctx, transport, dir, dirs, true)
	if err != nil {
		return err
	}
	requests := make([]lfs.BatchObject, 0)
	sizes := make(map[string]int64)
	for _, f := range files {
		if len(f.pointer) == 0 || f.hydrated {
			continue
		}
		oid := f.ref.BareOID()
		if size, ok := sizes[oid]; ok {
			if size != f.ref.Size {
				return errors.New("missing_hydration")
			}
			continue
		}
		sizes[oid] = f.ref.Size
		requests = append(requests, lfs.BatchObject{OID: oid, Size: f.ref.Size})
	}
	if len(requests) == 0 {
		return nil
	}
	client, err := lfs.NewReadClient(opts.Endpoint, opts.RepoID, opts.ReadURL)
	if err != nil {
		return err
	}
	resp, err := client.BatchDownloadContext(ctx, requests)
	if err != nil {
		return err
	}
	actions := make(map[string]*lfs.Action, len(resp.Objects))
	for _, object := range resp.Objects {
		size, requested := sizes[object.OID]
		if !requested || actions[object.OID] != nil {
			return errors.New("missing_hydration")
		}
		if object.Error != nil && (object.Error.Code == 401 || object.Error.Code == 403) {
			return &lfs.HTTPError{StatusCode: object.Error.Code}
		}
		if size != object.Size || object.Error != nil || object.Actions == nil || object.Actions.Download == nil {
			return errors.New("missing_hydration")
		}
		actions[object.OID] = object.Actions.Download
	}
	if len(actions) != len(requests) {
		return errors.New("missing_hydration")
	}
	for _, f := range files {
		if len(f.pointer) == 0 || f.hydrated {
			continue
		}
		if err := materializeReadObject(ctx, actions[f.ref.BareOID()], filepath.Join(dir, f.path), f.ref); err != nil {
			return err
		}
	}
	return nil
}

func materializeReadObject(ctx context.Context, action *lfs.Action, path string, ref lfs.FileRef) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".ox-read-object-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := lfs.DownloadToFileContext(ctx, action, f, true, ref.BareOID()); err != nil {
		var httpErr *lfs.HTTPError
		if errors.As(err, &httpErr) || errors.Is(err, auth.ErrReadTokenUnavailable) || ctx.Err() != nil {
			return err
		}
		return errors.New("missing_hydration")
	}
	info, err := f.Stat()
	if err != nil || info.Size() != ref.Size {
		return errors.New("missing_hydration")
	}
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
		result.ErrorClass = readErrorClass(ctx, err)
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
		result.Hydration.State, result.ErrorClass = "missing", "missing_hydration"
		return result
	}
	result.Ready = true
	return result
}

// WithReadCheckout holds the SAME lock as materialization for the entire read.
// The callback must finish all filesystem reads before returning. It must not
// call another locking ledger function. Authorization belongs to the caller.
func WithReadCheckout(ctx context.Context, path, repoID, endpoint string, read func(ReadSyncResult) error) error {
	return gitutil.WithRepoLock(ctx, path, func() error {
		result := checkReadinessLocked(ctx, path, repoID, endpoint)
		if !result.Ready {
			return errors.New(result.ErrorClass)
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
		result.Ready, result.ErrorClass = false, "interrupted"
	}
	return result
}

const readReceiptRelative = ".sageox/cache/read-sync/receipt.json"

func loadReadReceipt(path, repoID, endpoint string) *readReceipt {
	if !safeReadParents(path, filepath.Join(path, ".sageox", "cache", "read-sync")) {
		return nil
	}
	if info, err := os.Lstat(filepath.Join(path, readReceiptRelative)); err != nil || !info.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(filepath.Join(path, readReceiptRelative))
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
