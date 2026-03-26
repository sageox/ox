package claude

// HasOxPrimeHook checks if an entry contains an ox agent prime hook.
func HasOxPrimeHook(entry HookEntry) bool {
	for _, hook := range entry.Hooks {
		if hook.Type == HookType && IsOxPrimeCommand(hook.Command) {
			return true
		}
	}
	return false
}

// HasAnyOxHook checks if an entry contains any ox hook command (prime or lifecycle).
func HasAnyOxHook(entry HookEntry) bool {
	for _, hook := range entry.Hooks {
		if hook.Type == HookType && IsAnyOxCommand(hook.Command) {
			return true
		}
	}
	return false
}

// RemoveOxPrimeHook removes all ox agent prime hooks from an entry,
// preserving hooks with non-command types.
func RemoveOxPrimeHook(entry *HookEntry) {
	filtered := make([]Hook, 0)
	for _, hook := range entry.Hooks {
		if !IsOxPrimeCommand(hook.Command) || hook.Type != HookType {
			filtered = append(filtered, hook)
		}
	}
	entry.Hooks = filtered
}

// RemoveAnyOxHook removes all ox commands (prime and lifecycle) from an entry,
// preserving hooks with non-command types.
func RemoveAnyOxHook(entry *HookEntry) {
	filtered := make([]Hook, 0)
	for _, hook := range entry.Hooks {
		if !IsAnyOxCommand(hook.Command) || hook.Type != HookType {
			filtered = append(filtered, hook)
		}
	}
	entry.Hooks = filtered
}

// MergeHookEntries merges new hook entries with existing ones.
// Preserves existing non-ox hooks while updating/adding ox hooks.
// Strips both old (ox agent prime) and new (ox agent hook) commands during merge.
func MergeHookEntries(existing, new []HookEntry) []HookEntry {
	// build map of new entries by matcher
	newByMatcher := make(map[string]HookEntry)
	for _, entry := range new {
		newByMatcher[entry.Matcher] = entry
	}

	// track which matchers we've handled
	handled := make(map[string]bool)

	// process existing entries: update ox hooks, preserve others
	result := make([]HookEntry, 0, len(existing)+len(new))
	for _, entry := range existing {
		if newEntry, hasNew := newByMatcher[entry.Matcher]; hasNew {
			// matcher exists in new - merge hooks
			mergedHooks := make([]Hook, 0)
			// add non-ox hooks from existing (strip both old and new ox commands)
			for _, hook := range entry.Hooks {
				if !IsAnyOxCommand(hook.Command) {
					mergedHooks = append(mergedHooks, hook)
				}
			}
			// add ox hooks from new
			mergedHooks = append(mergedHooks, newEntry.Hooks...)
			result = append(result, HookEntry{
				Matcher: entry.Matcher,
				Hooks:   mergedHooks,
			})
			handled[entry.Matcher] = true
		} else {
			// check if this is an old ox-only entry with a specific matcher
			// (e.g., old "startup", "resume", "clear", "compact" matchers)
			// If it only contains ox commands, skip it entirely (superseded by new format)
			hasNonOx := false
			for _, hook := range entry.Hooks {
				if !IsAnyOxCommand(hook.Command) {
					hasNonOx = true
					break
				}
			}
			if hasNonOx {
				result = append(result, entry)
			}
			// else: pure ox entry with old matcher — drop it (superseded)
		}
	}

	// add new entries that weren't handled
	for _, entry := range new {
		if !handled[entry.Matcher] {
			result = append(result, entry)
		}
	}

	return result
}
