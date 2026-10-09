package main

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sageox/ox/internal/cli"
)

const (
	importPreviewConcurrency   = 2
	importTerminalPreviewLimit = 8
	importTerminalPromptLimit  = 64
)

// importTerminalOptions replaces IO side effects for tests while running the
// same Bubble Tea program. The CLI uses its normal terminal and browser.
type importTerminalOptions struct {
	program []tea.ProgramOption
	browser func(context.Context, importDestination, []*importCandidate, importPreviewLoader) (importReviewResult, error)
}

// runImportTerminal gathers a selection only. Upload authorization remains in
// the terminal confirmation after this program (or the local browser) exits.
func runImportTerminal(ctx context.Context, dest importDestination, cands []*importCandidate, load importPreviewLoader, options ...importTerminalOptions) (importReviewResult, error) {
	previewCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	model := newImportReviewModel(previewCtx, dest, cands, load)
	programOptions := []tea.ProgramOption{tea.WithContext(previewCtx)}
	browser := runImportBrowser
	if len(options) > 0 {
		programOptions = append(programOptions, options[0].program...)
		if options[0].browser != nil {
			browser = options[0].browser
		}
	}
	final, err := tea.NewProgram(model, programOptions...).Run()
	cancel()
	if err != nil {
		return importReviewResult{}, fmt.Errorf("review sessions in terminal: %w", err)
	}
	m := final.(*importReviewModel)
	if m.browse {
		for _, c := range cands {
			c.Selected = m.selected[c.Session.NativeID] && c.State == stateReady
		}
		return browser(ctx, dest, cands, load)
	}
	return importReviewResult{IDs: m.selectedIDs(), Canceled: m.canceled}, nil
}

type importPreviewLoadedMsg struct {
	id      string
	preview *importContentPreview
	err     error
}

// The picker retains excerpts, never normalized conversation entries. Keeping
// whole preview pointers here would defeat the reader's bounded content cache.
type importTerminalPreview struct {
	OpeningRequest string
	Prompts        []string
	PromptCount    int
	LastReply      string
}

type importReviewModel struct {
	ctx      context.Context
	dest     importDestination
	cands    []*importCandidate
	load     importPreviewLoader
	selected map[string]bool
	previews map[string]*importTerminalPreview
	labels   map[string]string
	recent   []string
	loadErrs map[string]error
	pending  map[string]bool
	cursor   int
	width    int
	height   int
	scroll   int
	browse   bool
	canceled bool
	finished bool
}

func newImportReviewModel(ctx context.Context, dest importDestination, cands []*importCandidate, load importPreviewLoader) *importReviewModel {
	m := &importReviewModel{
		ctx: ctx, dest: dest, cands: cands, load: load,
		selected: map[string]bool{}, previews: map[string]*importTerminalPreview{}, labels: map[string]string{},
		loadErrs: map[string]error{}, pending: map[string]bool{}, width: 80, height: 24,
	}
	for _, c := range cands {
		m.selected[c.Session.NativeID] = c.Selected && c.State == stateReady
	}
	return m
}

func (m *importReviewModel) Init() tea.Cmd { return m.loadVisible() }

func (m *importReviewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.finished {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
		m.clampScroll()
		return m, m.loadVisible()
	case importPreviewLoadedMsg:
		delete(m.pending, msg.id)
		if msg.err != nil {
			m.loadErrs[msg.id] = msg.err
		} else if msg.preview != nil {
			m.rememberPreview(msg.id, msg.preview)
		} else {
			m.loadErrs[msg.id] = fmt.Errorf("session reader returned no content")
		}
		m.clampScroll()
		return m, m.loadVisible()
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.canceled, m.finished = true, true
			return m, tea.Quit
		case "enter":
			m.finished = true
			return m, tea.Quit
		case "b":
			m.browse, m.finished = true, true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				m.scroll = 0
				m.touchPreview(m.cands[m.cursor].Session.NativeID)
			}
			return m, m.loadVisible()
		case "down", "j":
			if m.cursor+1 < len(m.cands) {
				m.cursor++
				m.scroll = 0
				m.touchPreview(m.cands[m.cursor].Session.NativeID)
			}
			return m, m.loadVisible()
		case "space", " ":
			if len(m.cands) > 0 {
				c := m.cands[m.cursor]
				if c.State == stateReady {
					m.selected[c.Session.NativeID] = !m.selected[c.Session.NativeID]
				}
			}
		case "a", "x":
			for _, c := range m.cands {
				m.selected[c.Session.NativeID] = msg.String() == "a" && c.State == stateReady
			}
		case "pgdown", "ctrl+d":
			m.scroll += max(1, m.detailHeight()-1)
			m.clampScroll()
		case "pgup", "ctrl+u":
			m.scroll -= max(1, m.detailHeight()-1)
			m.clampScroll()
		case "home":
			m.scroll = 0
		case "end":
			m.scroll = len(m.detailLines(m.detailWidth()))
			m.clampScroll()
		}
	}
	return m, nil
}

func (m *importReviewModel) selectedIDs() []string {
	ids := make([]string, 0)
	for _, c := range m.cands {
		if c.State == stateReady && m.selected[c.Session.NativeID] {
			ids = append(ids, c.Session.NativeID)
		}
	}
	return ids
}

func (m *importReviewModel) rememberPreview(id string, p *importContentPreview) {
	preview := &importTerminalPreview{
		OpeningRequest: importTerminalExcerpt(p.OpeningRequest, 2048),
		LastReply:      importTerminalExcerpt(p.LastReply, 2048),
		PromptCount:    len(p.Prompts),
	}
	for _, prompt := range p.Prompts[:min(len(p.Prompts), importTerminalPromptLimit)] {
		text := strings.Join(strings.Fields(sanitizeImportText(prompt.Content)), " ")
		preview.Prompts = append(preview.Prompts, importTerminalExcerpt(text, 180))
	}
	label := strings.Join(strings.Fields(sanitizeImportText(p.OpeningRequest)), " ")
	if label == "" {
		label = "No human request available"
	}
	m.labels[id] = importTerminalExcerpt(label, 180)
	m.previews[id] = preview
	m.touchPreview(id)
	for len(m.recent) > importTerminalPreviewLimit {
		i := 0
		if len(m.cands) > 0 && m.recent[0] == m.cands[m.cursor].Session.NativeID {
			i = 1 // keep the currently displayed excerpt while other reads finish
		}
		delete(m.previews, m.recent[i])
		m.recent = append(m.recent[:i], m.recent[i+1:]...)
	}
}

func (m *importReviewModel) touchPreview(id string) {
	if m.previews[id] == nil {
		return
	}
	for i, cachedID := range m.recent {
		if cachedID == id {
			m.recent = append(m.recent[:i], m.recent[i+1:]...)
			break
		}
	}
	m.recent = append(m.recent, id)
}

// Only visible rows are read. Completion of one request schedules the next,
// keeping native-file reads bounded even for a large session history.
func (m *importReviewModel) loadVisible() tea.Cmd {
	if m.load == nil || len(m.cands) == 0 {
		return nil
	}
	start, end := m.visibleRange()
	order := []int{m.cursor}
	for i := start; i < end; i++ {
		if i != m.cursor {
			order = append(order, i)
		}
	}
	var commands []tea.Cmd
	for _, i := range order {
		if len(m.pending) >= importPreviewConcurrency {
			break
		}
		id := m.cands[i].Session.NativeID
		if m.previews[id] != nil || m.loadErrs[id] != nil || m.pending[id] {
			continue
		}
		if i != m.cursor && m.labels[id] != "" {
			continue // an evicted row already has its label; reload only on focus
		}
		m.pending[id] = true
		commands = append(commands, func() tea.Msg {
			preview, err := m.load(m.ctx, id)
			return importPreviewLoadedMsg{id: id, preview: preview, err: err}
		})
	}
	return tea.Batch(commands...)
}

func (m *importReviewModel) wide() bool { return m.width >= 92 }

func (m *importReviewModel) bodyHeight() int { return max(1, m.height-8) }

func (m *importReviewModel) listHeight() int {
	if m.wide() {
		return m.bodyHeight()
	}
	return min(6, max(1, m.bodyHeight()/3))
}

func (m *importReviewModel) detailHeight() int {
	if m.wide() {
		return m.bodyHeight()
	}
	return max(1, m.bodyHeight()-m.listHeight()-1)
}

func (m *importReviewModel) listWidth() int {
	if m.wide() {
		return min(48, m.width*2/5)
	}
	return m.width
}

func (m *importReviewModel) detailWidth() int {
	if m.wide() {
		return max(1, m.width-m.listWidth()-3)
	}
	return m.width
}

func (m *importReviewModel) visibleRange() (int, int) {
	rows := max(1, m.listHeight()/2)
	start := max(0, m.cursor-rows/2)
	start = min(start, max(0, len(m.cands)-rows))
	return start, min(len(m.cands), start+rows)
}

func (m *importReviewModel) View() tea.View {
	if m.finished {
		return tea.NewView("")
	}
	primary := lipgloss.NewStyle().Bold(true).Foreground(cli.ColorPrimary)
	dim := lipgloss.NewStyle().Foreground(cli.ColorDim)
	title := fmt.Sprintf("Choose sessions to import · %d selected", len(m.selectedIDs()))
	destination := sanitizeImportText(m.dest.Team + " · " + m.dest.RepoID + " · " + m.dest.Visibility)
	if m.dest.Visibility == "public" {
		destination += " · anyone can read the Ledger"
	}
	var body string
	if m.wide() {
		list := lipgloss.NewStyle().Width(m.listWidth()).Render(m.listView())
		detail := lipgloss.NewStyle().Width(m.detailWidth()).Render(m.detailView())
		body = lipgloss.JoinHorizontal(lipgloss.Top, list, " │ ", detail)
	} else {
		body = m.listView() + "\n" + dim.Render(strings.Repeat("─", m.width)) + "\n" + m.detailView()
	}
	help := "↑/↓ move · space toggle · a all · x clear · enter review · b browser · q cancel"
	scrollHelp := "pgup/pgdown scroll · home/end first/last excerpt"
	if m.width < 92 {
		help = "↑/↓ browse · space toggle · enter review\na all · x clear · b browser · q cancel"
		scrollHelp = "pgup/pgdown scroll · home/end"
	}
	content := primary.Render(ansi.Truncate(title, m.width, "…")) + "\n" +
		ansi.Truncate(destination, m.width, "…") + "\n" +
		dim.Render(ansi.Truncate("Preview only. Nothing uploads until you confirm in this terminal.", m.width, "…")) + "\n\n" +
		body + "\n" + dim.Render(help) + "\n" + dim.Render(scrollHelp)
	// Small terminals still get a bounded frame rather than overflowing into
	// the shell. The content can be inspected with b or --preview there.
	lines := strings.Split(content, "\n")
	if len(lines) > m.height {
		lines = append(lines[:max(0, m.height-1)], ansi.Truncate("b browser · enter review · q cancel", m.width, "…"))
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, m.width, "…")
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}

func (m *importReviewModel) listView() string {
	if len(m.cands) == 0 {
		return "No sessions found."
	}
	start, end := m.visibleRange()
	lines := make([]string, 0, m.listHeight())
	for i := start; i < end; i++ {
		c := m.cands[i]
		id := c.Session.NativeID
		cursor, check := " ", "[ ]"
		if i == m.cursor {
			cursor = ">"
		}
		if c.State != stateReady {
			check = "[-]"
		} else if m.selected[id] {
			check = "[x]"
		}
		label := "Loading request…"
		if cachedLabel := m.labels[id]; cachedLabel != "" {
			label = cachedLabel
		} else if m.loadErrs[id] != nil {
			label = "Content unavailable"
		}
		line := ansi.Truncate(cursor+" "+check+" "+label, m.listWidth(), "…")
		if i == m.cursor {
			line = lipgloss.NewStyle().Bold(true).Foreground(cli.ColorPrimary).Render(line)
		}
		lines = append(lines, line)
		date := c.Session.StartedAt.Local().Format("Jan 02 15:04")
		if c.Session.StartedAt.IsZero() {
			date = "date unavailable"
		}
		metadata := fmt.Sprintf("      %s · %s · %s", date, c.Session.Agent, c.State)
		lines = append(lines, ansi.Truncate(sanitizeImportText(metadata), m.listWidth(), "…"))
	}
	for len(lines) < m.listHeight() {
		lines = append(lines, "")
	}
	return strings.Join(lines[:m.listHeight()], "\n")
}

func (m *importReviewModel) detailView() string {
	lines := m.detailLines(m.detailWidth())
	start := min(m.scroll, max(0, len(lines)-m.detailHeight()))
	end := min(len(lines), start+m.detailHeight())
	visible := append([]string(nil), lines[start:end]...)
	for len(visible) < m.detailHeight() {
		visible = append(visible, "")
	}
	return strings.Join(visible, "\n")
}

func (m *importReviewModel) clampScroll() {
	m.scroll = max(0, min(m.scroll, len(m.detailLines(m.detailWidth()))-m.detailHeight()))
}

func (m *importReviewModel) detailLines(width int) []string {
	if len(m.cands) == 0 {
		return []string{"Native sessions from this repo will appear here."}
	}
	c := m.cands[m.cursor]
	id := c.Session.NativeID
	var b strings.Builder
	fmt.Fprintf(&b, "%s · %s\n", c.Session.Agent, sanitizeImportText(id))
	if c.State != stateReady {
		fmt.Fprintf(&b, "Skipped: %s\n", sanitizeImportText(c.Reason))
	}
	if c.Covered != "" {
		fmt.Fprintf(&b, "Coverage: %s\n", sanitizeImportText(c.Covered))
	}
	if err := m.loadErrs[id]; err != nil {
		fmt.Fprintf(&b, "\nContent unavailable\n%s\n", sanitizeImportText(err.Error()))
		b.WriteString("Choose another session, or rerun after the native file is readable.")
	} else if p := m.previews[id]; p != nil {
		b.WriteString("\nOpening request\n")
		b.WriteString(importTerminalExcerpt(p.OpeningRequest, 2048))
		fmt.Fprintf(&b, "\n\nHuman prompts (%d)\n", p.PromptCount)
		for i, prompt := range p.Prompts {
			fmt.Fprintf(&b, "%d. %s\n", i+1, prompt)
		}
		if p.PromptCount > len(p.Prompts) {
			fmt.Fprintf(&b, "%d more human prompts · open b to read them all\n", p.PromptCount-len(p.Prompts))
		}
		b.WriteString("\nLast AI reply\n")
		if p.LastReply == "" {
			b.WriteString("No AI reply available.")
		} else {
			b.WriteString(importTerminalExcerpt(p.LastReply, 2048))
		}
		b.WriteString("\n\nExcerpts help you choose. Import retains the redacted conversation and available tool activity. Open b for the conversation.")
	} else {
		b.WriteString("\nLoading session content…\nReading locally; no summary or upload is running.")
	}
	return strings.Split(ansi.Wrap(b.String(), max(1, width), ""), "\n")
}

// Native content is data, not terminal instructions. Strip escape sequences and
// control characters, including bidi controls that can disguise displayed text.
func sanitizeImportText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || (r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069') {
			return -1
		}
		return r
	}, ansi.Strip(s))
}

func importTerminalExcerpt(s string, limit int) string {
	runes := []rune(strings.TrimSpace(sanitizeImportText(s)))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}
