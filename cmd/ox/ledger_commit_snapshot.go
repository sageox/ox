package main

import (
	"context"

	"github.com/sageox/ox/internal/gitutil"
)

// allButPlansPathspec is the whole Ledger except data/plans/. A plan file
// still uncommitted there can be an author's unsaved edit, or a plan write
// waiting on its retry, such as a large plan.html whose upload failed.
// Maintenance leaves it to the plan's own writers and their retries (#1409).
// Session publication calls gitutil.CommitLedgerSnapshot directly to recover
// staged saves, while leaving unstaged plan edits alone.
var allButPlansPathspec = []string{":/", ":(exclude)data/plans"}

// commitLedgerSnapshot commits the ledger index, all but data/plans/
// (allButPlansPathspec), as an immutable, pre-validated tree, closing the
// validation↔commit TOCTOU that a plain `git add` + validate + `git commit`
// pair leaves open (PR #811 review, Greptile P1; PR #910 review, CodeRabbit).
// The engine now lives in gitutil.CommitLedgerSnapshot — see that function's
// doc comment for the write-tree → validate → sacred-guard → commit-tree →
// update-ref rationale.
func commitLedgerSnapshot(ctx context.Context, ledgerPath, message string) (bool, error) {
	return gitutil.CommitLedgerSnapshot(ctx, ledgerPath, message, allButPlansPathspec...)
}
