package claude

// HookType is the standard hook type for command-based hooks.
const HookType = "command"

// Hook represents a single hook configuration.
type Hook struct {
	Command string `json:"command"`
	Type    string `json:"type"`
}

// HookEntry represents an entry in the hooks array.
type HookEntry struct {
	Hooks   []Hook `json:"hooks"`
	Matcher string `json:"matcher"`
}

// Settings represents the structure of a Claude settings file (hooks section).
type Settings struct {
	Hooks map[string][]HookEntry `json:"hooks,omitempty"`
}
