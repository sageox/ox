package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMainView_ViewportClampingKeepsCursorVisible(t *testing.T) {
	dir := setupIsolatedUserConfig(t)
	m, err := newConfigModel(dir)
	require.NoError(t, err)
	require.Greater(t, len(m.displayOrder), 15, "need realistic number of settings")

	m.height = 24
	m.width = 80

	testCursors := []struct {
		name         string
		cursor       int
		wantUpMore   bool
		wantDownMore bool
	}{
		{"top of list", 0, false, true},
		{"middle of list", len(m.displayOrder) / 2, true, true},
		{"bottom of list", len(m.displayOrder) - 1, true, false},
	}

	for _, tc := range testCursors {
		t.Run(tc.name, func(t *testing.T) {
			m.cursor = tc.cursor
			view := m.mainView(80)
			lines := strings.Split(view, "\n")

			// Clamped height must not exceed terminal height
			assert.LessOrEqual(t, len(lines), 24, "mainView lines should not exceed m.height")

			// Cursor must be visible in the output lines
			hasCursor := false
			for _, line := range lines {
				if strings.Contains(line, "> ") {
					hasCursor = true
					break
				}
			}
			assert.True(t, hasCursor, "active cursor '> ' must be visible within viewport")

			// Check scroll indicators
			if tc.wantUpMore {
				assert.Contains(t, view, "↑ more", "expected scroll up indicator")
			}
			if tc.wantDownMore {
				assert.Contains(t, view, "↓ more", "expected scroll down indicator")
			}
		})
	}
}

func TestEditView_TelemetryRadioOptionsNearTop(t *testing.T) {
	dir := setupIsolatedUserConfig(t)
	m, err := newConfigModel(dir)
	require.NoError(t, err)

	// Locate telemetry setting
	telemetryCursor := -1
	for pos, itemIdx := range m.displayOrder {
		if m.items[itemIdx].setting.Key == "telemetry" {
			telemetryCursor = pos
			break
		}
	}
	require.GreaterOrEqual(t, telemetryCursor, 0, "telemetry setting must exist")

	m.cursor = telemetryCursor
	m.editing = true
	m.height = 24
	m.width = 80

	view := m.editView(80)
	lines := strings.Split(view, "\n")

	// Viewport height must not exceed 24 rows
	assert.LessOrEqual(t, len(lines), 24, "editView lines should fit within 24 rows")

	// Radio options must appear near top (within first 12 rows)
	radioLineIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "●") || strings.Contains(line, "○") {
			radioLineIdx = i
			break
		}
	}
	require.GreaterOrEqual(t, radioLineIdx, 0, "radio button must be present in editView")
	assert.LessOrEqual(t, radioLineIdx, 12, "radio button must appear within the first 12 rows, found at row %d", radioLineIdx)

	// Save and cancel hints must be visible
	assert.Contains(t, view, "enter save", "key hints must be visible in editView")
}
