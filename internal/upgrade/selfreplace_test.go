package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testVer  = "9.9.9"
	testOS   = "testos"
	testArch = "testarch"
)

// makeTarball builds an in-memory .tar.gz from name->content, each a 0755 file.
func makeTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write body %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// releaseServer serves a fake GitHub release: /v<ver>/checksums.txt and the
// platform tarball. checksumOverride, when non-empty, replaces the real
// tarball hash so the mismatch path can be exercised.
func releaseServer(t *testing.T, tarball []byte, checksumOverride string) *httptest.Server {
	t.Helper()
	asset := AssetName(testVer, testOS, testArch)
	sum := sha256hex(tarball)
	if checksumOverride != "" {
		sum = checksumOverride
	}
	checksums := fmt.Sprintf("%s  %s\n%s  %s\n", sum, asset, sha256hex([]byte("other")), "ox_9.9.9_other_arch.tar.gz")

	mux := http.NewServeMux()
	mux.HandleFunc("/v"+testVer+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(checksums))
	})
	mux.HandleFunc("/v"+testVer+"/"+asset, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(tarball)
	})
	return httptest.NewServer(mux)
}

func baseConfig(installDir, releaseBase string) Config {
	return Config{
		Version:         testVer,
		OS:              testOS,
		Arch:            testArch,
		ReleaseBase:     releaseBase,
		InstallDir:      installDir,
		DisableCodesign: true,
	}
}

func TestReplaceRunningBinary_HappyPath(t *testing.T) {
	dir := t.TempDir()
	// pre-existing install: ox + one adapter. A second adapter is NOT installed.
	writeFile(t, filepath.Join(dir, "ox"), "OLD-ox")
	writeFile(t, filepath.Join(dir, "ox-adapter-claude-code"), "OLD-adapter")

	tarball := makeTarball(t, map[string]string{
		"ox":                     "NEW-ox",
		"ox-adapter-claude-code": "NEW-adapter",
		"ox-adapter-gemini":      "NEW-gemini", // not installed => must be skipped
		"README.md":              "docs",       // non-binary => ignored
	})
	srv := releaseServer(t, tarball, "")
	defer srv.Close()

	if err := ReplaceRunningBinary(context.Background(), baseConfig(dir, srv.URL)); err != nil {
		t.Fatalf("ReplaceRunningBinary: %v", err)
	}

	if got := readFile(t, filepath.Join(dir, "ox")); got != "NEW-ox" {
		t.Errorf("ox not replaced: got %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "ox-adapter-claude-code")); got != "NEW-adapter" {
		t.Errorf("installed adapter not replaced: got %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "ox-adapter-gemini")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("uninstalled adapter should not have been created")
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("non-binary file should not have been extracted")
	}
	// no stray temp files left behind
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if len(e.Name()) > 0 && e.Name()[0] == '.' {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestReplaceRunningBinary_ChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ox"), "OLD-ox")

	tarball := makeTarball(t, map[string]string{"ox": "EVIL-ox"})
	// serve a wrong checksum for the asset
	srv := releaseServer(t, tarball, sha256hex([]byte("not-the-tarball")))
	defer srv.Close()

	err := ReplaceRunningBinary(context.Background(), baseConfig(dir, srv.URL))
	if err == nil {
		t.Fatal("expected checksum-mismatch error, got nil")
	}
	if got := readFile(t, filepath.Join(dir, "ox")); got != "OLD-ox" {
		t.Errorf("ox must be untouched on checksum mismatch: got %q", got)
	}
}

func TestReplaceRunningBinary_MissingPlatformAsset(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ox"), "OLD-ox")

	tarball := makeTarball(t, map[string]string{"ox": "NEW-ox"})
	srv := releaseServer(t, tarball, "")
	defer srv.Close()

	// ask for an arch that has no checksum entry
	cfg := baseConfig(dir, srv.URL)
	cfg.Arch = "no-such-arch"
	if err := ReplaceRunningBinary(context.Background(), cfg); err == nil {
		t.Fatal("expected error for unsupported platform asset")
	}
	if got := readFile(t, filepath.Join(dir, "ox")); got != "OLD-ox" {
		t.Errorf("ox must be untouched when asset missing: got %q", got)
	}
}

func TestReplaceRunningBinary_NotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ox"), "OLD-ox")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(dir, 0o700) //nolint:errcheck // best-effort cleanup

	tarball := makeTarball(t, map[string]string{"ox": "NEW-ox"})
	srv := releaseServer(t, tarball, "")
	defer srv.Close()

	err := ReplaceRunningBinary(context.Background(), baseConfig(dir, srv.URL))
	if !errors.Is(err, ErrNotWritable) {
		t.Fatalf("expected ErrNotWritable, got %v", err)
	}
}

// A crafted --target must never reach the network: it is validated and
// rejected before any URL is built. Asserts on the specific validation error
// so removing the guard (which would instead produce a network error) turns
// this test red.
func TestReplaceRunningBinary_RejectsMalformedVersion(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ox"), "OLD-ox")

	for _, bad := range []string{"../../evil", "https://evil.example/x", "1.2", "1.2.3/../x", "1.2.3 ", "v1.2.3"} {
		cfg := baseConfig(dir, "http://127.0.0.1:1")
		cfg.Version = bad
		err := ReplaceRunningBinary(context.Background(), cfg)
		if err == nil || !strings.Contains(err.Error(), "invalid target version") {
			t.Errorf("version %q: want invalid-target-version rejection, got %v", bad, err)
		}
	}
	if got := readFile(t, filepath.Join(dir, "ox")); got != "OLD-ox" {
		t.Errorf("ox must be untouched: got %q", got)
	}
}

// An archive with no ox binary must be refused outright — it must never
// replace installed adapters while leaving ox on the old version.
func TestReplaceRunningBinary_AdapterOnlyArchiveRejected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ox"), "OLD-ox")
	writeFile(t, filepath.Join(dir, "ox-adapter-claude-code"), "OLD-adapter")

	// tarball contains only an (installed) adapter, no ox.
	tarball := makeTarball(t, map[string]string{"ox-adapter-claude-code": "NEW-adapter"})
	srv := releaseServer(t, tarball, "")
	defer srv.Close()

	err := ReplaceRunningBinary(context.Background(), baseConfig(dir, srv.URL))
	if err == nil || !strings.Contains(err.Error(), "no ox binary") {
		t.Fatalf("want no-ox-binary rejection, got %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "ox-adapter-claude-code")); got != "OLD-adapter" {
		t.Errorf("adapter must be untouched when ox is absent: got %q", got)
	}
}

// A single archive entry larger than the per-entry cap must be rejected, not
// silently truncated into place.
func TestReplaceRunningBinary_OversizedEntryRejected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ox"), "OLD-ox")

	oldMax := maxEntryBytes
	maxEntryBytes = 4
	t.Cleanup(func() { maxEntryBytes = oldMax })

	tarball := makeTarball(t, map[string]string{"ox": "NEW-ox-way-too-big"})
	srv := releaseServer(t, tarball, "")
	defer srv.Close()

	if err := ReplaceRunningBinary(context.Background(), baseConfig(dir, srv.URL)); err == nil {
		t.Fatal("expected oversized-entry rejection")
	}
	if got := readFile(t, filepath.Join(dir, "ox")); got != "OLD-ox" {
		t.Errorf("ox must be untouched when an entry is oversized: got %q", got)
	}
}

// If a late rename fails mid-commit, every already-committed rename must roll
// back so the install is fully unchanged rather than version-skewed.
func TestReplaceRunningBinary_RollsBackOnLateRenameFailure(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ox"), "OLD-ox")
	writeFile(t, filepath.Join(dir, "ox-adapter-claude-code"), "OLD-adapter")

	// fail the first rename that targets the ox binary (ox commits last, after
	// the adapter is already swapped), then behave normally so rollback works.
	oldRename := renameFunc
	failed := false
	renameFunc = func(oldPath, newPath string) error {
		if !failed && filepath.Base(newPath) == "ox" {
			failed = true
			return errors.New("injected rename failure")
		}
		return oldRename(oldPath, newPath)
	}
	t.Cleanup(func() { renameFunc = oldRename })

	tarball := makeTarball(t, map[string]string{
		"ox":                     "NEW-ox",
		"ox-adapter-claude-code": "NEW-adapter",
	})
	srv := releaseServer(t, tarball, "")
	defer srv.Close()

	if err := ReplaceRunningBinary(context.Background(), baseConfig(dir, srv.URL)); err == nil {
		t.Fatal("expected error from injected rename failure")
	}
	// both binaries must be back to their originals — no partial upgrade.
	if got := readFile(t, filepath.Join(dir, "ox")); got != "OLD-ox" {
		t.Errorf("ox must be rolled back: got %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "ox-adapter-claude-code")); got != "OLD-adapter" {
		t.Errorf("adapter must be rolled back after ox rename failed: got %q", got)
	}
	// no backup/temp litter left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("leftover temp/backup file: %s", e.Name())
		}
	}
}

// When rollback itself cannot restore an original, the failure must be
// surfaced (with the preserved backup path) rather than swallowed behind the
// primary error — otherwise a stranded binary is invisible to the user.
func TestReplaceRunningBinary_RollbackFailureIsSurfaced(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ox"), "OLD-ox")
	writeFile(t, filepath.Join(dir, "ox-adapter-claude-code"), "OLD-adapter")

	// every rename that targets ox fails: the ox swap fails AND its backup
	// cannot be restored, so rollback is incomplete for ox.
	oldRename := renameFunc
	renameFunc = func(oldPath, newPath string) error {
		if filepath.Base(newPath) == "ox" {
			return errors.New("injected ox rename failure")
		}
		return oldRename(oldPath, newPath)
	}
	t.Cleanup(func() { renameFunc = oldRename })

	tarball := makeTarball(t, map[string]string{
		"ox":                     "NEW-ox",
		"ox-adapter-claude-code": "NEW-adapter",
	})
	srv := releaseServer(t, tarball, "")
	defer srv.Close()

	err := ReplaceRunningBinary(context.Background(), baseConfig(dir, srv.URL))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "rollback incomplete") {
		t.Errorf("error must surface the incomplete rollback, got: %v", err)
	}
	// the adapter (whose renames don't target ox) must still be rolled back.
	if got := readFile(t, filepath.Join(dir, "ox-adapter-claude-code")); got != "OLD-adapter" {
		t.Errorf("adapter must be rolled back: got %q", got)
	}
}

// If adapter discovery (os.ReadDir) fails, the upgrade must abort rather than
// silently replace ox alone and skew adapter versions.
func TestReplaceRunningBinary_ReadDirFailureAborts(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ox"), "OLD-ox")
	writeFile(t, filepath.Join(dir, "ox-adapter-claude-code"), "OLD-adapter")
	// 0o300 = write+execute, NO read: checkWritable's CreateTemp still works,
	// but os.ReadDir fails — the exact "discovery fails after writable check"
	// case.
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	tarball := makeTarball(t, map[string]string{
		"ox":                     "NEW-ox",
		"ox-adapter-claude-code": "NEW-adapter",
	})
	srv := releaseServer(t, tarball, "")
	defer srv.Close()

	err := ReplaceRunningBinary(context.Background(), baseConfig(dir, srv.URL))
	if err == nil {
		t.Fatal("expected error when adapter discovery fails")
	}
	// restore read access to inspect: ox must be untouched (not upgraded alone).
	_ = os.Chmod(dir, 0o700)
	if got := readFile(t, filepath.Join(dir, "ox")); got != "OLD-ox" {
		t.Errorf("ox must not be upgraded when discovery fails: got %q", got)
	}
}

// A staging failure after at least one binary is staged must clean up the
// already-written temp files (the named-return defer must see the populated
// slice, not a nil'd one).
func TestReplaceRunningBinary_PartialStagingCleansTemps(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ox"), "OLD-ox")
	writeFile(t, filepath.Join(dir, "ox-adapter-claude-code"), "OLD-adapter")

	// fail staging on the second binary (after one is already staged).
	oldHook := afterStageHook
	count := 0
	afterStageHook = func(string) error {
		count++
		if count == 2 {
			return errors.New("injected staging failure")
		}
		return nil
	}
	t.Cleanup(func() { afterStageHook = oldHook })

	tarball := makeTarball(t, map[string]string{
		"ox":                     "NEW-ox",
		"ox-adapter-claude-code": "NEW-adapter",
	})
	srv := releaseServer(t, tarball, "")
	defer srv.Close()

	if err := ReplaceRunningBinary(context.Background(), baseConfig(dir, srv.URL)); err == nil {
		t.Fatal("expected staging failure")
	}
	// both originals untouched (nothing was committed) and no temp litter.
	if got := readFile(t, filepath.Join(dir, "ox")); got != "OLD-ox" {
		t.Errorf("ox must be untouched: got %q", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".ox-upgrade-") {
			t.Errorf("leftover staged temp file: %s", e.Name())
		}
	}
}

func TestAssetName(t *testing.T) {
	if got := AssetName("0.14.0", "darwin", "arm64"); got != "ox_0.14.0_darwin_arm64.tar.gz" {
		t.Errorf("AssetName = %q", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
