package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/fileutil"
)

// envInstallID overrides the stored install ID. The daemon has honored it since
// before the CLI shared the ID.
const envInstallID = "SAGEOX_CLIENT_ID"

// installIDFile keeps the name the daemon has always written, so installs that
// already have an ID keep it.
const installIDFile = "client_id"

// InstallID returns the random ID that identifies this install in usage
// telemetry: PostHog's distinct_id and the daemon's OTel client.id. It is
// created on first use in the user config directory and read back unchanged
// on every later run, by the CLI and the daemon alike.
//
// A non-nil error means the ID could not be read or saved. The ID returned
// with it lasts only for this process, so callers that count installs must
// not send it.
func InstallID() (string, error) {
	if id := os.Getenv(envInstallID); id != "" {
		return id, nil
	}

	// Without a home directory the config directory comes back relative
	// ("sageox"), and saving there would drop the ID into whatever directory
	// the command ran in.
	dir := GetUserConfigDir()
	if !filepath.IsAbs(dir) {
		return uuid.NewString(), os.ErrNotExist
	}
	path := filepath.Join(dir, installIDFile)
	if id := readInstallID(path); id != "" {
		return id, nil
	}

	// First use. The lock makes concurrent first runs, such as a SessionStart
	// hook and the daemon it starts, settle on one ID instead of each saving
	// its own. Its holder only reads and writes this small file, so a wait
	// longer than a second means locking itself is failing (for example, a
	// lock directory that belongs to another user), and saving unlocked still
	// beats an ID that changes on every run.
	id := uuid.NewString()
	save := func() error {
		if existing := readInstallID(path); existing != "" {
			id = existing
			return nil
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		return fileutil.AtomicWriteBytes(path, []byte(id+"\n"), 0o600)
	}
	if err := fileutil.WithFileLockTimeout(context.Background(), path, time.Second, save); err != nil {
		return id, save()
	}
	return id, nil
}

// readInstallID returns "" unless the file holds a UUID, so a damaged file is
// replaced once rather than sent as an ID.
func readInstallID(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(data))
	if _, err := uuid.Parse(id); err != nil {
		return ""
	}
	return id
}
