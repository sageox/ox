package theme_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNoRawLipglossColorOutsideTUI keeps the contrast fix from eroding.
//
// lipgloss v2's Style.Render() always emits 24-bit color and leaves
// downsampling to the caller. ox degrades at the color instead (theme.Color /
// theme.Adapt), because its ~300 fmt.Print(style.Render(...)) call sites write
// past any colorprofile.Writer. A new raw lipgloss.Color("#…") in print-path
// code silently reintroduces the unreadable-background bug on every terminal
// without truecolor support.
//
// Two exemptions, both with a reason rather than a grandfather clause:
//   - internal/theme itself, which is where the adapter is implemented.
//   - Bubble Tea code, which downsamples its own frames. A file qualifies by
//     importing bubbletea, or by living under internal/dashboard — that whole
//     tree renders into the dashboard's Bubble Tea frame, including the leaf
//     renderers that import only lipgloss.
func TestNoRawLipglossColorOutsideTUI(t *testing.T) {
	root := repoRoot(t)

	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if name := info.Name(); name == ".git" || name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/theme/") || strings.HasPrefix(rel, "internal/dashboard/") {
			return nil
		}

		src, readErr := os.ReadFile(path) //nolint:gosec // walking our own tree
		if readErr != nil {
			return readErr
		}
		body := string(src)
		if strings.Contains(body, "charm.land/bubbletea/v2") {
			return nil
		}
		if strings.Contains(body, "lipgloss.Color(") {
			offenders = append(offenders, rel+" (lipgloss.Color)")
		}
		if tok := unadaptedToken(body); tok != "" {
			offenders = append(offenders, rel+" ("+tok+")")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	assert.Empty(t, offenders,
		"use theme.Color / theme.Adapt instead of lipgloss.Color in non-TUI code — "+
			"raw 24-bit color renders as an unreadable background block on terminals "+
			"without truecolor support (see internal/theme/profile.go)")
}

// adaptiveTokenRef matches a generated adaptive token (theme.ColorPrimary and
// friends) that is not already wrapped in theme.Adapt.
//
// These are the other half of the same bug and the half that is easy to miss:
// they carry 24-bit values just like a hex literal, but read as "already a theme
// color, therefore already safe". A second wordmark in internal/cli/styles.go
// survived the original sweep for exactly that reason.
var adaptiveTokenRef = regexp.MustCompile(
	`theme\.Color(?:Primary|Secondary|Accent|Success|Warning|Error|Info|Dim|Public|Private|WordmarkSage|WordmarkOx)\b`)

// unadaptedToken returns the first adaptive-token reference in src that is not
// wrapped in theme.Adapt, or "" if there is none. Comment lines are ignored so
// prose about a token does not trip the check.
func unadaptedToken(src string) string {
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		for _, m := range adaptiveTokenRef.FindAllStringIndex(line, -1) {
			if !strings.HasSuffix(line[:m[0]], "theme.Adapt(") {
				return line[m[0]:m[1]]
			}
		}
	}
	return ""
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		if filepath.Dir(dir) == dir {
			t.Fatalf("no go.mod above %s", wd)
		}
	}
}
