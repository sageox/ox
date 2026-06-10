package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These tests cover the curated-path install integrity invariant from ADR-022 /
// ox-5ihl: download -> checksum gate -> chmod -> verifyAdapterProtocol -> rename.
// The security claim is that no untrusted byte is made executable or run before
// the checksum gate passes, and that unverifiable installs fail closed.

// fakeReleaseServer stands in for the GitHub API + asset CDN. It serves a tagged
// release whose single asset for the current platform returns assetBody. The
// asset URL is rooted at the same httptest host so ValidateHTTPSHost can be
// driven by passing that host (or omitting it) in allowedHosts.
type fakeReleaseServer struct {
	srv       *httptest.Server
	tagName   string // value returned in tag_name (may differ from requested to test mismatch)
	assetBody []byte // bytes served for the platform asset
	assetHost string // override host in browser_download_url (for host-guard tests); "" => same server
	hits      map[string]int
}

func newFakeReleaseServer(t *testing.T, tagName string, assetBody []byte) *fakeReleaseServer {
	t.Helper()
	f := &fakeReleaseServer{tagName: tagName, assetBody: assetBody, hits: map[string]int{}}
	mux := http.NewServeMux()
	platform := fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH)
	assetName := "ox-adapter-fake_" + platform

	mux.HandleFunc("/asset", func(w http.ResponseWriter, _ *http.Request) {
		f.hits["asset"]++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(f.assetBody)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// expect /repos/<owner>/<repo>/releases/tags/<tag>
		f.hits["api"]++
		if !strings.Contains(r.URL.Path, "/releases/tags/") {
			http.Error(w, "only releases/tags/<tag> is served", http.StatusNotFound)
			return
		}
		host := f.srv.URL
		if f.assetHost != "" {
			host = f.assetHost
		}
		resp := map[string]any{
			"tag_name": f.tagName,
			"assets": []map[string]string{
				{"name": assetName, "browser_download_url": host + "/asset"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// TLS server so the https transport guard (ValidateHTTPSHost) applies; tests
	// inject f.srv.Client() which trusts the test cert.
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// allowAllHosts returns nil so ValidateHTTPSHost skips the allowlist check, which
// is what we want for httptest (the server is on an ephemeral 127.0.0.1 host).
func allowAllHosts() map[string]bool { return nil }

// TestInstallAdapter_ChecksumMatch_Verifies the happy path: matching checksum
// passes the gate, the binary is chmod'd and the verify hook runs, then rename.
func TestInstallAdapter_ChecksumMatch(t *testing.T) {
	body := []byte("trusted adapter bytes")
	f := newFakeReleaseServer(t, "v1.0.0", body)

	installDir := t.TempDir()
	verifyCalled := false
	cfg := installConfig{
		plan: installPlan{
			owner: "sageox", repo: "ox-adapters", tag: "v1.0.0",
			checksum: sha256Hex(body), curated: true,
			platform: fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH),
		},
		apiBaseURL:   f.srv.URL,
		httpClient:   f.srv.Client(),
		allowedHosts: allowAllHosts(),
		installDir:   installDir,
		verify: func(path string) error {
			verifyCalled = true
			// by the time verify runs, bytes are on disk and executable
			fi, err := os.Stat(path)
			if err != nil {
				return err
			}
			if fi.Mode().Perm()&0o100 == 0 {
				t.Errorf("verify reached but binary not executable (perm=%o)", fi.Mode().Perm())
			}
			return nil
		},
	}

	if err := installAdapter(cfg); err != nil {
		t.Fatalf("install with matching checksum should succeed: %v", err)
	}
	if !verifyCalled {
		t.Error("verifyAdapterProtocol should run after the checksum gate passes")
	}
	// installed file should exist with the trusted bytes
	got, err := os.ReadFile(filepath.Join(installDir, "ox-adapter-fake"))
	if err != nil {
		t.Fatalf("installed binary missing: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("installed bytes = %q, want %q", got, body)
	}
}

// TestInstallAdapter_ChecksumMismatch_FailsBeforeChmodAndExec is the core security
// test: a swapped/compromised asset (wrong bytes) must abort BEFORE the binary is
// made executable or handed to verifyAdapterProtocol, and nothing is installed.
func TestInstallAdapter_ChecksumMismatch_FailsBeforeChmodAndExec(t *testing.T) {
	wrongBody := []byte("malicious swapped bytes")
	f := newFakeReleaseServer(t, "v1.0.0", wrongBody)

	installDir := t.TempDir()
	cfg := installConfig{
		plan: installPlan{
			owner: "sageox", repo: "ox-adapters", tag: "v1.0.0",
			// pin the checksum of DIFFERENT (legitimate) bytes
			checksum: sha256Hex([]byte("the bytes SageOx actually curated")),
			curated:  true,
			platform: fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH),
		},
		apiBaseURL:   f.srv.URL,
		httpClient:   f.srv.Client(),
		allowedHosts: allowAllHosts(),
		installDir:   installDir,
		verify: func(string) error {
			t.Fatal("verifyAdapterProtocol must NOT be reached on checksum mismatch (sentinel)")
			return nil
		},
	}

	err := installAdapter(cfg)
	if err == nil {
		t.Fatal("install must fail on checksum mismatch")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error should mention checksum mismatch, got: %v", err)
	}
	// nothing installed
	if entries, _ := os.ReadDir(installDir); len(entries) != 0 {
		t.Errorf("no file should remain in install dir on mismatch, found %d", len(entries))
	}
}

// TestInstallAdapter_AllowUnverified_SkipsChecksumKeepsVerify mirrors the
// --allow-unverified path: with an empty checksum the gate is skipped (no compare)
// but verifyAdapterProtocol STILL runs and the install proceeds.
func TestInstallAdapter_AllowUnverified_SkipsChecksumKeepsVerify(t *testing.T) {
	body := []byte("user-vouched-for bytes")
	f := newFakeReleaseServer(t, "v0.1.0", body)

	installDir := t.TempDir()
	verifyCalled := false
	cfg := installConfig{
		plan: installPlan{
			owner: "me", repo: "ox-adapter-x", tag: "v0.1.0",
			checksum: "", curated: false, // unverifiable, allow-unverified
			platform: fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH),
		},
		apiBaseURL:   f.srv.URL,
		httpClient:   f.srv.Client(),
		allowedHosts: allowAllHosts(),
		installDir:   installDir,
		verify:       func(string) error { verifyCalled = true; return nil },
	}

	if err := installAdapter(cfg); err != nil {
		t.Fatalf("allow-unverified install should proceed: %v", err)
	}
	if !verifyCalled {
		t.Error("protocol verification must still run on the allow-unverified path")
	}
	if _, err := os.Stat(filepath.Join(installDir, "ox-adapter-fake")); err != nil {
		t.Errorf("binary should be installed on allow-unverified path: %v", err)
	}
}

// TestInstallAdapter_RejectsAttackerDownloadHost verifies the defense-in-depth
// transport guard: a browser_download_url pointing at a host outside the
// allowlist is refused before any download.
func TestInstallAdapter_RejectsAttackerDownloadHost(t *testing.T) {
	body := []byte("bytes")
	f := newFakeReleaseServer(t, "v1.0.0", body)
	f.assetHost = "https://attacker.example.com" // asset URL points off-allowlist

	cfg := installConfig{
		plan: installPlan{
			owner: "sageox", repo: "ox-adapters", tag: "v1.0.0",
			checksum: sha256Hex(body), curated: true,
			platform: fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH),
		},
		apiBaseURL:   f.srv.URL,
		httpClient:   f.srv.Client(),
		allowedHosts: adapterDownloadHosts, // real allowlist, attacker host not in it
		installDir:   t.TempDir(),
		verify:       func(string) error { return nil },
	}

	err := installAdapter(cfg)
	if err == nil {
		t.Fatal("install must refuse an off-allowlist download host")
	}
	if !strings.Contains(err.Error(), "allowlist") {
		t.Errorf("error should mention host allowlist, got: %v", err)
	}
}

// TestInstallAdapter_TagNameMismatchRejected verifies that a release whose
// tag_name differs from the pinned tag is rejected (a retag/substitution attempt).
func TestInstallAdapter_TagNameMismatchRejected(t *testing.T) {
	body := []byte("bytes")
	f := newFakeReleaseServer(t, "v9.9.9", body) // API returns a different tag

	cfg := installConfig{
		plan: installPlan{
			owner: "sageox", repo: "ox-adapters", tag: "v1.0.0", // we asked for v1.0.0
			checksum: sha256Hex(body), curated: true,
			platform: fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH),
		},
		apiBaseURL:   f.srv.URL,
		httpClient:   f.srv.Client(),
		allowedHosts: allowAllHosts(),
		installDir:   t.TempDir(),
		verify:       func(string) error { return nil },
	}

	err := installAdapter(cfg)
	if err == nil {
		t.Fatal("install must reject a tag_name that does not match the pin")
	}
	if !strings.Contains(err.Error(), "tag mismatch") {
		t.Errorf("error should mention tag mismatch, got: %v", err)
	}
}

// TestInstallAdapter_UsesTagsEndpointNotLatest verifies the install hits
// releases/tags/<tag>, never releases/latest (the gap ox-5ihl closes).
func TestInstallAdapter_UsesTagsEndpointNotLatest(t *testing.T) {
	body := []byte("bytes")
	var requestedPath string
	mux := http.NewServeMux()
	platform := fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH)
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/asset", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		resp := map[string]any{
			"tag_name": "v2.0.0",
			"assets": []map[string]string{
				{"name": "ox-adapter-fake_" + platform, "browser_download_url": srv.URL + "/asset"},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	cfg := installConfig{
		plan: installPlan{
			owner: "sageox", repo: "ox-adapters", tag: "v2.0.0",
			checksum: sha256Hex(body), curated: true, platform: platform,
		},
		apiBaseURL:   srv.URL,
		httpClient:   srv.Client(),
		allowedHosts: allowAllHosts(),
		installDir:   t.TempDir(),
		verify:       func(string) error { return nil },
	}
	if err := installAdapter(cfg); err != nil {
		t.Fatalf("install should succeed: %v", err)
	}
	if !strings.Contains(requestedPath, "/releases/tags/v2.0.0") {
		t.Errorf("expected releases/tags/v2.0.0 endpoint, got %q", requestedPath)
	}
	if strings.Contains(requestedPath, "releases/latest") {
		t.Errorf("install must not use releases/latest, got %q", requestedPath)
	}
}

// TestInstallAdapter_NoTag_FailsClosed verifies the internal invariant that a
// plan without a pinned tag cannot install (defense in depth behind the cobra layer).
func TestInstallAdapter_NoTag_FailsClosed(t *testing.T) {
	cfg := installConfig{
		plan:       installPlan{owner: "me", repo: "x", curated: false, platform: "linux_amd64"},
		apiBaseURL: "https://api.github.com",
		installDir: t.TempDir(),
		verify:     func(string) error { return nil },
	}
	if err := installAdapter(cfg); err == nil {
		t.Fatal("install with no pinned tag must fail closed")
	}
}

// --- resolveAdapterSource trust-anchor behavior ---

// TestResolveAdapterSource_ArbitraryRepoRequiresTag verifies that an arbitrary
// github.com/<owner>/<repo> source without an explicit @<tag> is rejected.
func TestResolveAdapterSource_ArbitraryRepoRequiresTag(t *testing.T) {
	_, err := resolveAdapterSource("github.com/me/ox-adapter-x", "linux_amd64")
	if err == nil {
		t.Fatal("arbitrary repo without @tag must be rejected")
	}
	if !strings.Contains(err.Error(), "@<tag>") {
		t.Errorf("error should instruct to pin a tag, got: %v", err)
	}
}

// TestResolveAdapterSource_ArbitraryRepoWithTag verifies a tagged arbitrary repo
// resolves to an uncurated, checksum-less plan (so the caller fails closed
// without --allow-unverified).
func TestResolveAdapterSource_ArbitraryRepoWithTag(t *testing.T) {
	plan, err := resolveAdapterSource("github.com/me/ox-adapter-x@v0.1.0", "linux_amd64")
	if err != nil {
		t.Fatalf("tagged arbitrary repo should resolve: %v", err)
	}
	if plan.curated {
		t.Error("arbitrary repo must not be marked curated")
	}
	if plan.tag != "v0.1.0" {
		t.Errorf("tag = %q, want v0.1.0", plan.tag)
	}
	if plan.checksum != "" {
		t.Error("arbitrary repo must have no SageOx checksum")
	}
}

// TestResolveAdapterSource_CuratedUnpinned verifies a curated entry that lacks a
// tag/checksum (the current external entries) resolves to a checksum-less plan,
// driving fail-closed behavior until a maintainer pins one.
func TestResolveAdapterSource_CuratedUnpinned(t *testing.T) {
	// "cursor" is a real external registry entry shipped without a pin.
	plan, err := resolveAdapterSource("cursor", "darwin_arm64")
	if err != nil {
		t.Fatalf("curated lookup should resolve: %v", err)
	}
	if !plan.curated {
		t.Error("registry short-name must be marked curated")
	}
	if plan.checksum != "" {
		t.Error("unpinned curated entry must have empty checksum (fail-closed)")
	}
}

// --- runAdapterInstall fail-closed / allow-unverified at the cobra layer ---

// TestRunAdapterInstall_UnverifiableFailsClosed verifies that installing an
// arbitrary tagged repo WITHOUT --allow-unverified fails closed (no network needed
// — the gate fires before any fetch).
func TestRunAdapterInstall_UnverifiableFailsClosed(t *testing.T) {
	cmd := adapterInstallCmd
	// ensure the flag defaults to false for this invocation
	_ = cmd.Flags().Set("allow-unverified", "false")
	err := cmd.RunE(cmd, []string{"github.com/me/ox-adapter-x@v0.1.0"})
	if err == nil {
		t.Fatal("unverifiable install without --allow-unverified must fail closed")
	}
	if !strings.Contains(err.Error(), "allow-unverified") {
		t.Errorf("error should point to --allow-unverified, got: %v", err)
	}
}

// TestRunAdapterInstall_CuratedUnpinnedFailsClosed verifies the documented
// transition: `ox adapter install cursor` (no pin yet) fails closed without
// --allow-unverified.
func TestRunAdapterInstall_CuratedUnpinnedFailsClosed(t *testing.T) {
	cmd := adapterInstallCmd
	_ = cmd.Flags().Set("allow-unverified", "false")
	err := cmd.RunE(cmd, []string{"cursor"})
	if err == nil {
		t.Fatal("curated entry with no pinned tag must fail closed")
	}
}
