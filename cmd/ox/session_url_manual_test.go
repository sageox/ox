package main

import (
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
)

// TestSessionLinkOutputs_ManualPublishingMintsNoLink guards a dead-link class
// created by manual publishing.
//
// Manual mode suppresses start-registration, so the server never learns the
// session id and a /c/<id> URL resolves to a 404. These links are not
// ephemeral: they get written into git commit trailers and PR bodies, where a
// dead link outlives the session and the person who made it. The existing
// "pending" guard encodes the same rule for a different cause — a locally
// minted id is not evidence the remote resolver knows it.
func TestSessionLinkOutputs_ManualPublishingMintsNoLink(t *testing.T) {
	projCfg := &config.ProjectConfig{
		RepoID:   "repo_test",
		Endpoint: "https://sageox.ai",
	}
	state := &session.RecordingState{
		SessionID:                  "ses_01jftest",
		SessionPath:                "/tmp/sessions/2026-09-10T00-00-tester-OxTest",
		LifecycleRegistrationState: "deferred",
	}

	t.Run("manual mints nothing", func(t *testing.T) {
		restore := stubSessionPublishing(t, config.SessionPublishingManual)
		defer restore()

		url, directive := sessionLinkOutputs(projCfg, state, "on")
		if url != "" {
			t.Errorf("manual publishing must not mint a session URL, got %q", url)
		}
		if directive != "" {
			t.Errorf("manual publishing must not emit a PR directive, got %q", directive)
		}
	})

	// The regression that matters more: auto mode is the overwhelming default
	// and must be completely unaffected by the guard above.
	t.Run("auto still mints", func(t *testing.T) {
		restore := stubSessionPublishing(t, config.SessionPublishingAuto)
		defer restore()

		url, directive := sessionLinkOutputs(projCfg, state, "on")
		if url == "" {
			t.Fatal("auto publishing must still mint a session URL")
		}
		if directive == "" {
			t.Error("auto publishing must still emit the PR directive")
		}
	})
}

// stubSessionPublishing swaps the publishing-mode resolver for the duration of
// a test and returns a restore func.
func stubSessionPublishing(t *testing.T, mode string) func() {
	t.Helper()
	prev := effectiveSessionPublishing
	effectiveSessionPublishing = func() string { return mode }
	return func() { effectiveSessionPublishing = prev }
}
