package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
)

// writeUserConfig points OX_USER_CONFIG at a config file holding the given
// body. OX_USER_CONFIG overrides all path discovery, so the agent-worker
// readers below resolve against this file instead of the developer's real
// ~/.config/sageox — the seam that makes these two functions testable at all.
func writeUserConfig(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write user config: %v", err)
	}
	t.Setenv(config.EnvUserConfig, path)
}

// --- Agent worker (`Summarizer` row, and .daemon.agent_worker in --json) ---

// TestAgentWorkerResolution covers the agent-worker states that resolve
// identically on every machine, in both output forms.
//
// Failure prevented: source is the only field separating a deliberate opt-out
// or an explicit pin from ox picking an agent by scanning PATH. Collapsing
// those hides a pin, which matters wherever a build and its review must not
// land on the same model.
func TestAgentWorkerResolution(t *testing.T) {
	tests := []struct {
		name       string
		userConfig string
		wantAgent  string
		wantSource string
		wantInLine string // substring the rendered Summarizer row must contain
	}{
		{
			name:       "explicitly disabled",
			userConfig: "agent_worker:\n  agent: none\n",
			wantAgent:  "none",
			wantSource: "disabled",
			wantInLine: "disabled",
		},
		{
			name:       "explicitly pinned to an agent",
			userConfig: "agent_worker:\n  agent: codex\n",
			wantAgent:  "codex",
			wantSource: "configured",
			wantInLine: "codex",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeUserConfig(t, tt.userConfig)

			got := buildAgentWorkerJSON()
			if got == nil {
				t.Fatal("buildAgentWorkerJSON() = nil, want a value")
			}
			if got.Agent != tt.wantAgent {
				t.Errorf("Agent = %q, want %q", got.Agent, tt.wantAgent)
			}
			if got.Source != tt.wantSource {
				t.Errorf("Source = %q, want %q", got.Source, tt.wantSource)
			}

			line := renderAgentWorkerLine()
			if !strings.Contains(line, "Summarizer") {
				t.Errorf("line %q missing the Summarizer label", line)
			}
			if !strings.Contains(line, tt.wantInLine) {
				t.Errorf("line %q does not contain %q", line, tt.wantInLine)
			}
			if !strings.HasSuffix(line, "\n") {
				t.Errorf("line %q must end in a newline or it runs into the next row", line)
			}
		})
	}
}

// --- Daemon Sync section ---

// TestRenderDaemonSyncSection covers the branches that do not depend on whether
// a daemon happens to be running on the machine executing the test.
func TestRenderDaemonSyncSection(t *testing.T) {
	syncedLedger := &config.LocalConfig{
		Ledger: &config.LedgerConfig{
			Path:     "/tmp/ledger",
			LastSync: time.Now().Add(-2 * time.Hour),
		},
	}
	neverSynced := &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: "/tmp/ledger"}, // LastSync zero
	}

	tests := []struct {
		name               string
		localCfg           *config.LocalConfig
		noProject          bool
		projectInitialized bool
		wantContains       []string
		wantOmits          []string
	}{
		{
			name:         "outside a git repo the section reports n/a",
			noProject:    true,
			wantContains: []string{"Daemon Sync", "not in a git repo"},
		},
		{
			// The point of persisting last_sync is that it still answers
			// "when did this last sync?" once the daemon is gone.
			name:               "last known ledger sync survives a stopped daemon",
			localCfg:           syncedLedger,
			projectInitialized: true,
			wantContains:       []string{"Last ledger sync"},
		},
		{
			name:               "a zero last-sync is not rendered as a real time",
			localCfg:           neverSynced,
			projectInitialized: true,
			wantOmits:          []string{"Last ledger sync"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderDaemonSyncSection(nil, nil, tt.localCfg, tt.noProject, tt.projectInitialized)

			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("output %q missing %q", got, want)
				}
			}
			for _, omit := range tt.wantOmits {
				if strings.Contains(got, omit) {
					t.Errorf("output %q unexpectedly contains %q", got, omit)
				}
			}
		})
	}
}

// TestRenderDaemonSyncSection_StoppedDaemonWording drives daemonGetState
// directly so every branch of the nil-status switch is reachable regardless of
// whether a daemon happens to be running on the machine under test.
//
// Failure prevented: the same "daemon not running" fact means different things
// either side of `ox init`. Showing the initialized wording in an uninitialized
// repo tells the reader to start a daemon for a project that does not exist
// yet. Reading the real daemon state instead made this assertion vacuous on any
// machine with a daemon up, which is the normal developer state.
func TestRenderDaemonSyncSection_StoppedDaemonWording(t *testing.T) {
	stubDaemonState := func(t *testing.T, state daemon.DaemonState) {
		t.Helper()
		prev := daemonGetState
		daemonGetState = func() daemon.DaemonState { return state }
		t.Cleanup(func() { daemonGetState = prev })
	}

	tests := []struct {
		name               string
		state              daemon.DaemonState
		projectInitialized bool
		wantContains       []string
		wantOmits          []string
	}{
		{
			name:               "stopped in an uninitialized project blames init, not the daemon",
			state:              daemon.DaemonStateStopped,
			projectInitialized: false,
			wantContains:       []string{"ox init"},
			wantOmits:          []string{"ox daemon start"},
		},
		{
			name:               "stopped in an initialized project points at the daemon",
			state:              daemon.DaemonStateStopped,
			projectInitialized: true,
			wantContains:       []string{"ox daemon start"},
			wantOmits:          []string{"ox init"},
		},
		{
			name:               "starting short-circuits both wordings",
			state:              daemon.DaemonStateStarting,
			projectInitialized: false,
			wantContains:       []string{"starting"},
			wantOmits:          []string{"ox init", "ox daemon start"},
		},
		{
			name:               "stuck recommends a restart, not init",
			state:              daemon.DaemonStateStuck,
			projectInitialized: false,
			wantContains:       []string{"stuck", "ox daemon restart"},
			wantOmits:          []string{"ox init"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubDaemonState(t, tt.state)

			got := renderDaemonSyncSection(nil, nil, nil, false, tt.projectInitialized)

			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("output %q should contain %q", got, want)
				}
			}
			for _, omit := range tt.wantOmits {
				if strings.Contains(got, omit) {
					t.Errorf("output %q unexpectedly contains %q", got, omit)
				}
			}
		})
	}
}

// --- AI Coworkers section ---

// TestRenderAICoworkersSection_NoDaemonRendersNothing verifies the section stays
// silent rather than reporting zero.
//
// Failure prevented: this row is suppressed by design when there is nothing to
// say. Emitting a header or "0 active" with no daemon adds a line to every
// status run on every machine without a daemon.
func TestRenderAICoworkersSection_NoDaemonRendersNothing(t *testing.T) {
	if got := renderAICoworkersSection(nil); got != "" {
		t.Errorf("renderAICoworkersSection(nil) = %q, want empty", got)
	}
}
