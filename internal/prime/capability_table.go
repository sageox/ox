package prime

// MechanismClass names how an ox capability reaches an agent. The three classes
// map to the two-layer skill-injection model (see epic ox-fvjh):
//
//   - floor   — Layer-1 portable core. Rides `ox agent prime` output into every
//     adapter's context via the AGENTS.md/CLAUDE.md prime marker. No agent-
//     specific install; reaches Claude, Codex, and Droid alike.
//   - command — Layer-2 lifecycle/diagnostic surface. Installed as a Claude
//     slash command; explicit invocation only (never auto-activates).
//   - skill   — Layer-2 fat playbook. Installed as a Claude skill that both
//     answers a slash invocation AND can auto-activate from a one-line summary.
//
// These are kebab/lowercase slugs, NOT Go iota enums (per CLAUDE.md: no enums),
// so they serialize stably to JSON and survive across tool boundaries.
type MechanismClass string

const (
	// MechanismFloor is the Layer-1 deterministic floor (prime output).
	MechanismFloor MechanismClass = "floor"
	// MechanismSkill is a Layer-2 auto-activating Claude skill (fat playbook).
	MechanismSkill MechanismClass = "skill"
	// MechanismCommand is a Layer-2 explicit Claude slash command.
	MechanismCommand MechanismClass = "command"
)

// CapabilitySupport records which invocation affordances a capability offers on
// an agent that fully supports it. Floor capabilities set neither (they are not
// invoked — they stand in the context). Commands set Slash only. Skills set both.
type CapabilitySupport struct {
	Slash        bool `json:"slash"`
	AutoActivate bool `json:"autoActivate"`
}

// ConsultRoute is one cue→corpus row of the consult-first reflex. The consult
// reflex is a SINGLE retrieval reflex with several entry points: a cue (the
// shape of the user's ask) routes to exactly one corpus, because chronological,
// semantic, and code retrieval are different modes, not interchangeable. These
// rows are the single source the Layer-1 <consult-first> reminder renders from,
// so the reminder and the additive `ox-consult` skill cannot drift.
type ConsultRoute struct {
	// Cue is the human-readable trigger shape, rendered before the arrow
	// (e.g. `Recency / "I just did X"`).
	Cue string `json:"cue"`
	// Command is the routed corpus + its inline guidance, rendered after the
	// arrow (e.g. "`ox session list --limit 20 --json` (chronological; ...)").
	Command string `json:"command"`
}

// Capability is one row of the conformance contract: metadata about a single ox
// surface, keyed by a stable ID. Bodies and descriptions stay in their files
// (commands/skills) or emit sites (floor) — this table is metadata, NOT a
// content store. It is the single source of truth that (1) Layer-1 reminder
// assembly composes from the floor entries and (2) the adapter conformance test
// checks every adapter against.
type Capability struct {
	// ID is the stable surface key: a command/skill basename without the `.md`
	// extension (e.g. "ox-plan"), or a floor-block tag name (e.g. "consult-first").
	ID string `json:"id"`
	// MechanismClass is one of floor | command | skill.
	MechanismClass MechanismClass `json:"mechanism_class"`
	// Supports records slash/auto-activate affordances (see CapabilitySupport).
	Supports CapabilitySupport `json:"supports"`
	// ConsultRoutes, when set, carries the structured cue→corpus rows the
	// Layer-1 <consult-first> reminder renders from. Only the consult-first
	// floor block carries them — it is the single source of the routing table,
	// so the reminder and the `ox-consult` skill description cannot drift.
	ConsultRoutes []ConsultRoute `json:"consult_routes,omitempty"`
	// Layer1Source, when set, names the ox subcommand whose output carries a
	// floor capability into agent context — so the conformance test can assert
	// every floor capability has a Layer-1 home. Only floor entries set it.
	Layer1Source string `json:"layer1_source,omitempty"`
}

// OxCapabilities returns the ordered, fully-populated capability table for the
// current ox surfaces. It is PURE: no filesystem or network I/O, safe to call
// from both Layer-1 reminder assembly and the adapter conformance test.
//
// Order is floor → command → skill. IDs are stable: command IDs resolve to
// extensions/claude/commands/<id>.md; skill IDs resolve to
// extensions/skills/<id>/SKILL.md; floor IDs resolve to a <id> tag emit
// site in cmd/ox/agent_prime_xml.go (all verified by capability_table_test.go).
func OxCapabilities() []Capability {
	return []Capability{
		// ── floor: Layer-1 standing reminders in cmd/ox/agent_prime_xml.go ──
		// Ride `ox agent prime` output to every adapter. No slash/auto-activate.
		{
			ID:             "consult-first",
			MechanismClass: MechanismFloor,
			Layer1Source:   "agent prime",
			// the cue→corpus routing table the Layer-1 <consult-first> reminder
			// renders from. Keep cue/command text in sync with the activation
			// description of the additive extensions/skills/ox-consult/SKILL.md
			// (enforced by TestConsultRoutes_NoDriftWithSkill in
			// cmd/ox/agent_prime_xml_test.go).
			ConsultRoutes: []ConsultRoute{
				{
					Cue:     `Recency / "I just did X"`,
					Command: "`ox session list --limit 20 --json` (chronological; the summary is in the list, not in view). A specific recent session is the likeliest source; semantic search can rank it below older fuzzy matches and miss it.",
				},
				{
					Cue:     `Conceptual / "did we decide or discuss X?"`,
					Command: "`ox query \"<question>\"` (semantic; default `--source=team` covers discussions, docs, and session history). Add `--source=all` to include code.",
				},
				{
					Cue:     `"Who or what touched this code?"`,
					Command: "`ox code search \"<pattern>\"` / `ox code insights`.",
				},
				{
					Cue:     `A decision record (ADR/DDR) is being created, edited, or cited by number`,
					Command: "`ox decision enrich --topic \"<subject>\"` (new) / `--file <dr.md>` (edit) — related DRs, numbering, ref verification.",
				},
			},
		},
		{
			ID:             "rule-promotion-guidance",
			MechanismClass: MechanismFloor,
			Layer1Source:   "agent prime",
		},
		{
			ID:             "plan-enrichment-guidance",
			MechanismClass: MechanismFloor,
			Layer1Source:   "agent prime",
		},
		{
			ID:             "visualization-guidance",
			MechanismClass: MechanismFloor,
			Layer1Source:   "agent prime",
		},
		{
			ID:             "decision-record-guidance",
			MechanismClass: MechanismFloor,
			Layer1Source:   "agent prime",
		},

		// ── command: Layer-2 lifecycle/diagnostic surfaces (stay explicit) ──
		// extensions/claude/commands/<id>.md. Slash-only; never auto-activate.
		{ID: "ox", MechanismClass: MechanismCommand, Supports: CapabilitySupport{Slash: true}},
		{ID: "ox-status", MechanismClass: MechanismCommand, Supports: CapabilitySupport{Slash: true}},
		{ID: "ox-doctor", MechanismClass: MechanismCommand, Supports: CapabilitySupport{Slash: true}},
		{ID: "ox-init", MechanismClass: MechanismCommand, Supports: CapabilitySupport{Slash: true}},
		{ID: "ox-prime", MechanismClass: MechanismCommand, Supports: CapabilitySupport{Slash: true}},
		{ID: "ox-session-start", MechanismClass: MechanismCommand, Supports: CapabilitySupport{Slash: true}},
		{ID: "ox-session-stop", MechanismClass: MechanismCommand, Supports: CapabilitySupport{Slash: true}},
		{ID: "ox-session-status", MechanismClass: MechanismCommand, Supports: CapabilitySupport{Slash: true}},
		{ID: "ox-session-list", MechanismClass: MechanismCommand, Supports: CapabilitySupport{Slash: true}},
		{ID: "ox-session-abort", MechanismClass: MechanismCommand, Supports: CapabilitySupport{Slash: true}},
		{ID: "ox-cart", MechanismClass: MechanismCommand, Supports: CapabilitySupport{Slash: true}},
		{ID: "ox-cart-start", MechanismClass: MechanismCommand, Supports: CapabilitySupport{Slash: true}},
		{ID: "ox-cart-done", MechanismClass: MechanismCommand, Supports: CapabilitySupport{Slash: true}},
		{ID: "ox-cart-drop", MechanismClass: MechanismCommand, Supports: CapabilitySupport{Slash: true}},

		// ── skill: Layer-2 fat playbooks (slash AND auto-activate) ──
		// extensions/skills/<id>/SKILL.md. ox-consult is intentionally
		// NOT a row here — it is an additive skill (see additiveSkills below).
		{ID: "ox-plan", MechanismClass: MechanismSkill, Supports: CapabilitySupport{Slash: true, AutoActivate: true}},
		{ID: "ox-session-review", MechanismClass: MechanismSkill, Supports: CapabilitySupport{Slash: true, AutoActivate: true}},
		{ID: "ox-recap", MechanismClass: MechanismSkill, Supports: CapabilitySupport{Slash: true, AutoActivate: true}},
		// ox-conversation's id also names its slash-relay command file at
		// extensions/claude/commands/ox-conversation.md — one surface id, two
		// on-disk affordances, both accounted by this single row (the
		// deterministic floor for citation-walking is prime's KB guidance,
		// which names `ox conversation` for every adapter).
		{ID: "ox-conversation", MechanismClass: MechanismSkill, Supports: CapabilitySupport{Slash: true, AutoActivate: true}},
	}
}

// additiveSkills names on-disk skills that are intentionally OUTSIDE the
// conformance table in OxCapabilities(). They are additive Layer-2 ergonomics:
// a Claude-only skill whose deterministic floor is already carried by a separate
// floor-class entry, so the skill itself is not a conformance surface (its
// absence on Codex/Droid is the documented additive case, not a regression).
//
// Keeping the allowlist explicit closes the disk→table direction of the
// conformance contract: TestEveryOnDiskSurfaceIsAccounted walks the commands and
// skills directories and fails if any on-disk surface is neither an
// OxCapabilities() row NOR listed here — so a future un-accounted skill/command
// can never silently escape the contract.
//
// The map value documents WHY each skill is additive rather than a table row.
var additiveSkills = map[string]string{
	"ox-consult":       "additive Layer-2 ergonomics; its deterministic floor is the consult-first floor entry (ConsultRoutes), so it is not a separate conformance surface",
	"ox-decision":      "additive Layer-2 ergonomics; its deterministic floor is the decision-record-guidance floor entry plus the consult-first decision route, so it is not a separate conformance surface",
	"ox-skill-manager": "native Agent Skills lifecycle guidance; the deterministic installer and ownership rules live in ox CLI code rather than this playbook",
	"ox-attest-goal":   "opt-in Attest BDD authoring + drive-to-green playbook; it sharpens customer-journey judgment and pursues the capability to proven without becoming a portable product requirement",
	"ox-attest-create": "opt-in Attest evidence-recording playbook; the attestation CLI remains usable without the Claude-specific skill",
	"ox-viz":           "additive Layer-2 ergonomics; its deterministic floor is the visualization-guidance entry and the live ox viz pr output, so it is not a separate conformance surface",
	"ox-pr-header":     "additive Layer-2 ergonomics; its deterministic floor is the `ox pr header` command output plus the PR-header pointer in the prime attribution guidance, so the skill itself is not a separate conformance surface",
}
