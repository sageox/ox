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
//
// The recording's saved mode counts as much as the setting now: a recording
// started under manual is held on this machine and never registered, even once
// the setting resolves to auto (notifySessionStarted skips it). Red-first: drop
// the saved-mode half of the rule and "started manual, setting now auto" mints
// a link.
func TestSessionLinkOutputs_ManualPublishingMintsNoLink(t *testing.T) {
	projCfg := &config.ProjectConfig{
		RepoID:   "repo_test",
		Endpoint: "https://sageox.ai",
	}

	tests := []struct {
		name        string
		modeAtStart string // the mode the recording saved
		modeNow     string
		wantLink    bool
	}{
		{"setting now manual", config.SessionPublishingAuto, config.SessionPublishingManual, false},
		{"started manual, setting now auto", config.SessionPublishingManual, config.SessionPublishingAuto, false},
		// The regression that matters more: auto mode is the overwhelming
		// default and must be completely unaffected by the guard.
		{"auto throughout still mints", config.SessionPublishingAuto, config.SessionPublishingAuto, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(config.EnvSessionPublishing, tt.modeNow)
			state := &session.RecordingState{
				SessionID:                  "ses_01jftest",
				SessionPath:                "/tmp/sessions/2026-09-10T00-00-tester-OxTest",
				LifecycleRegistrationState: "deferred",
				PublishingMode:             tt.modeAtStart,
			}

			url, directive := sessionLinkOutputs(t.TempDir(), projCfg, state, "on")
			if !tt.wantLink {
				if url != "" {
					t.Errorf("a session held on this machine must not mint a session URL, got %q", url)
				}
				if directive != "" {
					t.Errorf("a session held on this machine must not emit a PR directive, got %q", directive)
				}
				return
			}
			if url == "" {
				t.Fatal("auto publishing must still mint a session URL")
			}
			if directive == "" {
				t.Error("auto publishing must still emit the PR directive")
			}
		})
	}
}
