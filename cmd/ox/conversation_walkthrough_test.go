package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/conversation/read"
)

// TestWalkthroughTextNamesAreaAndMarks: --text output for an area take says
// "a screen area" rather than a blank window name, and prints presenter marks
// as their own lines, ahead of a click at the same instant.
// Failure prevented: a human reading --text sees "window:  (800x600)" and
// marks indistinguishable from clicks.
func TestWalkthroughTextNamesAreaAndMarks(t *testing.T) {
	root := repoPath("..", "..", "internal", "conversation", "read", "testdata", "walkthrough-area", "discussions")
	env := read.New(root, time.Time{}).Walkthrough("cnv_01a0f490-0000-7000-8000-0000000000d1", read.WalkthroughOptions{})
	if !env.Success {
		t.Fatalf("walkthrough failed: %+v", env.Error)
	}
	var buf bytes.Buffer
	renderConversationWalkthroughText(&buf, env)
	out := buf.String()

	for _, want := range []string{
		"area: a screen area (800x600)",
		"mark     marked by the presenter (#1)",
		"pointer outside the area",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\nwindow:") {
		t.Errorf("an area take must not print a window line:\n%s", out)
	}
	mark, click := strings.Index(out, "marked by the presenter (#2)"), strings.Index(out, `click    AXButton "Pay now"`)
	if mark < 0 || click < 0 || mark > click {
		t.Errorf("the mark at 00:00:04 must print before the click at the same instant:\n%s", out)
	}
}
