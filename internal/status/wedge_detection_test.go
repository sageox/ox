package status

import "testing"

// TestGitRepoStatus_IsWedged pins the wedge classifier — what counts as
// a state ox status should scream about, and what doesn't. Three
// failure modes the bare-eye check has to distinguish:
//
//  1. clean repo (synced or just behind) → NOT wedged
//  2. local in-progress work (ahead, no rebase) → NOT wedged
//  3. true divergence (ahead AND behind) → WEDGED
//  4. stuck rebase (RebaseInProgress) → WEDGED
//
// Each case carries a 'why this matters' note documenting the failure
// it prevents — false positives erode trust in the warning, false
// negatives are the original ox-un4u bug.
func TestGitRepoStatus_IsWedged(t *testing.T) {
	tests := []struct {
		name string
		s    GitRepoStatus
		want bool
		why  string
	}{
		{
			name: "clean and synced",
			s:    GitRepoStatus{Exists: true, IsSynced: true},
			want: false,
			why:  "Healthy repo must not be flagged or users will tune out the warning.",
		},
		{
			name: "ahead only (local commits not yet pushed)",
			s:    GitRepoStatus{Exists: true, AheadCount: 3, BehindCount: 0},
			want: false,
			why:  "Pure ahead is a normal in-progress state. Push will reconcile, no wedge.",
		},
		{
			name: "behind only (just need to pull)",
			s:    GitRepoStatus{Exists: true, AheadCount: 0, BehindCount: 5},
			want: false,
			why:  "Pure behind is a normal state pre-pull. Daemon's pull cycle handles it.",
		},
		{
			name: "true divergence",
			s:    GitRepoStatus{Exists: true, AheadCount: 2, BehindCount: 4},
			want: true,
			why:  "Both sides have commits the other doesn't — push will fail until rebase reconciles. This is the wedge ox-un4u was reported for.",
		},
		{
			name: "rebase in progress with no divergence counts",
			s:    GitRepoStatus{Exists: true, RebaseInProgress: true},
			want: true,
			why:  "Stuck rebase is the most direct wedge signal. Even with ahead=0/behind=0 (e.g. mid-rebase the divergence query fails), the in-progress flag must trip the warning.",
		},
		{
			name: "rebase in progress with divergence",
			s:    GitRepoStatus{Exists: true, RebaseInProgress: true, AheadCount: 1, BehindCount: 1},
			want: true,
			why:  "Belt-and-suspenders: both signals point at a wedge.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.why == "" {
				t.Fatal("test case missing 'why' — every case must document the failure it prevents")
			}
			if got := tt.s.IsWedged(); got != tt.want {
				t.Errorf("IsWedged() = %v, want %v\nwhy: %s", got, tt.want, tt.why)
			}
		})
	}
}
