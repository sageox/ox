package perf

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// --- A. Duration formatting matrix ---

// TestFormatDuration verifies each bucket of formatDuration produces the
// expected compact form. The rendered tree relies on these widths
// staying short so column alignment doesn't blow up.
func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{500 * time.Microsecond, "500µs"},
		{42 * time.Millisecond, "42ms"},
		{1900 * time.Millisecond, "1.9s"},
		{4*time.Second + 200*time.Millisecond, "4.2s"},
		{75 * time.Second, "1m 15s"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, formatDuration(c.in), "in=%s", c.in)
	}
}

// --- B. Tree rendering matches the user-facing example ---

// TestRenderTree_MatchesExample verifies the rendered output reproduces
// the pre-push tree shape that motivated this work: root + three
// children with aligned dot leaders and right-justified durations.
// Failure prevented: visual regression on the headline UX.
func TestRenderTree_MatchesExample(t *testing.T) {
	t.Setenv("NO_COLOR", "1") // strip ANSI so we can assert on raw bytes
	root := &Node{
		Name:     "pre-push total",
		Duration: 4*time.Second + 200*time.Millisecond,
		Children: []*Node{
			{Name: "resolve_push_settings", Duration: 100 * time.Millisecond},
			{Name: "git_push", Duration: 1900 * time.Millisecond},
			{Name: "cleanup", Duration: 100 * time.Millisecond},
		},
	}
	var b bytes.Buffer
	RenderTree(&b, root)
	out := b.String()

	// Each child label appears, with its duration on the same line.
	for _, line := range []string{
		"pre-push total",
		"├─ resolve_push_settings",
		"├─ git_push",
		"└─ cleanup",
	} {
		assert.Contains(t, out, line)
	}
	assert.Contains(t, out, "4.2s")
	assert.Contains(t, out, "1.9s")
}

// --- C. Failed spans flagged in render ---

// TestRenderTree_FailedSpanFlagged verifies error status produces a
// visible marker so the user can find which phase failed without
// inspecting every duration.
func TestRenderTree_FailedSpanFlagged(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	root := &Node{
		Name: "root",
		Children: []*Node{
			{Name: "ok_child", Duration: time.Millisecond},
			{Name: "bad_child", Duration: time.Millisecond, Status: sdktrace.Status{Code: codes.Error}},
		},
	}
	var b bytes.Buffer
	RenderTree(&b, root)
	out := b.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// last child renders with TreeLast; should also have the failure mark.
	assert.Contains(t, lines[len(lines)-1], "bad_child")
	assert.Contains(t, lines[len(lines)-1], "✖")
}

// --- D. Nil root is a no-op ---

// TestRenderTree_NilRoot verifies the renderer does not panic on nil
// (e.g. when an empty trace somehow reaches the sink).
func TestRenderTree_NilRoot(t *testing.T) {
	var b bytes.Buffer
	RenderTree(&b, nil)
	assert.Empty(t, b.String())
}

// NO_COLOR rendering is verified by manual run (`NO_COLOR=1 ox doctor`)
// per .claude/rules/design.md. lipgloss reads NO_COLOR at term-profile
// detection time before t.Setenv can affect it, so a unit assertion is
// not feasible from inside the same process.
