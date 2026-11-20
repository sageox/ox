package mediapool

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestPutReadRoundTripAndRefusesWrongDigest(t *testing.T) {
	dir := t.TempDir()
	data := []byte("image bytes")
	if err := Put(dir, sum(data), data); err != nil {
		t.Fatal(err)
	}
	got, err := Read(dir, sum(data))
	if err != nil || string(got) != string(data) {
		t.Fatalf("read = %q, %v", got, err)
	}
	if err := Put(dir, sum([]byte("other")), data); err == nil {
		t.Fatalf("Put accepted bytes that do not match their digest")
	}
	info, err := os.Stat(filepath.Join(dir, dirName, sum(data)))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("staged file mode = %v, %v; image bytes are content, owner-only", info, err)
	}
}

func TestReadMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	if _, err := Read(dir, sum([]byte("nope"))); !errors.Is(err, ErrMissing) {
		t.Fatalf("err = %v, want ErrMissing", err)
	}
	data := []byte("real")
	_ = Put(dir, sum(data), data)
	if err := os.WriteFile(filepath.Join(dir, dirName, sum(data)), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir, sum(data)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
	if _, err := Read(dir, "../../etc/passwd"); err == nil {
		t.Fatalf("Read accepted a path-shaped digest")
	}
}

func TestPutSweepsOldFilesOnly(t *testing.T) {
	dir := t.TempDir()
	old, fresh := []byte("old"), []byte("fresh")
	_ = Put(dir, sum(old), old)
	past := time.Now().Add(-2 * maxAge)
	if err := os.Chtimes(filepath.Join(dir, dirName, sum(old)), past, past); err != nil {
		t.Fatal(err)
	}
	if err := Put(dir, sum(fresh), fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir, sum(old)); !errors.Is(err, ErrMissing) {
		t.Fatalf("old file survived the sweep: %v", err)
	}
	if _, err := Read(dir, sum(fresh)); err != nil {
		t.Fatalf("fresh file swept: %v", err)
	}
}
