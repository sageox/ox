package main

import (
	"context"

	"github.com/sageox/ox/internal/gitutil"
)

// commitLedgerSnapshot commits the whole ledger index as an immutable,
// pre-validated tree, closing the validation↔commit TOCTOU that a plain
// `git add` + validate + `git commit` pair leaves open (PR #811 review,
// Greptile P1; PR #910 review, CodeRabbit). The engine now lives in
// gitutil.CommitLedgerSnapshot — see that function's doc comment for the
// write-tree → validate → sacred-guard → commit-tree → update-ref rationale.
func commitLedgerSnapshot(ctx context.Context, ledgerPath, message string) (bool, error) {
	return gitutil.CommitLedgerSnapshot(ctx, ledgerPath, message)
}
