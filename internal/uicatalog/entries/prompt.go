package entries

import (
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "prompt",
		Family:  uicatalog.FamilyInput,
		Package: "internal/cli",
		Exports: []string{"SelectOne"},
		Since:   "0.3.0",
		WhenToUse: "Free-form text input with a default suggestion. " +
			"Reads from stdin in non-TTY mode without changing the contract — so prompts compose with `echo X | ox foo` cleanly.",
		WhenNotTo: "Sensitive input (no echo masking yet — file an issue) or " +
			"structured data better served by flags.",
		Render: func() string {
			label := cli.StyleBrand.Render("? ") + "Workspace name " + cli.StyleDim.Render("(my-project)") + ":"
			return label + " \n"
		},
	})
}
