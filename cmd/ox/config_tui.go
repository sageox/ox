package main

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/cli"
)

// Layout constants
const (
	minWidth  = 60
	minHeight = 20
)

// Styles using semantic theme tokens
var (
	// Frame and structure
	frameStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(cli.ColorDim)

	// Title bar
	titleBarStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(cli.ColorPrimary).
			Padding(0, 1)

	// Category headers
	categoryStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(cli.ColorPrimary).
			MarginTop(1)

	// Setting rows
	settingKeyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#AAAAAA"))

	settingKeySelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#FFFFFF"))

	settingValueStyle = lipgloss.NewStyle().
				Foreground(cli.ColorSecondary)

	settingValueDefaultStyle = lipgloss.NewStyle().
					Foreground(cli.ColorDim)

	// Cursor
	cursorStyle = lipgloss.NewStyle().
			Foreground(cli.ColorSecondary).
			Bold(true)

	// Detail panel
	detailHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(cli.ColorSecondary)

	detailDescStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#CCCCCC"))

	detailMutedStyle = lipgloss.NewStyle().
				Foreground(cli.ColorDim)

	// Override chain table
	chainHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(cli.ColorDim)

	chainCellStyle = lipgloss.NewStyle().
			Foreground(cli.ColorDim).
			Width(10).
			Align(lipgloss.Center)

	chainCellActiveStyle = lipgloss.NewStyle().
				Foreground(cli.ColorSecondary).
				Bold(true).
				Width(10).
				Align(lipgloss.Center)

	chainCellEmptyStyle = lipgloss.NewStyle().
				Foreground(cli.ColorDim).
				Width(10).
				Align(lipgloss.Center)

	// Divider
	dividerStyle = lipgloss.NewStyle().
			Foreground(cli.ColorDim)

	// Help
	helpStyle = lipgloss.NewStyle().
			Foreground(cli.ColorDim)

	// Success indicator
	successStyle = lipgloss.NewStyle().
			Foreground(cli.ColorSuccess).
			Bold(true)

	// Edit mode
	radioSelectedStyle = lipgloss.NewStyle().
				Foreground(cli.ColorSecondary).
				Bold(true)

	radioUnselectedStyle = lipgloss.NewStyle().
				Foreground(cli.ColorDim)

	optionDescStyle = lipgloss.NewStyle().
			Foreground(cli.ColorDim)
)

// configItem represents a setting in the TUI
type configItem struct {
	setting ConfigSetting
	value   *ConfigValue
}

// configModel is the bubbletea model for the config TUI
type configModel struct {
	items        []configItem
	categories   map[string][]int // category -> indices into items
	catOrder     []string         // ordered category names
	displayOrder []int            // indices into items in visual display order
	projectRoot  string
	cursor       int // index into displayOrder, not items
	editing      bool
	editCursor   int
	width        int
	height       int
	quitting     bool
	saved        bool
}

type keyMap struct {
	Enter key.Binding
	Esc   key.Binding
	Quit  key.Binding
	Up    key.Binding
	Down  key.Binding
}

var keys = keyMap{
	Enter: key.NewBinding(key.WithKeys("enter")),
	Esc:   key.NewBinding(key.WithKeys("esc")),
	Quit:  key.NewBinding(key.WithKeys("q", "ctrl+c")),
	Up:    key.NewBinding(key.WithKeys("up", "k")),
	Down:  key.NewBinding(key.WithKeys("down", "j")),
}

func newConfigModel(projectRoot string) (*configModel, error) {
	items := []configItem{}
	categories := make(map[string][]int)
	catOrder := []string{}
	catSeen := make(map[string]bool)

	for _, setting := range AllSettings {
		cv, err := ResolveConfigValue(setting.Key, projectRoot)
		if err != nil {
			continue
		}
		idx := len(items)
		items = append(items, configItem{
			setting: setting,
			value:   cv,
		})

		cat := setting.Category
		if !catSeen[cat] {
			catSeen[cat] = true
			catOrder = append(catOrder, cat)
		}
		categories[cat] = append(categories[cat], idx)
	}

	// Build display order: items in the order they appear visually (by category)
	displayOrder := []int{}
	for _, cat := range catOrder {
		displayOrder = append(displayOrder, categories[cat]...)
	}

	return &configModel{
		items:        items,
		categories:   categories,
		catOrder:     catOrder,
		displayOrder: displayOrder,
		projectRoot:  projectRoot,
	}, nil
}

func (m configModel) Init() tea.Cmd {
	return nil
}

func (m configModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		// Clear saved indicator on any key press
		m.saved = false

		if m.editing {
			return m.handleEditMode(msg)
		}

		switch {
		case key.Matches(msg, keys.Quit):
			m.quitting = true
			return m, tea.Quit

		case key.Matches(msg, keys.Enter):
			m.editing = true
			item := m.items[m.displayOrder[m.cursor]]
			m.editCursor = 0
			for i, v := range item.setting.ValidValues {
				if v == item.value.Value {
					m.editCursor = i
					break
				}
			}
			return m, nil

		case key.Matches(msg, keys.Up):
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil

		case key.Matches(msg, keys.Down):
			if m.cursor < len(m.displayOrder)-1 {
				m.cursor++
			}
			return m, nil
		}
	}

	return m, nil
}

func (m configModel) handleEditMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	itemIdx := m.displayOrder[m.cursor]
	item := m.items[itemIdx]

	switch {
	case key.Matches(msg, keys.Esc):
		m.editing = false
		return m, nil

	case key.Matches(msg, keys.Up):
		if m.editCursor > 0 {
			m.editCursor--
		}
		return m, nil

	case key.Matches(msg, keys.Down):
		if m.editCursor < len(item.setting.ValidValues)-1 {
			m.editCursor++
		}
		return m, nil

	case key.Matches(msg, keys.Enter):
		if len(item.setting.ValidValues) > 0 {
			newValue := item.setting.ValidValues[m.editCursor]
			err := SetConfigValue(item.setting.Key, newValue, ConfigLevelUser, m.projectRoot)
			if err == nil {
				cv, _ := ResolveConfigValue(item.setting.Key, m.projectRoot)
				m.items[itemIdx].value = cv
				m.saved = true
			}
		}
		m.editing = false
		return m, nil
	}

	return m, nil
}

func (m configModel) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}

	width := m.width
	if width < minWidth {
		width = minWidth
	}
	if width > 100 {
		width = 100
	}

	var content string
	if m.editing {
		content = m.editView(width)
	} else {
		content = m.mainView(width)
	}

	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func (m configModel) mainView(width int) string {
	innerWidth := width - 4 // account for border padding

	// Build settings list section
	settingsList := m.buildSettingsList(innerWidth)

	// Build detail panel for selected item
	detailPanel := m.buildDetailPanel(innerWidth)

	// Combine with divider
	divider := dividerStyle.Render(strings.Repeat("─", innerWidth))

	// Help line
	help := "↑/↓ navigate • enter edit • q quit"
	if m.saved {
		help = successStyle.Render("✓ saved") + "  " + help
	}

	content := fmt.Sprintf("%s\n%s\n%s\n\n%s",
		settingsList,
		divider,
		detailPanel,
		helpStyle.Render(help),
	)

	// Wrap in frame
	frame := frameStyle.Width(width - 2).Render(content)
	title := titleBarStyle.Render("ox config")

	// Overlay title on top border
	lines := strings.Split(frame, "\n")
	if len(lines) > 0 {
		// Insert title into first line after the corner
		firstLine := lines[0]
		if len(firstLine) > 3 {
			lines[0] = firstLine[:2] + title + firstLine[2+len(title):]
		}
	}

	return strings.Join(lines, "\n")
}

func (m configModel) buildSettingsList(width int) string {
	var b strings.Builder

	// Track which display position we're at
	displayPos := 0

	for _, cat := range m.catOrder {
		indices := m.categories[cat]
		if len(indices) == 0 {
			continue
		}

		// Category header
		b.WriteString(categoryStyle.Render(strings.ToUpper(cat)))
		b.WriteString("\n")

		// Settings in this category
		for _, idx := range indices {
			item := m.items[idx]
			isSelected := displayPos == m.cursor

			// Cursor
			cursor := "  "
			if isSelected {
				cursor = cursorStyle.Render("> ")
			}

			// Key
			keyText := item.setting.Key
			if isSelected {
				keyText = settingKeySelectedStyle.Render(keyText)
			} else {
				keyText = settingKeyStyle.Render(keyText)
			}

			// Value with source indicator
			valueText := item.value.Value
			sourceText := m.formatSourceArrow(item.value.Source)

			var styledValue string
			if item.value.Source == ConfigLevelDefault {
				styledValue = settingValueDefaultStyle.Render(valueText)
			} else {
				styledValue = settingValueStyle.Render(valueText)
			}

			// Format: cursor + key (padded) + value + source
			row := fmt.Sprintf("%s%-26s %s %s", cursor, keyText, styledValue, sourceText)
			b.WriteString(row)
			b.WriteString("\n")

			displayPos++
		}
	}

	return b.String()
}

func (m configModel) formatSourceArrow(source ConfigLevel) string {
	switch source {
	case ConfigLevelUser:
		return detailMutedStyle.Render("← user")
	case ConfigLevelRepo:
		return detailMutedStyle.Render("← repo")
	case ConfigLevelTeam:
		return detailMutedStyle.Render("← team")
	default:
		return detailMutedStyle.Render("← default")
	}
}

func (m configModel) buildDetailPanel(width int) string {
	if len(m.displayOrder) == 0 {
		return ""
	}

	item := m.items[m.displayOrder[m.cursor]]
	var b strings.Builder

	// Setting name and description
	b.WriteString(detailHeaderStyle.Render(item.setting.Key))
	b.WriteString(" — ")
	b.WriteString(detailDescStyle.Render(item.setting.Description))
	b.WriteString("\n\n")

	// Long description (wrap to width)
	if item.setting.LongDescription != "" {
		desc := m.wrapText(item.setting.LongDescription, width-2)
		b.WriteString(detailMutedStyle.Render(desc))
		b.WriteString("\n\n")
	}

	// Override chain visualization
	b.WriteString(chainHeaderStyle.Render("Override Chain (highest priority wins):"))
	b.WriteString("\n")
	b.WriteString(m.buildOverrideChain(item))
	b.WriteString("\n")

	// Effective value
	effectiveText := fmt.Sprintf("Effective: %s (from %s)",
		settingValueStyle.Render(item.value.Value),
		item.value.Source)
	b.WriteString(effectiveText)

	return b.String()
}

func (m configModel) buildOverrideChain(item configItem) string {
	var b strings.Builder

	// Header row
	headers := []string{"User", "Repo", "Team", "Default"}
	headerRow := ""
	for _, h := range headers {
		headerRow += chainCellStyle.Render(h)
	}
	b.WriteString(headerRow)
	b.WriteString("\n")

	// Value row
	values := []struct {
		val      string
		isActive bool
	}{
		{item.value.UserVal, item.value.Source == ConfigLevelUser},
		{item.value.RepoVal, item.value.Source == ConfigLevelRepo},
		{item.value.TeamVal, item.value.Source == ConfigLevelTeam},
		{item.value.Default, item.value.Source == ConfigLevelDefault},
	}

	valueRow := ""
	for _, v := range values {
		cellContent := "—"
		if v.val != "" {
			cellContent = v.val
		}

		if v.isActive && v.val != "" {
			valueRow += chainCellActiveStyle.Render("● " + cellContent)
		} else if v.val != "" {
			valueRow += chainCellStyle.Render(cellContent)
		} else if cellContent == "—" && v.isActive {
			// Default is active but shown as the default value
			valueRow += chainCellActiveStyle.Render("● " + item.value.Default)
		} else {
			valueRow += chainCellEmptyStyle.Render(cellContent)
		}
	}
	b.WriteString(valueRow)

	return b.String()
}

func (m configModel) wrapText(text string, width int) string {
	if width <= 0 {
		width = 60
	}

	var result strings.Builder
	lines := strings.Split(text, "\n")

	for i, line := range lines {
		if i > 0 {
			result.WriteString("\n")
		}

		// Handle indented lines (preserve indentation)
		indent := ""
		trimmed := strings.TrimLeft(line, " ")
		if len(trimmed) < len(line) {
			indent = line[:len(line)-len(trimmed)]
		}

		words := strings.Fields(trimmed)
		if len(words) == 0 {
			continue
		}

		currentLine := indent + words[0]
		for _, word := range words[1:] {
			if len(currentLine)+1+len(word) > width {
				result.WriteString(currentLine)
				result.WriteString("\n")
				currentLine = indent + word
			} else {
				currentLine += " " + word
			}
		}
		result.WriteString(currentLine)
	}

	return result.String()
}

func (m configModel) editView(width int) string {
	item := m.items[m.displayOrder[m.cursor]]
	innerWidth := width - 4
	var b strings.Builder

	// Title
	b.WriteString(detailHeaderStyle.Render(item.setting.Key))
	b.WriteString("\n")
	b.WriteString(detailDescStyle.Render(item.setting.Description))
	b.WriteString("\n\n")

	// Description
	if item.setting.LongDescription != "" {
		desc := m.wrapText(item.setting.LongDescription, innerWidth-2)
		b.WriteString(detailMutedStyle.Render(desc))
		b.WriteString("\n\n")
	}

	// Value selection
	b.WriteString(chainHeaderStyle.Render("Select value:"))
	b.WriteString("\n")

	// Get descriptions for each value from LongDescription
	valueDescs := m.parseValueDescriptions(item.setting.LongDescription, item.setting.ValidValues)

	for i, val := range item.setting.ValidValues {
		var radio, valText string
		if i == m.editCursor {
			radio = radioSelectedStyle.Render("●")
			valText = radioSelectedStyle.Render(val)
		} else {
			radio = radioUnselectedStyle.Render("○")
			valText = detailMutedStyle.Render(val)
		}

		// Current indicator
		indicator := ""
		if val == item.value.Value {
			indicator = detailMutedStyle.Render(" (current)")
		}

		// Value description
		desc := ""
		if d, ok := valueDescs[val]; ok {
			desc = optionDescStyle.Render("  " + d)
		}

		fmt.Fprintf(&b, "  %s %s%s\n", radio, valText, indicator)
		if desc != "" {
			b.WriteString(desc + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(detailMutedStyle.Render("Sets user-level override (highest priority)"))
	b.WriteString("\n\n")
	b.WriteString(helpStyle.Render("↑/↓ select • enter save • esc cancel"))

	// Wrap in frame
	frame := frameStyle.Width(width - 2).Render(b.String())
	title := titleBarStyle.Render("Edit: " + item.setting.Key)

	lines := strings.Split(frame, "\n")
	if len(lines) > 0 {
		firstLine := lines[0]
		if len(firstLine) > 3 {
			lines[0] = firstLine[:2] + title + firstLine[2+len(title):]
		}
	}

	return strings.Join(lines, "\n")
}

func (m configModel) parseValueDescriptions(longDesc string, validValues []string) map[string]string {
	result := make(map[string]string)

	for _, val := range validValues {
		// Look for pattern: "value - description" or "value  description"
		patterns := []string{
			val + " - ",
			val + "  ",
			val + " — ",
		}

		for _, pattern := range patterns {
			idx := strings.Index(longDesc, pattern)
			if idx >= 0 {
				// Find the end of this line
				rest := longDesc[idx+len(pattern):]
				endIdx := strings.Index(rest, "\n")
				if endIdx < 0 {
					endIdx = len(rest)
				}
				result[val] = strings.TrimSpace(rest[:endIdx])
				break
			}
		}
	}

	return result
}

// runConfigTUI launches the interactive config TUI
func runConfigTUI() error {
	projectRoot, _ := findProjectRoot()

	m, err := newConfigModel(projectRoot)
	if err != nil {
		return err
	}

	p := tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		return err
	}

	return nil
}
