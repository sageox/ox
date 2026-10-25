package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: every project daemon independently sweeps every local
// Ledger, multiplying background repair work by the number of open projects.
func TestAutofixLedgerPaths_OnlyGlobalSyncOwnerEnumeratesFleet(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	projectRoot := t.TempDir()
	const endpointURL = "https://autofix-ledgers.invalid"
	require.NoError(t, config.SaveProjectConfig(projectRoot, &config.ProjectConfig{
		ConfigVersion: config.CurrentConfigVersion,
		RepoID:        "repo_current",
		Endpoint:      endpointURL,
	}))
	ledgerPath := paths.LedgersDataDir("repo_other", endpointURL)
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerPath, ".git"), 0o755))

	daemonConfig := DefaultConfig()
	daemonConfig.ProjectRoot = projectRoot
	d := New(daemonConfig, testLogger())
	d.scheduler = NewSyncScheduler(daemonConfig, testLogger())
	otherOwner, err := AcquireGlobalSyncLease(endpointURL)
	require.NoError(t, err)
	d.scheduler.SetGlobalSyncLease(endpoint.NormalizeEndpoint(endpointURL), nil)
	assert.Empty(t, d.autofixLedgerPaths(), "follower must not sweep the Ledger fleet")
	require.NoError(t, otherOwner.Release())

	lease, err := AcquireGlobalSyncLease(endpointURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lease.Release() })
	d.scheduler.SetGlobalSyncLease(endpoint.NormalizeEndpoint(endpointURL), lease)
	assert.Equal(t, []string{ledgerPath}, d.autofixLedgerPaths())
}
