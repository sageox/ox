package main

import (
	"fmt"
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

	heights := []int{12, 20, 24, 35}
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

	for _, h := range heights {
		for _, tc := range testCursors {
			t.Run(fmt.Sprintf("height=%d/%s", h, tc.name), func(t *testing.T) {
				m.height = h
				m.width = 80
				m.cursor = tc.cursor

				view := m.mainView(80)
				lines := strings.Split(view, "\n")

				// Clamped height must not exceed terminal height
				assert.LessOrEqual(t, len(lines), h, "mainView lines should not exceed m.height")

				// Selected row (cursor + active setting key) must be visible in the output
				selectedKey := m.items[m.displayOrder[tc.cursor]].setting.Key
				hasSelectedRow := false
				for _, line := range lines {
					if strings.Contains(line, "> ") && strings.Contains(line, selectedKey) {
						hasSelectedRow = true
						break
					}
				}
				assert.True(t, hasSelectedRow, "selected row '> %s' must be visible within viewport", selectedKey)

				// Help bar must remain visible even at compact heights
				assert.Contains(t, view, "q quit", "help bar must remain visible")

				// Check scroll indicators when list exceeds available rows
				allLines, _ := m.allSettingsLines(80 - 4)
				maxRows, _ := m.maxSettingsListRows(80-4, m.buildDetailPanel(80-4), 1)
				if maxRows > 0 && len(allLines) > maxRows {
					if tc.wantUpMore {
						assert.Contains(t, view, "↑ more", "expected scroll up indicator")
					}
					if tc.wantDownMore {
						assert.Contains(t, view, "↓ more", "expected scroll down indicator")
					}
				}
			})
		}
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

	// Extended LongDescription should be truncated with "..." to fit 24 rows
	assert.Contains(t, view, "...", "expected truncated LongDescription marker in editView")

	// Save and cancel hints must be visible
	assert.Contains(t, view, "enter save", "key hints must be visible in editView")
}

func TestEditView_MultiScopeSettingsRadioOptionsNearTop(t *testing.T) {
	dir := setupIsolatedUserConfig(t)
	m, err := newConfigModel(dir)
	require.NoError(t, err)

	tested := 0
	for pos, itemIdx := range m.displayOrder {
		setting := m.items[itemIdx].setting
		if len(setting.Levels) <= 1 || len(setting.ValidValues) == 0 {
			continue
		}
		tested++

		t.Run(setting.Key, func(t *testing.T) {
			m.cursor = pos
			m.editing = true
			m.editLevel = setting.Levels[0]
			m.editCursor = 0
			m.height = 24
			m.width = 80

			view := m.editView(80)
			lines := strings.Split(view, "\n")

			assert.LessOrEqual(t, len(lines), 24, "editView lines should fit within 24 rows")
			assert.Contains(t, view, "Scope:", "scope selector must be visible")

			radioLineIdx := -1
			for i, line := range lines {
				if strings.Contains(line, "●") || strings.Contains(line, "○") {
					radioLineIdx = i
					break
				}
			}
			require.GreaterOrEqual(t, radioLineIdx, 0, "radio button must be present in editView")
			assert.LessOrEqual(t, radioLineIdx, 12, "radio button must appear within the first 12 rows, found at row %d", radioLineIdx)
			assert.Contains(t, view, "tab scope", "scope switch hint must be visible")
			assert.Contains(t, view, "enter save", "save hint must be visible")
		})
	}
	require.Greater(t, tested, 0, "expected at least one multi-scope setting with radio options")
}

func TestMainView_CompactFooterWithLongSaveErrorAndLongValue(t *testing.T) {
	dir := setupIsolatedUserConfig(t)
	m, err := newConfigModel(dir)
	require.NoError(t, err)

	// Select attribution.pr (or fall back to last item) and give it a long value that would wrap without truncation
	targetCursor := len(m.displayOrder) - 1
	for pos, itemIdx := range m.displayOrder {
		if m.items[itemIdx].setting.Key == "attribution.pr" {
			targetCursor = pos
			break
		}
	}
	selectedItem := &m.items[m.displayOrder[targetCursor]]
	selectedItem.value.Value = strings.Repeat("Co-Authored-By: SageOx ", 4)

	m.cursor = targetCursor
	m.width = 80
	m.height = 12
	m.saveErr = "failed to write config file: permission denied while updating repository configuration on disk"

	view := m.mainView(80)
	lines := strings.Split(view, "\n")

	assert.LessOrEqual(t, len(lines), 12, "mainView lines should not exceed 12 rows")
	assert.Contains(t, view, "q quit", "navigation hints must remain visible with a long wrapped save error")

	hasSelectedRow := false
	for _, line := range lines {
		if strings.Contains(line, "> ") && strings.Contains(line, selectedItem.setting.Key) {
			hasSelectedRow = true
			break
		}
	}
	assert.True(t, hasSelectedRow, "selected row '> %s' must remain visible", selectedItem.setting.Key)
}
