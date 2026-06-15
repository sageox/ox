package doctor

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestFDPressureVerdict covers the FD-pressure verdict across the relative
// (% of soft limit) and absolute (raw count) signals.
//
// Failure prevented: the exact shape of the recursive-fsnotify-watcher
// regression — thousands of FDs held under a high inherited ulimit, where the
// percentage stays low and the relative-only check stays green. The absolute
// ceiling must catch it regardless of the limit.
func TestFDPressureVerdict(t *testing.T) {
	const name = "FD pressure"
	tests := []struct {
		name       string
		openFDs    int
		openFDs2   uint64 // limit
		wantStatus Status
		wantFix    bool // expect a non-empty Fix
	}{
		{"platform cannot report", 0, 256, StatusSkip, false},
		{"healthy low count + low pct", 50, 10240, StatusPass, false},

		// relative signal in isolation (count well under the absolute ceiling)
		{"relative warn (66% of small limit)", 200, 300, StatusWarn, true},
		{"relative fail (93% of small limit)", 280, 300, StatusFail, true},

		// absolute signal in isolation — the blind spot: huge limit keeps pct
		// negligible, but the raw count is anomalous for a watcher-less daemon.
		{"absolute warn under huge limit", 2000, 1_000_000, StatusWarn, true},
		{"absolute fail = the ~11k watcher regression", 11246, 1_000_000, StatusFail, true},

		// limit unknown — absolute ceiling still applies (old code returned Pass).
		{"limit unknown + anomalous count", 5000, 0, StatusFail, true},
		{"limit unknown + normal count", 50, 0, StatusPass, false},

		// boundaries
		{"just below absolute warn", fdAbsoluteWarn - 1, 1_000_000, StatusPass, false},
		{"exactly absolute warn", fdAbsoluteWarn, 1_000_000, StatusWarn, true},
		{"exactly absolute fail", fdAbsoluteFail, 1_000_000, StatusFail, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fdPressureVerdict(name, tt.openFDs, tt.openFDs2)
			assert.Equal(t, tt.wantStatus, got.Status)
			assert.Equal(t, name, got.Name)
			if tt.wantFix {
				assert.NotEmpty(t, got.Fix, "warn/fail should carry an actionable fix")
			} else {
				assert.Empty(t, got.Fix)
			}
		})
	}
}

func TestMoreSevereStatus(t *testing.T) {
	cases := []struct {
		a, b, want Status
	}{
		{StatusFail, StatusPass, StatusFail},
		{StatusPass, StatusFail, StatusFail},
		{StatusWarn, StatusPass, StatusWarn},
		{StatusPass, StatusWarn, StatusWarn},
		{StatusWarn, StatusFail, StatusFail},
		{StatusPass, StatusPass, StatusPass},
		{StatusSkip, StatusPass, StatusPass},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, moreSevereStatus(c.a, c.b))
	}
}
