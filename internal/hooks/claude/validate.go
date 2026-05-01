package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// SchemaError is the structured error returned when a settings.json body
// fails the Claude Code parser contract. It carries the offending event /
// index so callers can produce actionable diagnostics, and wraps a sentinel
// (ErrSchemaViolation) so tests can `errors.Is` against the failure mode
// without depending on free-form message text.
type SchemaError struct {
	// Event is the top-level hook event ("SessionStart", "PostToolUse",
	// etc.) that failed validation. May be empty for top-level shape errors.
	Event string
	// Index is the position within Hooks[Event] for entry-level errors
	// (-1 when not applicable).
	Index int
	// Reason is a one-line human-readable explanation suitable for inclusion
	// in doctor / CI output.
	Reason string
}

// ErrSchemaViolation is the sentinel for any settings.json contract failure.
// All SchemaError instances wrap this so callers can check `errors.Is(err,
// ErrSchemaViolation)` without inspecting the concrete type.
var ErrSchemaViolation = errors.New("settings.json schema violation")

func (e *SchemaError) Error() string {
	switch {
	case e.Event != "" && e.Index >= 0:
		return fmt.Sprintf("hooks.%s[%d]: %s", e.Event, e.Index, e.Reason)
	case e.Event != "":
		return fmt.Sprintf("hooks.%s: %s", e.Event, e.Reason)
	default:
		return e.Reason
	}
}

func (e *SchemaError) Unwrap() error { return ErrSchemaViolation }

// ValidateSettingsBytes runs a strict shape validation against the same
// contract Claude Code's parser enforces. Empty input is OK (no hooks
// configured). On any violation it returns *SchemaError wrapping
// ErrSchemaViolation; on a malformed top-level JSON document it returns
// the underlying parse error.
//
// Why this exists separate from ParseSettingsRaw: the lenient parser
// promotes legacy string-format hooks (parseHooksMixed) so callers can
// repair them. ValidateSettingsBytes is the strict consumer's view —
// what Claude Code itself accepts. Doctor and the init contract test
// use this strict view to catch any divergence between what we write and
// what the consumer accepts.
//
// The contract, as of Claude Code's hooks doc:
//
//   - Top-level "hooks" is an object whose keys are event names.
//   - Each event value MUST be an array (NOT a bare string — the legacy
//     form Claude Code rejects).
//   - Each array element is an object with:
//   - "matcher": string (may be empty)
//   - "hooks": array of {"type":"command","command":<non-empty string>}
//
// Unknown top-level keys (e.g., "permissions") are deliberately allowed —
// Claude Code carries other settings alongside hooks and we don't want to
// reject files that simply have keys we don't model.
func ValidateSettingsBytes(data []byte) error {
	if len(data) == 0 {
		return nil
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return fmt.Errorf("parse settings.json: %w", err)
	}

	hooksRaw, ok := top["hooks"]
	if !ok {
		return nil // no hooks section is fine
	}

	var events map[string]json.RawMessage
	if err := json.Unmarshal(hooksRaw, &events); err != nil {
		return &SchemaError{Index: -1, Reason: "hooks must be an object keyed by event name"}
	}

	for event, val := range events {
		// the load-bearing rule: bare-string event values are the
		// historical failure mode that motivated this validator.
		if len(val) > 0 && val[0] == '"' {
			return &SchemaError{Event: event, Index: -1,
				Reason: "value is a bare string; must be an array of {matcher, hooks[]} entries"}
		}
		var entries []json.RawMessage
		if err := json.Unmarshal(val, &entries); err != nil {
			return &SchemaError{Event: event, Index: -1,
				Reason: "value must be an array of {matcher, hooks[]} entries"}
		}
		for i, entryRaw := range entries {
			if err := validateHookEntry(entryRaw); err != nil {
				var se *SchemaError
				if errors.As(err, &se) {
					se.Event = event
					se.Index = i
					return se
				}
				return &SchemaError{Event: event, Index: i, Reason: err.Error()}
			}
		}
	}
	return nil
}

func validateHookEntry(data json.RawMessage) error {
	var entry struct {
		Matcher *string           `json:"matcher"`
		Hooks   []json.RawMessage `json:"hooks"`
	}
	// disallow unknown fields so a typo (e.g., "matchers") is caught here
	// instead of silently producing a hook that never fires.
	dec := json.NewDecoder(newReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&entry); err != nil {
		return &SchemaError{Index: -1, Reason: fmt.Sprintf("entry shape invalid: %v", err)}
	}
	// matcher may be empty/absent; when present it must be a string —
	// already enforced by the *string typing above.
	if len(entry.Hooks) == 0 {
		return &SchemaError{Index: -1, Reason: "entry.hooks must be a non-empty array"}
	}
	for j, hookRaw := range entry.Hooks {
		var h struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		}
		hdec := json.NewDecoder(newReader(hookRaw))
		hdec.DisallowUnknownFields()
		if err := hdec.Decode(&h); err != nil {
			return &SchemaError{Index: -1, Reason: fmt.Sprintf("hooks[%d] shape invalid: %v", j, err)}
		}
		if h.Type != "command" {
			return &SchemaError{Index: -1, Reason: fmt.Sprintf("hooks[%d].type must be \"command\", got %q", j, h.Type)}
		}
		if h.Command == "" {
			return &SchemaError{Index: -1, Reason: fmt.Sprintf("hooks[%d].command must be a non-empty string", j)}
		}
	}
	return nil
}

// newReader returns a fresh bytes.Reader over the raw JSON. Used so each
// json.NewDecoder we create has fresh state without copying the slice.
func newReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
