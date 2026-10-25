package entries

import (
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "confirm",
		Family:  uicatalog.FamilyInput,
		Package: "internal/cli",
		Exports: []string{"ConfirmYesNo", "ConfirmYesNoRequired", "ConfirmDangerousOperation", "ConfirmUninstall"},
		Since:   "0.4.0",
		WhenToUse: "Yes/no gates with a clear default. When taking the default silently " +
			"would lose work, use ConfirmYesNoRequired — it fails loudly instead of guessing " +
			"when nobody is there to answer. For destructive operations " +
			"use ConfirmDangerousOperation — it requires typing the exact target name, not just pressing y.",
		WhenNotTo: "Selecting between equal options (use Select) or " +
			"any flow where users will reflexively press enter — defaulting to dangerous is a footgun.",
		Render: func() string {
			label := cli.StyleBrand.Render("? ") + "Continue with sync?"
			yes := cli.StyleSuccess.Render("[Y]")
			no := cli.StyleDim.Render("n")
			return label + " " + yes + "/" + no + "\n"
		},
	})
}
