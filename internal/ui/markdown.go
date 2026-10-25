package ui

import (
	"encoding/json"
	"os"
	"sync"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"

	"github.com/sageox/ox/internal/theme"
)

// SageOxDarkStyleJSON defines a glamour style for dark terminals using SageOx brand colors.
// Colors reference the theme package constants for consistency.
var SageOxDarkStyleJSON = `{
	"document": {
		"block_prefix": "",
		"block_suffix": "",
		"margin": 0
	},
	"block_quote": {
		"indent": 2,
		"color": "` + theme.HexMuted + `",
		"indent_token": "│ "
	},
	"paragraph": {},
	"list": {
		"level_indent": 2
	},
	"heading": {
		"block_suffix": "\n",
		"color": "` + theme.HexAccent + `",
		"bold": true
	},
	"h1": {
		"prefix": "# ",
		"color": "` + theme.HexAccent + `",
		"bold": true
	},
	"h2": {
		"prefix": "## ",
		"color": "` + theme.HexAccent + `",
		"bold": true
	},
	"h3": {
		"prefix": "### ",
		"color": "` + theme.HexAccent + `"
	},
	"h4": {
		"prefix": "#### ",
		"color": "` + theme.HexAccent + `"
	},
	"h5": {
		"prefix": "##### ",
		"color": "` + theme.HexMuted + `"
	},
	"h6": {
		"prefix": "###### ",
		"color": "` + theme.HexMuted + `"
	},
	"text": {},
	"strikethrough": {
		"crossed_out": true
	},
	"emph": {
		"italic": true,
		"color": "` + theme.HexWarn + `"
	},
	"strong": {
		"bold": true,
		"color": "` + theme.HexText + `"
	},
	"hr": {
		"color": "` + theme.HexMuted + `",
		"format": "──────────────────────────────────────────"
	},
	"item": {
		"block_prefix": "• "
	},
	"enumeration": {
		"block_prefix": ". "
	},
	"task": {
		"ticked": "[✓] ",
		"unticked": "[ ] "
	},
	"link": {
		"color": "` + theme.HexAccent + `",
		"underline": true
	},
	"link_text": {
		"color": "` + theme.HexAccent + `"
	},
	"image": {
		"color": "` + theme.HexAccent + `"
	},
	"image_text": {
		"color": "` + theme.HexWarn + `"
	},
	"code": {
		"color": "` + theme.HexWarn + `",
		"background_color": "` + theme.HexBgCode + `"
	},
	"code_block": {
		"color": "` + theme.HexText + `",
		"margin": 2,
		"chroma": {
			"text": { "color": "` + theme.HexText + `" },
			"error": { "color": "` + theme.HexFail + `" },
			"comment": { "color": "` + theme.HexMuted + `" },
			"comment_preproc": { "color": "` + theme.HexMuted + `" },
			"keyword": { "color": "` + theme.HexAccent + `" },
			"keyword_reserved": { "color": "` + theme.HexAccent + `" },
			"keyword_namespace": { "color": "` + theme.HexAccent + `" },
			"keyword_type": { "color": "` + theme.HexAccent + `" },
			"operator": { "color": "` + theme.HexText + `" },
			"punctuation": { "color": "` + theme.HexMuted + `" },
			"name": { "color": "` + theme.HexText + `" },
			"name_builtin": { "color": "` + theme.HexAccent + `" },
			"name_tag": { "color": "` + theme.HexAccent + `" },
			"name_attribute": { "color": "` + theme.HexPrimary + `" },
			"name_class": { "color": "` + theme.HexPrimary + `", "bold": true },
			"name_constant": { "color": "` + theme.HexWarn + `" },
			"name_decorator": { "color": "` + theme.HexWarn + `" },
			"name_exception": { "color": "` + theme.HexFail + `" },
			"name_function": { "color": "` + theme.HexPrimary + `" },
			"name_other": { "color": "` + theme.HexText + `" },
			"literal": { "color": "` + theme.HexWarn + `" },
			"literal_number": { "color": "` + theme.HexWarn + `" },
			"literal_date": { "color": "` + theme.HexWarn + `" },
			"literal_string": { "color": "` + theme.HexPrimary + `" },
			"literal_string_escape": { "color": "` + theme.HexWarn + `" },
			"generic_deleted": { "color": "` + theme.HexFail + `" },
			"generic_emph": { "italic": true },
			"generic_inserted": { "color": "` + theme.HexPrimary + `" },
			"generic_strong": { "bold": true },
			"generic_subheading": { "color": "` + theme.HexAccent + `" },
			"background": { "background_color": "` + theme.HexBgDark + `" }
		}
	},
	"table": {
		"center_separator": "┼",
		"column_separator": "│",
		"row_separator": "─"
	},
	"definition_list": {},
	"definition_term": {
		"color": "` + theme.HexAccent + `",
		"bold": true
	},
	"definition_description": {
		"block_prefix": "  "
	},
	"html_block": {},
	"html_span": {}
}`

// SageOxLightStyleJSON defines a glamour style for light terminals using SageOx brand colors.
// Adjusted for light backgrounds with appropriate contrast.
var SageOxLightStyleJSON = `{
	"document": {
		"block_prefix": "",
		"block_suffix": "",
		"margin": 0
	},
	"block_quote": {
		"indent": 2,
		"color": "` + theme.HexLightMuted + `",
		"indent_token": "│ "
	},
	"paragraph": {},
	"list": {
		"level_indent": 2
	},
	"heading": {
		"block_suffix": "\n",
		"color": "` + theme.HexLightAccent + `",
		"bold": true
	},
	"h1": {
		"prefix": "# ",
		"color": "` + theme.HexLightAccent + `",
		"bold": true
	},
	"h2": {
		"prefix": "## ",
		"color": "` + theme.HexLightAccent + `",
		"bold": true
	},
	"h3": {
		"prefix": "### ",
		"color": "` + theme.HexLightAccent + `"
	},
	"h4": {
		"prefix": "#### ",
		"color": "` + theme.HexLightAccent + `"
	},
	"h5": {
		"prefix": "##### ",
		"color": "` + theme.HexLightMuted + `"
	},
	"h6": {
		"prefix": "###### ",
		"color": "` + theme.HexLightMuted + `"
	},
	"text": {},
	"strikethrough": {
		"crossed_out": true
	},
	"emph": {
		"italic": true,
		"color": "` + theme.HexLightWarn + `"
	},
	"strong": {
		"bold": true,
		"color": "` + theme.HexLightText + `"
	},
	"hr": {
		"color": "` + theme.HexLightMuted + `",
		"format": "──────────────────────────────────────────"
	},
	"item": {
		"block_prefix": "• "
	},
	"enumeration": {
		"block_prefix": ". "
	},
	"task": {
		"ticked": "[✓] ",
		"unticked": "[ ] "
	},
	"link": {
		"color": "` + theme.HexLightAccent + `",
		"underline": true
	},
	"link_text": {
		"color": "` + theme.HexLightAccent + `"
	},
	"image": {
		"color": "` + theme.HexLightAccent + `"
	},
	"image_text": {
		"color": "` + theme.HexLightWarn + `"
	},
	"code": {
		"color": "` + theme.HexLightWarn + `",
		"background_color": "` + theme.HexLightBgCode + `"
	},
	"code_block": {
		"color": "` + theme.HexLightText + `",
		"margin": 2,
		"chroma": {
			"text": { "color": "` + theme.HexLightText + `" },
			"error": { "color": "` + theme.HexLightFail + `" },
			"comment": { "color": "` + theme.HexLightMuted + `" },
			"comment_preproc": { "color": "` + theme.HexLightMuted + `" },
			"keyword": { "color": "` + theme.HexLightAccent + `" },
			"keyword_reserved": { "color": "` + theme.HexLightAccent + `" },
			"keyword_namespace": { "color": "` + theme.HexLightAccent + `" },
			"keyword_type": { "color": "` + theme.HexLightAccent + `" },
			"operator": { "color": "` + theme.HexLightText + `" },
			"punctuation": { "color": "` + theme.HexLightMuted + `" },
			"name": { "color": "` + theme.HexLightText + `" },
			"name_builtin": { "color": "` + theme.HexLightAccent + `" },
			"name_tag": { "color": "` + theme.HexLightAccent + `" },
			"name_attribute": { "color": "` + theme.HexLightPrimary + `" },
			"name_class": { "color": "` + theme.HexLightPrimary + `", "bold": true },
			"name_constant": { "color": "` + theme.HexLightWarn + `" },
			"name_decorator": { "color": "` + theme.HexLightWarn + `" },
			"name_exception": { "color": "` + theme.HexLightFail + `" },
			"name_function": { "color": "` + theme.HexLightPrimary + `" },
			"name_other": { "color": "` + theme.HexLightText + `" },
			"literal": { "color": "` + theme.HexLightWarn + `" },
			"literal_number": { "color": "` + theme.HexLightWarn + `" },
			"literal_date": { "color": "` + theme.HexLightWarn + `" },
			"literal_string": { "color": "` + theme.HexLightPrimary + `" },
			"literal_string_escape": { "color": "` + theme.HexLightWarn + `" },
			"generic_deleted": { "color": "` + theme.HexLightFail + `" },
			"generic_emph": { "italic": true },
			"generic_inserted": { "color": "` + theme.HexLightPrimary + `" },
			"generic_strong": { "bold": true },
			"generic_subheading": { "color": "` + theme.HexLightAccent + `" },
			"background": { "background_color": "` + theme.HexLightBgLight + `" }
		}
	},
	"table": {
		"center_separator": "┼",
		"column_separator": "│",
		"row_separator": "─"
	},
	"definition_list": {},
	"definition_term": {
		"color": "` + theme.HexLightAccent + `",
		"bold": true
	},
	"definition_description": {
		"block_prefix": "  "
	},
	"html_block": {},
	"html_span": {}
}`

var (
	sageoxDarkStyle      *ansi.StyleConfig
	sageoxDarkStyleOnce  sync.Once
	sageoxLightStyle     *ansi.StyleConfig
	sageoxLightStyleOnce sync.Once
)

// GetSageOxDarkStyle returns the SageOx dark theme style configuration.
func GetSageOxDarkStyle() ansi.StyleConfig {
	sageoxDarkStyleOnce.Do(func() {
		var style ansi.StyleConfig
		if err := json.Unmarshal([]byte(SageOxDarkStyleJSON), &style); err == nil {
			sageoxDarkStyle = &style
		}
	})
	if sageoxDarkStyle == nil {
		return ansi.StyleConfig{}
	}
	return *sageoxDarkStyle
}

// GetSageOxLightStyle returns the SageOx light theme style configuration.
func GetSageOxLightStyle() ansi.StyleConfig {
	sageoxLightStyleOnce.Do(func() {
		var style ansi.StyleConfig
		if err := json.Unmarshal([]byte(SageOxLightStyleJSON), &style); err == nil {
			sageoxLightStyle = &style
		}
	})
	if sageoxLightStyle == nil {
		return ansi.StyleConfig{}
	}
	return *sageoxLightStyle
}

// GetSageOxStyle returns the appropriate SageOx style based on terminal background.
// Uses lipgloss to detect if the terminal has a dark or light background.
func GetSageOxStyle() ansi.StyleConfig {
	if terminalHasDarkBackground(os.Stdin, os.Stdout) {
		return GetSageOxDarkStyle()
	}
	return GetSageOxLightStyle()
}

// terminalHasDarkBackground reports whether the terminal background is dark,
// defaulting to dark when the terminal cannot be interrogated.
//
// lipgloss.HasDarkBackground is only able to honor its own 2s timeout when
// stdin is the handle it will read the reply from. On Windows it substitutes
// CONIN$ (and CONOUT$) whenever stdin or stdout is not a console, and the
// reader it then gets back cannot cancel an in-flight ReadConsole — Cancel()
// only takes effect on the *next* read — so the query blocks forever instead
// of timing out. Any non-console stdin therefore hangs the process: a cmd/ox
// subprocess test and internal/ui both died at the Go test timeout on the
// Windows runner with a goroutine parked there, internal/uicatalog was still
// parked in it when the job hit its own 120-minute limit, and Git Bash or an
// AI coworker capturing output hangs the same way, which makes `ox guide` and
// `ox doctor` unusable on Windows.
//
// Only query when stdin is a console, which is exactly the condition under
// which lipgloss reads from the handle it was given — and stdin being a
// console is also what makes uv's cancelable reader (GetConsoleMode succeeds,
// fd matches os.Stdin) the one it picks, so the timeout works again. Anything
// else gets the dark default, which is what HasDarkBackground already returns
// when its query fails.
func terminalHasDarkBackground(in, out *os.File) bool {
	if !term.IsTerminal(in.Fd()) {
		return true
	}
	return lipgloss.HasDarkBackground(in, out)
}

// NewMarkdownRenderer creates a glamour renderer with SageOx branding.
// Automatically selects dark or light theme based on terminal background.
func NewMarkdownRenderer() (*glamour.TermRenderer, error) {
	return glamour.NewTermRenderer(
		glamour.WithStyles(GetSageOxStyle()),
		glamour.WithWordWrap(80),
	)
}

// RenderMarkdown renders markdown text with SageOx branding.
// Automatically selects dark or light theme based on terminal background.
// Returns the original text if rendering fails (graceful degradation).
func RenderMarkdown(text string) string {
	r, err := NewMarkdownRenderer()
	if err != nil {
		return text
	}

	out, err := r.Render(text)
	if err != nil {
		return text
	}

	return out
}
