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
//
// HTML and Open are deliberately SEPARATE axes, not one combined enum:
//   - HTML governs whether/when the enriched plan gets RENDERED to HTML at
//     all (off disables rendering entirely; recommend and always both render
//     on a Material plan — see cmd/ox's agent_hook_plan_nudge.go).
//   - Open governs whether/how the plan-exit nudge may instruct OPENING that
//     render in a browser, independent of whether it was rendered.
//
// Open only matters once HTML has already allowed a render/nudge to happen;
// HTML=off still short-circuits everything upstream of Open. Today HTML's
// recommend and always values behave identically (both render, neither
// auto-opens) — Open is the axis that actually varies the open behavior.
// Whether these two should eventually collapse into one model is an open
// design question, not resolved here (see PR description / handoff notes).
type PlanConfig struct {
	// Save auto-saves approved plans to the repo's ledger.
	// Default: true.
	Save *bool `yaml:"save,omitempty" json:"save,omitempty"`

	// HTML controls the enriched-HTML RENDER behavior as a tri-state:
	//   off       — never render, never nudge (wins over Open unconditionally).
	//   recommend — nudge on a material or non-trivial plan; a MATERIAL plan
	//               (real team-context signals) is also auto-rendered to HTML
	//               in the background, so it's ready the moment the Open
	//               policy allows surfacing it. (default)
	//   always    — currently identical to recommend. Reserved for a future
	//               stronger render tier.
	// Collapsing the prior pair of recommend/auto-render bools into one enum
	// removes the nonsensical "don't recommend but auto-render" combination.
	// Does NOT govern opening a browser — see Open.
	HTML *string `yaml:"html,omitempty" json:"html,omitempty"`

	// Open controls whether the plan-exit nudge may instruct OPENING a
	// rendered plan in a browser, as a tri-state:
	//   never  — no open or ask instruction, ever. Silent: the agent is told
	//            (at most) that a render exists and its slug, nothing more.
	//   ask    — the nudge directs the agent to confirm via AskUserQuestion
	//            before opening anything; opens only on explicit yes.
	//            (default) The only mode with an unconditional
	//            never-auto-open guarantee.
	//   always — the user has explicitly opted in to auto-open: the nudge
	//            instructs opening directly, no confirmation step. The ONLY
	//            mode where an open directive may appear without a preceding
	//            AskUserQuestion.
	// Does NOT govern rendering — see HTML. A human can set this from the
	// ask-mode prompt itself ("Always open" / "Never ask again" both run
	// `ox config set plan.open ...`), so the preference sticks without
	// editing config by hand.
	Open *string `yaml:"open,omitempty" json:"open,omitempty"`
}

// Plan setting defaults and enums. Centralized so the resolvers, the
// config-settings registry, and tests all agree on one source of truth.
const (
	DefaultPlanSave = true

	PlanHTMLOff       = "off"
	PlanHTMLRecommend = "recommend"
	PlanHTMLAlways    = "always"

	DefaultPlanHTML = PlanHTMLRecommend

	PlanOpenNever  = "never"
	PlanOpenAsk    = "ask"
	PlanOpenAlways = "always"

	DefaultPlanOpen = PlanOpenAsk
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

// isPlanOpenValue reports whether v is one of the three known plan.open enum
// values. Used to validate the env override before it can win precedence.
func isPlanOpenValue(v string) bool {
	switch v {
	case PlanOpenNever, PlanOpenAsk, PlanOpenAlways:
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

// IsOpenSet reports whether plan.open was explicitly set.
func (c *PlanConfig) IsOpenSet() bool {
	return c != nil && c.Open != nil
}

// IsEmpty reports whether no plan setting is explicitly set. Used by unset
// paths to nil out the struct once its last field is cleared.
func (c *PlanConfig) IsEmpty() bool {
	return c == nil || (c.Save == nil && c.HTML == nil && c.Open == nil)
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

// PlanOpen resolves the browser-open policy for the plan-exit nudge,
// returning one of PlanOpenNever / PlanOpenAsk / PlanOpenAlways. This axis
// governs ONLY whether/how the nudge may instruct OPENING an already-eligible
// render — rendering itself is governed entirely by PlanHTML plus the
// Material earned-gate (see cmd/ox's agent_hook_plan_nudge.go); PlanHTML=off
// still short-circuits everything upstream of this resolver.
// Precedence: env SAGEOX_PLAN_OPEN > user config > project config > default
// (PlanOpenAsk). Mirrors PlanHTML's resolution shape exactly. The env var is
// a DIRECT override (it must itself be a valid enum value); an unknown or
// empty env value falls through rather than forcing anything.
func PlanOpen(projectRoot string) string {
	if env := strings.TrimSpace(strings.ToLower(os.Getenv(EnvPlanOpen))); env != "" {
		if isPlanOpenValue(env) {
			return env
		}
		slog.Debug("plan: ignoring unrecognized env override", "env", EnvPlanOpen, "value", env)
	}
	userCfg, _ := LoadUserConfig()
	if userCfg != nil && userCfg.Plan.IsOpenSet() && isPlanOpenValue(*userCfg.Plan.Open) {
		return *userCfg.Plan.Open
	}
	if projectRoot != "" {
		if cfg, _ := LoadProjectConfig(projectRoot); cfg != nil && cfg.Plan.IsOpenSet() && isPlanOpenValue(*cfg.Plan.Open) {
			return *cfg.Plan.Open
		}
	}
	return DefaultPlanOpen
}
