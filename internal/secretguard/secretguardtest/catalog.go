// Package secretguardtest serves the checked-in gitleaks catalog to the
// publication freshness check inside test binaries.
//
// Why this exists: publication refuses to proceed without the current upstream
// catalog (secretguard.LatestRules). A test package that publishes sessions for
// real — a ledger push, an LFS upload — then depends on raw.githubusercontent.com
// answering within 15s, and fails with "current rules unavailable; publication
// pending" whenever it does not.
//
// How to apply: a test package that drives publication calls
// InstallOfflineCatalog from its TestMain and RequireOfflineCatalog from one
// test. Production keeps its unconditional current-rules refresh; nothing here
// is an option the product can set.
package secretguardtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/secretguard"
)

// The upstream location secretguard.LatestCatalog fetches. The query string is
// ignored so a cache-busting parameter cannot route the fetch back to the network.
const (
	catalogHost = "raw.githubusercontent.com"
	catalogPath = "/gitleaks/gitleaks/master/config/gitleaks.toml"
)

var served atomic.Int64

// checkedInCatalog is resolved during package initialization, before any
// TestMain can change the working directory.
var checkedInCatalog = locateCatalog()

// InstallOfflineCatalog wraps http.DefaultTransport so the upstream catalog
// request is answered from internal/secretguard/gitleaks.toml. Every other
// request uses the previous transport unchanged. Call it once, from TestMain.
func InstallOfflineCatalog() {
	if !testing.Testing() {
		panic("secretguardtest: InstallOfflineCatalog called outside a test binary")
	}
	http.DefaultTransport = transport{base: http.DefaultTransport, catalogFile: checkedInCatalog}
}

// RequireOfflineCatalog fails t unless the production fetch path returns the
// checked-in catalog from the offline transport rather than from the network.
func RequireOfflineCatalog(t testing.TB) {
	t.Helper()
	if checkedInCatalog == "" {
		t.Fatal("cannot locate internal/secretguard/gitleaks.toml from the source path or the working directory")
	}
	want, err := os.ReadFile(checkedInCatalog)
	if err != nil {
		t.Fatalf("read checked-in catalog: %v", err)
	}
	before := served.Load()
	got, err := secretguard.LatestCatalog(context.Background())
	if err != nil {
		t.Fatalf("publication catalog fetch failed; is InstallOfflineCatalog called from TestMain? %v", err)
	}
	if served.Load() == before {
		t.Fatal("publication catalog came from the network; call InstallOfflineCatalog from TestMain")
	}
	if !bytes.Equal(got, want) {
		t.Fatal("publication catalog differs from internal/secretguard/gitleaks.toml")
	}
}

func locateCatalog() string {
	_, thisFile, _, _ := runtime.Caller(0)
	wd, _ := os.Getwd()
	return resolveCatalogFile(thisFile, wd)
}

// resolveCatalogFile prefers this source file's location, which still holds in
// a re-exec'd test binary whose working directory is a sandbox. Under -trimpath
// that location is module-relative, so it falls back to the directory go test
// starts in (the package directory) and walks up to the checkout. It returns ""
// when neither finds the catalog.
func resolveCatalogFile(sourceFile, wd string) string {
	if filepath.IsAbs(sourceFile) {
		return filepath.Join(filepath.Dir(sourceFile), "..", "gitleaks.toml")
	}
	if !filepath.IsAbs(wd) {
		return ""
	}
	for dir := wd; ; {
		candidate := filepath.Join(dir, "internal", "secretguard", "gitleaks.toml")
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

type transport struct {
	base        http.RoundTripper
	catalogFile string
}

// The catalog is read per request, not at install time, so only a process that
// actually fetches it depends on the source tree being readable. A read failure
// is returned, never passed to the network.
func (tr transport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Host != catalogHost || request.URL.Path != catalogPath {
		return tr.base.RoundTrip(request)
	}
	if tr.catalogFile == "" {
		return nil, errors.New("secretguardtest: cannot locate checked-in catalog")
	}
	catalog, err := os.ReadFile(tr.catalogFile)
	if err != nil {
		return nil, fmt.Errorf("secretguardtest: cannot read checked-in catalog: %w", err)
	}
	served.Add(1)
	return &http.Response{
		Status:        "200 OK",
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"text/plain; charset=utf-8"}},
		Body:          io.NopCloser(bytes.NewReader(catalog)),
		ContentLength: int64(len(catalog)),
		Request:       request,
	}, nil
}
