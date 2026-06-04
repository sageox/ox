package config

import (
	"log/slog"
	"os"
	"strings"
)

// PlanConfig holds the `plan.*` settings namespace for the `ox plan` feature.
// Pointer fields distinguish "unset" (nil, fall through to the next precedence
// level / documented default) from an explicit value. This is what keeps a
// missing value resolving to the *documented* default rather than the Go
// zero-value, which would be a surprise for the true-by-default Save key.
type PlanConfig struct {
	// Save auto-saves approved plans to the repo's ledger.
	// Default: true.
	Save *bool `yaml:"save,omitempty" json:"save,omitempty"`

	// HTML controls the enriched-HTML render behavior as a tri-state:
	//   off       — never render, never nudge.
	//   recommend — prime nudges; render only on user confirm. (default)
	//   always    — auto-render without a confirm step.
	// Collapsing the prior pair of recommend/auto-render bools into one enum
	// removes the nonsensical "don't recommend but auto-render" combination.
	HTML *string `yaml:"html,omitempty" json:"html,omitempty"`
}

// Plan setting defaults and the html enum. Centralized so the resolvers, the
// config-settings registry, and tests all agree on one source of truth.
const (
	DefaultPlanSave = true

	PlanHTMLOff       = "off"
	PlanHTMLRecommend = "recommend"
	PlanHTMLAlways    = "always"

	DefaultPlanHTML = PlanHTMLRecommend
)

// isPlanHTMLValue reports whether v is one of the three known plan.html enum
// values. Used to validate the env override before it can win precedence.
func isPlanHTMLValue(v string) bool {
	switch v {
	case PlanHTMLOff, PlanHTMLRecommend, PlanHTMLAlways:
		return true
	default:
		return false
	}
}

// IsSaveSet reports whether plan.save was explicitly set.
func (c *PlanConfig) IsSaveSet() bool {
	return c != nil && c.Save != nil
}

// IsHTMLSet reports whether plan.html was explicitly set.
func (c *PlanConfig) IsHTMLSet() bool {
	return c != nil && c.HTML != nil
}

// IsEmpty reports whether no plan setting is explicitly set. Used by unset
// paths to nil out the struct once its last field is cleared.
func (c *PlanConfig) IsEmpty() bool {
	return c == nil || (c.Save == nil && c.HTML == nil)
}

// PlanSave resolves whether approved plans auto-save to the ledger.
// Precedence: user config > project config > default (true).
func PlanSave(projectRoot string) bool {
	userCfg, _ := LoadUserConfig()
	if userCfg != nil && userCfg.Plan.IsSaveSet() {
		return *userCfg.Plan.Save
	}
	if projectRoot != "" {
		if cfg, _ := LoadProjectConfig(projectRoot); cfg != nil && cfg.Plan.IsSaveSet() {
			return *cfg.Plan.Save
		}
	}
	return DefaultPlanSave
}

// PlanHTML resolves the enriched-HTML render mode, returning one of
// PlanHTMLOff / PlanHTMLRecommend / PlanHTMLAlways.
// Precedence: env SAGEOX_PLAN_HTML > user config > project config > default.
// The env var is a DIRECT override (it must itself be a valid enum value);
// an unknown or empty env value falls through rather than forcing anything.
func PlanHTML(projectRoot string) string {
	if env := strings.TrimSpace(strings.ToLower(os.Getenv(EnvPlanHTML))); env != "" {
		if isPlanHTMLValue(env) {
			return env
		}
		slog.Debug("plan: ignoring unrecognized env override", "env", EnvPlanHTML, "value", env)
	}
	userCfg, _ := LoadUserConfig()
	if userCfg != nil && userCfg.Plan.IsHTMLSet() && isPlanHTMLValue(*userCfg.Plan.HTML) {
		return *userCfg.Plan.HTML
	}
	if projectRoot != "" {
		if cfg, _ := LoadProjectConfig(projectRoot); cfg != nil && cfg.Plan.IsHTMLSet() && isPlanHTMLValue(*cfg.Plan.HTML) {
			return *cfg.Plan.HTML
		}
	}
	return DefaultPlanHTML
}
