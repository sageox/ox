package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/claudemod"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/pet"
	"github.com/sageox/ox/internal/ui"
)

// fakeClaudeCLI stands in for the claude CLI: only whether it exists matters to
// the offers.
type fakeClaudeCLI struct{}

func (fakeClaudeCLI) Run(context.Context, ...string) (string, error) { return "", nil }

// TestSessionReachFor pins the one sentence init tells a person about who can
// read their sessions to the two settings that decide it.
// Failure prevented: the box saying "teammates can read them" in a repo where
// recording is off or uploads are manual, which would be a false disclosure
// about what leaves the machine.
func TestSessionReachFor(t *testing.T) {
	recording := func(mode string) *config.ResolvedSessionRecording {
		return &config.ResolvedSessionRecording{Mode: mode}
	}
	publishing := func(mode string) *config.ResolvedSessionPublishing {
		return &config.ResolvedSessionPublishing{Mode: mode}
	}
	tests := []struct {
		name       string
		recording  string
		publishing string
		want       ui.SessionReach
	}{
		{"recorded automatically, uploaded automatically", config.SessionRecordingAuto, config.SessionPublishingAuto, ui.SessionsToTeam},
		{"recorded on request, uploaded automatically", config.SessionRecordingManual, config.SessionPublishingAuto, ui.SessionsToTeam},
		{"recorded, but upload is manual", config.SessionRecordingAuto, config.SessionPublishingManual, ui.SessionsStayLocal},
		{"recording off beats any publishing setting", config.SessionRecordingDisabled, config.SessionPublishingAuto, ui.SessionsOff},
		{"recording off, manual publishing", config.SessionRecordingDisabled, config.SessionPublishingManual, ui.SessionsOff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sessionReachFor(recording(tt.recording), publishing(tt.publishing)))
		})
	}
}

// TestInitDockFor verifies the dock names a team only when registration just
// confirmed one, and falls back to the team ID when the server sent no name.
// Failure prevented: a skipped registration looking connected because an old
// team ID was still in config, and a blank team name printing an empty arrow.
func TestInitDockFor(t *testing.T) {
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	gitRoot := t.TempDir()

	tests := []struct {
		name     string
		cfg      *config.ProjectConfig
		onTeam   bool
		wantTeam string
	}{
		{"on a team, with a name", &config.ProjectConfig{TeamID: "team_1", TeamName: "Team A"}, true, "Team A"},
		{"on a team, name blank: the ID stands in", &config.ProjectConfig{TeamID: "team_1", TeamName: "  "}, true, "team_1"},
		{"a name from the server cannot carry terminal control codes", &config.ProjectConfig{TeamID: "team_1", TeamName: "Team\x1b[31m A"}, true, "Team A"},
		{"a stale team ID from an earlier init: registration did not confirm", &config.ProjectConfig{TeamID: "team_1", TeamName: "Team A"}, false, ""},
		{"no team ID", &config.ProjectConfig{TeamName: "Team A"}, true, ""},
		{"no project config", nil, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dock := initDockFor(gitRoot, tt.cfg, tt.onTeam)
			assert.Equal(t, tt.wantTeam, dock.Team)
			assert.NotEmpty(t, dock.Repo)
		})
	}
}

// TestPrintInitOffersOnce_SpendsNothingWhenThereIsNothingToOffer verifies a
// machine without Claude Code is not marked as having been offered.
// Failure prevented: installing Claude Code later and never being told about
// Oxy because the one-time offer was used up on a machine that could not use it.
func TestPrintInitOffersOnce_SpendsNothingWhenThereIsNothingToOffer(t *testing.T) {
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	prevOxy, prevPane := newPetInstaller, newUsagePaneInstaller
	newPetInstaller = func() pet.Installer { return pet.Installer{} }
	newUsagePaneInstaller = func() claudemod.Installer { return claudemod.Installer{} }
	t.Cleanup(func() { newPetInstaller, newUsagePaneInstaller = prevOxy, prevPane })

	var out bytes.Buffer
	printInitOffersOnce(&out)
	assert.Empty(t, out.String())

	userCfg, err := config.LoadUserConfig()
	require.NoError(t, err)
	assert.False(t, userCfg.HasSeenInitOffers(), "the offer is still available")
}

func TestPendingInitOffers(t *testing.T) {
	present := t.TempDir()
	absent := filepath.Join(t.TempDir(), "never-written")
	claude := claudemod.Claude(fakeClaudeCLI{})

	tests := []struct {
		name   string
		oxy    pet.Installer
		pane   claudemod.Installer
		paneOn bool
		want   []string
	}{
		{
			name:   "claude is here and neither add-on is: offer both",
			oxy:    pet.Installer{Claude: claude, Dir: absent},
			pane:   claudemod.Installer{Claude: claude, Dir: absent},
			paneOn: true,
			want:   []string{"ox pet enable", "ox session report pane enable"},
		},
		{
			name:   "no claude CLI: nothing to install into",
			oxy:    pet.Installer{Dir: absent},
			pane:   claudemod.Installer{Dir: absent},
			paneOn: true,
		},
		{
			name:   "both already installed",
			oxy:    pet.Installer{Claude: claude, Dir: present},
			pane:   claudemod.Installer{Claude: claude, Dir: present},
			paneOn: true,
		},
		{
			name:   "the pane was switched off on purpose: do not nag",
			oxy:    pet.Installer{Claude: claude, Dir: present},
			pane:   claudemod.Installer{Claude: claude, Dir: absent},
			paneOn: false,
		},
		{
			name:   "init installed the pane itself: only Oxy is left to offer",
			oxy:    pet.Installer{Claude: claude, Dir: absent},
			pane:   claudemod.Installer{Claude: claude, Dir: present},
			paneOn: true,
			want:   []string{"ox pet enable"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, o := range pendingInitOffers(tt.oxy, tt.pane, tt.paneOn) {
				got = append(got, o.Command)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// withInstallers points the offers at fake Claude Code installs, so a test
// does not depend on whether claude is on the developer's PATH.
func withInstallers(t *testing.T, oxyDir, paneDir string) {
	t.Helper()
	prevOxy, prevPane := newPetInstaller, newUsagePaneInstaller
	newPetInstaller = func() pet.Installer { return pet.Installer{Claude: fakeClaudeCLI{}, Dir: oxyDir} }
	newUsagePaneInstaller = func() claudemod.Installer { return claudemod.Installer{Claude: fakeClaudeCLI{}, Dir: paneDir} }
	t.Cleanup(func() { newPetInstaller, newUsagePaneInstaller = prevOxy, prevPane })
}

// TestPrintInitOffersOnce verifies the offer is made once per machine: shown the
// first time, remembered, and silent after that, in this repo and every other.
// Failure prevented: the offer repeating on every `ox init`, which turns a
// mention of Oxy into a nag.
func TestPrintInitOffersOnce(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "never-written")
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	withInstallers(t, absent, absent)

	var first bytes.Buffer
	printInitOffersOnce(&first)
	got := ansi.Strip(first.String())
	assert.Contains(t, got, "ox pet enable")
	assert.Contains(t, got, "ox session report pane enable")

	var second bytes.Buffer
	printInitOffersOnce(&second)
	assert.Empty(t, second.String(), "already offered on this machine")
}

func TestPrintInitOffersOnce_StaysQuietWhenTipsAreOff(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "never-written")
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv(config.EnvUserConfig, path)
	require.NoError(t, os.WriteFile(path, []byte("tips_enabled: false\n"), 0o600))
	withInstallers(t, absent, absent)

	var out bytes.Buffer
	printInitOffersOnce(&out)
	assert.Empty(t, out.String(), "someone who turned tips off asked not to be told about extras")
}

// TestInitOfferAudience verifies the offer reaches exactly one audience, and that
// each condition alone keeps it from anyone else.
func TestInitOfferAudience(t *testing.T) {
	person := initOfferAudience{Terminal: true}
	require.True(t, person.isPerson())

	tests := []struct {
		name   string
		mutate func(*initOfferAudience)
	}{
		{"stdout is a pipe or file", func(a *initOfferAudience) { a.Terminal = false }},
		{"an AI coworker is running ox", func(a *initOfferAudience) { a.Agent = true }},
		{"--quiet", func(a *initOfferAudience) { a.Quiet = true }},
		{"--json", func(a *initOfferAudience) { a.JSON = true }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := person
			tt.mutate(&a)
			assert.False(t, a.isPerson())
		})
	}
}

// TestOfferInitAddons_NeverSpendsTheOfferOnAnythingButATerminal verifies a run
// whose output no person reads prints nothing and leaves the offer unspent.
// Failure prevented: an AI coworker or a CI job running `ox init` using up the
// one time a person would have been shown Oxy, so they never learn he exists.
func TestOfferInitAddons_NeverSpendsTheOfferOnAnythingButATerminal(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "never-written")
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv(config.EnvUserConfig, path)
	withInstallers(t, absent, absent) // something is on offer, so only the audience check can stop it

	var out bytes.Buffer
	offerInitAddons(&out)
	assert.Empty(t, out.String(), "a buffer is not a terminal")

	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "the offer was not marked as shown")
}
