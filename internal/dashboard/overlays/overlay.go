package overlays

import tea "charm.land/bubbletea/v2"

// Overlay is a floating UI element (modal, help dialog, confirm) rendered
// on top of the pane layout. The topmost overlay on the Stack receives input first.
type Overlay interface {
	// ID returns the overlay's stable identifier.
	ID() OverlayID

	// Update processes a message. Returns:
	//   - updated overlay (or nil if overlay should close)
	//   - any commands to run
	//   - consumed: true if the message was handled and should not propagate
	Update(msg tea.Msg) (Overlay, tea.Cmd, bool)

	// View renders the overlay content to a string sized for the given terminal dimensions.
	View(width, height int) string
}

// OverlayID uniquely identifies an overlay type.
type OverlayID int

const (
	OverlayHelp OverlayID = iota
	OverlayConfirm
	OverlayPalette
)
