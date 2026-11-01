package main

import (
	"fmt"
	"math"
	"os"
	"strconv"

	"github.com/sageox/ox/internal/config"
)

// ConfigLevel represents where a config setting can be stored.
type ConfigLevel string

const (
	ConfigLevelUser    ConfigLevel = "user"
	ConfigLevelRepo    ConfigLevel = "repo"
	ConfigLevelTeam    ConfigLevel = "team"
	ConfigLevelDefault ConfigLevel = "default"
	// ConfigLevelEnv is display-only: it identifies an env-var override in
	// ConfigValue.Source (e.g. OX_SESSION_PUBLISHING) so 'ox config get'
	// shows the value actually in effect. It is not a storage location —
	// there is nothing to set/unset "at env level" — so it never appears in
	// ConfigSetting.Levels and SetConfigValue/UnsetConfigValue don't handle
	// it.
	ConfigLevelEnv ConfigLevel = "env"
)

// ConfigSetting defines a configurable setting.
type ConfigSetting struct {
	Key             string        // setting key (e.g., "session_recording")
	Label           string        // short user-facing name (e.g., "Session recording")
	Description     string        // one plain-language sentence for list views and 'ox config schema'
	LongDescription string        // detailed explanation for help
	Category        string        // grouping category (e.g., "Sessions", "Privacy")
	ValidValues     []string      // allowed values (empty = any string)
	Default         string        // default value
	Levels          []ConfigLevel // which levels support this setting

	// Number marks a free-text setting that must be a number in
	// [Number.Min, Number.Max]. 'ox config set' validates against it and
	// 'ox config schema' publishes it, so the two cannot disagree.
	Number *NumberRange

	// Deprecated marks a setting kept only for backward compatibility: it
	// remains resolvable and settable (existing config files and scripts
	// must not break), but is either not wired to any behavior or is an old
	// name that reads and writes its replacement's value. Never wire new
	// behavior to a deprecated setting — remove the deprecation instead.
	Deprecated bool
	// DeprecationNotice is the honest, specific explanation surfaced by
	// 'ox config get/set/list' for a Deprecated setting: it must say what
	// the setting actually does (that it never took effect, not merely that
	// it's "no longer used"; or that it is a renamed alias), and name the
	// setting that does the job. Empty unless Deprecated is true.
	DeprecationNotice string
}

// NumberRange is the inclusive range a numeric setting accepts.
type NumberRange struct {
	Min, Max float64
}

// ConfigValue represents a resolved config value with its source.
type ConfigValue struct {
	Key     string      `json:"key"`
	Value   string      `json:"value"`
	Source  ConfigLevel `json:"source"`
	Default string      `json:"default,omitempty"`
	EnvVal  string      `json:"env_value,omitempty"` // highest priority; only populated for session_publishing/session_recording today (extending to every setting is tracked separately as ox-8d4t)
	UserVal string      `json:"user_value,omitempty"`
	RepoVal string      `json:"repo_value,omitempty"`
	TeamVal string      `json:"team_value,omitempty"`
}

// AllSettings defines all configurable settings.
var AllSettings = []ConfigSetting{
	{
		Key:         "session_recording",
		Label:       "Session recording",
		Description: "Whether AI coding sessions are recorded to the ledger, and when recording starts.",
		LongDescription: `Controls whether and how AI agent sessions are recorded.

  disabled - No sessions are recorded
  manual   - Recording starts only when you run 'ox session start'
  auto     - Recording starts automatically when an agent begins work

Sessions are saved to this repo's ledger (created by 'ox init'). Each
repository has its own ledger containing session history for that repo.

Team Context (shared across repos) is separate - it contains team
conventions and knowledge that apply to ALL repos your team owns.`,
		Category:    "Sessions",
		ValidValues: []string{"disabled", "manual", "auto"},
		Default:     "auto",
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo, ConfigLevelTeam},
	},
	{
		Key:         "session_publishing",
		Label:       "Session publishing",
		Description: "Whether a recorded session is published to the ledger, or held locally until you upload it explicitly.",
		LongDescription: `Controls whether a recorded session is published to the ledger, or held
locally until you upload it explicitly.

  auto   - Publish the session to this repo's ledger on session stop (default).
  manual - Hold the session locally only: no transcript upload, no mid-session
           draft placeholder, and no session-started notice to the server.
           Run 'ox session upload' to publish it explicitly. Because the
           server never hears about a manual-mode session, any /c/<id> link
           for it will 404 — surfaces that would print one stay silent
           instead in manual mode.

Precedence (highest wins): OX_SESSION_PUBLISHING env var > user config >
this repo's .sageox/config.json > team config > default(auto). User config
deliberately beats repo and team, so you can always decline to publish
regardless of what the repo or team says.

Note: telemetry, OTLP tracing, and the friction-feedback channel are
separate switches with their own defaults — this setting does not affect
them.`,
		Category:    "Sessions",
		ValidValues: config.ValidSessionPublishingModes,
		Default:     config.SessionPublishingAuto,
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo, ConfigLevelTeam},
	},
	{
		Key:         "murmur_send",
		Label:       "Send murmurs",
		Description: "Whether AI coworkers are nudged to share work-in-progress signals with teammates.",
		LongDescription: `Controls work-in-progress signals to teammates.

Murmurs are short coordination signals that let teammates know what
AI coworkers are working on before PRs or commits appear. Signals
propagate via the ledger and are delivered as whispers to active
coworkers.

  manual - Murmurs via 'ox murmur' only
  auto   - Daemon also nudges agents to murmur periodically (~15 min) (default)`,
		Category:    "Collaboration",
		ValidValues: []string{"manual", "auto"},
		Default:     "auto",
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo},
	},
	{
		Key:         "murmur_receive",
		Label:       "Receive murmurs",
		Description: "Whether teammates' work-in-progress signals reach your AI coworker.",
		LongDescription: `Controls whether work-in-progress signals from other coworkers
appear in your whisper stream.

  on  - Receive murmurs as whispers (default)
  off - Suppress murmur whispers (other coworkers still receive them)

This is a personal preference — it only affects YOUR whisper
delivery, not whether murmurs are relayed for others.`,
		Category:    "Collaboration",
		ValidValues: []string{"on", "off"},
		Default:     "on",
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo},
	},
	{
		Key:         "telemetry",
		Label:       "Usage telemetry",
		Description: "Send usage data that helps improve ox. Never includes your code or session conversations.",
		LongDescription: `Controls the usage data ox sends to help improve it.

When on, ox sends:
  - To PostHog, once per command: its name and the names of its
    flags; whether it succeeded and, if it failed, a category such
    as auth or network and which failure it was, named by ox's own
    wording or the error's type with no values filled in; how long
    it took; the ox version, OS, and CPU architecture; whether a
    person, an AI coworker (and which one), or CI ran it; why ox
    runs in ephemeral mode, if it does; and, inside a repository set
    up for SageOx, the repository's and team's SageOx IDs. Events
    carry a random install ID from the client_id file in ox's config
    directory (~/.config/sageox by default). When signed in to
    production, they also carry your SageOx user ID. No names,
    emails or tokens are sent. Deleting client_id gives a new
    install ID, but does not change your person ID.
  - To SageOx: command, session, and AI coworker events tagged with
    the repository's SageOx ID; timing traces while you are logged
    in; and what was typed when ox cannot parse a command.

Telemetry never includes your code, file contents, or session
conversations. Nothing sent to PostHog includes command arguments,
file paths, or values from an error message.

DO_NOT_TRACK=1 turns all of it off, the same as setting this to off.`,
		Category:    "Privacy",
		ValidValues: []string{"on", "off"},
		Default:     "on",
		Levels:      []ConfigLevel{ConfigLevelUser},
	},
	{
		Key:         "disposable_machine",
		Label:       "Disposable machine",
		Description: "This machine runs one job and goes away; only its home directory persists.",
		LongDescription: `Turn on for a machine that runs ox for one job and then goes away,
keeping only its home directory, such as a pod with a persistent home
volume. Leave it off on a laptop or a long-lived server.

When on:
  - The daemon's first start on a machine removes the Ledger's git lock
    files, which a machine that went away mid-command leaves behind.
  - The daemon runs no periodic auto-fix and no clone garbage collection.
  - The daemon restarts once the Ledger's first clone lands, so the work
    that needs a Ledger starts.
  - A recording stores when its AI coworker's process started, so a
    process on a later machine that reuses the same PID is not taken
    for it.
  - Git commands that ox runs in its own clones (the Ledger, team
    contexts, Knowledge Bubbles) run no hooks and get credentials only
    from ox.

The daemon reads this setting when it starts: run 'ox daemon restart'
after changing it.`,
		Category:    "Machine",
		ValidValues: []string{"on", "off"},
		Default:     "off",
		Levels:      []ConfigLevel{ConfigLevelUser},
	},
	{
		Key:         "tips",
		Label:       "Tips",
		Description: "Show contextual tips after command output.",
		LongDescription: `Controls whether ox shows contextual tips and suggestions.

When enabled, ox displays helpful hints about features and workflows
after command output. Useful for learning ox, but can be disabled
once you're familiar with the tool.`,
		Category:    "Display",
		ValidValues: []string{"on", "off"},
		Default:     "on",
		Levels:      []ConfigLevel{ConfigLevelUser},
	},
	{
		Key:         "statusline",
		Label:       "Status line",
		Description: "Show how much team context shaped your session in Claude Code's status line.",
		LongDescription: `Controls ox's row in Claude Code's status line.

When enabled, the row counts the team context that influenced this AI
coworker session — "● ox  3 discussions · 1 plan · 1 murmur" — with a red
"● rec" while the session records. Turn it off to remove the row.`,
		Category:    "Display",
		ValidValues: []string{"on", "off"},
		Default:     "on",
		Levels:      []ConfigLevel{ConfigLevelUser},
	},
	{
		Key:         "session_report_pane",
		Label:       "Session report pane",
		Description: "Show the /ox-session-report pane in Claude Code (installs the ox-cli-brief-pane plugin)",
		LongDescription: `Controls whether ox keeps the /ox-session-report pane installed in Claude Code.

The pane lists this session's ox calls under the ox session report summary.
It is the Claude Code plugin ox-cli-brief-pane@ox-cli-brief, installed at
user scope; /ox-session-report opens it and /ox-session-report off closes it.

  on  - ox installs the plugin (ox init, ox doctor --fix, and right away
        when you set this) and keeps it on this ox's version (default).
  off - ox removes the plugin and its files.

Replaces brief_pane, which it falls back to when unset.`,
		Category:    "Display",
		ValidValues: []string{"on", "off"},
		Default:     "on",
		Levels:      []ConfigLevel{ConfigLevelUser},
	},
	{
		Key:         "brief_pane",
		Label:       "Brief pane (deprecated)",
		Description: "DEPRECATED — renamed to session_report_pane",
		LongDescription: `DEPRECATED: brief_pane is the old name of session_report_pane, from before
` + "`ox brief`" + ` became ` + "`ox session report`" + `. It still reads and sets the same value, so
existing config files and scripts keep working.

Use instead:
    ox config set session_report_pane on|off`,
		Category:          "Display",
		ValidValues:       []string{"on", "off"},
		Default:           "on",
		Levels:            []ConfigLevel{ConfigLevelUser},
		Deprecated:        true,
		DeprecationNotice: "brief_pane is renamed to session_report_pane; it still sets the same value.\nUse instead:\n    ox config set session_report_pane on|off",
	},
	{
		Key:         "recap.trace_consultations",
		Label:       "Trace consultations",
		Description: "Record what your AI coworker read and which ox calls it made.",
		LongDescription: `Controls whether ox records, in your own session trace, which team
knowledge your AI coworker consulted and which ox commands it ran (with the
size of what each returned). The trace syncs to your team's Ledger with the
session, so this is the switch for whether your reads reach the team.

  on  - Record consultations and ox calls (default). Powers ox recap and
        the per-session usage report.
  off - Record neither. Context ox delivered (team context at prime,
        teammate whispers) is still noted, since that is ox's own output.

Only refs are ever recorded — a command's subcommand words, a doc path, a
query subject — never a murmur body, prompt text, or command output.`,
		Category:    "Privacy",
		ValidValues: []string{"on", "off"},
		Default:     "on",
		Levels:      []ConfigLevel{ConfigLevelUser},
	},
	{
		Key:         "context_git.auto_commit",
		Label:       "Auto-commit sessions (deprecated)",
		Description: "DEPRECATED — never had any effect; use session_publishing",
		LongDescription: `DEPRECATED: this setting has never had any effect. Sessions were committed
to the ledger regardless of what it was set to — it was never wired to any
behavior.

To hold sessions locally until you publish them explicitly, use:
    ox config set session_publishing manual

The value is still accepted and stored, so 'ox config set/get/unset' keep
working for scripts and older habits, but it does not — and never did —
control anything. See bead ox-6p5y.11.`,
		Category:          "Sessions",
		ValidValues:       []string{"on", "off"},
		Default:           "on",
		Levels:            []ConfigLevel{ConfigLevelUser},
		Deprecated:        true,
		DeprecationNotice: "context_git.auto_commit has never had any effect — sessions were committed regardless of this setting.\nTo hold sessions locally until you publish them explicitly, use:\n    ox config set session_publishing manual",
	},
	{
		Key:         "context_git.auto_push",
		Label:       "Auto-push sessions (deprecated)",
		Description: "DEPRECATED — never had any effect; use session_publishing",
		LongDescription: `DEPRECATED: this setting has never had any effect. Sessions were published
to the ledger regardless of what it was set to — it was never wired to any
behavior.

To hold sessions locally until you publish them explicitly, use:
    ox config set session_publishing manual

The value is still accepted and stored, so 'ox config set/get/unset' keep
working for scripts and older habits, but it does not — and never did —
control anything. See bead ox-6p5y.11.`,
		Category:          "Sessions",
		ValidValues:       []string{"on", "off"},
		Default:           "on",
		Levels:            []ConfigLevel{ConfigLevelUser},
		Deprecated:        true,
		DeprecationNotice: "context_git.auto_push has never had any effect — sessions were published regardless of this setting.\nTo hold sessions locally until you publish them explicitly, use:\n    ox config set session_publishing manual",
	},
	{
		Key:         "view_format",
		Label:       "Session view format",
		Description: "Default output format for 'ox session view'.",
		LongDescription: `Controls the default output format for 'ox session view'.

  html - Opens session in browser with rich HTML viewer (default)
  text - Renders session as markdown in terminal
  json - Outputs structured JSON (useful for scripting)

Override per-invocation with --html, --text, or --json flags.`,
		Category:    "Display",
		ValidValues: []string{"html", "text", "json"},
		Default:     "html",
		Levels:      []ConfigLevel{ConfigLevelUser},
	},
	{
		Key:         "session.draft",
		Label:       "Early session link",
		Category:    "Sessions",
		Description: "Publish a placeholder so a session's link works while it is still recording.",
		LongDescription: `Controls whether ox commits an early placeholder for a recording.

  on  — At turn 2, ox commits a metadata-only placeholder to the ledger so
        https://<endpoint>/c/<session_id> resolves while the session is still
        running. Agents put that link in PR bodies during a session; without
        the placeholder it stays a dead link until session stop, which makes a
        working setup look broken. The placeholder carries NO conversation
        data — only the session id, the agent, and a turn counter — and is
        replaced wholesale by the real recording at session stop.
        **Default.**

  off — Never publish a placeholder. The /c/ link stays unresolvable until
        session stop.

Aborting a session (ox agent session abort --force) deletes any published placeholder from
the ledger.`,
		ValidValues: config.ValidSessionDraftModes,
		Default:     config.SessionDraftOn,
		Levels:      []ConfigLevel{ConfigLevelUser},
	},
	{
		Key:         "agent.summarizer",
		Label:       "Session summarizer",
		Description: "Who writes the session summary at stop: your AI coworker, the background daemon, or nobody.",
		LongDescription: `Selects who runs the LLM call that summarizes a session at stop.

  inline    — The calling agent (Claude Code, Cursor, etc.) runs the LLM
              in its already-warm prompt cache. Cheap (input tokens are
              mostly cache reads). Blocks the user in the foreground for
              ~30–120s while the agent finishes. **Default.**

  delegated — The daemon runs the LLM in a fresh subprocess against the
              cached raw.jsonl. Every call is a *cold* prompt — input
              tokens are paid in full, roughly 10× the cost of inline on
              the same session. The only thing this buys you is getting
              your terminal back immediately at session-stop.

  off       — Skip LLM summarization entirely. Sessions are still uploaded
              to the ledger, but team-context surfacing and search will
              be degraded for these recordings.

  cloud     — Reserved for future SageOx cloud-side summarization. Not
              yet implemented; rejected by 'ox config set'.

Default is 'inline' because the cost asymmetry is too large to default
to 'delegated'. Switch to 'delegated' only if you want session-stop to
return immediately and you're OK paying the token cost on every close.

When the SessionEnd hook fires (Claude Code exit), summarization always
goes through 'delegated' regardless of this setting — at that moment the
calling agent process is being torn down, so 'inline' has no agent to
whisper back to.

See ADR-016 for full rationale.`,
		Category:    "Sessions",
		ValidValues: []string{"inline", "delegated", "off"}, // "cloud" intentionally absent — reserved
		Default:     "inline",
		Levels:      []ConfigLevel{ConfigLevelUser},
	},
	{
		Key:         "agent_worker",
		Label:       "Background AI coworker",
		Description: "Which AI coworker CLI the daemon uses for background tasks, such as filling in missing summaries.",
		LongDescription: `Selects which agent CLI the daemon uses for background tasks like
session anti-entropy (generating missing summaries for orphaned sessions).

  auto   — auto-detect from PATH (default). Prefers claude > codex.
  none   — explicitly disabled. Only lightweight cleanup runs.
  claude — use Claude Code CLI for background agent work.
  codex  — use OpenAI Codex CLI for background agent work.

When an agent is available (configured or auto-detected), the daemon
automatically detects incomplete sessions and spawns the agent to
generate summaries, then uploads them to the ledger.
Rate-limited to 60 invocations/hour, 4 concurrent.`,
		Category:    "Sessions",
		ValidValues: []string{"auto", "none", "claude", "codex"},
		Default:     "auto",
		Levels:      []ConfigLevel{ConfigLevelUser},
	},
	{
		Key:         "recording_reminder",
		Label:       "Recording reminders",
		Description: "Periodically remind AI coworkers that the session is being recorded.",
		LongDescription: `Controls whether AI coworkers receive periodic reminders
that their session is being recorded.

  on  - Whisper recording status (turn count, duration) periodically (default)
  off - No periodic reminders (startup banner still appears)

Useful for confirming sessions are actively being captured, especially
during long-running sessions. Reminders appear roughly once per hour
as whispers in the agent's context.`,
		Category:    "Sessions",
		ValidValues: []string{"on", "off"},
		Default:     "on",
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo},
	},
	{
		Key:         "plan.save",
		Label:       "Save plans",
		Description: "Automatically save approved plans to the ledger.",
		LongDescription: `Controls whether 'ox plan' auto-saves the enriched plan to this repo's
ledger.

  on  - Persist finalized plans to data/plans/ (default). Plans become
        first-class ledger artifacts: searchable, attributable, reusable.
  off - Never auto-save. 'ox plan save' (the explicit persist path used by
        the html-plan skill) still saves regardless of this setting.`,
		Category:    "Plans",
		ValidValues: []string{"on", "off"},
		Default:     "on",
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo},
	},
	{
		Key:         "plan.html",
		Label:       "Plan HTML pages",
		Description: "When ox renders an enriched HTML page for a plan.",
		LongDescription: `Controls when 'ox plan' renders an enriched HTML plan for human review.

  off       - Never render, never nudge.
  recommend - Prime nudges for a render on complex/material plans; a MATERIAL
              plan (real team-context signals) is also rendered to HTML in
              the background automatically. (default)
  always    - Currently identical to recommend.

This setting governs RENDERING only. Whether the nudge may then instruct
OPENING that render in a browser is the separate 'plan.open' setting.

Override per-invocation with the SAGEOX_PLAN_HTML env var (same three
values).`,
		Category:    "Plans",
		ValidValues: []string{config.PlanHTMLOff, config.PlanHTMLRecommend, config.PlanHTMLAlways},
		Default:     config.DefaultPlanHTML,
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo},
	},
	{
		Key:         "plan.open",
		Label:       "Open rendered plans",
		Description: "Whether a rendered plan may open in your browser, and whether to ask first.",
		LongDescription: `Controls whether the plan-exit nudge may instruct opening a rendered
plan in a browser. Independent of 'plan.html' (which controls rendering) —
this setting only matters once a render/nudge would already happen; 'plan.html
off' always wins and suppresses both.

  never  - No open or ask instruction, ever. The agent is told (at most) that
           a render exists and its slug — nothing more.
  ask    - The nudge directs the agent to confirm via AskUserQuestion before
           opening anything; opens only on an explicit yes. (default)
  always - You've opted in to auto-open: the nudge instructs opening
           directly, no confirmation step.

The AskUserQuestion posed in 'ask' mode offers "Always open from now on" and
"Never ask again" options, which set this value for you.

Override per-invocation with the SAGEOX_PLAN_OPEN env var (same three
values).`,
		Category:    "Plans",
		ValidValues: []string{config.PlanOpenNever, config.PlanOpenAsk, config.PlanOpenAlways},
		Default:     config.DefaultPlanOpen,
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo},
	},
	{
		Key:         "skills.claude_org_dedupe",
		Label:       "Skip skills your Claude org delivers",
		Description: "Stop copying team skills into .claude/skills when your Claude organization's plugin already gives them to Claude Code here.",
		LongDescription: `Controls whether ox skips its own copy of a team skill in .claude/skills
when Claude Code on this machine already gets that skill from your team's
Claude organization plugin, so Claude Code does not list it twice.

  on  - Skip a team skill only when Claude Code here is signed in to the
        claude.ai account that has your organization's plugin, and that
        plugin carries the skill. Anything ox cannot confirm keeps its
        copy. (default)
  off - Always copy every team skill into .claude/skills.

Turn this off if your Claude Code sessions sign in some way ox cannot see
from Claude Code's files, such as an API key, Bedrock or Vertex set only in
your shell. Those sessions never load organization plugins, so without
ox's copy they would have no team skills at all.

Other AI coworkers (Codex, Cursor, ...) always get ox's copies.`,
		Category:    "Skills",
		ValidValues: []string{"on", "off"},
		Default:     "on",
		Levels:      []ConfigLevel{ConfigLevelUser},
	},
	{
		Key:         "pr_visuals.rich",
		Label:       "Rich PR visuals",
		Description: "Guide AI coworkers to make rich images for pull requests.",
		LongDescription: `Controls whether AI coworkers are guided to generate rich, review-only
PNG/SVG visuals for GitHub pull requests.

  on  - Offer and generate rich HTML/SVG-derived visuals for comparisons,
        architecture, flows, and other review-relevant explanations. PNGs are
        uploaded through GitHub's editor and never committed to the branch.
        (default)
  off - Do not use the automated rich PNG/SVG PR-image workflow. The complete
        ox viz catalog and ` + "`ox viz suggest`" + ` remain available for plans, docs,
        reports, and manual authoring; use a small GitHub-safe Mermaid diagram
        in the PR only when it materially improves the review.

Set this at team, repository, or personal scope. Personal choices override a
repository choice; repository choices override a team default.`,
		Category:    "Pull requests",
		ValidValues: []string{"on", "off"},
		Default:     "on",
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo, ConfigLevelTeam},
	},
	{
		Key:         "pr_visuals.theme",
		Label:       "PR visual theme",
		Description: "Light or dark appearance for pull-request visuals.",
		LongDescription: `Selects the intended appearance of rich GitHub pull-request visuals.

  light - White/light canvas with SageOx accents. Default: aligns with GitHub's
          reading surface and keeps a PR visual integrated with its discussion.
  dark  - Dark canvas with SageOx accents. Use when the visual is intentionally
          presented as a distinct artifact.

This affects the design brief given to AI coworkers; it does not change
GitHub's own color scheme. Set it at team, repository, or personal scope.`,
		Category:    "Pull requests",
		ValidValues: []string{config.PRVisualsThemeLight, config.PRVisualsThemeDark},
		Default:     config.DefaultPRVisualsTheme,
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo, ConfigLevelTeam},
	},
	{
		Key:         "pr_visuals.header",
		Label:       "PR credit line",
		Description: "Add the SageOx credit line to the top of pull requests.",
		LongDescription: `Controls whether the ox pr header command emits the top-of-PR SageOx credit
line — the human-facing counterpart to the SageOx-Session: trailer. It links the
session(s) and plan(s) that produced the change, names the team, and whispers a
subtle enrichment stat.

  on  - Emit the credit line for AI coworker PRs. (default)
  off - Do not emit it; ox pr header no-ops with a hint.

Set at team, repository, or personal scope. Personal choices override a
repository choice; repository choices override a team default.`,
		Category:    "Pull requests",
		ValidValues: []string{"on", "off"},
		Default:     boolToOnOff(config.DefaultPRVisualsHeader),
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo, ConfigLevelTeam},
	},
	// NOTE: attribution.plan and attribution.session are intentionally not exposed
	// in ox config — they are always-on transparency requirements, not user preferences.
	{
		Key:         "attribution.commit",
		Label:       "Commit attribution",
		Description: "Git trailer added to commits made with an AI coworker. Empty turns it off.",
		LongDescription: `Controls the git trailer added to commits made with AI coworker assistance.

Any coding agent that touches your code and is deeply involved in the plan
for execution is as much responsible for the resulting code as the human is.
Attribution makes this shared responsibility visible in your git history.

  Set to any string to customize the commit trailer.
  Set to "" (empty) to disable commit attribution entirely.
  Unset to restore the default.

The default format is recognized by GitHub for contributor profile links.

Default: "Co-Authored-By: SageOx <ox@sageox.ai>"`,
		Category:    "Attribution",
		ValidValues: []string{}, // free-text: any string allowed, "" disables
		Default:     "Co-Authored-By: SageOx <ox@sageox.ai>",
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo},
	},
	{
		Key:         "attribution.pr",
		Label:       "PR attribution",
		Description: "Attribution line added to pull request descriptions. Empty turns it off.",
		LongDescription: `Controls the attribution line added to pull request descriptions.

When team context shapes the code in a PR — architecture decisions,
conventions, prior session learnings — attribution makes that influence
visible to reviewers. It signals that this work was informed by more
than one contributor's knowledge.

  Set to any string to customize the PR attribution.
  Set to "" (empty) to disable PR attribution entirely.
  Unset to restore the default.

Default: "Co-Authored-By: [SageOx](https://github.com/SageOx)"`,
		Category:    "Attribution",
		ValidValues: []string{}, // free-text: any string allowed, "" disables
		Default:     "Co-Authored-By: [SageOx](https://github.com/SageOx)",
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo},
	},
	{
		Key:         "hooks.userpromptsubmit.cloud_query",
		Label:       "Cloud recall on prompts",
		Description: "Also search SageOx cloud for context on each prompt. Sends redacted prompt text.",
		LongDescription: `Controls whether the UserPromptSubmit hook may issue a remote SageOx
query IN ADDITION to the always-on local query.

  off - Default. Zero network calls happen on the prompt path, regardless
        of any other setting. Local-ledger query only.
  on  - Run the local query AND a parallel cloud query (when authenticated).
        Cloud results are tagged '[ox-recall:remote]' so the source is
        visible. If 'ox login' has not run, this silently degrades to
        local-only — the prompt never errors.

Tradeoff: enabling improves recall (the server-side index sees more than
the local cached ledger), at the cost of sending REDACTED prompt text to
the configured SageOx endpoint. Prompt content always passes through the
secrets-redaction pipeline before any byte leaves the machine —
redaction is non-negotiable, not a separate toggle.`,
		Category:    "Privacy",
		ValidValues: []string{"on", "off"},
		Default:     "off",
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo},
	},
	{
		Key:         "attribution.score_threshold",
		Label:       "Attribution threshold",
		Description: "Minimum SageOx contribution score (0.0 to 1.0) before a commit is attributed.",
		LongDescription: `Controls the threshold at which SageOx earns commit attribution.

AI coworkers self-report how much SageOx team context influenced their work
on a 0.0-1.0 scale. When the score meets or exceeds this threshold, the
commit hook adds the Co-Authored-By trailer.

  Set to a value between 0.0 and 1.0.
  Set to 0.0 to always attribute (when a score is reported).
  Set to 1.0 to only attribute when SageOx was critically influential.
  Unset to restore the default.

Default: 0.5`,
		Category:    "Attribution",
		ValidValues: []string{},
		Number:      &NumberRange{Min: 0, Max: 1},
		Default:     "0.5",
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo},
	},
}

// boolToOnOff renders a bool as "on"/"off" for config display.
func boolToOnOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// GetSetting returns the setting definition for a key.
func GetSetting(key string) *ConfigSetting {
	for i := range AllSettings {
		if AllSettings[i].Key == key {
			return &AllSettings[i]
		}
	}
	return nil
}

// ResolveConfigValue resolves a config value from all levels.
// Priority: User > Repo > Team > Default
func ResolveConfigValue(key string, projectRoot string) (*ConfigValue, error) {
	setting := GetSetting(key)
	if setting == nil {
		return nil, fmt.Errorf("unknown setting: %s", key)
	}

	cv := &ConfigValue{
		Key:     key,
		Default: setting.Default,
	}

	// load user config
	userCfg, _ := config.LoadUserConfig()

	// load repo config
	var repoCfg *config.ProjectConfig
	if projectRoot != "" {
		repoCfg, _ = config.LoadProjectConfig(projectRoot)
	}

	// load team config (repo's own team, not cross-team)
	var teamCfg *config.TeamConfig
	if projectRoot != "" {
		if tc := config.FindRepoTeamContext(projectRoot); tc != nil {
			teamCfg, _ = config.LoadTeamConfig(tc.Path)
		}
	}

	// resolve based on key
	switch key {
	case "session_recording":
		// OX_SESSION_RECORDING beats every stored layer at runtime
		// (config.ResolveSessionRecording) — display must agree, or 'ox
		// config get session_recording' shows a value that isn't in effect.
		if envMode := os.Getenv(config.EnvSessionRecording); envMode != "" {
			cv.EnvVal = config.NormalizeSessionRecording(envMode)
		}
		if userCfg != nil && userCfg.Sessions != nil {
			mode := userCfg.Sessions.GetMode()
			if mode != "" && mode != "none" {
				cv.UserVal = config.NormalizeSessionRecording(mode)
			}
		}
		if repoCfg != nil && repoCfg.SessionRecording != "" {
			cv.RepoVal = config.NormalizeSessionRecording(repoCfg.SessionRecording)
		}
		if teamCfg != nil && teamCfg.SessionRecording != "" {
			cv.TeamVal = config.NormalizeSessionRecording(teamCfg.SessionRecording)
		}

	case "session_publishing":
		// OX_SESSION_PUBLISHING beats every stored layer at runtime
		// (config.ResolveSessionPublishing) — display must agree, since this
		// is exactly the privacy control a coworker runs 'ox config get' on
		// to check their posture.
		if envMode := os.Getenv(config.EnvSessionPublishing); envMode != "" {
			cv.EnvVal = config.NormalizeSessionPublishing(envMode)
		}
		if userCfg != nil && userCfg.SessionPublishing != "" {
			cv.UserVal = config.NormalizeSessionPublishing(userCfg.SessionPublishing)
		}
		if repoCfg != nil && repoCfg.SessionPublishing != "" {
			cv.RepoVal = config.NormalizeSessionPublishing(repoCfg.SessionPublishing)
		}
		if teamCfg != nil && teamCfg.SessionPublishing != "" {
			cv.TeamVal = config.NormalizeSessionPublishing(teamCfg.SessionPublishing)
		}

	case "context_git.auto_commit":
		// DEPRECATED (bead ox-6p5y.11) — never wired to any behavior. Kept
		// resolvable only so existing config files/scripts don't break; see
		// the catalog entry's DeprecationNotice for the user-facing message.
		if userCfg != nil && userCfg.ContextGit != nil && userCfg.ContextGit.AutoCommit != nil {
			if *userCfg.ContextGit.AutoCommit {
				cv.UserVal = "on"
			} else {
				cv.UserVal = "off"
			}
		}

	case "context_git.auto_push":
		// DEPRECATED (bead ox-6p5y.11) — never wired to any behavior. Kept
		// resolvable only so existing config files/scripts don't break; see
		// the catalog entry's DeprecationNotice for the user-facing message.
		if userCfg != nil && userCfg.ContextGit != nil && userCfg.ContextGit.AutoPush != nil {
			if *userCfg.ContextGit.AutoPush {
				cv.UserVal = "on"
			} else {
				cv.UserVal = "off"
			}
		}

	case "murmur_send":
		if userCfg != nil && userCfg.GetMurmuring() != "" {
			cv.UserVal = config.NormalizeMurmuring(userCfg.GetMurmuring())
		}
		if repoCfg != nil && repoCfg.GetMurmuring() != "" {
			cv.RepoVal = config.NormalizeMurmuring(repoCfg.GetMurmuring())
		}

	case "murmur_receive":
		if userCfg != nil && userCfg.MurmurReceive != "" {
			cv.UserVal = config.NormalizeMurmurReceive(userCfg.MurmurReceive)
		}
		if repoCfg != nil && repoCfg.MurmurReceive != "" {
			cv.RepoVal = config.NormalizeMurmurReceive(repoCfg.MurmurReceive)
		}

	case "recording_reminder":
		if userCfg != nil && userCfg.RecordingReminder != "" {
			cv.UserVal = config.NormalizeRecordingReminder(userCfg.RecordingReminder)
		}
		if repoCfg != nil && repoCfg.RecordingReminder != "" {
			cv.RepoVal = config.NormalizeRecordingReminder(repoCfg.RecordingReminder)
		}

	case "telemetry":
		if userCfg != nil {
			if userCfg.IsTelemetryEnabled() {
				cv.UserVal = "on"
			} else if userCfg.TelemetryEnabled != nil {
				cv.UserVal = "off"
			}
		}

	case "tips":
		if userCfg != nil {
			if userCfg.AreTipsEnabled() {
				cv.UserVal = "on"
			} else if userCfg.TipsEnabled != nil {
				cv.UserVal = "off"
			}
		}

	case "statusline":
		// Unlike tips, only an explicit value counts as a user layer, so an
		// unset key reports source "default" rather than a phantom "user".
		if userCfg != nil && userCfg.StatuslineEnabled != nil {
			cv.UserVal = boolToOnOff(*userCfg.StatuslineEnabled)
		}

	case "session_report_pane", "brief_pane":
		// only an explicit value counts as a user layer (see statusline);
		// brief_pane is session_report_pane's deprecated name, and an old
		// brief_pane_enabled still counts until session_report_pane is set
		if v := userCfg.SessionReportPaneSetting(); v != nil {
			cv.UserVal = boolToOnOff(*v)
		}

	case "recap.trace_consultations":
		// only an explicit value counts as a user layer (see statusline)
		if userCfg != nil && userCfg.Recap != nil && userCfg.Recap.TraceConsultations != nil {
			cv.UserVal = boolToOnOff(*userCfg.Recap.TraceConsultations)
		}

	case "disposable_machine":
		if userCfg != nil && userCfg.DisposableMachine != nil {
			cv.UserVal = boolToOnOff(*userCfg.DisposableMachine)
		}

	case "skills.claude_org_dedupe":
		if userCfg != nil && userCfg.Skills != nil && userCfg.Skills.ClaudeOrgDedupe != nil {
			cv.UserVal = boolToOnOff(*userCfg.Skills.ClaudeOrgDedupe)
		}

	case "view_format":
		if userCfg != nil && userCfg.ViewFormat != "" {
			cv.UserVal = userCfg.ViewFormat
		}

	case "agent_worker":
		if userCfg != nil && userCfg.AgentWorker != nil {
			agent := userCfg.AgentWorker.GetAgent()
			switch agent {
			case "":
				// not configured — auto-detect (don't set UserVal, use default)
			case "none":
				cv.UserVal = "none"
			default:
				cv.UserVal = agent
			}
		}

	case "agent.summarizer":
		if userCfg != nil && userCfg.AgentSummarizer != "" {
			cv.UserVal = config.NormalizeAgentSummarizer(userCfg.AgentSummarizer)
		}

	case "session.draft":
		if userCfg != nil && userCfg.SessionDraft != "" {
			cv.UserVal = config.NormalizeSessionDraft(userCfg.SessionDraft)
		}

	case "attribution.commit":
		if userCfg != nil && userCfg.Attribution != nil && userCfg.Attribution.IsCommitSet() {
			if v := userCfg.Attribution.GetCommit(); v == "" {
				cv.UserVal = "(disabled)"
			} else {
				cv.UserVal = v
			}
		}
		if repoCfg != nil && repoCfg.Attribution != nil && repoCfg.Attribution.IsCommitSet() {
			if v := repoCfg.Attribution.GetCommit(); v == "" {
				cv.RepoVal = "(disabled)"
			} else {
				cv.RepoVal = v
			}
		}

	case "attribution.pr":
		if userCfg != nil && userCfg.Attribution != nil && userCfg.Attribution.IsPRSet() {
			if v := userCfg.Attribution.GetPR(); v == "" {
				cv.UserVal = "(disabled)"
			} else {
				cv.UserVal = v
			}
		}
		if repoCfg != nil && repoCfg.Attribution != nil && repoCfg.Attribution.IsPRSet() {
			if v := repoCfg.Attribution.GetPR(); v == "" {
				cv.RepoVal = "(disabled)"
			} else {
				cv.RepoVal = v
			}
		}

	case "attribution.score_threshold":
		if userCfg != nil && userCfg.Attribution != nil && userCfg.Attribution.IsScoreThresholdSet() {
			cv.UserVal = strconv.FormatFloat(userCfg.Attribution.GetScoreThreshold(), 'f', -1, 64)
		}
		if repoCfg != nil && repoCfg.Attribution != nil && repoCfg.Attribution.IsScoreThresholdSet() {
			cv.RepoVal = strconv.FormatFloat(repoCfg.Attribution.GetScoreThreshold(), 'f', -1, 64)
		}

	case "hooks.userpromptsubmit.cloud_query":
		if userCfg != nil && userCfg.Hooks != nil && userCfg.Hooks.UserPromptSubmit != nil && userCfg.Hooks.UserPromptSubmit.CloudQuery != nil {
			cv.UserVal = boolToOnOff(*userCfg.Hooks.UserPromptSubmit.CloudQuery)
		}
		if repoCfg != nil && repoCfg.Hooks != nil && repoCfg.Hooks.UserPromptSubmit != nil && repoCfg.Hooks.UserPromptSubmit.CloudQuery != nil {
			cv.RepoVal = boolToOnOff(*repoCfg.Hooks.UserPromptSubmit.CloudQuery)
		}

	case "plan.save":
		if userCfg != nil && userCfg.Plan.IsSaveSet() {
			cv.UserVal = boolToOnOff(*userCfg.Plan.Save)
		}
		if repoCfg != nil && repoCfg.Plan.IsSaveSet() {
			cv.RepoVal = boolToOnOff(*repoCfg.Plan.Save)
		}

	case "plan.html":
		if userCfg != nil && userCfg.Plan.IsHTMLSet() {
			cv.UserVal = *userCfg.Plan.HTML
		}
		if repoCfg != nil && repoCfg.Plan.IsHTMLSet() {
			cv.RepoVal = *repoCfg.Plan.HTML
		}

	case "plan.open":
		if userCfg != nil && userCfg.Plan.IsOpenSet() {
			cv.UserVal = *userCfg.Plan.Open
		}
		if repoCfg != nil && repoCfg.Plan.IsOpenSet() {
			cv.RepoVal = *repoCfg.Plan.Open
		}

	case "pr_visuals.rich":
		if userCfg != nil && userCfg.PRVisuals.IsRichSet() {
			cv.UserVal = boolToOnOff(*userCfg.PRVisuals.Rich)
		}
		if repoCfg != nil && repoCfg.PRVisuals.IsRichSet() {
			cv.RepoVal = boolToOnOff(*repoCfg.PRVisuals.Rich)
		}
		if teamCfg != nil && teamCfg.PRVisuals.IsRichSet() {
			cv.TeamVal = boolToOnOff(*teamCfg.PRVisuals.Rich)
		}

	case "pr_visuals.theme":
		if userCfg != nil && userCfg.PRVisuals.IsThemeSet() {
			cv.UserVal = *userCfg.PRVisuals.Theme
		}
		if repoCfg != nil && repoCfg.PRVisuals.IsThemeSet() {
			cv.RepoVal = *repoCfg.PRVisuals.Theme
		}
		if teamCfg != nil && teamCfg.PRVisuals.IsThemeSet() {
			cv.TeamVal = *teamCfg.PRVisuals.Theme
		}

	case "pr_visuals.header":
		if userCfg != nil && userCfg.PRVisuals.IsHeaderSet() {
			cv.UserVal = boolToOnOff(*userCfg.PRVisuals.Header)
		}
		if repoCfg != nil && repoCfg.PRVisuals.IsHeaderSet() {
			cv.RepoVal = boolToOnOff(*repoCfg.PRVisuals.Header)
		}
		if teamCfg != nil && teamCfg.PRVisuals.IsHeaderSet() {
			cv.TeamVal = boolToOnOff(*teamCfg.PRVisuals.Header)
		}

	}

	// determine effective value and source (Env > User > Repo > Team > Default).
	// EnvVal is only ever populated for session_publishing/session_recording
	// (see their cases above) — every other setting falls through untouched.
	if cv.EnvVal != "" {
		cv.Value = cv.EnvVal
		cv.Source = ConfigLevelEnv
	} else if cv.UserVal != "" {
		cv.Value = cv.UserVal
		cv.Source = ConfigLevelUser
	} else if cv.RepoVal != "" {
		cv.Value = cv.RepoVal
		cv.Source = ConfigLevelRepo
	} else if cv.TeamVal != "" {
		cv.Value = cv.TeamVal
		cv.Source = ConfigLevelTeam
	} else {
		cv.Value = cv.Default
		cv.Source = ConfigLevelDefault
	}

	return cv, nil
}

// UnsetConfigValue clears a config value at a specific level, causing it to
// fall back to the next level in the precedence chain (user > repo > team > default).
func UnsetConfigValue(key string, level ConfigLevel, projectRoot string) error {
	setting := GetSetting(key)
	if setting == nil {
		return fmt.Errorf("unknown setting: %s", key)
	}

	levelSupported := false
	for _, l := range setting.Levels {
		if l == level {
			levelSupported = true
			break
		}
	}
	if !levelSupported {
		return fmt.Errorf("setting %s cannot be set at %s level", key, level)
	}

	switch level {
	case ConfigLevelUser:
		if err := unsetUserConfig(key); err != nil {
			return err
		}
		if key == "statusline" {
			applyStatuslineSetting()
		}
		return nil
	case ConfigLevelRepo:
		return unsetRepoConfig(key, projectRoot)
	case ConfigLevelTeam:
		return unsetTeamConfig(key, projectRoot)
	default:
		return fmt.Errorf("cannot unset config at %s level", level)
	}
}

// SetConfigValue sets a config value at a specific level.
func SetConfigValue(key, value string, level ConfigLevel, projectRoot string) error {
	setting := GetSetting(key)
	if setting == nil {
		return fmt.Errorf("unknown setting: %s", key)
	}

	// validate value — numeric settings use their range, others use ValidValues list
	if r := setting.Number; r != nil {
		f, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(f) || f < r.Min || f > r.Max { // NaN passes both comparisons
			return fmt.Errorf("invalid value %q for %s: must be a number between %.1f and %.1f", value, key, r.Min, r.Max)
		}
	} else if len(setting.ValidValues) > 0 {
		valid := false
		for _, v := range setting.ValidValues {
			if v == value {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid value %q for %s: valid values are %v", value, key, setting.ValidValues)
		}
	}

	// check if level is supported for this setting
	levelSupported := false
	for _, l := range setting.Levels {
		if l == level {
			levelSupported = true
			break
		}
	}
	if !levelSupported {
		return fmt.Errorf("setting %s cannot be set at %s level", key, level)
	}

	switch level {
	case ConfigLevelUser:
		if err := setUserConfig(key, value); err != nil {
			return err
		}
		if key == "statusline" {
			applyStatuslineSetting()
		}
		return nil
	case ConfigLevelRepo:
		return setRepoConfig(key, value, projectRoot)
	case ConfigLevelTeam:
		return setTeamConfig(key, value, projectRoot)
	default:
		return fmt.Errorf("cannot set config at %s level", level)
	}
}

func setUserConfig(key, value string) error {
	cfg, err := config.LoadUserConfig()
	if err != nil {
		cfg = &config.UserConfig{}
	}

	switch key {
	case "session_recording":
		if cfg.Sessions == nil {
			cfg.Sessions = &config.SessionsConfig{}
		}
		cfg.Sessions.Mode = value

	case "session_publishing":
		cfg.SessionPublishing = value

	case "context_git.auto_commit":
		// DEPRECATED (bead ox-6p5y.11) — accepted and stored for backward
		// compatibility only. Never wire this to behavior; see
		// SetConfigValue's deprecation-notice print and session_publishing.
		if cfg.ContextGit == nil {
			cfg.ContextGit = &config.ContextGitConfig{}
		}
		enabled := value == "on"
		cfg.ContextGit.AutoCommit = &enabled

	case "context_git.auto_push":
		// DEPRECATED (bead ox-6p5y.11) — accepted and stored for backward
		// compatibility only. Never wire this to behavior; see
		// SetConfigValue's deprecation-notice print and session_publishing.
		if cfg.ContextGit == nil {
			cfg.ContextGit = &config.ContextGitConfig{}
		}
		enabled := value == "on"
		cfg.ContextGit.AutoPush = &enabled

	case "telemetry":
		enabled := value == "on"
		cfg.TelemetryEnabled = &enabled

	case "tips":
		enabled := value == "on"
		cfg.TipsEnabled = &enabled

	case "statusline":
		enabled := value == "on"
		cfg.StatuslineEnabled = &enabled

	case "session_report_pane", "brief_pane":
		// both names write session_report_pane_enabled and drop the old key,
		// so the two can never disagree
		enabled := value == "on"
		cfg.SessionReportPaneEnabled = &enabled
		cfg.BriefPaneEnabled = nil

	case "recap.trace_consultations":
		enabled := value == "on"
		if cfg.Recap == nil {
			cfg.Recap = &config.RecapConfig{}
		}
		cfg.Recap.TraceConsultations = &enabled

	case "disposable_machine":
		enabled := value == "on"
		cfg.DisposableMachine = &enabled

	case "skills.claude_org_dedupe":
		if cfg.Skills == nil {
			cfg.Skills = &config.SkillsConfig{}
		}
		enabled := value == "on"
		cfg.Skills.ClaudeOrgDedupe = &enabled

	case "view_format":
		cfg.ViewFormat = value

	case "murmur_send":
		cfg.SetMurmuring(value)

	case "murmur_receive":
		cfg.SetMurmurReceive(value)

	case "recording_reminder":
		cfg.RecordingReminder = value

	case "agent_worker":
		if value == "auto" {
			cfg.SetAgentWorkerAgent("") // empty = auto-detect
		} else {
			cfg.SetAgentWorkerAgent(value)
		}

	case "agent.summarizer":
		// `cloud` is the future SageOx cloud-side path; rejected here so users
		// don't silently set a value that won't take effect. ValidValues in
		// AllSettings already prevents this for fresh users, but the explicit
		// guard catches scripts and direct YAML edits.
		if value == config.AgentSummarizerCloud {
			return fmt.Errorf("agent.summarizer=cloud is reserved for future SageOx cloud-side summarization and is not yet implemented; pick inline, delegated, or off")
		}
		cfg.AgentSummarizer = value

	case "session.draft":
		if !config.IsValidSessionDraft(value) {
			return fmt.Errorf("session.draft must be one of: on, off")
		}
		cfg.SessionDraft = value

	case "attribution.commit":
		if cfg.Attribution == nil {
			cfg.Attribution = &config.Attribution{}
		}
		if value == "(disabled)" || value == "" {
			cfg.Attribution.Commit = config.StringPtr("")
		} else {
			cfg.Attribution.Commit = config.StringPtr(value)
		}

	case "attribution.pr":
		if cfg.Attribution == nil {
			cfg.Attribution = &config.Attribution{}
		}
		if value == "(disabled)" || value == "" {
			cfg.Attribution.PR = config.StringPtr("")
		} else {
			cfg.Attribution.PR = config.StringPtr(value)
		}

	case "attribution.score_threshold":
		if cfg.Attribution == nil {
			cfg.Attribution = &config.Attribution{}
		}
		f, _ := strconv.ParseFloat(value, 64) // already validated
		cfg.Attribution.ScoreThreshold = config.Float64Ptr(f)

	case "hooks.userpromptsubmit.cloud_query":
		if cfg.Hooks == nil {
			cfg.Hooks = &config.HooksConfig{}
		}
		cfg.Hooks.SetUserPromptSubmitCloudQuery(value == "on")

	case "plan.save":
		if cfg.Plan == nil {
			cfg.Plan = &config.PlanConfig{}
		}
		enabled := value == "on"
		cfg.Plan.Save = &enabled

	case "plan.html":
		if cfg.Plan == nil {
			cfg.Plan = &config.PlanConfig{}
		}
		cfg.Plan.HTML = config.StringPtr(value)

	case "plan.open":
		if cfg.Plan == nil {
			cfg.Plan = &config.PlanConfig{}
		}
		cfg.Plan.Open = config.StringPtr(value)

	case "pr_visuals.rich":
		if cfg.PRVisuals == nil {
			cfg.PRVisuals = &config.PRVisualsConfig{}
		}
		enabled := value == "on"
		cfg.PRVisuals.Rich = &enabled

	case "pr_visuals.theme":
		if cfg.PRVisuals == nil {
			cfg.PRVisuals = &config.PRVisualsConfig{}
		}
		cfg.PRVisuals.Theme = config.StringPtr(value)

	case "pr_visuals.header":
		if cfg.PRVisuals == nil {
			cfg.PRVisuals = &config.PRVisualsConfig{}
		}
		enabled := value == "on"
		cfg.PRVisuals.Header = &enabled

	default:
		return fmt.Errorf("unknown user setting: %s", key)
	}

	return config.SaveUserConfig(cfg)
}

func setRepoConfig(key, value, projectRoot string) error {
	if projectRoot == "" {
		return fmt.Errorf("not in a SageOx project")
	}

	cfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to load project config: %w", err)
	}

	switch key {
	case "session_recording":
		cfg.SessionRecording = value

	case "session_publishing":
		cfg.SessionPublishing = value

	case "murmur_send":
		cfg.SetMurmuring(value)

	case "murmur_receive":
		cfg.MurmurReceive = value

	case "recording_reminder":
		cfg.RecordingReminder = value

	case "attribution.commit":
		if cfg.Attribution == nil {
			cfg.Attribution = &config.Attribution{}
		}
		if value == "(disabled)" || value == "" {
			cfg.Attribution.Commit = config.StringPtr("")
		} else {
			cfg.Attribution.Commit = config.StringPtr(value)
		}

	case "attribution.pr":
		if cfg.Attribution == nil {
			cfg.Attribution = &config.Attribution{}
		}
		if value == "(disabled)" || value == "" {
			cfg.Attribution.PR = config.StringPtr("")
		} else {
			cfg.Attribution.PR = config.StringPtr(value)
		}

	case "attribution.score_threshold":
		if cfg.Attribution == nil {
			cfg.Attribution = &config.Attribution{}
		}
		f, _ := strconv.ParseFloat(value, 64) // already validated
		cfg.Attribution.ScoreThreshold = config.Float64Ptr(f)

	case "hooks.userpromptsubmit.cloud_query":
		if cfg.Hooks == nil {
			cfg.Hooks = &config.HooksConfig{}
		}
		cfg.Hooks.SetUserPromptSubmitCloudQuery(value == "on")

	case "plan.save":
		if cfg.Plan == nil {
			cfg.Plan = &config.PlanConfig{}
		}
		enabled := value == "on"
		cfg.Plan.Save = &enabled

	case "plan.html":
		if cfg.Plan == nil {
			cfg.Plan = &config.PlanConfig{}
		}
		cfg.Plan.HTML = config.StringPtr(value)

	case "plan.open":
		if cfg.Plan == nil {
			cfg.Plan = &config.PlanConfig{}
		}
		cfg.Plan.Open = config.StringPtr(value)

	case "pr_visuals.rich":
		if cfg.PRVisuals == nil {
			cfg.PRVisuals = &config.PRVisualsConfig{}
		}
		enabled := value == "on"
		cfg.PRVisuals.Rich = &enabled

	case "pr_visuals.theme":
		if cfg.PRVisuals == nil {
			cfg.PRVisuals = &config.PRVisualsConfig{}
		}
		cfg.PRVisuals.Theme = config.StringPtr(value)

	case "pr_visuals.header":
		if cfg.PRVisuals == nil {
			cfg.PRVisuals = &config.PRVisualsConfig{}
		}
		enabled := value == "on"
		cfg.PRVisuals.Header = &enabled

	default:
		return fmt.Errorf("setting %s not supported at repo level", key)
	}

	return config.SaveProjectConfig(projectRoot, cfg)
}

func setTeamConfig(key, value, projectRoot string) error {
	if projectRoot == "" {
		return fmt.Errorf("not in a SageOx project")
	}

	tc := config.FindRepoTeamContext(projectRoot)
	if tc == nil {
		return fmt.Errorf("no team context configured")
	}

	teamPath := tc.Path
	cfg, err := config.LoadTeamConfig(teamPath)
	if err != nil {
		return fmt.Errorf("failed to load team config: %w", err)
	}
	if cfg == nil {
		cfg = &config.TeamConfig{}
	}

	switch key {
	case "session_recording":
		cfg.SessionRecording = value

	case "session_publishing":
		cfg.SessionPublishing = value

	case "pr_visuals.rich":
		if cfg.PRVisuals == nil {
			cfg.PRVisuals = &config.PRVisualsConfig{}
		}
		enabled := value == "on"
		cfg.PRVisuals.Rich = &enabled

	case "pr_visuals.theme":
		if cfg.PRVisuals == nil {
			cfg.PRVisuals = &config.PRVisualsConfig{}
		}
		cfg.PRVisuals.Theme = config.StringPtr(value)

	case "pr_visuals.header":
		if cfg.PRVisuals == nil {
			cfg.PRVisuals = &config.PRVisualsConfig{}
		}
		enabled := value == "on"
		cfg.PRVisuals.Header = &enabled

	default:
		return fmt.Errorf("setting %s not supported at team level", key)
	}

	return config.SaveTeamConfig(teamPath, cfg)
}

func unsetUserConfig(key string) error {
	cfg, err := config.LoadUserConfig()
	if err != nil {
		cfg = &config.UserConfig{}
	}

	switch key {
	case "session_recording":
		if cfg.Sessions != nil {
			cfg.Sessions.Mode = ""
			// nil the struct if nothing else is set
			if cfg.Sessions.GetMode() == "" {
				cfg.Sessions = nil
			}
		}

	case "session_publishing":
		cfg.SessionPublishing = ""

	case "context_git.auto_commit":
		if cfg.ContextGit != nil {
			cfg.ContextGit.AutoCommit = nil
			if cfg.ContextGit.AutoPush == nil {
				cfg.ContextGit = nil
			}
		}

	case "context_git.auto_push":
		if cfg.ContextGit != nil {
			cfg.ContextGit.AutoPush = nil
			if cfg.ContextGit.AutoCommit == nil {
				cfg.ContextGit = nil
			}
		}

	case "telemetry":
		cfg.TelemetryEnabled = nil

	case "tips":
		cfg.TipsEnabled = nil

	case "statusline":
		cfg.StatuslineEnabled = nil

	case "session_report_pane", "brief_pane":
		cfg.SessionReportPaneEnabled = nil
		cfg.BriefPaneEnabled = nil

	case "recap.trace_consultations":
		if cfg.Recap != nil {
			cfg.Recap.TraceConsultations = nil
			if *cfg.Recap == (config.RecapConfig{}) {
				cfg.Recap = nil
			}
		}

	case "disposable_machine":
		cfg.DisposableMachine = nil

	case "skills.claude_org_dedupe":
		if cfg.Skills != nil {
			cfg.Skills.ClaudeOrgDedupe = nil
			if *cfg.Skills == (config.SkillsConfig{}) {
				cfg.Skills = nil
			}
		}

	case "view_format":
		cfg.ViewFormat = ""

	case "murmur_send":
		cfg.SetMurmuring("")

	case "murmur_receive":
		cfg.SetMurmurReceive("")

	case "recording_reminder":
		cfg.RecordingReminder = ""

	case "agent_worker":
		cfg.AgentWorker = nil

	case "agent.summarizer":
		cfg.AgentSummarizer = ""

	case "session.draft":
		cfg.SessionDraft = ""

	case "attribution.commit":
		if cfg.Attribution != nil {
			cfg.Attribution.Commit = nil
			if !cfg.Attribution.IsPlanSet() && !cfg.Attribution.IsPRSet() && !cfg.Attribution.IsSessionSet() && !cfg.Attribution.IsScoreThresholdSet() {
				cfg.Attribution = nil
			}
		}

	case "attribution.pr":
		if cfg.Attribution != nil {
			cfg.Attribution.PR = nil
			if !cfg.Attribution.IsPlanSet() && !cfg.Attribution.IsCommitSet() && !cfg.Attribution.IsSessionSet() && !cfg.Attribution.IsScoreThresholdSet() {
				cfg.Attribution = nil
			}
		}

	case "attribution.score_threshold":
		if cfg.Attribution != nil {
			cfg.Attribution.ScoreThreshold = nil
			if !cfg.Attribution.IsPlanSet() && !cfg.Attribution.IsCommitSet() && !cfg.Attribution.IsPRSet() && !cfg.Attribution.IsSessionSet() {
				cfg.Attribution = nil
			}
		}

	case "hooks.userpromptsubmit.cloud_query":
		if cfg.Hooks != nil && cfg.Hooks.UserPromptSubmit != nil {
			cfg.Hooks.UserPromptSubmit.CloudQuery = nil
			if cfg.Hooks.UserPromptSubmit.CloudQuery == nil {
				cfg.Hooks.UserPromptSubmit = nil
			}
			if cfg.Hooks.UserPromptSubmit == nil {
				cfg.Hooks = nil
			}
		}

	case "plan.save":
		if cfg.Plan != nil {
			cfg.Plan.Save = nil
			if cfg.Plan.IsEmpty() {
				cfg.Plan = nil
			}
		}

	case "plan.html":
		if cfg.Plan != nil {
			cfg.Plan.HTML = nil
			if cfg.Plan.IsEmpty() {
				cfg.Plan = nil
			}
		}

	case "plan.open":
		if cfg.Plan != nil {
			cfg.Plan.Open = nil
			if cfg.Plan.IsEmpty() {
				cfg.Plan = nil
			}
		}

	case "pr_visuals.rich":
		if cfg.PRVisuals != nil {
			cfg.PRVisuals.Rich = nil
			if cfg.PRVisuals.IsEmpty() {
				cfg.PRVisuals = nil
			}
		}

	case "pr_visuals.theme":
		if cfg.PRVisuals != nil {
			cfg.PRVisuals.Theme = nil
			if cfg.PRVisuals.IsEmpty() {
				cfg.PRVisuals = nil
			}
		}

	case "pr_visuals.header":
		if cfg.PRVisuals != nil {
			cfg.PRVisuals.Header = nil
			if cfg.PRVisuals.IsEmpty() {
				cfg.PRVisuals = nil
			}
		}

	default:
		return fmt.Errorf("unknown user setting: %s", key)
	}

	return config.SaveUserConfig(cfg)
}

func unsetRepoConfig(key, projectRoot string) error {
	if projectRoot == "" {
		return fmt.Errorf("not in a SageOx project")
	}

	cfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to load project config: %w", err)
	}

	switch key {
	case "session_recording":
		cfg.SessionRecording = ""

	case "session_publishing":
		cfg.SessionPublishing = ""

	case "murmur_send":
		cfg.SetMurmuring("")

	case "murmur_receive":
		cfg.MurmurReceive = ""

	case "recording_reminder":
		cfg.RecordingReminder = ""

	case "attribution.commit":
		if cfg.Attribution != nil {
			cfg.Attribution.Commit = nil
			if !cfg.Attribution.IsPlanSet() && !cfg.Attribution.IsPRSet() && !cfg.Attribution.IsSessionSet() && !cfg.Attribution.IsScoreThresholdSet() {
				cfg.Attribution = nil
			}
		}

	case "attribution.pr":
		if cfg.Attribution != nil {
			cfg.Attribution.PR = nil
			if !cfg.Attribution.IsPlanSet() && !cfg.Attribution.IsCommitSet() && !cfg.Attribution.IsSessionSet() && !cfg.Attribution.IsScoreThresholdSet() {
				cfg.Attribution = nil
			}
		}

	case "attribution.score_threshold":
		if cfg.Attribution != nil {
			cfg.Attribution.ScoreThreshold = nil
			if !cfg.Attribution.IsPlanSet() && !cfg.Attribution.IsCommitSet() && !cfg.Attribution.IsPRSet() && !cfg.Attribution.IsSessionSet() {
				cfg.Attribution = nil
			}
		}

	case "hooks.userpromptsubmit.cloud_query":
		if cfg.Hooks != nil && cfg.Hooks.UserPromptSubmit != nil {
			cfg.Hooks.UserPromptSubmit.CloudQuery = nil
			if cfg.Hooks.UserPromptSubmit.CloudQuery == nil {
				cfg.Hooks.UserPromptSubmit = nil
			}
			if cfg.Hooks.UserPromptSubmit == nil {
				cfg.Hooks = nil
			}
		}

	case "plan.save":
		if cfg.Plan != nil {
			cfg.Plan.Save = nil
			if cfg.Plan.IsEmpty() {
				cfg.Plan = nil
			}
		}

	case "plan.html":
		if cfg.Plan != nil {
			cfg.Plan.HTML = nil
			if cfg.Plan.IsEmpty() {
				cfg.Plan = nil
			}
		}

	case "plan.open":
		if cfg.Plan != nil {
			cfg.Plan.Open = nil
			if cfg.Plan.IsEmpty() {
				cfg.Plan = nil
			}
		}

	case "pr_visuals.rich":
		if cfg.PRVisuals != nil {
			cfg.PRVisuals.Rich = nil
			if cfg.PRVisuals.IsEmpty() {
				cfg.PRVisuals = nil
			}
		}

	case "pr_visuals.theme":
		if cfg.PRVisuals != nil {
			cfg.PRVisuals.Theme = nil
			if cfg.PRVisuals.IsEmpty() {
				cfg.PRVisuals = nil
			}
		}

	case "pr_visuals.header":
		if cfg.PRVisuals != nil {
			cfg.PRVisuals.Header = nil
			if cfg.PRVisuals.IsEmpty() {
				cfg.PRVisuals = nil
			}
		}

	default:
		return fmt.Errorf("setting %s not supported at repo level", key)
	}

	return config.SaveProjectConfig(projectRoot, cfg)
}

func unsetTeamConfig(key, projectRoot string) error {
	if projectRoot == "" {
		return fmt.Errorf("not in a SageOx project")
	}

	tc := config.FindRepoTeamContext(projectRoot)
	if tc == nil {
		return fmt.Errorf("no team context configured")
	}

	teamPath := tc.Path
	cfg, err := config.LoadTeamConfig(teamPath)
	if err != nil {
		cfg = &config.TeamConfig{}
	}

	switch key {
	case "session_recording":
		cfg.SessionRecording = ""

	case "session_publishing":
		cfg.SessionPublishing = ""

	case "pr_visuals.rich":
		if cfg.PRVisuals != nil {
			cfg.PRVisuals.Rich = nil
			if cfg.PRVisuals.IsEmpty() {
				cfg.PRVisuals = nil
			}
		}

	case "pr_visuals.theme":
		if cfg.PRVisuals != nil {
			cfg.PRVisuals.Theme = nil
			if cfg.PRVisuals.IsEmpty() {
				cfg.PRVisuals = nil
			}
		}

	case "pr_visuals.header":
		if cfg.PRVisuals != nil {
			cfg.PRVisuals.Header = nil
			if cfg.PRVisuals.IsEmpty() {
				cfg.PRVisuals = nil
			}
		}

	default:
		return fmt.Errorf("setting %s not supported at team level", key)
	}

	return config.SaveTeamConfig(teamPath, cfg)
}
