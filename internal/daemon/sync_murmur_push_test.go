package daemon

import "testing"

// shouldPushMurmurs is the murmur-path counterpart to shouldPushSessionDrafts,
// and the reason it exists is identical: the daemon's batched push runs neither
// pushLedger's pre-push secret gate nor its LFS reconcile, and `git push` moves
// the WHOLE branch. So the only thing standing between the daemon and shipping a
// commit the CLI deliberately refused — or a pointer whose LFS blob is not yet
// uploaded (the GH #810 wedge, smuggled via the murmur path) — is the rule that
// every unpushed commit must be a murmur commit.
//
// Every ok=false row is a way an ungated commit would otherwise reach the remote.
func TestShouldPushMurmurs(t *testing.T) {
	tests := []struct {
		name        string
		subjects    string
		wantOK      bool
		wantBlocker string
		why         string
	}{
		{
			name:     "all murmur commits",
			subjects: "murmur: 2026-08-24 wip\nmurmur: delete abc",
			wantOK:   true,
			why:      "the only case where pushing without the gate stack is acceptable",
		},
		{
			name:        "a plan commit is unpushed underneath",
			subjects:    "murmur: 2026-08-24 wip\nplan: 2026-08-24-some-plan",
			wantBlocker: "plan: 2026-08-24-some-plan",
			why: "a deferred plan commit may carry a plan.html pointer; pushing it here " +
				"without the LFS reconcile is exactly the #810 wedge this guard prevents",
		},
		{
			name:        "an arbitrary CLI commit is unpushed underneath",
			subjects:    "murmur: 2026-08-24 wip\nsummary: 2026-08-24 something",
			wantBlocker: "summary: 2026-08-24 something",
			why:         "it may be a commit the CLI's pre-push secret gate refused",
		},
		{
			name:        "the blocking commit is on top",
			subjects:    "plan: 2026-08-24-some-plan\nmurmur: 2026-08-24 wip",
			wantBlocker: "plan: 2026-08-24-some-plan",
			why:         "order does not matter — any non-murmur commit blocks",
		},
		{
			name:     "nothing to push",
			subjects: "",
			wantOK:   false,
			why:      "empty log: no murmur seen, nothing to do",
		},
		{
			name:     "blank lines are ignored",
			subjects: "murmur: a\n\n  \nmurmur: b",
			wantOK:   true,
			why:      "whitespace-only lines must not be mistaken for a non-murmur commit",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			blocker, ok := shouldPushMurmurs(tc.subjects)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (%s)", ok, tc.wantOK, tc.why)
			}
			if blocker != tc.wantBlocker {
				t.Fatalf("blocker = %q, want %q (%s)", blocker, tc.wantBlocker, tc.why)
			}
		})
	}
}
