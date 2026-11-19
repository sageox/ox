package livesession_test

import (
	"os"
	"testing"

	"github.com/sageox/ox/internal/secretguard/secretguardtest"
)

// TestMain serves the checked-in gitleaks catalog to the current-rules fetch,
// so the publication scan every drain runs never depends on the network.
func TestMain(m *testing.M) {
	secretguardtest.InstallOfflineCatalog()
	os.Exit(m.Run())
}

func TestCurrentRulesComeFromTheOfflineCatalog(t *testing.T) {
	secretguardtest.RequireOfflineCatalog(t)
}
