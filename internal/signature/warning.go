package signature

import (
	"fmt"
	"os"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/theme"
)

// WarningMessage is the standard warning for unsigned/invalid SAGEOX.md
const WarningMessage = "Warning: SAGEOX.md has not been signed by SageOx, unable to verify it has not been tampered with. See https://sageox.ai/i/signed for more info."

// warning style using lipgloss
var warningStyle = lipgloss.NewStyle().Foreground(theme.Color("214"))

// EmitWarning prints the warning message to stderr with yellow color
// Only emits if SAGEOX.md exists but is not verified
func EmitWarning() {
	fmt.Fprintln(os.Stderr, warningStyle.Render(WarningMessage))
}

// CheckAndWarn checks the SAGEOX.md file and emits warning if needed.
// This should be called from PersistentPreRun in root.go.
//
// DISABLED: Server-side signing not implemented for MVP.
// Re-enable when SAGEOX.md signing is available on the SageOx cloud.
func CheckAndWarn() {
	// no-op for MVP - server-side signing not yet implemented
}
