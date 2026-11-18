//go:build !windows

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestWrappedEnv_AppKeepsItsColor: ox forces NO_COLOR=1 on itself in some
// contexts. Failure prevented: the wrapped app (Codex) inheriting it and
// drawing with no color at all, losing its shaded input band and highlights.
func TestWrappedEnv_AppKeepsItsColor(t *testing.T) {
	prevVal, prevHas := statuslineNoColor, statuslineHasNoC
	t.Cleanup(func() { statuslineNoColor, statuslineHasNoC = prevVal, prevHas })

	statuslineNoColor, statuslineHasNoC = "", false
	assert.Equal(t, []string{"TERM=xterm", "HOME=/h"}, wrappedEnv([]string{"TERM=xterm", "NO_COLOR=1", "HOME=/h"}),
		"the coworker never set NO_COLOR: ox's own is dropped")

	statuslineNoColor, statuslineHasNoC = "1", true
	assert.Equal(t, []string{"TERM=xterm", "NO_COLOR=1"}, wrappedEnv([]string{"NO_COLOR=1", "TERM=xterm"}),
		"the coworker did set it: the app honors it too")
}
