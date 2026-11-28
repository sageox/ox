package claude

import "encoding/json"

// HookType is the standard hook type for command-based hooks.
const HookType = "command"

// Hook represents a single hook configuration.
type Hook struct {
	Command string `json:"command"`
	Type    string `json:"type"`

	// extra holds every other field of the hook (timeout, statusMessage,
	// args, ...) as a compact JSON object, "" when there are none. ox rewrites
	// the whole hooks block when it installs its own hooks, and a coworker's
	// hook must come back out with the fields ox does not model. A string
	// keeps Hook comparable with ==.
	extra string
}

// UnmarshalJSON reads command and type and keeps all other fields.
func (h *Hook) UnmarshalJSON(data []byte) error {
	var known struct {
		Command string `json:"command"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal(data, &known); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	delete(fields, "command")
	delete(fields, "type")
	extra := ""
	if len(fields) > 0 {
		b, err := marshalNoHTMLEscape(fields)
		if err != nil {
			return err
		}
		extra = string(b)
	}
	*h = Hook{Command: known.Command, Type: known.Type, extra: extra}
	return nil
}

// MarshalJSON writes command and type together with the fields UnmarshalJSON
// kept.
func (h Hook) MarshalJSON() ([]byte, error) {
	fields := make(map[string]json.RawMessage)
	if h.extra != "" {
		if err := json.Unmarshal([]byte(h.extra), &fields); err != nil {
			return nil, err
		}
	}
	command, err := marshalNoHTMLEscape(h.Command)
	if err != nil {
		return nil, err
	}
	hookType, err := marshalNoHTMLEscape(h.Type)
	if err != nil {
		return nil, err
	}
	fields["command"] = command
	fields["type"] = hookType
	return marshalNoHTMLEscape(fields)
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
