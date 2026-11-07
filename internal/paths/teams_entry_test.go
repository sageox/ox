package paths

import "testing"

// Failure prevented: a clone staging directory inside the teams directory was
// listed, and imported into, as if it were a team.
func TestIsHiddenTeamsEntry(t *testing.T) {
	for name, want := range map[string]bool{
		"team_abc":                 false,
		".team_abc.tmp-123":        true,
		"team_abc.cache-aside-123": false, // only the dot prefix marks scratch
	} {
		if got := IsHiddenTeamsEntry(name); got != want {
			t.Errorf("IsHiddenTeamsEntry(%q) = %v, want %v", name, got, want)
		}
	}
}
