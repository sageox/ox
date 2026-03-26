package claude

import (
	"testing"

	"github.com/sageox/ox/internal/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasOxPrimeHook(t *testing.T) {
	tests := []struct {
		name  string
		entry HookEntry
		want  bool
	}{
		{
			"has prime hook",
			HookEntry{Hooks: []Hook{
				{Command: constants.OxPrimeCommandClaudeCode, Type: HookType},
			}},
			true,
		},
		{
			"has legacy prime hook",
			HookEntry{Hooks: []Hook{
				{Command: constants.OxPrimeCommand, Type: HookType},
			}},
			true,
		},
		{
			"has hook command but not prime",
			HookEntry{Hooks: []Hook{
				{Command: "ox agent hook SessionStart", Type: HookType},
			}},
			false,
		},
		{
			"wrong type ignored",
			HookEntry{Hooks: []Hook{
				{Command: constants.OxPrimeCommandClaudeCode, Type: "notification"},
			}},
			false,
		},
		{
			"empty hooks",
			HookEntry{Hooks: []Hook{}},
			false,
		},
		{
			"nil hooks",
			HookEntry{},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, HasOxPrimeHook(tt.entry))
		})
	}
}

func TestHasAnyOxHook(t *testing.T) {
	tests := []struct {
		name  string
		entry HookEntry
		want  bool
	}{
		{
			"prime hook counts",
			HookEntry{Hooks: []Hook{
				{Command: constants.OxPrimeCommandClaudeCode, Type: HookType},
			}},
			true,
		},
		{
			"hook command counts",
			HookEntry{Hooks: []Hook{
				{Command: "ox agent hook Stop", Type: HookType},
			}},
			true,
		},
		{
			"non-ox command",
			HookEntry{Hooks: []Hook{
				{Command: "echo hello", Type: HookType},
			}},
			false,
		},
		{
			"empty hooks",
			HookEntry{Hooks: []Hook{}},
			false,
		},
		{
			"nil hooks",
			HookEntry{},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, HasAnyOxHook(tt.entry))
		})
	}
}

func TestRemoveOxPrimeHook(t *testing.T) {
	entry := HookEntry{
		Hooks: []Hook{
			{Command: "echo before", Type: HookType},
			{Command: constants.OxPrimeCommandClaudeCode, Type: HookType},
			{Command: constants.OxPrimeCommand, Type: HookType},
			{Command: "echo after", Type: HookType},
		},
	}
	RemoveOxPrimeHook(&entry)

	assert.Len(t, entry.Hooks, 2)
	assert.Equal(t, "echo before", entry.Hooks[0].Command)
	assert.Equal(t, "echo after", entry.Hooks[1].Command)
}

func TestRemoveOxPrimeHook_PreservesNonCommandType(t *testing.T) {
	entry := HookEntry{
		Hooks: []Hook{
			{Command: constants.OxPrimeCommandClaudeCode, Type: "notification"},
		},
	}
	RemoveOxPrimeHook(&entry)
	assert.Len(t, entry.Hooks, 1)
}

func TestRemoveOxPrimeHook_EmptyEntry(t *testing.T) {
	entry := HookEntry{Hooks: []Hook{}}
	RemoveOxPrimeHook(&entry)
	assert.Empty(t, entry.Hooks)
}

func TestRemoveAnyOxHook(t *testing.T) {
	entry := HookEntry{
		Hooks: []Hook{
			{Command: "echo user-hook", Type: HookType},
			{Command: constants.OxPrimeCommandClaudeCode, Type: HookType},
			{Command: "ox agent hook SessionStart", Type: HookType},
			{Command: "echo another", Type: HookType},
		},
	}
	RemoveAnyOxHook(&entry)

	assert.Len(t, entry.Hooks, 2)
	assert.Equal(t, "echo user-hook", entry.Hooks[0].Command)
	assert.Equal(t, "echo another", entry.Hooks[1].Command)
}

func TestRemoveAnyOxHook_PreservesNonCommandType(t *testing.T) {
	entry := HookEntry{
		Hooks: []Hook{
			{Command: "ox agent hook Stop", Type: "notification"},
		},
	}
	RemoveAnyOxHook(&entry)
	assert.Len(t, entry.Hooks, 1)
}

func TestRemoveAnyOxHook_EmptyEntry(t *testing.T) {
	entry := HookEntry{Hooks: []Hook{}}
	RemoveAnyOxHook(&entry)
	assert.Empty(t, entry.Hooks)
}

func TestRemoveAnyOxHook_NilHooks(t *testing.T) {
	entry := HookEntry{}
	RemoveAnyOxHook(&entry)
	assert.Empty(t, entry.Hooks)
}

func TestMergeHookEntries_EmptyExisting(t *testing.T) {
	newEntries := []HookEntry{{
		Matcher: "",
		Hooks:   []Hook{{Command: "ox agent hook SessionStart", Type: HookType}},
	}}

	result := MergeHookEntries(nil, newEntries)
	require.Len(t, result, 1)
	assert.Equal(t, "", result[0].Matcher)
	assert.Len(t, result[0].Hooks, 1)
}

func TestMergeHookEntries_PreservesUserHooksSameMatcher(t *testing.T) {
	existing := []HookEntry{{
		Matcher: "",
		Hooks: []Hook{
			{Command: "echo user-hook", Type: HookType},
			{Command: constants.OxPrimeCommand, Type: HookType},
		},
	}}
	newEntries := []HookEntry{{
		Matcher: "",
		Hooks:   []Hook{{Command: "ox agent hook SessionStart", Type: HookType}},
	}}

	result := MergeHookEntries(existing, newEntries)
	require.Len(t, result, 1)

	commands := make([]string, len(result[0].Hooks))
	for i, h := range result[0].Hooks {
		commands[i] = h.Command
	}
	assert.Contains(t, commands, "echo user-hook")
	assert.Contains(t, commands, "ox agent hook SessionStart")
	assert.NotContains(t, commands, constants.OxPrimeCommand)
}

func TestMergeHookEntries_DropsOldPureOxEntryWithDifferentMatcher(t *testing.T) {
	existing := []HookEntry{
		{
			Matcher: "startup",
			Hooks:   []Hook{{Command: constants.OxPrimeCommand, Type: HookType}},
		},
		{
			Matcher: "user-matcher",
			Hooks:   []Hook{{Command: "echo user-stuff", Type: HookType}},
		},
	}
	newEntries := []HookEntry{{
		Matcher: "",
		Hooks:   []Hook{{Command: "ox agent hook SessionStart", Type: HookType}},
	}}

	result := MergeHookEntries(existing, newEntries)

	assert.Len(t, result, 2)
	matchers := make([]string, len(result))
	for i, e := range result {
		matchers[i] = e.Matcher
	}
	assert.Contains(t, matchers, "user-matcher")
	assert.Contains(t, matchers, "")
	assert.NotContains(t, matchers, "startup")
}

func TestMergeHookEntries_MixedOldMatcherWithUserHooksKept(t *testing.T) {
	existing := []HookEntry{{
		Matcher: "compact",
		Hooks: []Hook{
			{Command: constants.OxPrimeCommand, Type: HookType},
			{Command: "echo user-compact-hook", Type: HookType},
		},
	}}
	newEntries := []HookEntry{{
		Matcher: "",
		Hooks:   []Hook{{Command: "ox agent hook PreCompact", Type: HookType}},
	}}

	result := MergeHookEntries(existing, newEntries)
	assert.Len(t, result, 2)
}

func TestMergeHookEntries_BothEmpty(t *testing.T) {
	result := MergeHookEntries(nil, nil)
	assert.Empty(t, result)
}

func TestMergeHookEntries_EmptyNew(t *testing.T) {
	existing := []HookEntry{{
		Matcher: "",
		Hooks:   []Hook{{Command: "echo user", Type: HookType}},
	}}
	result := MergeHookEntries(existing, nil)
	assert.Len(t, result, 1)
	assert.Equal(t, "echo user", result[0].Hooks[0].Command)
}
