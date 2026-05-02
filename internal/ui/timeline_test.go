package ui

import (
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
)

func TestRenderTimelineConnector(t *testing.T) {
	t.Parallel()
	got := RenderTimelineConnector()
	assert.Contains(t, got, TimelineBar, "connector should contain timeline bar character")
	assert.True(t, strings.HasSuffix(got, "\n"), "connector should end with newline")
}

func TestRenderTimelineEnd(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		label string
	}{
		{"standard label", "Done"},
		{"empty label", ""},
		{"long label", "All checks completed successfully"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := RenderTimelineEnd(tt.label)
			assert.Contains(t, got, TimelineCircle, "end should contain hollow circle")
			if tt.label != "" {
				assert.Contains(t, got, tt.label, "end should contain label text")
			}
			assert.True(t, strings.HasSuffix(got, "\n"), "end should end with newline")
		})
	}
}

func TestRenderTimelineNode(t *testing.T) {
	t.Parallel()

	testStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#00FF00"))

	tests := []struct {
		name       string
		node       TimelineNode
		wantParts  []string
		wantAbsent []string
	}{
		{
			name: "node with title only",
			node: TimelineNode{
				Title: "Health Checks",
				Style: testStyle,
			},
			wantParts: []string{TimelineDot, "Health Checks"},
		},
		{
			name: "node with title and summary",
			node: TimelineNode{
				Title:   "Configuration",
				Style:   testStyle,
				Summary: "2 passed",
			},
			wantParts: []string{TimelineDot, "Configuration", "2 passed"},
		},
		{
			name: "node with items",
			node: TimelineNode{
				Title: "Checks",
				Style: testStyle,
				Items: []TimelineItem{
					{Icon: IconPass, Style: PassStyle, Text: "Auth valid"},
					{Icon: IconFail, Style: FailStyle, Text: "Config missing"},
				},
			},
			wantParts: []string{
				TimelineDot, "Checks",
				IconPass, "Auth valid",
				IconFail, "Config missing",
			},
		},
		{
			name: "item with badge",
			node: TimelineNode{
				Title: "Fixes",
				Style: testStyle,
				Items: []TimelineItem{
					{Icon: IconWarn, Style: WarnStyle, Text: "Stale token", Badge: "[auto-fix]"},
				},
			},
			wantParts: []string{"Stale token", "[auto-fix]"},
		},
		{
			name: "item with detail",
			node: TimelineNode{
				Title: "Issues",
				Style: testStyle,
				Items: []TimelineItem{
					{
						Icon:   IconFail,
						Style:  FailStyle,
						Text:   "Missing config",
						Detail: "Run ox init to fix",
					},
				},
			},
			wantParts: []string{"Missing config", "Run ox init to fix"},
		},
		{
			name: "item with raw detail skips muted styling",
			node: TimelineNode{
				Title: "Status",
				Style: testStyle,
				Items: []TimelineItem{
					{
						Icon:      IconInfo,
						Style:     AccentStyle,
						Text:      "Version info",
						Detail:    "pre-styled detail",
						DetailRaw: true,
					},
				},
			},
			wantParts: []string{"Version info", "pre-styled detail"},
		},
		{
			name: "node with box instead of items",
			node: TimelineNode{
				Title: "Summary",
				Style: testStyle,
				Box:   "box line 1\nbox line 2",
			},
			wantParts:  []string{"Summary", "box line 1", "box line 2"},
			wantAbsent: []string{IconPass}, // no items rendered when box is set
		},
		{
			name: "node with box containing empty lines",
			node: TimelineNode{
				Title: "Details",
				Style: testStyle,
				Box:   "first\n\nlast",
			},
			wantParts: []string{"first", "last"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := RenderTimelineNode(tt.node)
			assert.NotEmpty(t, got)

			for _, part := range tt.wantParts {
				assert.Contains(t, got, part,
					"RenderTimelineNode output should contain %q", part)
			}
			for _, absent := range tt.wantAbsent {
				assert.NotContains(t, got, absent)
			}
		})
	}
}

func TestRenderTimeline(t *testing.T) {
	t.Parallel()

	testStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#00FF00"))

	tests := []struct {
		name      string
		nodes     []TimelineNode
		endLabel  string
		wantParts []string
	}{
		{
			name:      "empty timeline with end label",
			nodes:     nil,
			endLabel:  "Done",
			wantParts: []string{TimelineCircle, "Done"},
		},
		{
			name: "single node timeline",
			nodes: []TimelineNode{
				{Title: "Check", Style: testStyle},
			},
			endLabel:  "Complete",
			wantParts: []string{TimelineDot, "Check", TimelineCircle, "Complete"},
		},
		{
			name: "multiple nodes have connectors between them",
			nodes: []TimelineNode{
				{Title: "First", Style: testStyle},
				{Title: "Second", Style: testStyle},
				{Title: "Third", Style: testStyle},
			},
			endLabel:  "End",
			wantParts: []string{"First", "Second", "Third", "End"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := RenderTimeline(tt.nodes, tt.endLabel)
			assert.NotEmpty(t, got)

			for _, part := range tt.wantParts {
				assert.Contains(t, got, part)
			}

			// verify connectors exist between nodes
			if len(tt.nodes) > 1 {
				barCount := strings.Count(got, TimelineBar)
				// at least one bar per connector + bars for items
				assert.GreaterOrEqual(t, barCount, len(tt.nodes),
					"should have connector bars between nodes")
			}
		})
	}
}

func TestRenderTimeline_ConnectorCount(t *testing.T) {
	t.Parallel()

	testStyle := lipgloss.NewStyle()

	// with 3 nodes: 2 connectors between nodes + 1 connector before end = 3 connectors
	nodes := []TimelineNode{
		{Title: "A", Style: testStyle},
		{Title: "B", Style: testStyle},
		{Title: "C", Style: testStyle},
	}

	got := RenderTimeline(nodes, "End")

	// each connector is a line with just the bar character
	lines := strings.Split(got, "\n")
	connectorLines := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == TimelineBar || trimmed == MutedStyle.Render(TimelineBar) {
			connectorLines++
		}
	}

	// 2 between nodes + 1 before end = 3
	assert.Equal(t, 3, connectorLines,
		"3 nodes should produce 3 connector lines (between each + before end)")
}

func TestRenderTimelineNode_MultilineDetail(t *testing.T) {
	t.Parallel()

	node := TimelineNode{
		Title: "Verbose",
		Style: lipgloss.NewStyle(),
		Items: []TimelineItem{
			{
				Icon:   IconInfo,
				Style:  AccentStyle,
				Text:   "Details",
				Detail: "line one\nline two\nline three",
			},
		},
	}

	got := RenderTimelineNode(node)
	assert.Contains(t, got, "line one")
	assert.Contains(t, got, "line two")
	assert.Contains(t, got, "line three")
}
