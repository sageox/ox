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
			return nil, nil, fmt.Errorf("failed to parse hooks: %w", err)
		}
	}
	if settings.Hooks == nil {
		settings.Hooks = make(map[string][]HookEntry)
	}

	return &settings, rawMap, nil
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
