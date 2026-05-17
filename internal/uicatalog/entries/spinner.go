package entries

import (
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "spinner",
		Family:  uicatalog.FamilyFeedback,
		Package: "internal/cli",
		Exports: []string{"WithSpinner", "WithSpinnerNoResult"},
		Since:   "0.3.0",
		WhenToUse: "Wrap any operation expected to take > 300ms (network, git clone, daemon RPC). " +
			"Auto-hides if the work completes faster, so fast paths don't flicker.",
		WhenNotTo: "Operations < 100ms — the spinner appears, blinks, vanishes, " +
			"and adds noise without value. Also: anything in a non-TTY (the spinner falls back silently, which is correct).",
		Render: func() string {
			// The spinner is animation-only; the static catalog renders a
			// representative frame. The .cast recording captures the motion.
			frame := cli.StyleBrand.Render("⠋") + " Syncing ledger..."
			done := cli.StyleSuccess.Render("✓") + " Synced 12 sessions in 1.8s"
			return frame + "\n" + done
		},
	})
}
