package daemon

import (
	"os"
	"testing"

	"github.com/sageox/ox/internal/secretguard/secretguardtest"
)

// TestMain serves the publication catalog offline: ledger sync tests push for
// real, and a slow or unreachable upstream otherwise fails them with "current
// rules unavailable; publication pending".
func TestMain(m *testing.M) {
	secretguardtest.InstallOfflineCatalog()
	os.Exit(m.Run())
}

// Failure prevented: daemon publication tests depend on raw.githubusercontent.com.
func TestPublicationCatalogIsServedOffline(t *testing.T) {
	secretguardtest.RequireOfflineCatalog(t)
}
