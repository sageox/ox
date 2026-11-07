package agentwork

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
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
// SAGEOX_TOKEN with the team prefix counts, and so does a stored login that
// cannot be read (lock timeout, corrupt file): both can only make runs stricter.
var teamTokenActive = func(projectRoot string) bool {
	ep := endpoint.GetForProject(projectRoot)
	if _, state := auth.EnvTokenFor(ep); state != auth.EnvTokenAbsent {
		return auth.EnvTokenIsTeamFamily(ep)
	}
	tok, err := auth.GetTokenForEndpoint(ep)
	return err != nil || (tok != nil && strings.HasPrefix(tok.AccessToken, auth.TeamTokenPrefix))
}

// workerDir is the directory a worker CLI runs in: the caller's, or a fresh
// empty one removed after the run. A worker never runs in the Ledger or the
// daemon's directory, whose hooks and instructions a CLI too old to isolate
// would load. The fresh directory lives under the user's own cache, not the
// shared temp dir: CLIs also load instructions such as CLAUDE.md from every
// parent directory, and any account on the machine can write /tmp/CLAUDE.md.
func workerDir(dir string) (string, func(), error) {
	if dir != "" {
		return dir, func() {}, nil
	}
	base := filepath.Join(paths.CacheDir(), "agent-worker")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", nil, fmt.Errorf("worker directory: %w", err)
	}
	d, err := os.MkdirTemp(base, "run-*")
	if err != nil {
		return "", nil, fmt.Errorf("worker directory: %w", err)
	}
	return d, func() { _ = os.RemoveAll(d) }, nil
}
