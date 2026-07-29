package daemon

// sync_kb_endpoint_test.go — regression tests for bead ox-651l: the daemon
// resolved KB store paths through endpoint.Get() (env → single-login →
// production default), ignoring the project's configured endpoint. A
// test.sageox.ai project's bubbles were filed under the production
// sageox.ai slug, defeating KBDir's staging/production isolation invariant.
//
// The class under test: every KB path the daemon derives (clone target,
// symlink target, GC root, status root) must be scoped by the PROJECT's
// endpoint, with the SAGEOX_ENDPOINT env var as the only override.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kbEndpointTestProject creates an initialized project whose config.json
// pins an endpoint the machine is NOT otherwise using, so any fallback to
// endpoint.Get()'s default is observable as a wrong path slug.
func kbEndpointTestProject(t *testing.T, ep string) string {
	t.Helper()
	root := setupProjectWithConfig(t, "")
	body := `{"endpoint":"` + ep + `","repo_id":"repo_kb_ep"}`
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "config.json"), []byte(body), 0o644))
	return root
}

// TestKBEndpoint_ProjectConfigWinsOverDefault verifies the scheduler
// resolves KB paths from the project's configured endpoint when no env
// override is set.
//
// Failure prevented: a test/staging project's bubbles silently landing in
// the production sageox.ai store, where a production daemon's GC (which
// trusts the production API list) would treat them as orphans — and where
// kb_ids from different endpoints share one namespace with no collision
// guarantee.
func TestKBEndpoint_ProjectConfigWinsOverDefault(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_DATA_HOME", tmp)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmp, "cache"))
	t.Setenv("SAGEOX_ENDPOINT", "") // empty = unset: project config must win

	projectRoot := kbEndpointTestProject(t, "https://test.sageox.ai")
	s := newSymlinkTestScheduler(t, projectRoot)

	assert.Equal(t, "https://test.sageox.ai", s.kbEndpoint(),
		"kbEndpoint must resolve the project's endpoint, not the machine default")

	// Drive the real symlink reconciler: the per-project mount must point
	// into the project-endpoint slug's store, not the production default.
	s.reconcileProjectSymlinks(context.Background(), projectRoot, []api.KB{
		{KBID: "kb_team", KBType: api.KBTypeTeam, Slug: "platform"},
	})

	target, err := os.Readlink(filepath.Join(projectRoot, ".sageox", "kb", "team", "platform"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(tmp, "sageox", "test.sageox.ai", "kb", "kb_team"), target,
		"symlink must target the test.sageox.ai store, not the sageox.ai default")
}

// TestKBEndpoint_EnvOverrideStillWins verifies SAGEOX_ENDPOINT (the
// explicit operator override) beats the project config, preserving the
// documented precedence of endpoint.GetForProject.
//
// Failure prevented: fixing the default-fallback bug by accidentally
// inverting precedence, which would break every workflow that pins an
// endpoint via env (twin tests, staging smoke runs).
func TestKBEndpoint_EnvOverrideStillWins(t *testing.T) {
	t.Setenv("SAGEOX_ENDPOINT", "https://staging.sageox.ai")

	projectRoot := kbEndpointTestProject(t, "https://test.sageox.ai")
	s := newSymlinkTestScheduler(t, projectRoot)

	assert.Equal(t, "https://staging.sageox.ai", s.kbEndpoint())
}
