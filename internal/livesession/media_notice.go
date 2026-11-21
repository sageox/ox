package livesession

import (
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/paths"
)

// PeekMediaNotice reports whether the screenshot notice is owed without marking
// it given. A caller that can fail to deliver the notice (its output write may
// fail) peeks, delivers, then calls ConfirmMediaNotice, so a lost notice is
// still owed at the next stop.
func PeekMediaNotice(dataDir string) bool {
	b, err := os.ReadFile(mediaNoticePath(dataDir))
	return err == nil && string(b) == "owed\n"
}

// ConfirmMediaNotice marks an owed notice given, once it was delivered.
func ConfirmMediaNotice(dataDir string) {
	path := mediaNoticePath(dataDir)
	if b, err := os.ReadFile(path); err == nil && string(b) == "owed\n" {
		_ = os.WriteFile(path, []byte("given\n"), 0o600) // unwritable: the notice is said again, never lost
	}
}

func mediaNoticePath(dataDir string) string {
	if dataDir == "" {
		dataDir = paths.DataDir()
	}
	return filepath.Join(dataDir, mediaNoticeFile)
}
