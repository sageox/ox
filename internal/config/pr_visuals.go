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
}

const (
	DefaultPRVisualsRich = true

	PRVisualsThemeLight = "light"
	PRVisualsThemeDark  = "dark"

	DefaultPRVisualsTheme = PRVisualsThemeLight

	DefaultPRVisualsHeader = true
)

func isPRVisualsTheme(v string) bool {
	return v == PRVisualsThemeLight || v == PRVisualsThemeDark
}

// IsRichSet reports whether pr_visuals.rich was explicitly set.
func (c *PRVisualsConfig) IsRichSet() bool { return c != nil && c.Rich != nil }

// IsThemeSet reports whether pr_visuals.theme was explicitly set.
func (c *PRVisualsConfig) IsThemeSet() bool { return c != nil && c.Theme != nil }

// IsHeaderSet reports whether pr_visuals.header was explicitly set.
func (c *PRVisualsConfig) IsHeaderSet() bool { return c != nil && c.Header != nil }

// IsEmpty reports whether no PR visual setting is explicitly set.
func (c *PRVisualsConfig) IsEmpty() bool {
	return c == nil || (c.Rich == nil && c.Theme == nil && c.Header == nil)
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
