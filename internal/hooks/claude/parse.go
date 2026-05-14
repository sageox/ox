package claude

import (
	"bytes"
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
// the raw map to preserve all non-hook keys. Returns indented JSON with a
// trailing newline.
//
// Uses json.Encoder with SetEscapeHTML(false) so that literal "<", ">", "&"
// inside user-authored values (e.g. permission-rule globs containing HTML-like
// substrings) survive the round-trip without being rewritten as <, >,
// & on every doctor pass. The default json.Marshal / json.MarshalIndent
// path HTML-escapes those bytes, which made every read-modify-write cycle
// mutate user content and triggered perpetual rewrites under the autofix
// scheduler.
//
// Note: encoding/json's Unmarshal still decodes any source \uXXXX escapes into
// literal runes on the way in, so a file authored with " " will come back
// out as the literal NBSP. That drift is one-way and cannot be prevented at
// the encoder level — callers comparing to disk must use
// SettingsSemanticallyEqual rather than byte equality.
func MarshalSettings(settings *Settings, rawMap map[string]json.RawMessage) ([]byte, error) {
	if rawMap == nil {
		rawMap = make(map[string]json.RawMessage)
	}

	if len(settings.Hooks) > 0 {
		hooksJSON, err := marshalNoHTMLEscape(settings.Hooks)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal hooks: %w", err)
		}
		rawMap["hooks"] = hooksJSON
	} else {
		delete(rawMap, "hooks")
	}

	data, err := marshalIndentedNoHTMLEscape(rawMap, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal settings: %w", err)
	}

	return data, nil
}

// marshalNoHTMLEscape encodes v as compact JSON with HTML escaping disabled.
// The trailing newline that json.Encoder always appends is stripped so callers
// can embed the result inside larger JSON values (e.g. as a json.RawMessage).
func marshalNoHTMLEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// marshalIndentedNoHTMLEscape encodes v as indented JSON with HTML escaping
// disabled. Preserves the trailing newline json.Encoder emits — POSIX-friendly
// and matches what most editors leave on the file.
func marshalIndentedNoHTMLEscape(v any, prefix, indent string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent(prefix, indent)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// IsCanonicalHooksFormat reports whether the on-disk bytes already parse
// strictly into Claude Code's expected shape: every value under "hooks" is an
// array of hook entries (never a bare string). The legacy string-form
// ("PostToolUse": "echo cmd") that older ox versions emitted still parses
// via parseHooksMixed's fallback, but Claude Code itself rejects it — so
// "in-memory equivalent" is not enough; the bytes must be strictly canonical.
//
// Empty or absent hooks is trivially canonical. An unparseable file is
// reported as non-canonical (with the parse error), so callers can decide
// whether to rewrite or warn.
func IsCanonicalHooksFormat(data []byte) (bool, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return true, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false, err
	}
	hooksRaw, ok := raw["hooks"]
	if !ok {
		return true, nil
	}
	var arrayShape map[string][]HookEntry
	if err := json.Unmarshal(hooksRaw, &arrayShape); err != nil {
		return false, nil
	}
	return true, nil
}

// SettingsSemanticallyEqual reports whether two settings byte streams parse
// into the same logical content: identical typed hooks structure plus
// identical non-hook keys (compared by canonical-compact JSON of each value,
// so whitespace/indentation inside opaque blocks like "permissions" is
// ignored). Both inputs are tolerated as empty/nil.
//
// This is the right comparison for "did anything actually change?" checks in
// the autofix and doctor paths. Byte equality is too strict — it catches
// encoder-cosmetic drift (HTML escaping, trailing newline, key order,
// indentation choices) and reports false positives that cause the file to be
// rewritten forever.
func SettingsSemanticallyEqual(a, b []byte) (bool, error) {
	settingsA, rawA, err := ParseSettingsRaw(a)
	if err != nil {
		return false, fmt.Errorf("parse a: %w", err)
	}
	settingsB, rawB, err := ParseSettingsRaw(b)
	if err != nil {
		return false, fmt.Errorf("parse b: %w", err)
	}

	if !hooksEqual(settingsA.Hooks, settingsB.Hooks) {
		return false, nil
	}

	return rawMapEqual(rawA, rawB)
}

// hooksEqual deep-compares two parsed hook maps. We can't rely on
// reflect.DeepEqual on the maps directly because empty slices vs nil and
// missing keys (with empty value) should both compare equal.
func hooksEqual(a, b map[string][]HookEntry) bool {
	keys := make(map[string]struct{}, len(a)+len(b))
	for k, v := range a {
		if len(v) > 0 {
			keys[k] = struct{}{}
		}
	}
	for k, v := range b {
		if len(v) > 0 {
			keys[k] = struct{}{}
		}
	}
	for k := range keys {
		entriesA, entriesB := a[k], b[k]
		if len(entriesA) != len(entriesB) {
			return false
		}
		for i := range entriesA {
			if entriesA[i].Matcher != entriesB[i].Matcher {
				return false
			}
			if len(entriesA[i].Hooks) != len(entriesB[i].Hooks) {
				return false
			}
			for j := range entriesA[i].Hooks {
				if entriesA[i].Hooks[j] != entriesB[i].Hooks[j] {
					return false
				}
			}
		}
	}
	return true
}

// rawMapEqual compares two raw-key maps by canonical-compact bytes for every
// non-hook key. The "hooks" key is skipped — its semantic content was already
// compared via hooksEqual on the typed view, so any byte difference here is
// purely cosmetic (key order, indentation, HTML escaping inside hook commands).
func rawMapEqual(a, b map[string]json.RawMessage) (bool, error) {
	keys := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		if k != "hooks" {
			keys[k] = struct{}{}
		}
	}
	for k := range b {
		if k != "hooks" {
			keys[k] = struct{}{}
		}
	}
	for k := range keys {
		va, oka := a[k]
		vb, okb := b[k]
		if oka != okb {
			return false, nil
		}
		canonA, err := compactJSON(va)
		if err != nil {
			return false, fmt.Errorf("compact a[%q]: %w", k, err)
		}
		canonB, err := compactJSON(vb)
		if err != nil {
			return false, fmt.Errorf("compact b[%q]: %w", k, err)
		}
		if !bytes.Equal(canonA, canonB) {
			return false, nil
		}
	}
	return true, nil
}

// compactJSON returns the canonical-compact form of a raw JSON value. Used
// to compare two arbitrary JSON values ignoring whitespace differences.
func compactJSON(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
