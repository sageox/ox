package lfs

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/session/pipeline"
)

// ContentFiles lists the session content files eligible for LFS upload.
// These are the files that get uploaded to LFS blob storage and replaced
// with pointer files in the git commit.
//
// Derived from pipeline.LedgerContentFiles — the canonical source of
// truth for "what counts as a session artifact." Adding a new artifact
// there automatically makes it eligible for LFS upload; the two call
// sites cannot drift out of sync.
var ContentFiles = pipeline.LedgerContentFiles

// UploadSessionFiles uploads session content files to LFS and returns
// the filename->FileRef manifest for inclusion in meta.json.
//
// The caller provides the LFS client (credential resolution is caller's
// responsibility — CLI and daemon resolve credentials differently).
//
// Flow:
//  1. Open content file snapshots from the session directory
//  2. Stream SHA256 OIDs + sizes
//  3. Call LFS batch API to get upload actions
//  4. Stream each missing blob from its validated file descriptor
//  5. Return filename->FileRef map for meta.json
//
// The returned manifest is a filename->FileRef map (not UploadedRef) because it
// flows straight into meta.json merge logic that is FileRef-shaped. Its blobs
// ARE uploaded on a nil-error return, so callers wrap it in AssertUploadedManifest
// at the WritePointerFiles boundary — the one audited place upload is asserted.
func UploadSessionFiles(client *Client, sessionPath string, logger *slog.Logger) (map[string]FileRef, error) {
	return UploadSessionFilesContext(context.Background(), client, sessionPath, logger)
}

// UploadSessionFilesContext hashes and transmits immutable file descriptors, so
// memory is bounded by transfer buffers rather than total conversation length.
func UploadSessionFilesContext(ctx context.Context, client *Client, sessionPath string, logger *slog.Logger) (map[string]FileRef, error) {
	if logger == nil {
		logger = slog.Default()
	}
	refs := map[string]FileRef{}
	snapshots := map[string]*uploadFileSnapshot{}
	var opened []*uploadFileSnapshot
	defer func() {
		for _, snapshot := range opened {
			_ = snapshot.file.Close()
		}
	}()
	var objects []BatchObject
	for _, name := range ContentFiles {
		path := filepath.Join(sessionPath, name)
		if IsPointerFile(path) {
			ref, err := ReadPointerFile(path)
			if err != nil {
				return nil, err
			}
			refs[name] = ref
			continue
		}
		snapshot, err := openUploadSnapshot(ctx, path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		opened = append(opened, snapshot)
		refs[name] = FileRef{Storage: StorageLFS, OID: "sha256:" + snapshot.oid, Size: snapshot.info.Size()}
		if snapshots[snapshot.oid] == nil {
			snapshots[snapshot.oid] = snapshot
			objects = append(objects, BatchObject{OID: snapshot.oid, Size: snapshot.info.Size()})
		}
	}
	if len(objects) == 0 {
		return refs, nil
	}
	response, err := client.doBatch(ctx, "upload", objects)
	if err != nil {
		return nil, fmt.Errorf("LFS batch upload: %w", err)
	}
	seen := map[string]bool{}
	// Session artifacts are few; sequential streaming bounds open HTTP buffers and
	// avoids retaining an entire transcript to support the old parallel byte API.
	for _, object := range response.Objects {
		snapshot := snapshots[object.OID]
		if snapshot == nil || seen[object.OID] || object.Size != snapshot.info.Size() {
			return nil, fmt.Errorf("LFS batch response does not match requested snapshot")
		}
		seen[object.OID] = true
		if object.Error != nil {
			return nil, fmt.Errorf("LFS object rejected: %s", object.Error.Message)
		}
		if object.Actions != nil && object.Actions.Upload != nil {
			if err := uploadSnapshot(ctx, object.Actions.Upload, snapshot); err != nil {
				return nil, err
			}
			if object.Actions.Verify != nil {
				if err := VerifyObjectContext(ctx, object.Actions.Verify, object.OID, object.Size); err != nil {
					return nil, err
				}
			}
		}
	}
	if len(seen) != len(snapshots) {
		return nil, fmt.Errorf("LFS batch omitted requested objects")
	}
	for _, snapshot := range opened {
		if err := snapshot.checkUnchanged(); err != nil {
			return nil, err
		}
	}
	logger.Info("session LFS upload complete", "files", len(refs))
	return refs, nil
}

// FindPointerStubsWithMissingBlobs checks which content files in sessionPath are
// LFS pointer stubs whose backing blobs do NOT exist in the remote LFS store.
// Returns the filenames of files whose blobs are missing. Returns nil if all
// blobs exist or if no pointer stubs are present.
//
// Use this before committing pointer stubs to the ledger — committing stubs with
// missing remote blobs causes GitLab's pre-receive hook to reject the entire push,
// blocking all sessions until the bad commits are removed.
func FindPointerStubsWithMissingBlobs(client *Client, sessionPath string, logger *slog.Logger) []string {
	type pointerInfo struct {
		filename string
		obj      BatchObject
	}
	var toCheck []pointerInfo

	for _, name := range ContentFiles {
		filePath := filepath.Join(sessionPath, name)
		if !IsPointerFile(filePath) {
			continue
		}
		ref, err := ReadPointerFile(filePath)
		if err != nil {
			if logger != nil {
				logger.Debug("FindPointerStubsWithMissingBlobs: skip unreadable pointer", "file", name, "err", err)
			}
			continue
		}
		toCheck = append(toCheck, pointerInfo{filename: name, obj: BatchObject{OID: ref.BareOID(), Size: ref.Size}})
	}

	if len(toCheck) == 0 {
		return nil // no pointer stubs — nothing to verify
	}

	objects := make([]BatchObject, len(toCheck))
	for i, p := range toCheck {
		objects[i] = p.obj
	}

	resp, err := client.BatchDownload(objects)
	if err != nil {
		// can't verify — optimistically allow the commit rather than blocking
		if logger != nil {
			logger.Debug("FindPointerStubsWithMissingBlobs: batch check failed, assuming blobs exist", "err", err)
		}
		return nil
	}

	// build OID → filename map for error reporting
	oidToFile := make(map[string]string, len(toCheck))
	for _, p := range toCheck {
		oidToFile[p.obj.OID] = p.filename
	}

	var missing []string
	for _, obj := range resp.Objects {
		if obj.Error != nil {
			if name, ok := oidToFile[obj.OID]; ok {
				missing = append(missing, name)
			}
		}
	}
	return missing
}

// EnsureSessionsGitignore ensures the sessions/.gitignore exists in the ledger.
// LFS pointer files and meta.json are committed to git; pointer files (~130 bytes)
// reference uploaded LFS objects by OID to prevent garbage collection.
// Overwrites legacy .gitignore that excluded content file extensions.
//
// It also excludes .needs-summary. That marker is machine-local scheduling
// state whose payload is ABSOLUTE local paths (cache_dir, raw_path under the
// writer's home directory), so its content can never agree between two
// machines — committing it leaks local usernames into a shared repo AND
// guarantees a perpetual merge conflict. Worse, the cloud summarizer deletes
// the marker when it finishes while a local writer modifies it, producing a
// modify/delete conflict that resolves in favor of the local modification and
// resurrects a marker the cloud already retired. Excluding it deletes the whole
// conflict class rather than merging it forever.
//
// It also excludes .recording.json for a stronger reason than tidiness. That
// marker is machine-local live-recording state (absolute paths, a local PID),
// and committing one into a git-tracked session directory arms a kill chain:
// the daemon's anti-entropy treats a stale .recording.json beside a missing
// raw.jsonl as a recovery opportunity and writes REAL transcript bytes to the
// git-tracked <ledger>/sessions/<name>/raw.jsonl — which breaks LFS linkage and
// makes the ledger reject every subsequent push, team-wide. Draft placeholders
// make that directory a live working area for the first time, so the marker
// only has to land there once. Excluding it removes the whole chain.
func EnsureSessionsGitignore(sessionsDir string) error {
	gitignorePath := filepath.Join(sessionsDir, ".gitignore")

	newContent := "# LFS pointer files and meta.json are committed to git.\n" +
		"# Content is stored in LFS; pointer files (~130 bytes) reference\n" +
		"# uploaded objects by OID to prevent garbage collection.\n" +
		"\n" +
		"# Machine-local summarization marker: holds absolute local paths and is\n" +
		"# deleted by the cloud summarizer, so it can never merge cleanly.\n" +
		".needs-summary\n" +
		"\n" +
		"# Machine-local recording state (absolute paths, local PID). Committing one\n" +
		"# into a tracked session dir lets daemon anti-entropy recover real transcript\n" +
		"# bytes onto the tracked raw.jsonl, breaking LFS linkage for the whole team.\n" +
		".recording.json\n"

	existing, _ := os.ReadFile(gitignorePath)
	if string(existing) == newContent {
		return nil // already up to date
	}

	return os.WriteFile(gitignorePath, []byte(newContent), 0644)
}
