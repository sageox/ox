package session

import "testing"

func TestChooseFinalizeMode(t *testing.T) {
	cases := []struct {
		name             string
		daemonViable     bool
		userPrefersAsync bool
		want             FinalizeDispatchMode
	}{
		{"daemon_off_user_sync", false, false, FinalizeSync},
		// The bugfix: even when the user opted into async/delegated, an
		// environment with no daemon must NOT fire the async path —
		// otherwise the session signal goes nowhere and the recording
		// never reaches the ledger.
		{"daemon_off_user_async_downgrades", false, true, FinalizeSync},
		{"daemon_on_user_sync", true, false, FinalizeSync},
		{"daemon_on_user_async", true, true, FinalizeAsyncDaemon},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := ChooseFinalizeMode(tc.daemonViable, tc.userPrefersAsync)
			if got != tc.want {
				t.Errorf("ChooseFinalizeMode(daemonViable=%v, userPrefersAsync=%v) = %v, want %v",
					tc.daemonViable, tc.userPrefersAsync, got, tc.want)
			}
		})
	}
}
