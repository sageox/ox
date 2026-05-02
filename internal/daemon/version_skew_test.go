package daemon

import "testing"

// TestIsSignificantVersionSkew is the regression test for ox-mt3k.
//
// Each case carries a 'why' note documenting the failure it prevents.
// Wrong answers here either spam users with warnings during routine
// rolling upgrades (false positive) or silently accept a CLI that's
// many releases behind producing legacy on-disk state the new daemon
// can't handle (false negative — the original ox-mt3k bug).
func TestIsSignificantVersionSkew(t *testing.T) {
	tests := []struct {
		name   string
		caller string
		daemon string
		want   bool
		why    string
	}{
		{
			name:   "exact match",
			caller: "0.6.5+2026-04-30T19:59:51Z",
			daemon: "0.6.5+2026-04-30T19:59:51Z",
			want:   false,
			why:    "Same release on both sides — must never warn.",
		},
		{
			name:   "patch-level diff during rolling upgrade",
			caller: "0.6.5+2026-04-30T19:59:51Z",
			daemon: "0.6.5+2026-05-01T08:00:00Z",
			want:   false,
			why:    "Build metadata differs but minor matches — routine for a daemon spawned by the same release with a different build timestamp.",
		},
		{
			name:   "minor behind (client lags)",
			caller: "0.5.0",
			daemon: "0.6.5",
			want:   true,
			why:    "The 2026-04-30 incident: a v0.6.5 daemon driven by a v0.5.0 CLI that still seeded AGENTS.md client-side. New daemon expectations diverge from old CLI behavior.",
		},
		{
			name:   "minor ahead (client leads)",
			caller: "0.7.0",
			daemon: "0.6.5",
			want:   true,
			why:    "Inverse skew is also a problem — the daemon may not know how to interpret newer CLI requests. Warn so the user knows to also upgrade the daemon.",
		},
		{
			name:   "major mismatch",
			caller: "1.0.0",
			daemon: "0.9.9",
			want:   true,
			why:    "Major version implies wire-shape break.",
		},
		{
			name:   "dev build on caller side",
			caller: "dev",
			daemon: "0.6.5",
			want:   false,
			why:    "Local development build — devs running a custom CLI against a release daemon must not get spam.",
		},
		{
			name:   "dev build on daemon side",
			caller: "0.6.5",
			daemon: "dev",
			want:   false,
			why:    "Inverse — a dev daemon shouldn't lecture release CLIs.",
		},
		{
			name:   "empty caller (older client predates the field)",
			caller: "",
			daemon: "0.6.5",
			want:   false,
			why:    "Older clients leave CallerVersion blank. Treat as unknown rather than guessing — empty is the bootstrap rollout state.",
		},
		{
			name:   "garbage version string",
			caller: "what-even-is-this",
			daemon: "0.6.5",
			want:   false,
			why:    "Unparseable input must not panic and must not warn — we'd rather under-report than emit a misleading message.",
		},
		{
			name:   "patch-level identical with build metadata",
			caller: "0.6.5",
			daemon: "0.6.5+ci",
			want:   false,
			why:    "Build-metadata-only difference is a no-op for skew purposes.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.why == "" {
				t.Fatal("missing 'why' — every case must document the failure it prevents")
			}
			if got := IsSignificantVersionSkew(tt.caller, tt.daemon); got != tt.want {
				t.Errorf("IsSignificantVersionSkew(%q, %q) = %v, want %v\nwhy: %s",
					tt.caller, tt.daemon, got, tt.want, tt.why)
			}
		})
	}
}
