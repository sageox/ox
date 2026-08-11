package identity

// anonymous_test.go pins Attribution.IsAnonymous — the predicate that lets a
// caller tell "we resolved an identity" from "we resolved nothing", which
// DisplayName alone cannot express because it is documented always-non-empty.
//
// Failure prevented: a caller writes the always-non-empty DisplayName into a
// shared, later-queried artifact, so total resolution failure is recorded as a
// name-shaped string. Attribution coverage then reads 100% forever and stops
// measuring anything — which is how the plan-attribution regression would have
// hidden its own recurrence.

import "testing"

func TestAttribution_IsAnonymous(t *testing.T) {
	tests := []struct {
		name string
		attr Attribution
		want bool
	}{
		{
			name: "absolute fallback — nothing resolved",
			attr: Attribution{Username: "anonymous", Name: "Anonymous", DisplayName: "Anonymous"},
			want: true,
		},
		{
			name: "OAuth identity resolved",
			attr: Attribution{Username: "person-a", Email: "person-a@example.com", Name: "Person A", DisplayName: "Person A."},
			want: false,
		},
		{
			name: "git identity without email still counts as resolved",
			attr: Attribution{Username: "person-a", Name: "Person A", DisplayName: "Person A."},
			want: false,
		},
		{
			name: "OS username only still counts as resolved",
			attr: Attribution{Username: "buildbot", Name: "buildbot", DisplayName: "Buildbot"},
			want: false,
		},
		{
			// The check is on the SOURCES, not on the string, so a user whose
			// name genuinely is Anonymous is not misclassified as unresolved.
			name: "genuinely named Anonymous with a real email is not anonymous",
			attr: Attribution{Username: "anonymous", Email: "anonymous@example.com", Name: "Anonymous", DisplayName: "Anonymous"},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.attr.IsAnonymous(); got != tc.want {
				t.Errorf("IsAnonymous() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestResolveAttribution_NeverReturnsEmptyDisplayName is the negative control
// for the above: it documents WHY IsAnonymous has to exist. If this ever fails,
// DisplayName has become a usable did-this-resolve test and IsAnonymous's
// justification changes.
func TestResolveAttribution_NeverReturnsEmptyDisplayName(t *testing.T) {
	t.Setenv("USER", "")
	t.Setenv("USERNAME", "")
	t.Setenv("GIT_CONFIG_GLOBAL", "/nonexistent/gitconfig")

	if got := ResolveAttribution("", "").DisplayName; got == "" {
		t.Fatal("DisplayName is empty — the always-non-empty contract changed; revisit IsAnonymous's rationale")
	}
}
