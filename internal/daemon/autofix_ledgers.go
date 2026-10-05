package daemon

import (
	"github.com/sageox/ox/internal/doctor/autofix"
	"github.com/sageox/ox/internal/endpoint"
)

// autofixLedgerPaths returns every canonical Ledger checkout for this daemon's
// endpoint, but only while this daemon owns the endpoint-wide sync lease. That
// makes the fleet sweep single-owner even when many project daemons are alive.
func (d *Daemon) autofixLedgerPaths() []string {
	if d.scheduler == nil || !d.scheduler.IsGlobalSyncOwner() {
		return nil
	}
	endpointURL := endpoint.GetForProject(d.config.ProjectRoot)
	ledgers, err := autofix.DiscoverLedgerPaths(endpointURL)
	if err != nil {
		d.logger.Warn("autofix ledger discovery failed", "endpoint", endpointURL, "error", err)
		return nil
	}
	return ledgers
}
