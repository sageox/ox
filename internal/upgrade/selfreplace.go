// Package upgrade implements in-place self-replacement of the ox binary for
// installs that were placed on disk directly (the curl | bash install.sh path).
//
// Homebrew and `go install` installs upgrade through their own package managers;
// this package covers the remaining cohort, which previously had no automatic
// upgrade at all — ox upgrade merely printed manual instructions.
//
// Security model: the release tarball and the release checksums.txt are both
// fetched over HTTPS from github.com, and the tarball's SHA-256 is verified
// against the checksum before anything is written over a real binary. This is
// strictly stronger than install.sh, which verifies nothing on download.
// Stronger provenance (ed25519 verification via internal/signing, or GitHub
// build-provenance attestations) is tracked as follow-up.
package upgrade

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/sageox/ox/internal/useragent"
)

// versionRe bounds the target version to a semver-ish shape BEFORE it is
// interpolated into a download URL. Without this, a crafted --target (e.g.
// "../../evil" or a full URL) could redirect the download off the release
// path. Accepts "1.2.3" and common suffixes ("1.2.3-rc1", "1.2.3-next").
var versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-.+][0-9A-Za-z.-]+)?$`)

// ErrNotWritable is returned when the directory holding the ox binary cannot be
// written to (e.g. /usr/local/bin owned by root). Callers should surface a hint
// to re-run with elevated permissions rather than failing opaquely.
var ErrNotWritable = errors.New("install directory is not writable")

// defaultReleaseBase is the GitHub release asset download root. Overridable in
// Config for tests.
const defaultReleaseBase = "https://github.com/sageox/ox/releases/download"

// maxDownloadBytes caps an individual asset download to guard against a
// malicious or misbehaving server streaming unbounded data.
const maxDownloadBytes = 256 << 20 // 256 MiB

// maxEntryBytes caps a single decompressed archive entry, and maxTotalBytes
// caps the whole archive's declared decompressed size. Together they bound a
// decompression bomb whose expanded contents dwarf the compressed download.
// Vars (not consts) only so tests can lower them to exercise the guard.
var (
	maxEntryBytes int64 = 256 << 20 // 256 MiB per entry
	maxTotalBytes int64 = 512 << 20 // 512 MiB total decompressed
)

// renameFunc is os.Rename, indirected so tests can force a rename failure and
// exercise the all-or-nothing rollback path.
var renameFunc = os.Rename

// afterStageHook, when non-nil, runs after each binary is staged; a test uses
// it to fail mid-staging and verify the temp-file cleanup defer.
var afterStageHook func(name string) error

// Config controls a self-replace operation. Network endpoints and the install
// directory are injectable so the flow is testable without touching the real
// binary or reaching the real GitHub.
type Config struct {
	// Version is the target release WITHOUT a leading "v" (e.g. "0.14.0").
	Version string
	// OS and Arch select the platform asset; default to the running platform.
	OS   string
	Arch string
	// ReleaseBase is the download root; defaults to the GitHub releases URL.
	ReleaseBase string
	// HTTPClient is used for all downloads; defaults to http.DefaultClient.
	HTTPClient *http.Client
	// InstallDir holds the ox binary; defaults to the directory of the running
	// executable. Tests set this to a temp dir.
	InstallDir string
	// DisableCodesign skips the macOS ad-hoc re-sign step (used in tests).
	DisableCodesign bool
}

func (c *Config) applyDefaults() {
	if c.OS == "" {
		c.OS = runtime.GOOS
	}
	if c.Arch == "" {
		c.Arch = runtime.GOARCH
	}
	if c.ReleaseBase == "" {
		c.ReleaseBase = defaultReleaseBase
	}
	if c.HTTPClient == nil {
		c.HTTPClient = newHardenedClient()
	}
}

// newHardenedClient returns an HTTP client that pins redirects to GitHub hosts
// and caps total request time, so a redirect cannot steer the download to an
// attacker-controlled origin and a stalled connection cannot hang forever.
func newHardenedClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Minute,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if !isGitHubHost(req.URL.Hostname()) {
				return fmt.Errorf("refusing redirect to non-GitHub host %q", req.URL.Hostname())
			}
			return nil
		},
	}
}

// isGitHubHost reports whether host is github.com or a *.githubusercontent.com
// CDN host (where release assets actually live behind a 302).
func isGitHubHost(host string) bool {
	host = strings.ToLower(host)
	return host == "github.com" || host == "githubusercontent.com" ||
		strings.HasSuffix(host, ".githubusercontent.com")
}

// AssetName returns the release tarball name for a platform, matching the
// GoReleaser name_template "{{.ProjectName}}_{{.Version}}_{{.Os}}_{{.Arch}}".
func AssetName(version, goos, goarch string) string {
	return fmt.Sprintf("ox_%s_%s_%s.tar.gz", version, goos, goarch)
}

// ReplaceRunningBinary downloads the release tarball for cfg.Version, verifies
// its SHA-256 against the release checksums.txt, and atomically replaces the ox
// binary — plus any bundled adapter binaries already installed alongside it —
// in place. The ox binary itself is replaced last so a mid-flight failure
// leaves the running binary intact.
func ReplaceRunningBinary(ctx context.Context, cfg Config) error {
	cfg.applyDefaults()
	if cfg.Version == "" {
		return errors.New("upgrade: empty target version")
	}
	if !versionRe.MatchString(cfg.Version) {
		return fmt.Errorf("upgrade: invalid target version %q", cfg.Version)
	}

	installDir, err := resolveInstallDir(cfg.InstallDir)
	if err != nil {
		return err
	}
	if err := checkWritable(installDir); err != nil {
		return err
	}

	asset := AssetName(cfg.Version, cfg.OS, cfg.Arch)
	tagBase := fmt.Sprintf("%s/v%s", strings.TrimRight(cfg.ReleaseBase, "/"), cfg.Version)

	// fetch and parse the checksum manifest first, so a download can be
	// verified the moment it completes.
	sums, err := fetchChecksums(ctx, cfg.HTTPClient, tagBase+"/checksums.txt")
	if err != nil {
		return fmt.Errorf("fetch checksums: %w", err)
	}
	wantSum, ok := sums[asset]
	if !ok {
		return fmt.Errorf("no checksum for %s in release v%s (unsupported platform?)", asset, cfg.Version)
	}

	tarball, err := download(ctx, cfg.HTTPClient, tagBase+"/"+asset)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset, err)
	}
	gotSum := sha256.Sum256(tarball)
	if hex.EncodeToString(gotSum[:]) != wantSum {
		return fmt.Errorf("checksum mismatch for %s: refusing to install", asset)
	}

	// extract the binaries we care about into staged temp files next to the
	// targets (same filesystem => rename is atomic).
	staged, err := stageBinaries(ctx, tarball, installDir)
	if err != nil {
		return err
	}
	// always clean up any staged file we do not end up renaming.
	defer func() {
		for _, s := range staged {
			_ = os.Remove(s.tmpPath)
		}
	}()

	// re-sign on macOS so Gatekeeper does not slow-path the fresh binary,
	// mirroring install.sh. Best-effort and non-fatal.
	if cfg.OS == "darwin" && !cfg.DisableCodesign {
		for _, s := range staged {
			codesignAdHoc(s.tmpPath)
		}
	}

	// commit all-or-nothing: adapters first, ox last, so a partial failure
	// never leaves a version-skewed ox/adapter set. Each replaced binary is
	// backed up first; any failure rolls back every completed rename.
	return commitStaged(orderAdaptersFirst(staged))
}

// orderAdaptersFirst returns staged binaries with adapters before ox, so ox —
// the running binary — is the last thing swapped.
func orderAdaptersFirst(staged []stagedBinary) []stagedBinary {
	ordered := make([]stagedBinary, 0, len(staged))
	for _, s := range staged {
		if !s.isOx {
			ordered = append(ordered, s)
		}
	}
	for _, s := range staged {
		if s.isOx {
			ordered = append(ordered, s)
		}
	}
	return ordered
}

// commitStaged atomically swaps each staged binary into place, backing up the
// prior file first. If any rename fails, every already-committed rename is
// rolled back (originals restored, new files removed) so the install is either
// fully upgraded or fully unchanged.
func commitStaged(ordered []stagedBinary) error {
	type commit struct {
		destPath, backupPath string
		hadOriginal          bool
	}
	var done []commit

	// rollback undoes every recorded rename and returns any restore failures.
	// A failed restore is reported (with the backup path) rather than swallowed,
	// so the caller can point the user at a recoverable original instead of
	// silently stranding it.
	rollback := func() []string {
		var problems []string
		for i := len(done) - 1; i >= 0; i-- {
			c := done[i]
			if err := os.Remove(c.destPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				problems = append(problems, fmt.Sprintf("remove %s: %v", c.destPath, err))
			}
			if c.hadOriginal {
				if err := renameFunc(c.backupPath, c.destPath); err != nil {
					problems = append(problems, fmt.Sprintf("restore %s (original preserved at %s): %v", c.destPath, c.backupPath, err))
				}
			}
		}
		return problems
	}

	for _, s := range ordered {
		backupPath := s.tmpPath + ".bak" // unique + on the same filesystem
		hadOriginal := false
		if _, err := os.Stat(s.destPath); err == nil {
			if err := renameFunc(s.destPath, backupPath); err != nil {
				return withRollbackProblems(fmt.Errorf("back up %s: %w", filepath.Base(s.destPath), err), rollback())
			}
			hadOriginal = true
		}
		if err := renameFunc(s.tmpPath, s.destPath); err != nil {
			// record this entry so rollback restores its just-made backup too,
			// then unwind everything committed so far.
			done = append(done, commit{destPath: s.destPath, backupPath: backupPath, hadOriginal: hadOriginal})
			return withRollbackProblems(fmt.Errorf("replace %s: %w", filepath.Base(s.destPath), err), rollback())
		}
		done = append(done, commit{destPath: s.destPath, backupPath: backupPath, hadOriginal: hadOriginal})
	}

	// success: drop the backups.
	for _, c := range done {
		if c.hadOriginal {
			_ = os.Remove(c.backupPath)
		}
	}
	return nil
}

// withRollbackProblems attaches any rollback restore failures to the primary
// error so a partially-unwound install surfaces the stranded backups instead
// of hiding them behind the original failure.
func withRollbackProblems(primary error, problems []string) error {
	if len(problems) == 0 {
		return primary
	}
	return fmt.Errorf("%w; rollback incomplete, manual recovery may be needed: %s",
		primary, strings.Join(problems, "; "))
}

// resolveInstallDir returns the directory holding the ox binary. When override
// is set (tests), it is used verbatim; otherwise the running executable's
// resolved directory is used.
func resolveInstallDir(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe), nil
}

// checkWritable verifies we can create files in dir, returning ErrNotWritable
// (wrapped with the path) otherwise so callers can suggest sudo.
func checkWritable(dir string) error {
	probe, err := os.CreateTemp(dir, ".ox-upgrade-probe-*")
	if err != nil {
		return fmt.Errorf("%w: %s", ErrNotWritable, dir)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}

// stagedBinary is one extracted binary written to a temp file, ready to rename
// over its destination.
type stagedBinary struct {
	destPath string
	tmpPath  string
	isOx     bool
}

// stageBinaries writes the ox binary — and any adapter already installed
// alongside it — from the tarball into sibling temp files ready to rename.
//
// Zip-slip safety by construction: the archive's entry names are used ONLY as
// map keys to look up file *content*; every filesystem path written here is
// built from an install-target name that comes from the local filesystem
// (os.ReadDir) or the literal "ox", never from the archive. An archive entry
// named "../../etc/whatever" therefore cannot influence any path — it simply
// never matches an install target.
func stageBinaries(ctx context.Context, tarball []byte, installDir string) (staged []stagedBinary, retErr error) {
	contents, err := readArchiveBinaries(ctx, tarball)
	if err != nil {
		return nil, err
	}
	// the ox binary itself must be present: an adapter-only archive must never
	// replace adapters while leaving ox on the old version.
	if _, ok := contents["ox"]; !ok {
		return nil, errors.New("release tarball contains no ox binary")
	}

	// if staging fails partway, remove the temp files already written so a
	// partial run leaves no .ox-upgrade-* litter behind.
	defer func() {
		if retErr != nil {
			for _, s := range staged {
				_ = os.Remove(s.tmpPath)
			}
		}
	}()

	cleanInstallDir := filepath.Clean(installDir)
	// error returns below are bare so the named `staged` result survives for
	// the deferred cleanup — `return nil, err` would blank it and leak temps.
	targets, err := installTargets(cleanInstallDir)
	if err != nil {
		retErr = err
		return
	}
	for _, name := range targets {
		data, ok := contents[name]
		if !ok {
			continue // installed here but not shipped in this release
		}
		// name is a trusted install-target ("ox" literal or an os.ReadDir
		// entry), so this path is not archive-derived.
		destPath := filepath.Join(cleanInstallDir, name)
		tmp, err := os.CreateTemp(cleanInstallDir, ".ox-upgrade-*")
		if err != nil {
			retErr = fmt.Errorf("stage %s: %w", name, err)
			return
		}
		if _, err := tmp.Write(data); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
			retErr = fmt.Errorf("stage %s: %w", name, err)
			return
		}
		if err := tmp.Chmod(0o755); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
			retErr = fmt.Errorf("chmod %s: %w", name, err)
			return
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmp.Name())
			retErr = err
			return
		}
		staged = append(staged, stagedBinary{destPath: destPath, tmpPath: tmp.Name(), isOx: name == "ox"})
		if afterStageHook != nil {
			if err := afterStageHook(name); err != nil {
				retErr = err
				return
			}
		}
	}
	return staged, nil
}

// installTargets returns the binaries to replace: ox (always, as the running
// binary) plus any ox-adapter-* already present in installDir. Names come from
// the local filesystem, never the archive. A directory-read failure is an
// error, not a silent fallback to ox-only — that would upgrade ox while
// leaving installed adapters behind, skewing their protocol versions.
func installTargets(installDir string) ([]string, error) {
	entries, err := os.ReadDir(installDir)
	if err != nil {
		return nil, fmt.Errorf("list install dir %s: %w", installDir, err)
	}
	targets := []string{"ox"}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if name := e.Name(); strings.HasPrefix(name, "ox-adapter-") {
			targets = append(targets, name)
		}
	}
	return targets, nil
}

// readArchiveBinaries reads the ox and ox-adapter-* regular files from the
// tarball into memory, keyed by basename. Entry names are used only as map
// keys (never as filesystem paths), and decompression is bounded per-entry and
// in total to cap a decompression bomb.
func readArchiveBinaries(ctx context.Context, tarball []byte) (map[string][]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tarball))
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	out := make(map[string][]byte)
	var totalBytes int64
	tr := tar.NewReader(gz)
	for {
		// bound extraction by the caller's deadline: a decompression-heavy
		// archive must not stream-discard entries past the timeout.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if hdr.Size < 0 || hdr.Size > maxEntryBytes {
			return nil, fmt.Errorf("archive entry has invalid size %d", hdr.Size)
		}
		totalBytes += hdr.Size
		if totalBytes > maxTotalBytes {
			return nil, fmt.Errorf("archive exceeds %d bytes decompressed", maxTotalBytes)
		}

		base := filepath.Base(hdr.Name)
		if base != "ox" && !strings.HasPrefix(base, "ox-adapter-") {
			continue // LICENSE, README, CHANGELOG, etc.
		}
		// hdr.Size <= maxEntryBytes was checked above, so this reads the whole
		// entry rather than truncating a large binary.
		data, err := io.ReadAll(io.LimitReader(tr, hdr.Size))
		if err != nil {
			return nil, fmt.Errorf("extract %s: %w", base, err)
		}
		out[base] = data
	}
	return out, nil
}

// fetchChecksums downloads and parses a GoReleaser checksums.txt (lines of
// "<sha256hex>  <filename>") into a filename -> hex map.
func fetchChecksums(ctx context.Context, client *http.Client, url string) (map[string]string, error) {
	body, err := download(ctx, client, url)
	if err != nil {
		return nil, err
	}
	sums := make(map[string]string)
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		sums[fields[1]] = strings.ToLower(fields[0])
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return sums, nil
}

// download GETs url and returns the body, bounded by maxDownloadBytes.
func download(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", useragent.String())
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes))
}

// codesignAdHoc re-signs a binary ad-hoc on macOS so Gatekeeper does not
// slow-path it. Best-effort: absence of codesign or a failure is ignored.
func codesignAdHoc(path string) {
	if _, err := exec.LookPath("codesign"); err != nil {
		return
	}
	_ = exec.Command("codesign", "--force", "--sign", "-", path).Run()
}
