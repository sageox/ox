// Package theme provides semantic design tokens and lipgloss styles for the
// dashboard TUI. Only this package and internal/cli may reference raw color
// values — all other dashboard packages consume styles from here.
package theme

// Semantic token name constants document which visual role each style fills.
// They are used as human-readable labels (e.g., in future theme-config files)
// but are not referenced at runtime by the style definitions below.
const (
	TokenBorderFocused   = "border-focused"
	TokenBorderUnfocused = "border-unfocused"
	TokenHeader          = "header"
	TokenTimelineNow     = "timeline-now"
	TokenTimelineRecent  = "timeline-recent"
	TokenTimelineEarlier = "timeline-earlier"
	TokenTimelineSync    = "timeline-sync"
	TokenTimelineSession = "timeline-session"
	TokenTimelineMurmur  = "timeline-murmur"
	TokenNavSelected     = "nav-selected"
	TokenNavSection      = "nav-section"
	TokenNavLeaf         = "nav-leaf"
	TokenStatusHealthy   = "status-healthy"
	TokenStatusWarning   = "status-warning"
	TokenStatusError     = "status-error"
	TokenInspectorLabel  = "inspector-label"
	TokenInspectorValue  = "inspector-value"
	TokenInspectorTitle  = "inspector-title"
)
