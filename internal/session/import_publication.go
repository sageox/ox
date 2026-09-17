package session

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sageox/ox/pkg/sessionprovenance"
)

// CheckImportPublication keeps importer-owned partial conversions out of the
// daemon's legacy recovery path. Only the importer may publish its transaction;
// the daemon may derive summaries after the remote snapshot has been verified.
func CheckImportPublication(ledger, dir string) error {
	name := filepath.Base(dir)
	paths := []string{filepath.Join(dir, ".import-journal.json")}
	if ledger != "" {
		canonical := filepath.Join(ledger, ".sageox", "cache", "sessions", name, ".import-journal.json")
		if canonical != paths[0] {
			paths = append(paths, canonical)
		}
	}
	for _, path := range paths {
		if err := checkImportPublicationJournal(path, name); err != nil {
			return err
		}
	}
	return nil
}

func checkImportPublicationJournal(path, name string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("import journal unavailable: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	// Journals contain identities and booleans, never content; 64KiB leaves ample
	// forward-compatible metadata room without permitting unbounded daemon reads.
	if !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return fmt.Errorf("invalid import journal size or type")
	}
	var journal struct {
		Version        int    `json:"version"`
		NativeID       string `json:"native_session_id"`
		Generation     string `json:"generation"`
		SnapshotDigest string `json:"snapshot_digest"`
		SessionName    string `json:"session_name"`
		UploadVerified bool   `json:"upload_verified"`
	}
	decoder := json.NewDecoder(io.LimitReader(f, 64*1024+1))
	if err := decoder.Decode(&journal); err != nil {
		return fmt.Errorf("invalid import journal: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("invalid trailing import journal data")
	}
	if journal.Version != 1 || journal.SessionName != name {
		return fmt.Errorf("unsupported import journal identity or version")
	}
	if _, err := sessionprovenance.Path(journal.NativeID); err != nil {
		return err
	}
	for _, digest := range []string{journal.Generation, journal.SnapshotDigest} {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != 32 {
			return fmt.Errorf("invalid import journal source digest")
		}
	}
	if !journal.UploadVerified {
		return fmt.Errorf("import transaction is pending; only the importer may resume it")
	}
	return nil
}
