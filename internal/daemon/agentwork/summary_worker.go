package agentwork

import (
	"fmt"
	"os"
	"strings"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
)

// summaryWorkerIsolation picks how strictly a summary or judge run isolates.
// A personal login keeps the fallback for a CLI too old to isolate. A team
// token, which an AI coworker runs under, never takes it: the run fails
// instead of reaching the coworker's credentials with tools on.
func summaryWorkerIsolation(projectRoot string) (isolated, ifSupported bool) {
	if teamTokenActive(projectRoot) {
		return true, false
	}
	return false, true
}

// teamTokenActive reports whether ox authenticates to the project's endpoint
// with a team token, from SAGEOX_TOKEN or a stored login. A malformed
// SAGEOX_TOKEN with the team prefix counts, so it can only make runs stricter.
var teamTokenActive = func(projectRoot string) bool {
	ep := endpoint.GetForProject(projectRoot)
	if _, state := auth.EnvTokenFor(ep); state != auth.EnvTokenAbsent {
		return auth.EnvTokenIsTeamFamily(ep)
	}
	tok, err := auth.GetTokenForEndpoint(ep)
	return err == nil && tok != nil && strings.HasPrefix(tok.AccessToken, auth.TeamTokenPrefix)
}

// workerDir is the directory a worker CLI runs in: the caller's, or a fresh
// empty one removed after the run. A worker never runs in the Ledger or the
// daemon's directory, whose hooks and instructions a CLI too old to isolate
// would load.
func workerDir(dir string) (string, func(), error) {
	if dir != "" {
		return dir, func() {}, nil
	}
	d, err := os.MkdirTemp("", "ox-agent-worker-*")
	if err != nil {
		return "", nil, fmt.Errorf("worker directory: %w", err)
	}
	return d, func() { _ = os.RemoveAll(d) }, nil
}
