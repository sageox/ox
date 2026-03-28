package lfs

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// ContentFiles lists the session content files eligible for LFS upload.
// These are the files that get uploaded to LFS blob storage and replaced
// with pointer files in the git commit.
var ContentFiles = []string{
	"raw.jsonl",
	"summary.md",
	"session.md",
	"plan.md",
}

// UploadSessionFiles uploads session content files to LFS and returns
// the filename->FileRef manifest for inclusion in meta.json.
//
// The caller provides the LFS client (credential resolution is caller's
// responsibility — CLI and daemon resolve credentials differently).
//
// Flow:
//  1. Read all content files from session dir
//  2. Compute SHA256 OIDs + sizes
//  3. Call LFS batch API to get upload actions
//  4. Upload all blobs in parallel
//  5. Return filename->FileRef map for meta.json
func UploadSessionFiles(client *Client, sessionPath string, logger *slog.Logger) (map[string]FileRef, error) {
	if logger == nil {
		logger = slog.Default()
	}

	// read all content files that exist
	files := make(map[string][]byte)     // bareOID -> content
	fileRefs := make(map[string]FileRef) // filename -> ref
	var batchObjects []BatchObject

	for _, name := range ContentFiles {
		filePath := filepath.Join(sessionPath, name)

		// if file is already an LFS pointer, extract its ref directly —
		// don't re-upload the pointer text as content
		if IsPointerFile(filePath) {
			ref, err := ReadPointerFile(filePath)
			if err != nil {
				return nil, fmt.Errorf("read pointer %s: %w", name, err)
			}
			fileRefs[name] = ref
			continue
		}

		content, err := os.ReadFile(filePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue // skip files that don't exist
			}
			return nil, fmt.Errorf("read %s: %w", name, err)
		}

		ref := NewFileRef(content)
		fileRefs[name] = ref
		files[ref.BareOID()] = content
		batchObjects = append(batchObjects, BatchObject{
			OID:  ref.BareOID(),
			Size: ref.Size,
		})
	}

	if len(batchObjects) == 0 {
		return fileRefs, nil // nothing to upload
	}

	logger.Info("uploading session to LFS", "path", sessionPath, "files", len(batchObjects))

	// request upload URLs from LFS batch API
	resp, err := client.BatchUpload(batchObjects)
	if err != nil {
		logger.Info("LFS batch API failed", "error", err, "path", sessionPath, "files", len(batchObjects))
		return nil, fmt.Errorf("LFS batch upload: %w", err)
	}

	// upload blobs in parallel (up to 4 concurrent)
	results := UploadAll(resp, files, 4)

	// collect all errors so devs can see everything that failed
	var uploadErrors []string
	for _, r := range results {
		if r.Error != nil {
			logger.Info("LFS blob upload failed", "oid", r.OID, "error", r.Error)
			uploadErrors = append(uploadErrors, fmt.Sprintf("OID %s: %s", r.OID, r.Error))
		}
	}
	if len(uploadErrors) > 0 {
		return nil, fmt.Errorf("LFS upload failed (%d/%d files):\n  %s",
			len(uploadErrors), len(results), strings.Join(uploadErrors, "\n  "))
	}

	logger.Info("LFS upload complete", "path", sessionPath, "files", len(fileRefs))
	return fileRefs, nil
}

// EnsureSessionsGitignore ensures the sessions/.gitignore exists in the ledger.
// LFS pointer files and meta.json are committed to git; pointer files (~130 bytes)
// reference uploaded LFS objects by OID to prevent garbage collection.
// Overwrites legacy .gitignore that excluded content file extensions.
func EnsureSessionsGitignore(sessionsDir string) error {
	gitignorePath := filepath.Join(sessionsDir, ".gitignore")

	newContent := "# LFS pointer files and meta.json are committed to git.\n" +
		"# Content is stored in LFS; pointer files (~130 bytes) reference\n" +
		"# uploaded objects by OID to prevent garbage collection.\n"

	existing, _ := os.ReadFile(gitignorePath)
	if string(existing) == newContent {
		return nil // already up to date
	}

	return os.WriteFile(gitignorePath, []byte(newContent), 0644)
}
