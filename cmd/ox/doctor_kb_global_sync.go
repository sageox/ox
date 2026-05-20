package main

// Doctor check for per-(user, endpoint) global-sync leader election
// (ox-6zme). Exactly one running daemon per endpoint should hold the
// global-sync flock lease — that daemon is the one running
// team-context pulls and KB ListBubbles. 0 owners (no daemon claimed
// the lease) or >1 owners (lease accounting drifted, only possible if
// flock semantics broke or the registry was hand-edited) are both
// anomalies.
//
// Detection reads internal/daemon's on-disk registry. The
// GlobalSyncOwner flag is set on each daemon entry during
// initComponents (see daemon.go::acquireGlobalSyncLease). The check
// groups daemons by normalized endpoint and validates each group.
//
// AutoFix posture: the 0-owner case self-heals on the next daemon
// startup attempting acquisition, so we don't take destructive action
// here. We log loud (warning) and let the operator decide whether to
// restart a daemon. The >1-owner case is impossible if flock works
// correctly; we still surface it as a warning rather than a panic.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
)

// daemonRegistryReader is the seam tests use to substitute a fake
// registry. Production wires it to daemon.ListRunningDaemons. Returning
// (nil, err) skips the check rather than failing it: a missing
// registry on a fresh install is not a doctor warning.
type daemonRegistryReader func() ([]daemon.DaemonInfo, error)

// kbGlobalSyncReader is the active hook; tests override via
// SetKBGlobalSyncReader. nil means "use production wiring."
var kbGlobalSyncReader daemonRegistryReader

// SetKBGlobalSyncReader installs a registry reader hook. Pass nil to
// reset to production behavior. Intended for tests only.
func SetKBGlobalSyncReader(r daemonRegistryReader) {
	kbGlobalSyncReader = r
}

// checkKBGlobalSyncNoOwner verifies that for every endpoint with a
// running daemon, exactly one daemon holds the global-sync lease.
//
// Skips silently when:
//   - the registry can't be read (fresh install, no daemon ever started)
//   - no daemons are running
//   - every running daemon has Endpoint="" (older daemon versions that
//     don't populate Endpoint in DaemonInfo)
//
// Returns a Warning when any endpoint group has 0 or >1 owners.
// AutoFix is intentionally a no-op: the next daemon's startup attempt
// will pick up the lease if it's free, and we don't want doctor to
// kill or restart daemons under the user's feet.
func checkKBGlobalSyncNoOwner(_ bool) checkResult {
	const name = "kb global-sync owner"

	reader := kbGlobalSyncReader
	if reader == nil {
		reader = daemon.ListRunningDaemons
	}

	daemons, err := reader()
	if err != nil {
		return SkippedCheck(name, "registry unreadable", err.Error())
	}
	if len(daemons) == 0 {
		return SkippedCheck(name, "no running daemons", "")
	}

	// Group daemons by normalized endpoint. Daemons with empty endpoint
	// are skipped — older daemon versions didn't populate the field,
	// and we'd rather have a quiet skip than a false-positive warning
	// during the rollout window.
	groups := make(map[string][]daemon.DaemonInfo)
	skipped := 0
	for _, d := range daemons {
		if d.Endpoint == "" {
			skipped++
			continue
		}
		ep := endpoint.NormalizeEndpoint(d.Endpoint)
		groups[ep] = append(groups[ep], d)
	}
	if len(groups) == 0 {
		// All daemons predate the endpoint field. Not an error.
		return SkippedCheck(name, "no daemons report endpoint",
			fmt.Sprintf("%d daemon(s) on older version; restart to populate", skipped))
	}

	var anomalies []string
	for ep, group := range groups {
		owners := 0
		for _, d := range group {
			if d.GlobalSyncOwner {
				owners++
			}
		}
		switch {
		case owners == 0:
			anomalies = append(anomalies, fmt.Sprintf("%s: 0 owners across %d daemon(s)", ep, len(group)))
		case owners > 1:
			anomalies = append(anomalies, fmt.Sprintf("%s: %d owners across %d daemon(s)", ep, owners, len(group)))
		}
	}

	if len(anomalies) == 0 {
		return PassedCheck(name, fmt.Sprintf("%d endpoint(s) with single owner", len(groups)))
	}

	sort.Strings(anomalies)
	msg := fmt.Sprintf("%d endpoint(s) with global-sync ownership anomaly", len(anomalies))
	detail := strings.Join(anomalies, "; ") +
		". Restart one daemon to retry lease acquisition; flock auto-releases on process death."
	return WarningCheck(name, msg, detail)
}
