package config

import (
	"os"
	"path/filepath"
	"strings"

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

	return saveNewInstallID(dir, path, uuid.NewString())
}

// saveNewInstallID saves id unless another process saved an ID first, and
// returns the ID the file ends up holding. The ID is written to a temporary
// file and hard-linked into place: the link either publishes a complete file
// or fails because one already exists, so concurrent first runs (a
// SessionStart hook and the daemon it starts) all settle on one ID.
func saveNewInstallID(dir, path, id string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return id, err
	}
	tmp, err := os.CreateTemp(dir, "."+installIDFile+"-*")
	if err != nil {
		return id, err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.WriteString(id + "\n")
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return id, err
	}
	if os.Link(tmp.Name(), path) == nil {
		return id, nil
	}
	if existing := readInstallID(path); existing != "" {
		return existing, nil
	}
	// A damaged file, or a filesystem without hard links: replace it. Only
	// two such runs racing each other could still disagree, once.
	return id, fileutil.AtomicWriteBytes(path, []byte(id+"\n"), 0o600)
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
