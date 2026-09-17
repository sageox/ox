package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/pkg/sessionprovenance"
)

// WithPublicationLock serializes transcript replacement with derived-artifact
// writes. Acquire it before the Ledger lock, and never hold it during inference.
func WithPublicationLock(ctx context.Context, ledger, name string, fn func() error) error {
	if !sessionprovenance.ValidSessionName(name) {
		return fmt.Errorf("invalid publication session name")
	}
	dir := filepath.Join(ledger, ".sageox", "cache", "session-publication-locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return fileutil.WithFileLock(ctx, filepath.Join(dir, name), fn)
}
