package daemon

import (
	"errors"
	"testing"
)

// TestIsTransientSyncError_MatchesGitsOwn5xxWording pins the gap that made a
// server outage look like a hard failure.
//
// The marker list was written from how a browser or proxy phrases an HTTP
// error ("503 Service Unavailable"). git-remote-https does not phrase it that
// way — it reports the STATUS ONLY:
//
//	fatal: unable to access '<url>': The requested URL returned error: 503
//
// so none of the 5xx markers ever matched a real one, and ox treated a
// self-clearing sageox.ai outage as a permanent failure: no retry, and an
// operator told to investigate their own machine.
//
// Failure prevented: a retryable outage being reported as a hard sync failure.
func TestIsTransientSyncError_MatchesGitsOwn5xxWording(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  string
		want bool
	}{
		{
			name: "the real observed 503 from git.sageox.ai",
			err:  "fetch failed: fatal: unable to access 'https://git.sageox.ai/sageox-team_x/team-context.git/': The requested URL returned error: 503: exit status 128",
			want: true,
		},
		{name: "502 in git's wording", err: "The requested URL returned error: 502", want: true},
		{name: "504 in git's wording", err: "The requested URL returned error: 504", want: true},
		// A 5xx HTML error page fed to git's smart-HTTP parser surfaces as this.
		{name: "HTML error page breaks ref listing", err: "fatal: expected flush after ref listing: exit status 128", want: true},
		// The browser-style phrasings stay matched; some proxies do emit them.
		{name: "proxy-style 503", err: "503 Service Unavailable", want: true},

		// Things that are NOT transient must stay hard failures, or the retry
		// loop hides a real problem forever.
		{name: "auth failure is permanent", err: "The requested URL returned error: 403", want: false},
		{name: "missing repo is permanent", err: "The requested URL returned error: 404", want: false},
		{name: "bad credentials", err: "Authentication failed for 'https://git.sageox.ai/x.git/'", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := isTransientSyncError(errors.New(tc.err))
			if got != tc.want {
				t.Errorf("isTransientSyncError(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
