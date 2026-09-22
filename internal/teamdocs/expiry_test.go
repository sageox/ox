package teamdocs

import (
	"testing"
	"time"
)

func TestExpiredOn(t *testing.T) {
	now := time.Date(2026, 9, 22, 13, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, value string
		wantDays    int
		wantOK      bool
	}{
		{"empty is evergreen", "", 0, false},
		{"future", "2027-03-21", 0, false},
		// Inclusive: an entry valid THROUGH today is still valid today. Off-by-one
		// here would retire every entry a day early, on the date its author chose.
		{"today is still valid", "2026-09-22", 0, false},
		{"yesterday is one day overdue", "2026-09-21", 1, true},
		{"long overdue", "2026-06-24", 90, true},
		// A typo must never retire a team's knowledge.
		{"malformed is not expired", "March 2027", 0, false},
		{"slashes are not the layout", "2026/09/21", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			days, ok := ExpiredOn(tc.value, now)
			if ok != tc.wantOK || days != tc.wantDays {
				t.Fatalf("ExpiredOn(%q) = (%d, %v), want (%d, %v)", tc.value, days, ok, tc.wantDays, tc.wantOK)
			}
		})
	}
}

func TestMalformedExpiry(t *testing.T) {
	for value, want := range map[string]bool{
		"": false, "2027-03-21": false, "March 2027": true, "2026/09/21": true,
	} {
		if got := MalformedExpiry(value); got != want {
			t.Fatalf("MalformedExpiry(%q) = %v, want %v", value, got, want)
		}
	}
}
