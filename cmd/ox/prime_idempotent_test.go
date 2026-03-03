//go:build !short

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIdempotentPrimeFlag(t *testing.T) {
	t.Run("idempotent flag is registered", func(t *testing.T) {
		// verify the flag exists on the command
		flag := agentPrimeCmd.Flags().Lookup("idempotent")
		assert.NotNil(t, flag, "idempotent flag should be registered")
		assert.Equal(t, "false", flag.DefValue, "default should be false")
	})
}

func TestMergeHookEntries(t *testing.T) {
	t.Run("adds new entries when none exist", func(t *testing.T) {
		existing := []ClaudeHookEntry{}
		new := []ClaudeHookEntry{
			{Matcher: "startup", Hooks: []ClaudeHook{{Command: "ox prime", Type: "command"}}},
		}
		result := mergeHookEntries(existing, new)
		assert.Len(t, result, 1)
		assert.Equal(t, "startup", result[0].Matcher)
	})

	t.Run("preserves non-ox hooks", func(t *testing.T) {
		existing := []ClaudeHookEntry{
			{
				Matcher: "startup",
				Hooks: []ClaudeHook{
					{Command: "echo 'other tool'", Type: "command"},
				},
			},
		}
		new := []ClaudeHookEntry{
			{Matcher: "startup", Hooks: []ClaudeHook{{Command: "ox agent prime", Type: "command"}}},
		}
		result := mergeHookEntries(existing, new)

		assert.Len(t, result, 1)
		assert.Len(t, result[0].Hooks, 2, "should have both hooks")

		// verify both hooks are present
		commands := make([]string, 0)
		for _, hook := range result[0].Hooks {
			commands = append(commands, hook.Command)
		}
		assert.Contains(t, commands, "echo 'other tool'")
		assert.Contains(t, commands, "ox agent prime")
	})

	t.Run("replaces existing ox hooks", func(t *testing.T) {
		existing := []ClaudeHookEntry{
			{
				Matcher: "startup",
				Hooks: []ClaudeHook{
					{Command: "ox agent prime --old", Type: "command"},
				},
			},
		}
		new := []ClaudeHookEntry{
			{Matcher: "startup", Hooks: []ClaudeHook{{Command: "ox agent prime --new", Type: "command"}}},
		}
		result := mergeHookEntries(existing, new)

		assert.Len(t, result, 1)
		assert.Len(t, result[0].Hooks, 1)
		assert.Equal(t, "ox agent prime --new", result[0].Hooks[0].Command)
	})

	t.Run("handles multiple matchers", func(t *testing.T) {
		existing := []ClaudeHookEntry{
			{Matcher: "startup", Hooks: []ClaudeHook{{Command: "existing startup", Type: "command"}}},
		}
		new := []ClaudeHookEntry{
			{Matcher: "startup", Hooks: []ClaudeHook{{Command: "ox prime startup", Type: "command"}}},
			{Matcher: "clear", Hooks: []ClaudeHook{{Command: "ox prime clear", Type: "command"}}},
		}
		result := mergeHookEntries(existing, new)

		assert.Len(t, result, 2, "should have both matchers")

		// find matchers
		matchers := make(map[string]bool)
		for _, entry := range result {
			matchers[entry.Matcher] = true
		}
		assert.True(t, matchers["startup"])
		assert.True(t, matchers["clear"])
	})

	t.Run("preserves entries with no new match", func(t *testing.T) {
		existing := []ClaudeHookEntry{
			{Matcher: "custom", Hooks: []ClaudeHook{{Command: "custom hook", Type: "command"}}},
		}
		new := []ClaudeHookEntry{
			{Matcher: "startup", Hooks: []ClaudeHook{{Command: "ox prime", Type: "command"}}},
		}
		result := mergeHookEntries(existing, new)

		assert.Len(t, result, 2, "should preserve both entries")

		matchers := make(map[string]bool)
		for _, entry := range result {
			matchers[entry.Matcher] = true
		}
		assert.True(t, matchers["custom"])
		assert.True(t, matchers["startup"])
	})
}

func TestAgentHookInput(t *testing.T) {
	t.Run("struct fields are correct", func(t *testing.T) {
		input := AgentHookInput{
			SessionID:     "test-session-123",
			HookEventName: "SessionStart",
		}
		assert.Equal(t, "test-session-123", input.SessionID)
		assert.Equal(t, "SessionStart", input.HookEventName)
	})
}
