package daemon

import (
	"testing"
	"time"
)

// TestIntervalForType pins the cadence-by-type selection so future
// callers (ox-yvc1.3 migration of ledger and team-context paths) can rely
// on stable behavior when they replace their per-callsite switches.
//
// Failure prevented: a kb type silently routes to the wrong cadence,
// either hammering the API (too fast) or falling stale (too slow).
func TestIntervalForType(t *testing.T) {
	cfg := &Config{
		SyncIntervalRead:        60 * time.Second,
		TeamContextSyncInterval: 15 * time.Second,
	}
	cases := []struct {
		kind string
		want time.Duration
	}{
		{"personal", 15 * time.Second},
		{"profile", 15 * time.Second},
		{"team", 15 * time.Second},
		{"repo", 60 * time.Second},
		{"custom", 60 * time.Second},
		{"unknown", 60 * time.Second},
		{"", 60 * time.Second},
		{"future_type_we_dont_know_yet", 60 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			got := intervalForType(cfg, c.kind)
			if got != c.want {
				t.Errorf("intervalForType(%q) = %v, want %v", c.kind, got, c.want)
			}
		})
	}
}

// TestIntervalForType_Defaults verifies the helper's hard-coded fallback
// kicks in when both Config fields are zero — a misconfigured daemon
// must still pick a sane cadence rather than busy-loop.
func TestIntervalForType_Defaults(t *testing.T) {
	cfg := &Config{} // both intervals zero
	got := intervalForType(cfg, "team")
	if got != 60*time.Second {
		t.Errorf("zero-config team interval = %v, want 60s fallback", got)
	}
}
