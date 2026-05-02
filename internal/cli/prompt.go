package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/mattn/go-isatty"
)

// SelectOption represents a single option in a selection menu.
type SelectOption[T any] struct {
	Label string
	Value T
}

// selectModel is the bubbletea model for interactive selection.
type selectModel struct {
	title    string
	options  []string
	cursor   int
	selected int
	done     bool
	canceled bool
}

func (m selectModel) Init() tea.Cmd {
	return nil
}

func (m selectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.canceled = true
			m.done = true
			return m, tea.Quit
		case "enter", " ", "space":
			m.selected = m.cursor
			m.done = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.options)-1 {
				m.cursor++
			}
		}
	}
	return m, nil
}

func (m selectModel) View() tea.View {
	if m.done {
		return tea.NewView("")
	}

	var b strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true)
	selectedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	cursorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))

	b.WriteString(titleStyle.Render(m.title))
	b.WriteString("\n\n")

	for i, opt := range m.options {
		cursor := "  "
		style := lipgloss.NewStyle()
		if i == m.cursor {
			cursor = cursorStyle.Render("> ")
			style = selectedStyle
		}
		b.WriteString(cursor + style.Render(opt) + "\n")
	}

	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("↑/↓ navigate • enter select • esc cancel"))

	return tea.NewView(b.String())
}

// SelectOne displays an interactive selection menu with arrow key navigation.
// Returns the index of the selected option (0-based) or -1 if canceled.
// Falls back to numbered prompt when stdin is not a TTY or in non-interactive mode.
func SelectOne(title string, options []string, defaultIdx int) (int, error) {
	if len(options) == 0 {
		return -1, errors.New("no options to select from")
	}

	cursor := 0
	if defaultIdx >= 0 && defaultIdx < len(options) {
		cursor = defaultIdx
	}

	// fall back to simple numbered prompt if non-interactive or stdin is not a TTY
	if !IsInteractive() || (!isatty.IsTerminal(os.Stdin.Fd()) && !isatty.IsCygwinTerminal(os.Stdin.Fd())) {
		return selectOneSimple(title, options, cursor)
	}

	m := selectModel{
		title:   title,
		options: options,
		cursor:  cursor,
	}

	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return -1, err
	}

	result := finalModel.(selectModel)
	if result.canceled {
		return -1, fmt.Errorf("selection canceled")
	}

	return result.selected, nil
}

// selectOneSimple is the fallback for non-TTY environments.
func selectOneSimple(title string, options []string, defaultIdx int) (int, error) {
	fmt.Println(title)
	fmt.Println()
	for i, opt := range options {
		marker := "  "
		if i == defaultIdx {
			marker = "> "
		}
		fmt.Printf("%s%d. %s\n", marker, i+1, opt)
	}
	fmt.Println()
	fmt.Printf("Enter number [%d]: ", defaultIdx+1)

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return defaultIdx, nil
	}

	input = strings.TrimSpace(input)
	if input == "" {
		return defaultIdx, nil
	}

	num, err := strconv.Atoi(input)
	if err != nil || num < 1 || num > len(options) {
		return -1, fmt.Errorf("invalid selection: %s", input)
	}

	return num - 1, nil
}

// SelectOneValue displays an interactive selection menu and returns the selected value.
func SelectOneValue[T comparable](title string, options []SelectOption[T], defaultIdx int) (T, error) {
	var zero T
	if len(options) == 0 {
		return zero, nil
	}

	labels := make([]string, len(options))
	for i, opt := range options {
		labels[i] = opt.Label
	}

	idx, err := SelectOne(title, labels, defaultIdx)
	if err != nil {
		return zero, err
	}

	return options[idx].Value, nil
}

// MultiSelectOption represents an item in a multi-select menu.
type MultiSelectOption struct {
	Label    string
	Value    string
	Selected bool
	Disabled bool   // cannot be toggled (renders dimmed)
	Hint     string // shown after label in dim text (e.g., "installed")
}

// multiSelectModel is the bubbletea model for checkbox-style selection.
type multiSelectModel struct {
	title    string
	items    []MultiSelectOption
	cursor   int
	done     bool
	canceled bool
}

func (m multiSelectModel) Init() tea.Cmd {
	return nil
}

func (m multiSelectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.canceled = true
			m.done = true
			return m, tea.Quit
		case "enter":
			m.done = true
			return m, tea.Quit
		case " ", "space", "x":
			if !m.items[m.cursor].Disabled {
				m.items[m.cursor].Selected = !m.items[m.cursor].Selected
			}
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case "a":
			for i := range m.items {
				if !m.items[i].Disabled {
					m.items[i].Selected = true
				}
			}
		case "n":
			for i := range m.items {
				if !m.items[i].Disabled {
					m.items[i].Selected = false
				}
			}
		}
	}
	return m, nil
}

func (m multiSelectModel) View() tea.View {
	if m.done {
		return tea.NewView("")
	}

	var b strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true)
	selectedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	cursorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	b.WriteString(titleStyle.Render(m.title))
	b.WriteString("\n\n")

	for i, item := range m.items {
		cursor := "  "
		style := lipgloss.NewStyle()
		if i == m.cursor {
			cursor = cursorStyle.Render("> ")
			style = selectedStyle
		}

		check := "[ ]"
		if item.Selected {
			check = "[x]"
		}

		label := check + " " + item.Label
		if item.Hint != "" {
			label += " " + dimStyle.Render(item.Hint)
		}

		if item.Disabled {
			b.WriteString(cursor + dimStyle.Render(check+" "+item.Label))
			if item.Hint != "" {
				b.WriteString(" " + dimStyle.Render(item.Hint))
			}
			b.WriteString("\n")
		} else {
			b.WriteString(cursor + style.Render(check+" "+item.Label))
			if item.Hint != "" {
				b.WriteString(" " + dimStyle.Render(item.Hint))
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render("space toggle • a all • n none • enter confirm • esc skip"))

	return tea.NewView(b.String())
}

// SelectMany displays an interactive multi-select with checkboxes.
// Returns the Values of all selected options, or error if canceled.
// Falls back to selectManySimple in non-TTY mode.
func SelectMany(title string, options []MultiSelectOption) ([]string, error) {
	if len(options) == 0 {
		return nil, nil
	}

	// fall back to simple prompt if non-interactive or stdin is not a TTY
	if !IsInteractive() || (!isatty.IsTerminal(os.Stdin.Fd()) && !isatty.IsCygwinTerminal(os.Stdin.Fd())) {
		return selectManySimple(title, options)
	}

	items := make([]MultiSelectOption, len(options))
	copy(items, options)

	m := multiSelectModel{
		title: title,
		items: items,
	}

	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return nil, err
	}

	result := finalModel.(multiSelectModel)
	if result.canceled {
		return nil, fmt.Errorf("selection canceled")
	}

	var selected []string
	for _, item := range result.items {
		if item.Selected {
			selected = append(selected, item.Value)
		}
	}

	return selected, nil
}

// selectManySimple is the fallback for non-TTY environments.
// Returns only pre-selected defaults.
func selectManySimple(title string, options []MultiSelectOption) ([]string, error) {
	fmt.Println(title)
	fmt.Println()
	for i, opt := range options {
		check := "[ ]"
		if opt.Selected {
			check = "[x]"
		}
		fmt.Printf("  %d. %s %s\n", i+1, check, opt.Label)
	}
	fmt.Println()
	fmt.Println("Non-interactive mode: using defaults")

	var selected []string
	for _, opt := range options {
		if opt.Selected {
			selected = append(selected, opt.Value)
		}
	}
	return selected, nil
}

// inputModel is the bubbletea model for text input.
type inputModel struct {
	title       string
	placeholder string
	value       string
	done        bool
	canceled    bool
}

func (m inputModel) Init() tea.Cmd {
	return nil
}

func (m inputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.canceled = true
			m.done = true
			return m, tea.Quit
		case "enter":
			m.done = true
			return m, tea.Quit
		case "backspace":
			if len(m.value) > 0 {
				m.value = m.value[:len(m.value)-1]
			}
		default:
			// only accept printable characters
			if len(msg.String()) == 1 && msg.String()[0] >= 32 {
				m.value += msg.String()
			}
		}
	}
	return m, nil
}

func (m inputModel) View() tea.View {
	if m.done {
		return tea.NewView("")
	}

	var b strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true)
	placeholderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	inputStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))

	b.WriteString(titleStyle.Render(m.title))
	b.WriteString("\n\n")

	if m.value == "" {
		b.WriteString(placeholderStyle.Render(m.placeholder))
	} else {
		b.WriteString(inputStyle.Render(m.value))
	}
	b.WriteString("█") // cursor

	b.WriteString("\n\n")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("enter confirm • esc cancel"))

	return tea.NewView(b.String())
}

// InputWithDefault prompts for text input with a placeholder showing the default.
// If user enters empty string, returns the default value.
// Falls back to simple text prompt when stdin is not a TTY or in non-interactive mode.
func InputWithDefault(title, defaultVal string) (string, error) {
	// fall back to simple prompt if non-interactive or stdin is not a TTY
	if !IsInteractive() || (!isatty.IsTerminal(os.Stdin.Fd()) && !isatty.IsCygwinTerminal(os.Stdin.Fd())) {
		return inputWithDefaultSimple(title, defaultVal)
	}

	m := inputModel{
		title:       title,
		placeholder: defaultVal,
	}

	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return "", err
	}

	result := finalModel.(inputModel)
	if result.canceled {
		return "", fmt.Errorf("input canceled")
	}

	if result.value == "" {
		return defaultVal, nil
	}

	return result.value, nil
}

// inputWithDefaultSimple is the fallback for non-TTY environments.
func inputWithDefaultSimple(title, defaultVal string) (string, error) {
	fmt.Printf("%s [%s]: ", title, defaultVal)

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return defaultVal, nil
	}

	input = strings.TrimSpace(input)
	if input == "" {
		return defaultVal, nil
	}

	return input, nil
}
