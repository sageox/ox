//go:build kb_twin

package daemon

// Test seam for the kb_twin harness in tests/kb_twin/. The harness lives
// outside the daemon package so it can compose its own scenario
// fixtures without polluting daemon's own _test.go files, but it still
// needs to drive the same private syncBubbles / runKBGC entry points
// that production hits. We expose the bare minimum surface here, gated
// by the kb_twin build tag so it never compiles into production code
// or non-twin test runs.
//
// This is a regular .go file (not _test.go) because Go test files are
// only visible inside their own package — the harness in tests/kb_twin/
// needs to import these symbols. The build tag ensures the symbols
// vanish entirely from production builds.

import "context"

// SyncBubblesForTest invokes the unexported syncBubbles reconciliation
// pass the way the daemon's scheduler would. Used by the kb_twin
// harness to assert the on-disk state after a full reconciliation.
func (s *SyncScheduler) SyncBubblesForTest(ctx context.Context) {
	s.syncBubbles(ctx)
}

// RunKBGCForTest invokes the unexported runKBGC pass with a caller-
// supplied list function. Lets twin scenarios exercise the trash +
// reaper lifecycle without standing up a real HTTP server.
func (s *SyncScheduler) RunKBGCForTest(ctx context.Context, listFn KBAPIListFnForTest) {
	s.runKBGC(ctx, kbAPIListFn(listFn))
}

// KBAPIListFnForTest is a build-tag-only alias for the unexported
// kbAPIListFn type. The harness needs the type name to construct its
// own list functions; without this alias the unexported type would be
// unreachable from outside the daemon package.
type KBAPIListFnForTest func(ctx context.Context) ([]string, error)
