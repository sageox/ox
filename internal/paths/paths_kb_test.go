package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestKBDir_EndpointScopedUnderDataHome verifies KB checkouts land under the
// XDG data dir with the active endpoint slug interposed.
//
// Failure prevented: bubbles silently appearing in the wrong XDG bucket
// (cache vs data) — that would cause OS cache cleaners to delete pulled
// git checkouts that should be persistent.
func TestKBDir_EndpointScopedUnderDataHome(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE", "SAGEOX_ENDPOINT", "XDG_DATA_HOME")
	defer restoreEnv(saved)

	clearXDGEnv()
	tmp := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmp)
	os.Setenv("SAGEOX_ENDPOINT", "https://decoy.example.invalid") // must not leak into the explicit-ep path

	dir := KBDir("https://api.sageox.ai", "kb_abc123")
	want := filepath.Join(tmp, "sageox", "sageox.ai", "kb", "kb_abc123")
	if dir != want {
		t.Errorf("KBDir(kb_abc123) = %q, want %q", dir, want)
	}
}

// TestKBDir_EndpointIsolation verifies a kb_id resolves to distinct on-disk
// paths under staging vs production.
//
// Failure prevented: staging data leaking into prod (or vice versa) through
// path collision — explicitly called out in CLAUDE.md as the reason endpoint
// scoping exists at all.
func TestKBDir_EndpointIsolation(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE", "SAGEOX_ENDPOINT", "XDG_DATA_HOME")
	defer restoreEnv(saved)
	clearXDGEnv()
	os.Setenv("XDG_DATA_HOME", t.TempDir())

	os.Setenv("SAGEOX_ENDPOINT", "https://decoy.example.invalid") // must not leak into the explicit-ep path
	staging := KBDir("https://staging.sageox.ai", "kb_xyz")

	prod := KBDir("https://api.sageox.ai", "kb_xyz")

	if staging == prod {
		t.Fatalf("KBDir for staging and prod must differ; both = %q", staging)
	}
	if !strings.Contains(staging, filepath.Join("staging.sageox.ai", "kb")) {
		t.Errorf("staging path missing staging.sageox.ai/kb: %q", staging)
	}
	if !strings.Contains(prod, filepath.Join("sageox.ai", "kb")) {
		t.Errorf("prod path missing sageox.ai/kb: %q", prod)
	}
	if strings.Contains(prod, "staging.sageox.ai") {
		t.Errorf("prod path leaked staging slug: %q", prod)
	}
}

// TestKBDir_EmptyKBIDReturnsBase verifies passing an empty kb_id returns the
// base kb directory for the endpoint, matching LedgersDataDir's convention.
//
// Failure prevented: callers needing to enumerate "all bubbles on disk" would
// otherwise have to reach into internals or hardcode the layout.
func TestKBDir_EmptyKBIDReturnsBase(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE", "SAGEOX_ENDPOINT", "XDG_DATA_HOME")
	defer restoreEnv(saved)
	clearXDGEnv()
	os.Setenv("XDG_DATA_HOME", t.TempDir())
	os.Setenv("SAGEOX_ENDPOINT", "https://decoy.example.invalid") // must not leak into the explicit-ep path

	dir := KBDir("https://sageox.ai", "")
	if !strings.HasSuffix(dir, filepath.Join("sageox.ai", "kb")) {
		t.Errorf("KBDir('') = %q, want suffix sageox.ai/kb", dir)
	}
	// must NOT have a synthetic component appended
	if strings.HasSuffix(dir, filepath.Join("kb", "unknown")) {
		t.Errorf("KBDir('') = %q, should not have unknown suffix", dir)
	}
}

// TestKBDir_KBIDSanitization verifies path separators in kb_id are scrubbed
// to prevent directory traversal.
//
// Failure prevented: a maliciously crafted kb_id like "../../etc" would
// otherwise let an attacker write outside the kb tree.
func TestKBDir_KBIDSanitization(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE", "SAGEOX_ENDPOINT", "XDG_DATA_HOME")
	defer restoreEnv(saved)
	clearXDGEnv()
	os.Setenv("XDG_DATA_HOME", t.TempDir())
	os.Setenv("SAGEOX_ENDPOINT", "https://decoy.example.invalid") // must not leak into the explicit-ep path

	dir := KBDir("https://sageox.ai", "../../etc/passwd")
	if strings.Contains(dir, "..") {
		t.Errorf("KBDir traversal not sanitized: %q", dir)
	}
}

// TestKBCacheDir_UsesCacheNotDataDir verifies derived KB state lands under
// the XDG cache home, not the data home.
//
// Failure prevented: bloating the user's persistent data tier with derived
// indexes that should be regeneratable and OS-evictable.
func TestKBCacheDir_UsesCacheNotDataDir(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE", "SAGEOX_ENDPOINT", "XDG_CACHE_HOME", "XDG_DATA_HOME")
	defer restoreEnv(saved)
	clearXDGEnv()

	cacheRoot := t.TempDir()
	dataRoot := t.TempDir()
	os.Setenv("XDG_CACHE_HOME", cacheRoot)
	os.Setenv("XDG_DATA_HOME", dataRoot)
	os.Setenv("SAGEOX_ENDPOINT", "https://decoy.example.invalid") // must not leak into the explicit-ep path

	cache := KBCacheDir("https://sageox.ai", "kb_abc")
	data := KBDir("https://sageox.ai", "kb_abc")

	wantCache := filepath.Join(cacheRoot, "sageox", "sageox.ai", "kb", "kb_abc")
	if cache != wantCache {
		t.Errorf("KBCacheDir(kb_abc) = %q, want %q", cache, wantCache)
	}

	// crucial property: cache dir must not be under data dir
	if strings.HasPrefix(cache, dataRoot) {
		t.Errorf("KBCacheDir leaked into data tier: cache=%q dataRoot=%q", cache, dataRoot)
	}
	if strings.HasPrefix(data, cacheRoot) {
		t.Errorf("KBDir leaked into cache tier: data=%q cacheRoot=%q", data, cacheRoot)
	}
}

// TestKBCacheDir_EndpointIsolation mirrors the data-dir isolation test for
// the cache tier.
//
// Failure prevented: a stale staging index served as if it were prod.
func TestKBCacheDir_EndpointIsolation(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE", "SAGEOX_ENDPOINT", "XDG_CACHE_HOME")
	defer restoreEnv(saved)
	clearXDGEnv()
	os.Setenv("XDG_CACHE_HOME", t.TempDir())

	os.Setenv("SAGEOX_ENDPOINT", "https://decoy.example.invalid") // must not leak into the explicit-ep path
	staging := KBCacheDir("https://staging.sageox.ai", "kb_xyz")

	prod := KBCacheDir("https://sageox.ai", "kb_xyz")

	if staging == prod {
		t.Fatalf("KBCacheDir for staging and prod must differ; both = %q", staging)
	}
}

// TestProjectKBLink_Composition verifies the per-project symlink path is
// the slug-keyed location inside the project's .sageox/kb/ directory.
//
// Failure prevented: drift between what the daemon writes (canonical layout)
// and what AI coworkers reference in skills (slug-keyed link).
func TestProjectKBLink_Composition(t *testing.T) {
	link := ProjectKBLink("/home/user/proj", "personal")
	want := filepath.Join("/home/user/proj", ".sageox", "kb", "personal")
	if link != want {
		t.Errorf("ProjectKBLink = %q, want %q", link, want)
	}
}

// TestProjectKBLink_TrailingSlashOnRoot verifies a trailing slash on the
// project root is normalized away.
//
// Failure prevented: stat/symlink calls failing on doubled separators when
// callers pass paths that came from different sources (config vs git rev-parse).
func TestProjectKBLink_TrailingSlashOnRoot(t *testing.T) {
	link := ProjectKBLink("/home/user/proj/", "personal")
	want := filepath.Join("/home/user/proj", ".sageox", "kb", "personal")
	if link != want {
		t.Errorf("ProjectKBLink with trailing slash = %q, want %q", link, want)
	}
}

// TestProjectKBLink_EmptyInputsReturnEmpty verifies empty projectRoot or slug
// returns "", matching the convention in CodeDBSharedDir / WhisperDBDir.
//
// Failure prevented: a caller with a missing slug would otherwise scribble
// into <project>/.sageox/kb/ at the directory level itself, clobbering the
// whole symlink tree.
func TestProjectKBLink_EmptyInputsReturnEmpty(t *testing.T) {
	if got := ProjectKBLink("", "personal"); got != "" {
		t.Errorf("ProjectKBLink('', slug) = %q, want empty", got)
	}
	if got := ProjectKBLink("/home/user/proj", ""); got != "" {
		t.Errorf("ProjectKBLink(root, '') = %q, want empty", got)
	}
	if got := ProjectKBLink("", ""); got != "" {
		t.Errorf("ProjectKBLink('', '') = %q, want empty", got)
	}
}

// TestProjectKBLink_SlugSanitization verifies slug values containing path
// separators are scrubbed before being used as a filesystem name.
//
// Failure prevented: a kb whose slug accidentally (or maliciously) contains
// "/" would otherwise let the daemon write outside .sageox/kb/.
func TestProjectKBLink_SlugSanitization(t *testing.T) {
	link := ProjectKBLink("/home/user/proj", "../escape")
	if strings.Contains(link, "..") {
		t.Errorf("ProjectKBLink slug traversal not sanitized: %q", link)
	}
	if !strings.HasPrefix(link, filepath.Join("/home/user/proj", ".sageox", "kb")) {
		t.Errorf("ProjectKBLink escaped its base dir: %q", link)
	}
}

// TestKBDir_XDGDataHomeOverride verifies XDG_DATA_HOME is honored.
//
// Failure prevented: users with non-default XDG layouts (Nix, dotfiles
// frameworks, custom $HOME) would otherwise see KB content in the wrong
// place — and ox doctor wouldn't know to look there either.
func TestKBDir_XDGDataHomeOverride(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE", "SAGEOX_ENDPOINT", "XDG_DATA_HOME")
	defer restoreEnv(saved)
	clearXDGEnv()

	custom := t.TempDir()
	os.Setenv("XDG_DATA_HOME", custom)
	os.Setenv("SAGEOX_ENDPOINT", "https://decoy.example.invalid") // must not leak into the explicit-ep path

	dir := KBDir("https://sageox.ai", "kb_abc")
	if !strings.HasPrefix(dir, custom) {
		t.Errorf("KBDir = %q, want prefix %q (XDG_DATA_HOME override)", dir, custom)
	}
}

// TestKBCacheDir_XDGCacheHomeOverride verifies XDG_CACHE_HOME is honored.
//
// Failure prevented: same as TestKBDir_XDGDataHomeOverride but for cache.
func TestKBCacheDir_XDGCacheHomeOverride(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE", "SAGEOX_ENDPOINT", "XDG_CACHE_HOME")
	defer restoreEnv(saved)
	clearXDGEnv()

	custom := t.TempDir()
	os.Setenv("XDG_CACHE_HOME", custom)
	os.Setenv("SAGEOX_ENDPOINT", "https://decoy.example.invalid") // must not leak into the explicit-ep path

	dir := KBCacheDir("https://sageox.ai", "kb_abc")
	if !strings.HasPrefix(dir, custom) {
		t.Errorf("KBCacheDir = %q, want prefix %q (XDG_CACHE_HOME override)", dir, custom)
	}
}

// TestKBDir_LegacyXDGDisable verifies OX_XDG_DISABLE routes KB paths into
// the legacy ~/.sageox tree, mirroring TeamsDataDir / LedgersDataDir behavior.
//
// Failure prevented: users on the OX_XDG_DISABLE escape hatch would see
// inconsistent path layout — some helpers honoring the flag, others not.
func TestKBDir_LegacyXDGDisable(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE", "SAGEOX_ENDPOINT")
	defer restoreEnv(saved)
	clearXDGEnv()

	os.Setenv("OX_XDG_DISABLE", "1")
	os.Setenv("SAGEOX_ENDPOINT", "https://decoy.example.invalid") // must not leak into the explicit-ep path

	dir := KBDir("https://sageox.ai", "kb_abc")
	// legacy mode: ~/.sageox/data/<endpoint>/kb/<kb_id>
	if !strings.Contains(dir, filepath.Join(".sageox", "data", "sageox.ai", "kb", "kb_abc")) {
		t.Errorf("KBDir under OX_XDG_DISABLE = %q, want path under .sageox/data/sageox.ai/kb/kb_abc", dir)
	}
}
