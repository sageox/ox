package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckRegistry_EmptyRegistry(t *testing.T) {
	t.Parallel()
	registry := NewCheckRegistry()

	assert.Equal(t, 0, registry.Count())
	assert.Empty(t, registry.GetCategories())
	assert.Empty(t, registry.RunAll(false))
	assert.Empty(t, registry.RunCategory("nonexistent", false))
}

func TestCheckRegistry_RunAll_PreservesRegistrationOrder(t *testing.T) {
	t.Parallel()
	registry := NewCheckRegistry()

	// register in specific order: C, A, B
	registry.Register(&mockCheck{name: "c1", category: "C"})
	registry.Register(&mockCheck{name: "a1", category: "A"})
	registry.Register(&mockCheck{name: "b1", category: "B"})

	categories := registry.RunAll(false)
	require.Len(t, categories, 3)

	// RunAll preserves registration order (not alphabetical)
	assert.Equal(t, "C", categories[0].name)
	assert.Equal(t, "A", categories[1].name)
	assert.Equal(t, "B", categories[2].name)
}

func TestCheckRegistry_RunCategory_NonexistentCategory(t *testing.T) {
	t.Parallel()
	registry := NewCheckRegistry()
	registry.Register(&mockCheck{name: "test", category: "real"})

	results := registry.RunCategory("fake", false)
	assert.Empty(t, results)
}

func TestCheckRegistry_GetCategories_Sorted(t *testing.T) {
	t.Parallel()
	registry := NewCheckRegistry()

	registry.Register(&mockCheck{name: "1", category: "Zulu"})
	registry.Register(&mockCheck{name: "2", category: "Alpha"})
	registry.Register(&mockCheck{name: "3", category: "Mike"})

	cats := registry.GetCategories()
	require.Len(t, cats, 3)
	assert.Equal(t, "Alpha", cats[0])
	assert.Equal(t, "Mike", cats[1])
	assert.Equal(t, "Zulu", cats[2])
}

func TestSimpleCheck_Interface(t *testing.T) {
	t.Parallel()

	sc := &simpleCheck{
		name:     "test-name",
		category: "test-cat",
		runFunc: func(fix bool) checkResult {
			if fix {
				return PassedCheck("test-name", "fixed")
			}
			return FailedCheck("test-name", "broken", "")
		},
	}

	assert.Equal(t, "test-name", sc.Name())
	assert.Equal(t, "test-cat", sc.Category())

	result := sc.Run(false)
	assert.False(t, result.passed)

	result = sc.Run(true)
	assert.True(t, result.passed)
}

func TestConditionalCheck_SkipsWhenConditionFalse(t *testing.T) {
	t.Parallel()

	inner := &mockCheck{
		name:     "inner",
		category: "cat",
		runFunc: func(fix bool) checkResult {
			return PassedCheck("inner", "ran")
		},
	}

	cc := &conditionalCheck{
		check:     inner,
		condition: func() bool { return false },
	}

	assert.Equal(t, "inner", cc.Name())
	assert.Equal(t, "cat", cc.Category())

	result := cc.Run(false)
	assert.True(t, result.skipped)
	assert.Contains(t, result.message, "condition not met")
}

func TestConditionalCheck_RunsWhenConditionTrue(t *testing.T) {
	t.Parallel()

	inner := &mockCheck{
		name:     "inner",
		category: "cat",
		runFunc: func(fix bool) checkResult {
			return PassedCheck("inner", "ran successfully")
		},
	}

	cc := &conditionalCheck{
		check:     inner,
		condition: func() bool { return true },
	}

	result := cc.Run(false)
	assert.True(t, result.passed)
	assert.False(t, result.skipped)
	assert.Equal(t, "ran successfully", result.message)
}

func TestConditionalCheck_PassesFixThrough(t *testing.T) {
	t.Parallel()

	receivedFix := false
	inner := &mockCheck{
		name:     "fixable",
		category: "cat",
		runFunc: func(fix bool) checkResult {
			receivedFix = fix
			return PassedCheck("fixable", "ok")
		},
	}

	cc := &conditionalCheck{
		check:     inner,
		condition: func() bool { return true },
	}

	cc.Run(true)
	assert.True(t, receivedFix, "fix parameter should be forwarded to inner check")
}

func TestCheckRegistry_RunAll_MultipleChecksInSameCategory(t *testing.T) {
	t.Parallel()
	registry := NewCheckRegistry()

	registry.Register(&mockCheck{name: "a", category: "Group1"})
	registry.Register(&mockCheck{name: "b", category: "Group1"})
	registry.Register(&mockCheck{name: "c", category: "Group1"})

	categories := registry.RunAll(false)
	require.Len(t, categories, 1)
	assert.Equal(t, "Group1", categories[0].name)
	assert.Len(t, categories[0].checks, 3)
}
