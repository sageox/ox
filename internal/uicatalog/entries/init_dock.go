package entries

import (
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/ui"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "init-dock",
		Family:  uicatalog.FamilyScreen,
		Package: "internal/ui/init_dock.go",
		Exports: []string{"ui.InitDock", "ui.RenderTryFirst", "ui.AddonOffer", "ui.RenderAddonOffers"},
		Since:   "0.26.0",
		WhenToUse: "The last thing a connecting command says: what is now connected to what, " +
			"and who can read what flows between. Use the docking box when a command joins " +
			"a person's work to a team's, so the reach of what is shared is visible at the " +
			"moment of connecting instead of only in a README.",
		WhenNotTo: "Commands that change nothing a teammate can see. The box is a disclosure; " +
			"used as decoration it trains people to skim it. Never print it for --quiet, --json, " +
			"or from automation: unframed, it is plain labeled lines.",
		Render: func() string {
			dim := cli.StyleDim.Render
			caption := func(s string) string { return dim("─── "+s+" ───") + "\n\n" }

			// Every block is drawn by the functions `ox init` calls.
			dock := ui.InitDock{Repo: "sageox/ox", Team: "Team A", Sessions: ui.SessionsToTeam}
			manual := ui.InitDock{Repo: "sageox/ox", Team: "Team A", Sessions: ui.SessionsStayLocal}
			offers := ui.RenderAddonOffers([]ui.AddonOffer{
				{Command: "ox pet enable", What: "Oxy beside Claude Code's thinking line"},
			})

			return caption("at a terminal") + dock.Render(true) + "\n\n" + ui.RenderTryFirst() + "\n" +
				caption("first time on this machine: the add-ons, offered once") + offers + "\n" +
				caption("piped, in CI, or run by an AI coworker: the same facts, no frame") + dock.Render(false) + "\n\n" +
				caption("when uploads are manual, the box says nobody can read a session yet") + manual.Render(false) + "\n"
		},
	})
}
