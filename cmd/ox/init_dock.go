package main

import (
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/sageox/agentx"

	"github.com/sageox/ox/internal/claudemod"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/pet"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/ui"
)

// init_dock.go — what `ox init` says last: the docking box (what is connected
// to what, and who can read what flows between), and the one-time offer of the
// optional Claude Code add-ons. The box itself is internal/ui/init_dock.go.

// initDockFor builds the dock from the repo and its settings. onTeam is whether
// registration just confirmed a team: an old team ID left in config from an
// earlier init must not make a skipped registration look connected.
func initDockFor(gitRoot string, projectCfg *config.ProjectConfig, onTeam bool) ui.InitDock {
	dock := ui.InitDock{Repo: cli.SanitizeTerminalText(repotools.GetRepoName(gitRoot))}
	if onTeam && projectCfg != nil && projectCfg.TeamID != "" {
		team := projectCfg.TeamName
		if strings.TrimSpace(team) == "" {
			team = projectCfg.TeamID
		}
		dock.Team = cli.SanitizeTerminalText(team)
	}
	dock.Sessions = sessionReachFor(config.ResolveSessionRecording(gitRoot), config.ResolveSessionPublishing(gitRoot))
	return dock
}

// sessionReachFor turns the repo's two session settings into the one thing the
// dock tells a person: whether their sessions are recorded, and whether
// teammates can read them. Recording decides first; publishing decides whether
// a recorded session leaves the machine on its own.
func sessionReachFor(recording *config.ResolvedSessionRecording, publishing *config.ResolvedSessionPublishing) ui.SessionReach {
	switch {
	case !recording.ShouldRecord():
		return ui.SessionsOff
	case publishing.Mode == config.SessionPublishingManual:
		return ui.SessionsStayLocal
	default:
		return ui.SessionsToTeam
	}
}

// pendingInitOffers lists the add-ons worth mentioning on this machine. Each
// needs the claude CLI. Oxy is skipped once his folder exists; the session
// report pane is skipped once installed, and when switched off on purpose.
// (init installs the pane itself when Claude Code is one of the integrations it
// was asked to set up, so it is only offered when it was not.)
func pendingInitOffers(oxy pet.Installer, pane claudemod.Installer, paneSettingOn bool) []ui.AddonOffer {
	var offers []ui.AddonOffer
	if oxy.Claude != nil && !pathExists(oxy.Dir) {
		offers = append(offers, ui.AddonOffer{Command: "ox pet enable", What: "Oxy beside Claude Code's thinking line"})
	}
	if pane.Claude != nil && paneSettingOn && !pathExists(pane.Dir) {
		offers = append(offers, ui.AddonOffer{Command: "ox session report pane enable", What: "/ox-session-report: the ox calls in a session"})
	}
	return offers
}

// initOfferAudience is who is reading init's output.
type initOfferAudience struct {
	Terminal bool // stdout is a terminal
	Agent    bool // an AI coworker is running ox
	Quiet    bool
	JSON     bool
}

// isPerson is true only for someone reading the terminal. The offer is for a
// person to read; showing it to a log would use up the only time they would
// have seen it.
func (a initOfferAudience) isPerson() bool {
	return a.Terminal && !a.Agent && !a.Quiet && !a.JSON
}

// offerInitAddons offers the optional add-ons to a person finishing `ox init`.
// For anyone else (a pipe, CI, an AI coworker running init for someone) it
// prints nothing and spends nothing.
func offerInitAddons(w io.Writer) {
	audience := initOfferAudience{Terminal: isTerminalWriter(w), Agent: agentx.IsAgentContext()}
	if cfg != nil {
		audience.Quiet, audience.JSON = cfg.Quiet, cfg.JSON
	}
	if !audience.isPerson() {
		return
	}
	printInitOffersOnce(w)
}

// printInitOffersOnce prints the offers the first time it is called on a
// machine with something to offer, and never again after that.
func printInitOffersOnce(w io.Writer) {
	userCfg, err := config.LoadUserConfig()
	if err != nil || userCfg.HasSeenInitOffers() || !userCfg.AreTipsEnabled() {
		return
	}
	offers := pendingInitOffers(newPetInstaller(), newUsagePaneInstaller(), userCfg.IsSessionReportPaneEnabled())
	if len(offers) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprint(w, ui.RenderAddonOffers(offers))

	userCfg.SetInitOffersShown(true)
	if err := config.SaveUserConfig(userCfg); err != nil {
		slog.Debug("init: could not remember that the add-ons were offered", "error", err)
	}
}
