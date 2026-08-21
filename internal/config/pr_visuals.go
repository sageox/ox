package config

// PRVisualsConfig holds the `pr_visuals.*` settings namespace. Pointer fields
// keep an explicit choice distinct from an inherited/default value.
type PRVisualsConfig struct {
	// Rich controls whether AI coworkers are guided to generate and upload
	// review-only rich PNG/SVG visuals for pull requests. It never disables the
	// ox viz catalog or deterministic suggestions. Default: true.
	Rich *bool `yaml:"rich,omitempty" json:"rich,omitempty" toml:"rich,omitempty"`

	// Theme selects the intended appearance of generated PR visuals. Values:
	// "light" (default) and "dark".
	Theme *string `yaml:"theme,omitempty" json:"theme,omitempty" toml:"theme,omitempty"`

	// Header controls whether `ox pr header` emits the top-of-PR SageOx credit
	// line (the human-facing counterpart to the SageOx-Session: trailer).
	// Default: true.
	Header *bool `yaml:"header,omitempty" json:"header,omitempty" toml:"header,omitempty"`

	// Style selects how the header's enrichment whisper renders: "text" (a
	// <sub> caption — always works, no hosting), "image" (a baked floated
	// light/dark strip — richer, needs an uploader), or "auto" (image when an
	// uploader is available, else text). Default: "auto".
	Style *string `yaml:"style,omitempty" json:"style,omitempty" toml:"style,omitempty"`
}

const (
	DefaultPRVisualsRich = true

	PRVisualsThemeLight = "light"
	PRVisualsThemeDark  = "dark"

	DefaultPRVisualsTheme = PRVisualsThemeLight

	DefaultPRVisualsHeader = true

	PRVisualsStyleText  = "text"
	PRVisualsStyleImage = "image"
	PRVisualsStyleAuto  = "auto"

	DefaultPRVisualsStyle = PRVisualsStyleAuto
)

func isPRVisualsTheme(v string) bool {
	return v == PRVisualsThemeLight || v == PRVisualsThemeDark
}

func isPRVisualsStyle(v string) bool {
	return v == PRVisualsStyleText || v == PRVisualsStyleImage || v == PRVisualsStyleAuto
}

// IsRichSet reports whether pr_visuals.rich was explicitly set.
func (c *PRVisualsConfig) IsRichSet() bool { return c != nil && c.Rich != nil }

// IsThemeSet reports whether pr_visuals.theme was explicitly set.
func (c *PRVisualsConfig) IsThemeSet() bool { return c != nil && c.Theme != nil }

// IsHeaderSet reports whether pr_visuals.header was explicitly set.
func (c *PRVisualsConfig) IsHeaderSet() bool { return c != nil && c.Header != nil }

// IsStyleSet reports whether pr_visuals.style was explicitly set.
func (c *PRVisualsConfig) IsStyleSet() bool { return c != nil && c.Style != nil }

// IsEmpty reports whether no PR visual setting is explicitly set.
func (c *PRVisualsConfig) IsEmpty() bool {
	return c == nil || (c.Rich == nil && c.Theme == nil && c.Header == nil && c.Style == nil)
}

// PRVisualsRich resolves the rich-PR-visual guidance policy.
// Precedence: user > repository > team > default.
func PRVisualsRich(projectRoot string) bool {
	userCfg, _ := LoadUserConfig()
	if userCfg != nil && userCfg.PRVisuals.IsRichSet() {
		return *userCfg.PRVisuals.Rich
	}
	if projectRoot != "" {
		if repoCfg, _ := LoadProjectConfig(projectRoot); repoCfg != nil && repoCfg.PRVisuals.IsRichSet() {
			return *repoCfg.PRVisuals.Rich
		}
		if tc := FindRepoTeamContext(projectRoot); tc != nil {
			if teamCfg, _ := LoadTeamConfig(tc.Path); teamCfg != nil && teamCfg.PRVisuals.IsRichSet() {
				return *teamCfg.PRVisuals.Rich
			}
		}
	}
	return DefaultPRVisualsRich
}

// PRVisualsTheme resolves the intended appearance of generated PR visuals.
// Precedence: user > repository > team > default.
func PRVisualsTheme(projectRoot string) string {
	userCfg, _ := LoadUserConfig()
	if userCfg != nil && userCfg.PRVisuals.IsThemeSet() && isPRVisualsTheme(*userCfg.PRVisuals.Theme) {
		return *userCfg.PRVisuals.Theme
	}
	if projectRoot != "" {
		if repoCfg, _ := LoadProjectConfig(projectRoot); repoCfg != nil && repoCfg.PRVisuals.IsThemeSet() && isPRVisualsTheme(*repoCfg.PRVisuals.Theme) {
			return *repoCfg.PRVisuals.Theme
		}
		if tc := FindRepoTeamContext(projectRoot); tc != nil {
			if teamCfg, _ := LoadTeamConfig(tc.Path); teamCfg != nil && teamCfg.PRVisuals.IsThemeSet() && isPRVisualsTheme(*teamCfg.PRVisuals.Theme) {
				return *teamCfg.PRVisuals.Theme
			}
		}
	}
	return DefaultPRVisualsTheme
}

// PRVisualsHeader resolves whether the top-of-PR credit line is emitted.
// Precedence: user > repository > team > default.
func PRVisualsHeader(projectRoot string) bool {
	userCfg, _ := LoadUserConfig()
	if userCfg != nil && userCfg.PRVisuals.IsHeaderSet() {
		return *userCfg.PRVisuals.Header
	}
	if projectRoot != "" {
		if repoCfg, _ := LoadProjectConfig(projectRoot); repoCfg != nil && repoCfg.PRVisuals.IsHeaderSet() {
			return *repoCfg.PRVisuals.Header
		}
		if tc := FindRepoTeamContext(projectRoot); tc != nil {
			if teamCfg, _ := LoadTeamConfig(tc.Path); teamCfg != nil && teamCfg.PRVisuals.IsHeaderSet() {
				return *teamCfg.PRVisuals.Header
			}
		}
	}
	return DefaultPRVisualsHeader
}

// PRVisualsStyle resolves the header's enrichment-whisper render style.
// Precedence: user > repository > team > default.
func PRVisualsStyle(projectRoot string) string {
	userCfg, _ := LoadUserConfig()
	if userCfg != nil && userCfg.PRVisuals.IsStyleSet() && isPRVisualsStyle(*userCfg.PRVisuals.Style) {
		return *userCfg.PRVisuals.Style
	}
	if projectRoot != "" {
		if repoCfg, _ := LoadProjectConfig(projectRoot); repoCfg != nil && repoCfg.PRVisuals.IsStyleSet() && isPRVisualsStyle(*repoCfg.PRVisuals.Style) {
			return *repoCfg.PRVisuals.Style
		}
		if tc := FindRepoTeamContext(projectRoot); tc != nil {
			if teamCfg, _ := LoadTeamConfig(tc.Path); teamCfg != nil && teamCfg.PRVisuals.IsStyleSet() && isPRVisualsStyle(*teamCfg.PRVisuals.Style) {
				return *teamCfg.PRVisuals.Style
			}
		}
	}
	return DefaultPRVisualsStyle
}
