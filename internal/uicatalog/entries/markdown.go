package entries

import (
	"github.com/sageox/ox/internal/ui"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "markdown",
		Family:  uicatalog.FamilyDataDisplay,
		Package: "internal/ui",
		Exports: []string{"RenderMarkdown"},
		Since:   "0.5.0",
		WhenToUse: "Long-form content with structure — release notes, agent " +
			"responses, knowledge bubble snippets. Glamour-backed with a SageOx-tuned theme so output matches brand.",
		WhenNotTo: "Short status output (markdown headers are overkill) or any " +
			"performance-sensitive hot path — glamour parsing has measurable cost.",
		Render: func() string {
			source := "# Release notes\n\n" +
				"ox **0.9.0** ships the component catalog.\n\n" +
				"- Hidden `ox dev catalog` command\n" +
				"- Theme tokens documented in `docs/design/`\n" +
				"- Published at [sageox-design.netlify.app](https://sageox-design.netlify.app/)\n\n" +
				"> Calm, precise output for humans and AI coworkers.\n"
			return ui.RenderMarkdown(source)
		},
	})
}
