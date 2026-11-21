package session

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/sessionid"
)

// UploadRetryPendingFile records the ID chosen for a local Session upload.
const UploadRetryPendingFile = ".upload-retry-pending"

// WriteUploadRetryPending binds the chosen publication ID to the exact cache
// bytes. A same-minute recording can reuse the cache directory and leave an
// earlier marker behind.
func WriteUploadRetryPending(cachePath, sessionID string) error {
	root, err := os.OpenRoot(cachePath)
	if err != nil {
		return err
	}
	defer root.Close()
	digest, err := sessionUploadRawDigest(root)
	if err != nil {
		return err
	}
	return fileutil.AtomicWriteBytesInRoot(root, UploadRetryPendingFile, []byte(sessionID+"\n"+digest+"\n"), 0o600)
}

// PendingUploadID returns the publication ID only if it belongs to the
// current raw recording. It may differ from the ID in an adopted draft's
// original recording header or supply the ID missing from a legacy header.
func PendingUploadID(cachePath string) string {
	root, err := os.OpenRoot(cachePath)
	if err != nil {
		return ""
	}
	defer root.Close()
	marker, err := root.ReadFile(UploadRetryPendingFile)
	if err != nil {
		return ""
	}
	id, expected, ok := strings.Cut(strings.TrimSpace(string(marker)), "\n")
	if !ok || !sessionid.IsValidSessionID(id) {
		return ""
	}
	digest, err := sessionUploadRawDigest(root)
	if err != nil || digest != expected {
		return ""
	}
	return id
}

func sessionUploadRawDigest(root *os.Root) (string, error) {
	raw, err := root.Open("raw.jsonl")
	if err != nil {
		return "", err
	}
	defer raw.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, raw); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
