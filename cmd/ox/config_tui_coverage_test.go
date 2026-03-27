package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWrapText_BasicWrapping(t *testing.T) {
	t.Parallel()

	m := configModel{}

	tests := []struct {
		name  string
		text  string
		width int
		want  string
	}{
		{
			name:  "short text fits in width",
			text:  "hello world",
			width: 40,
			want:  "hello world",
		},
		{
			name:  "long text wraps at word boundary",
			text:  "one two three four five six seven eight",
			width: 15,
			want:  "one two three\nfour five six\nseven eight",
		},
		{
			name:  "zero width uses default 60",
			text:  "short",
			width: 0,
			want:  "short",
		},
		{
			name:  "negative width uses default 60",
			text:  "short",
			width: -5,
			want:  "short",
		},
		{
			name:  "preserves indentation",
			text:  "  indented line here and more words follow",
			width: 20,
			want:  "  indented line here\n  and more words\n  follow",
		},
		{
			name:  "handles multiple newlines in input",
			text:  "line one\nline two",
			width: 40,
			want:  "line one\nline two",
		},
		{
			name:  "empty lines preserved as empty",
			text:  "before\n\nafter",
			width: 40,
			want:  "before\n\nafter",
		},
		{
			name:  "single word longer than width stays on one line",
			text:  "superlongword",
			width: 5,
			want:  "superlongword",
		},
		{
			name:  "empty text",
			text:  "",
			width: 40,
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := m.wrapText(tt.text, tt.width)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatSourceArrow_AllLevels(t *testing.T) {
	t.Parallel()

	m := configModel{}

	tests := []struct {
		source ConfigLevel
		expect string
	}{
		{ConfigLevelUser, "user"},
		{ConfigLevelRepo, "repo"},
		{ConfigLevelTeam, "team"},
		{ConfigLevelDefault, "default"},
		{ConfigLevel("unknown"), "default"},
	}

	for _, tt := range tests {
		t.Run(string(tt.source), func(t *testing.T) {
			t.Parallel()
			result := m.formatSourceArrow(tt.source)
			assert.Contains(t, result, tt.expect)
		})
	}
}

func TestParseValueDescriptions_ParsesPatterns(t *testing.T) {
	t.Parallel()

	m := configModel{}

	t.Run("dash separator", func(t *testing.T) {
		t.Parallel()
		desc := "auto - Recording starts automatically\nmanual - Recording starts on demand"
		vals := []string{"auto", "manual"}
		result := m.parseValueDescriptions(desc, vals)

		assert.Equal(t, "Recording starts automatically", result["auto"])
		assert.Equal(t, "Recording starts on demand", result["manual"])
	})

	t.Run("em dash separator", func(t *testing.T) {
		t.Parallel()
		desc := "enabled \u2014 Turns on the feature"
		vals := []string{"enabled"}
		result := m.parseValueDescriptions(desc, vals)

		assert.Equal(t, "Turns on the feature", result["enabled"])
	})

	t.Run("double space separator", func(t *testing.T) {
		t.Parallel()
		desc := "verbose  Extra detailed output"
		vals := []string{"verbose"}
		result := m.parseValueDescriptions(desc, vals)

		assert.Equal(t, "Extra detailed output", result["verbose"])
	})

	t.Run("no matching pattern", func(t *testing.T) {
		t.Parallel()
		desc := "some unrelated text"
		vals := []string{"auto"}
		result := m.parseValueDescriptions(desc, vals)

		assert.Empty(t, result)
	})

	t.Run("empty description", func(t *testing.T) {
		t.Parallel()
		result := m.parseValueDescriptions("", []string{"a", "b"})
		assert.Empty(t, result)
	})

	t.Run("empty valid values", func(t *testing.T) {
		t.Parallel()
		result := m.parseValueDescriptions("auto - desc", []string{})
		assert.Empty(t, result)
	})
}

func TestBuildSettingsList_EmptyModel(t *testing.T) {
	t.Parallel()

	m := configModel{
		items:        []configItem{},
		categories:   map[string][]int{},
		catOrder:     []string{},
		displayOrder: []int{},
	}

	result := m.buildSettingsList(80)
	assert.Empty(t, result)
}

func TestBuildSettingsList_SingleItem(t *testing.T) {
	t.Parallel()

	m := configModel{
		items: []configItem{
			{
				setting: ConfigSetting{
					Key:      "test_key",
					Category: "General",
				},
				value: &ConfigValue{
					Value:  "val1",
					Source: ConfigLevelDefault,
				},
			},
		},
		categories:   map[string][]int{"General": {0}},
		catOrder:     []string{"General"},
		displayOrder: []int{0},
		cursor:       0,
	}

	result := m.buildSettingsList(80)
	assert.Contains(t, result, "GENERAL")
	assert.Contains(t, result, "test_key")
	assert.Contains(t, result, "val1")
}

func TestBuildSettingsList_SelectedVsUnselected(t *testing.T) {
	t.Parallel()

	m := configModel{
		items: []configItem{
			{
				setting: ConfigSetting{Key: "first", Category: "Cat"},
				value:   &ConfigValue{Value: "a", Source: ConfigLevelUser},
			},
			{
				setting: ConfigSetting{Key: "second", Category: "Cat"},
				value:   &ConfigValue{Value: "b", Source: ConfigLevelDefault},
			},
		},
		categories:   map[string][]int{"Cat": {0, 1}},
		catOrder:     []string{"Cat"},
		displayOrder: []int{0, 1},
		cursor:       0,
	}

	result := m.buildSettingsList(80)
	// selected item has source arrow for user level
	assert.Contains(t, result, "user")
	// both items present
	assert.Contains(t, result, "first")
	assert.Contains(t, result, "second")
}

func TestBuildDetailPanel_EmptyDisplayOrder(t *testing.T) {
	t.Parallel()

	m := configModel{
		displayOrder: []int{},
	}

	result := m.buildDetailPanel(80)
	assert.Empty(t, result)
}

func TestBuildDetailPanel_WithItem(t *testing.T) {
	t.Parallel()

	m := configModel{
		items: []configItem{
			{
				setting: ConfigSetting{
					Key:             "session_recording",
					Description:     "Session recording mode",
					LongDescription: "Controls whether sessions are recorded.\nauto - starts automatically",
				},
				value: &ConfigValue{
					Value:   "auto",
					Source:  ConfigLevelUser,
					Default: "auto",
					UserVal: "auto",
				},
			},
		},
		displayOrder: []int{0},
		cursor:       0,
	}

	result := m.buildDetailPanel(80)
	assert.Contains(t, result, "session_recording")
	assert.Contains(t, result, "Session recording mode")
	assert.Contains(t, result, "Override Chain")
	assert.Contains(t, result, "Effective")
}

func TestBuildDetailPanel_NoLongDescription(t *testing.T) {
	t.Parallel()

	m := configModel{
		items: []configItem{
			{
				setting: ConfigSetting{
					Key:         "simple_key",
					Description: "A simple setting",
				},
				value: &ConfigValue{
					Value:  "yes",
					Source: ConfigLevelDefault,
				},
			},
		},
		displayOrder: []int{0},
		cursor:       0,
	}

	result := m.buildDetailPanel(80)
	assert.Contains(t, result, "simple_key")
	// should not have the long description section
	assert.NotContains(t, result, "Controls")
}

func TestBuildOverrideChain_UserActive(t *testing.T) {
	t.Parallel()

	m := configModel{}
	item := configItem{
		value: &ConfigValue{
			Value:   "auto",
			Source:  ConfigLevelUser,
			Default: "manual",
			UserVal: "auto",
			RepoVal: "",
			TeamVal: "",
		},
	}

	result := m.buildOverrideChain(item)
	// headers present
	assert.Contains(t, result, "User")
	assert.Contains(t, result, "Repo")
	assert.Contains(t, result, "Team")
	assert.Contains(t, result, "Default")
}

func TestBuildOverrideChain_DefaultActive(t *testing.T) {
	t.Parallel()

	m := configModel{}
	item := configItem{
		value: &ConfigValue{
			Value:   "auto",
			Source:  ConfigLevelDefault,
			Default: "auto",
		},
	}

	result := m.buildOverrideChain(item)
	assert.Contains(t, result, "User")
	assert.Contains(t, result, "Default")
}

func TestBuildOverrideChain_MultipleValuesSet(t *testing.T) {
	t.Parallel()

	m := configModel{}
	item := configItem{
		value: &ConfigValue{
			Value:   "disabled",
			Source:  ConfigLevelRepo,
			Default: "auto",
			UserVal: "",
			RepoVal: "disabled",
			TeamVal: "manual",
		},
	}

	result := m.buildOverrideChain(item)
	// repo is active
	assert.Contains(t, result, "disabled")
	// team value also shown
	assert.Contains(t, result, "manual")
}

func TestBuildSettingsList_MultipleCategories(t *testing.T) {
	t.Parallel()

	m := configModel{
		items: []configItem{
			{
				setting: ConfigSetting{Key: "a_key", Category: "Alpha"},
				value:   &ConfigValue{Value: "x", Source: ConfigLevelDefault},
			},
			{
				setting: ConfigSetting{Key: "b_key", Category: "Beta"},
				value:   &ConfigValue{Value: "y", Source: ConfigLevelUser},
			},
		},
		categories:   map[string][]int{"Alpha": {0}, "Beta": {1}},
		catOrder:     []string{"Alpha", "Beta"},
		displayOrder: []int{0, 1},
		cursor:       1,
	}

	result := m.buildSettingsList(80)
	assert.Contains(t, result, "ALPHA")
	assert.Contains(t, result, "BETA")
	// cursor on second item, so source arrow shows "user"
	assert.Contains(t, result, "user")
}

func TestBuildSettingsList_SkipsEmptyCategory(t *testing.T) {
	t.Parallel()

	m := configModel{
		items: []configItem{
			{
				setting: ConfigSetting{Key: "k", Category: "Filled"},
				value:   &ConfigValue{Value: "v", Source: ConfigLevelDefault},
			},
		},
		categories:   map[string][]int{"Filled": {0}, "Vacant": {}},
		catOrder:     []string{"Vacant", "Filled"},
		displayOrder: []int{0},
		cursor:       0,
	}

	result := m.buildSettingsList(80)
	assert.NotContains(t, result, "VACANT")
	assert.Contains(t, result, "FILLED")
}

func TestParseValueDescriptions_MultilineDescription(t *testing.T) {
	t.Parallel()

	m := configModel{}
	desc := `Controls session recording mode.

disabled - No sessions are recorded
manual - Recording starts only manually
auto - Recording starts automatically`

	vals := []string{"disabled", "manual", "auto"}
	result := m.parseValueDescriptions(desc, vals)

	require.Len(t, result, 3)
	assert.Equal(t, "No sessions are recorded", result["disabled"])
	assert.Equal(t, "Recording starts only manually", result["manual"])
	assert.Equal(t, "Recording starts automatically", result["auto"])
}

func TestWrapText_IndentedMultilineWrapping(t *testing.T) {
	t.Parallel()

	m := configModel{}
	text := "  first second third fourth fifth\nnormal line here"
	result := m.wrapText(text, 20)

	lines := strings.Split(result, "\n")
	assert.Greater(t, len(lines), 2, "wrapping should produce multiple lines")

	// find the boundary between indented paragraph and normal line
	normalIdx := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "normal") {
			normalIdx = i
			break
		}
	}
	assert.Greater(t, normalIdx, 0, "normal line should appear after indented lines")

	// all lines before "normal" should preserve the two-space indent
	for i := 0; i < normalIdx; i++ {
		if lines[i] == "" {
			continue
		}
		assert.True(t, strings.HasPrefix(lines[i], "  "),
			"line %d should preserve indent: %q", i, lines[i])
	}
}
