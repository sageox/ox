package ui

import (
	"fmt"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
)

// Timeline characters (circles - OpenClaw bubble style)
const (
	TimelineBar    = "│"
	TimelineDot    = "●"
	TimelineCircle = "○"
)

// TimelineNode represents a section on the vertical timeline
type TimelineNode struct {
	Title   string         // section name
	Style   lipgloss.Style // node circle color
	Items   []TimelineItem
	Summary string // optional collapsed summary like "2 passed"
	Box     string // optional: render a bordered box instead of items (set by caller)
}

// TimelineItem represents a single check within a timeline node
type TimelineItem struct {
	Icon      string         // one of IconPass, IconWarn, IconFail, IconSkip, IconInfo, IconAgent
	Style     lipgloss.Style // color for the icon
	Text      string         // check name
	Detail    string         // action hint (rendered dim, indented below)
	DetailRaw bool           // if true, detail is pre-styled; skip MutedStyle wrapping
	Badge     string         // e.g., "[auto-fix]", "[--fix]"
}

// RenderTimeline renders a complete timeline as a string.
// endLabel is the text for the final hollow circle node (e.g., "Done").
func RenderTimeline(nodes []TimelineNode, endLabel string) string {
	var b strings.Builder

	for i, node := range nodes {
		b.WriteString(RenderTimelineNode(node))

		if i < len(nodes)-1 {
			b.WriteString(RenderTimelineConnector())
		}
	}

	// connector before the end label
	if len(nodes) > 0 {
		b.WriteString(RenderTimelineConnector())
	}

	b.WriteString(RenderTimelineEnd(endLabel))

	return b.String()
}

// RenderTimelineNode renders a single node with its items.
// Used for streaming output in --fix mode.
func RenderTimelineNode(node TimelineNode) string {
	var b strings.Builder

	// render node header: ●  Title  or  ●  Title — Summary
	dot := node.Style.Render(TimelineDot)
	header := node.Title
	if node.Summary != "" {
		header = fmt.Sprintf("%s — %s", header, MutedStyle.Render(node.Summary))
	}
	fmt.Fprintf(&b, "%s  %s\n", dot, header)

	bar := MutedStyle.Render(TimelineBar)

	// if the node has a box, render it indented under the timeline bar
	if node.Box != "" {
		for _, line := range strings.Split(node.Box, "\n") {
			if line == "" {
				b.WriteString(bar + "\n")
			} else {
				b.WriteString(bar + "  " + line + "\n")
			}
		}
		return b.String()
	}

	for _, item := range node.Items {
		icon := item.Style.Render(item.Icon)

		line := fmt.Sprintf("%s   %s %s", bar, icon, item.Text)
		if item.Badge != "" {
			line += " " + MutedStyle.Render(item.Badge)
		}
		b.WriteString(line + "\n")

		if item.Detail != "" {
			for _, dline := range strings.Split(item.Detail, "\n") {
				if item.DetailRaw {
					fmt.Fprintf(&b, "%s     %s\n", bar, dline)
				} else {
					fmt.Fprintf(&b, "%s     %s\n", bar, MutedStyle.Render(dline))
				}
			}
		}
	}

	return b.String()
}

// RenderTimelineConnector renders just the "│" connector line.
func RenderTimelineConnector() string {
	return MutedStyle.Render(TimelineBar) + "\n"
}

// RenderTimelineEnd renders the closing hollow circle node.
func RenderTimelineEnd(label string) string {
	return fmt.Sprintf("%s  %s\n", MutedStyle.Render(TimelineCircle), MutedStyle.Render(label))
}
