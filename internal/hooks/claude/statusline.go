package claude

import (
	"bytes"
	"encoding/json"

	"github.com/sageox/ox/internal/constants"
)

// StatusLineKey is the top-level settings.json key for Claude Code's status line.
const StatusLineKey = "statusLine"

// StatusLineState is how a settings file's statusLine relates to ox's.
type StatusLineState int

const (
	// StatusLineMissing means the file sets no statusLine.
	StatusLineMissing StatusLineState = iota
	// StatusLineCurrent means the statusLine is ox's, exactly as ox installs it.
	StatusLineCurrent
	// StatusLineStale means the statusLine runs ox but differs from what ox
	// installs now (an older command, a different refresh interval).
	StatusLineStale
	// StatusLineForeign means the statusLine belongs to someone else. ox never
	// replaces it.
	StatusLineForeign
)

// String names the state for logs and doctor output.
func (s StatusLineState) String() string {
	switch s {
	case StatusLineMissing:
		return "missing"
	case StatusLineCurrent:
		return "current"
	case StatusLineStale:
		return "stale"
	default:
		return "foreign"
	}
}

// statusLineFields is the part of a statusLine object ox owns. Other fields
// (padding, …) are left as the coworker set them.
type statusLineFields struct {
	Type            string `json:"type"`
	Command         string `json:"command"`
	RefreshInterval int    `json:"refreshInterval"`
}

// ClassifyStatusLine reports the state of rawMap's statusLine. A value that is
// not an object is foreign: ox cannot tell what it is, so it leaves it alone.
func ClassifyStatusLine(rawMap map[string]json.RawMessage) StatusLineState {
	raw, ok := rawMap[StatusLineKey]
	if !ok || len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return StatusLineMissing
	}
	var f statusLineFields
	if err := json.Unmarshal(raw, &f); err != nil {
		return StatusLineForeign
	}
	if !IsOxStatuslineCommand(f.Command) {
		return StatusLineForeign
	}
	if f.Type == HookType && f.Command == constants.OxStatuslineCommandClaudeCode &&
		f.RefreshInterval == constants.OxStatuslineRefreshSeconds {
		return StatusLineCurrent
	}
	return StatusLineStale
}

// EnsureOxStatusLine installs ox's statusLine in rawMap when it is missing or
// stale, and reports whether it changed anything. A foreign statusLine is never
// touched.
func EnsureOxStatusLine(rawMap map[string]json.RawMessage) (bool, error) {
	state := ClassifyStatusLine(rawMap)
	if state == StatusLineCurrent || state == StatusLineForeign {
		return false, nil
	}
	obj := map[string]json.RawMessage{}
	if state == StatusLineStale {
		// keep fields ox does not own; the value is an object or it would be foreign
		_ = json.Unmarshal(rawMap[StatusLineKey], &obj)
	}
	set := func(key string, v any) error {
		b, err := marshalNoHTMLEscape(v)
		if err != nil {
			return err
		}
		obj[key] = b
		return nil
	}
	if err := set("type", HookType); err != nil {
		return false, err
	}
	if err := set("command", constants.OxStatuslineCommandClaudeCode); err != nil {
		return false, err
	}
	if err := set("refreshInterval", constants.OxStatuslineRefreshSeconds); err != nil {
		return false, err
	}
	b, err := marshalNoHTMLEscape(obj)
	if err != nil {
		return false, err
	}
	rawMap[StatusLineKey] = b
	return true, nil
}

// RemoveOxStatusLine deletes rawMap's statusLine when it is ox's, and reports
// whether it did.
func RemoveOxStatusLine(rawMap map[string]json.RawMessage) bool {
	switch ClassifyStatusLine(rawMap) {
	case StatusLineCurrent, StatusLineStale:
		delete(rawMap, StatusLineKey)
		return true
	}
	return false
}
