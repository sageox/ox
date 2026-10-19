package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotPriorRevision_RefusesWhenNoStoreAndPageAwaitsUpload(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	ledger := initPlanTestLedger(t, root)
	planDir := filepath.Join(ledger, "data", "plans", "2026-10-07-pending")
	require.NoError(t, os.MkdirAll(planDir, 0o755))
	writeLargeAuthoredPage(t, filepath.Join(planDir, "plan.html"), "Pending Upload")

	err := snapshotPriorRevision(context.Background(), ledger, planDir, planUploads{noStore: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no content store is reachable")
}
