package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestLedgerPushWedgedSummary_NamesDoctorFix guards the issue text `ox status` shows for a wedged Ledger.
// Without the doctor command in it, the coworker sees a wedge and no way out (#1174).
func TestLedgerPushWedgedSummary_NamesDoctorFix(t *testing.T) {
	summary := ledgerPushWedgedSummary(time.Date(2026, 10, 6, 9, 30, 0, 0, time.UTC))

	assert.Contains(t, summary, "ox doctor --fix-slug=session-pointer-restore")
	assert.Contains(t, summary, "9:30AM")
}
