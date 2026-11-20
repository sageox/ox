package dashboard

import (
	"reflect"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/dashboard/app"
)

// TestEveryGlobalKeyBindingAppearsInHelpView keeps the help view generated
// from the bindings the dashboard dispatches (DDR-051: every shortcut appears
// in a help list generated from the table that binds it).
// Failure prevented: a key is bound in app.GlobalKeyMap, or relabeled, while
// the `?` view keeps a hand-written row, so the key ships undiscoverable or
// described wrongly. Reflection means a new field cannot skip this check.
func TestEveryGlobalKeyBindingAppearsInHelpView(t *testing.T) {
	gk := app.DefaultGlobalKeys()
	rows := helpViewRows(newHelpOverlay(gk, app.DefaultPaneKeys()).View(120, 60))

	fields := reflect.ValueOf(gk)
	require.Positive(t, fields.NumField(), "GlobalKeyMap has no fields; nothing was checked")
	bindingType := reflect.TypeOf(key.Binding{})
	for i := 0; i < fields.NumField(); i++ {
		field := fields.Type().Field(i)
		require.Equal(t, bindingType, field.Type, "GlobalKeyMap.%s is not a key.Binding; teach this check its shape", field.Name)

		help := fields.Field(i).Interface().(key.Binding).Help()
		if !assert.NotEmpty(t, help.Key, "GlobalKeyMap.%s has no help key", field.Name) ||
			!assert.NotEmpty(t, help.Desc, "GlobalKeyMap.%s has no help description", field.Name) {
			continue
		}
		assert.Contains(t, rows, help.Key+" "+help.Desc,
			"GlobalKeyMap.%s is bound but the help view has no %q row", field.Name, help.Key+" "+help.Desc)
	}
}

// TestHelpViewFitsAStandardTerminal keeps the `?` modal readable on the
// smallest common terminal. Failure prevented: listing every binding pushes the
// modal past 24 rows, so its top and bottom are cut off on an 80x24 terminal.
func TestHelpViewFitsAStandardTerminal(t *testing.T) {
	const terminalRows = 24
	view := newHelpOverlay(app.DefaultGlobalKeys(), app.DefaultPaneKeys()).View(80, terminalRows)
	var modalRows int
	for _, line := range strings.Split(ansi.Strip(view), "\n") {
		if strings.TrimSpace(line) != "" {
			modalRows++
		}
	}
	require.Positive(t, modalRows, "the help view rendered nothing")
	assert.LessOrEqual(t, modalRows, terminalRows, "the help modal is taller than an 80x24 terminal")
}

// helpViewRows reduces the rendered modal to one "key desc" string per line:
// color codes and box borders stripped, padding collapsed to single spaces.
func helpViewRows(view string) []string {
	var rows []string
	for _, line := range strings.Split(ansi.Strip(view), "\n") {
		row := strings.Join(strings.Fields(strings.Trim(line, "│╭╮╰╯─ ")), " ")
		if row != "" {
			rows = append(rows, row)
		}
	}
	return rows
}
