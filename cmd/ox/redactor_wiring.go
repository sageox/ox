package main

import (
	"github.com/sageox/ox/internal/gitserver"
)

// init wires cross-cutting hooks that internal packages need but cannot
// resolve themselves without creating import cycles.
//
//  1. gitserver.SetHelperCommand — the shell command git invokes to
//     resolve credentials via ox-managed credential storage (ox-eeqi).
//     internal/gitserver can't import cmd/ox to discover the running
//     binary's absolute path.
//
// Previously also wired `ledger.SetFieldRedactor` (ox-8bkk) to scrub
// credential patterns from GitHub PR/Issue cache fields at write time.
// Removed per ox-cqdo (2026-05-12): `data/github/**` is a verbatim cache
// of bytes already published on GitHub. Replicating those bytes into the
// ledger is the same exposure surface we already accept by ingesting the
// PR/Issue at all — rewriting them on the way to disk introduces a
// false-positive class that blocks pushes for no actual security gain.
// The `FieldRedactor` plumbing in internal/ledger is retained as a
// no-op pass-through so a future warn-on-detect mode can re-use it.
//
// This hook applies to every ox CLI invocation, including the embedded
// daemon — the daemon does not have a separate main package.
func init() {
	gitserver.SetHelperCommand(HelperCommandString())
}
