package claude

import (
	"encoding/json"
	"fmt"
)

// ParseSettingsRaw parses raw JSON bytes into typed Settings and a raw map of
// all top-level keys. The raw map preserves non-hook keys (e.g., "permissions")
// for lossless round-tripping.
func ParseSettingsRaw(data []byte) (*Settings, map[string]json.RawMessage, error) {
	if len(data) == 0 {
		return &Settings{
			Hooks: make(map[string][]HookEntry),
		}, make(map[string]json.RawMessage), nil
	}

	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return nil, nil, fmt.Errorf("failed to parse settings: %w", err)
	}

	var settings Settings
	if hooksRaw, ok := rawMap["hooks"]; ok {
		if err := json.Unmarshal(hooksRaw, &settings.Hooks); err != nil {
			// fallback: some settings files use old string-format hooks (e.g. "PostToolUse": "cmd")
			// instead of the array format ("PostToolUse": [{hooks: [{command, type}]}]).
			// Try parsing each event value individually to handle mixed formats.
			mixed, mixedErr := parseHooksMixed(hooksRaw)
			if mixedErr != nil {
				return nil, nil, fmt.Errorf("failed to parse hooks: %w", err)
			}
			settings.Hooks = mixed
		}
	}
	if settings.Hooks == nil {
		settings.Hooks = make(map[string][]HookEntry)
	}

	return &settings, rawMap, nil
}

// parseHooksMixed handles settings files where hook event values are either
// the current array format ([]HookEntry) or the legacy string format ("command").
// Legacy entries are promoted to a single-element HookEntry with no matcher.
func parseHooksMixed(hooksRaw json.RawMessage) (map[string][]HookEntry, error) {
	var rawEvents map[string]json.RawMessage
	if err := json.Unmarshal(hooksRaw, &rawEvents); err != nil {
		return nil, err
	}
	result := make(map[string][]HookEntry, len(rawEvents))
	for event, val := range rawEvents {
		// try array format first
		var entries []HookEntry
		if err := json.Unmarshal(val, &entries); err == nil {
			result[event] = entries
			continue
		}
		// fall back to legacy string format: "command string"
		var cmd string
		if err := json.Unmarshal(val, &cmd); err != nil {
			return nil, fmt.Errorf("hook event %q: unsupported format", event)
		}
		result[event] = []HookEntry{{
			Hooks: []Hook{{Command: cmd, Type: HookType}},
		}}
	}
	return result, nil
}

// MarshalSettings serializes Settings back into JSON, merging typed hooks into
// the raw map to preserve all non-hook keys. Returns indented JSON.
func MarshalSettings(settings *Settings, rawMap map[string]json.RawMessage) ([]byte, error) {
	if rawMap == nil {
		rawMap = make(map[string]json.RawMessage)
	}

	if len(settings.Hooks) > 0 {
		hooksJSON, err := json.Marshal(settings.Hooks)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal hooks: %w", err)
		}
		rawMap["hooks"] = hooksJSON
	} else {
		delete(rawMap, "hooks")
	}

	data, err := json.MarshalIndent(rawMap, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal settings: %w", err)
	}

	return data, nil
}
