package carts

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// FormatIssueRow formats an issue as a single-line row for table display.
func FormatIssueRow(i *Issue) string {
	assignee := ""
	if i.Assignee != "" {
		assignee = fmt.Sprintf(" @%s", i.Assignee)
	}
	return fmt.Sprintf("%-12s P%d %-12s %-10s %s%s",
		i.ID, i.Priority, i.Status, i.IssueType, i.Title, assignee)
}

// FormatIssueDetail formats an issue for detailed display.
func FormatIssueDetail(i *Issue) string {
	var b strings.Builder

	fmt.Fprintf(&b, "ID:       %s\n", i.ID)
	fmt.Fprintf(&b, "Title:    %s\n", i.Title)
	fmt.Fprintf(&b, "Status:   %s\n", i.Status)
	fmt.Fprintf(&b, "Priority: P%d\n", i.Priority)
	fmt.Fprintf(&b, "Type:     %s\n", i.IssueType)

	if i.Assignee != "" {
		fmt.Fprintf(&b, "Assignee: %s\n", i.Assignee)
	}
	if i.Creator != "" {
		fmt.Fprintf(&b, "Creator:  %s\n", i.Creator)
	}

	fmt.Fprintf(&b, "Created:  %s\n", i.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "Updated:  %s\n", i.UpdatedAt.Format(time.RFC3339))

	if i.ClosedAt != nil {
		fmt.Fprintf(&b, "Closed:   %s\n", i.ClosedAt.Format(time.RFC3339))
	}
	if i.Description != "" {
		fmt.Fprintf(&b, "\nDescription:\n%s\n", i.Description)
	}

	return b.String()
}

// FormatIssueJSON returns the JSON representation of an issue.
func FormatIssueJSON(i *Issue) ([]byte, error) {
	return json.MarshalIndent(i, "", "  ")
}

// FormatIssueListJSON returns the JSON representation of a list of issues.
func FormatIssueListJSON(issues []*Issue) ([]byte, error) {
	if issues == nil {
		issues = []*Issue{}
	}
	return json.MarshalIndent(issues, "", "  ")
}
