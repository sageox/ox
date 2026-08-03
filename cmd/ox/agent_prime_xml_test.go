package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/agenttask"
	"github.com/sageox/ox/internal/claude"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/prime"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/spf13/cobra"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestOutputAgentPrimeXML_UserNotices(t *testing.T) {
	tests := []struct {
		name                 string
		userNotices          []UserNotice
		wantUserNoticesBlock bool
		wantNoticeTypes      []string
		wantNoticeMessages   []string
		wantNotInActions     []string // strings that should NOT appear in <immediate-actions>
		wantInActions        []string // strings that should appear in <immediate-actions>
	}{
		{
			name:                 "no notices omits user-notices block",
			userNotices:          nil,
			wantUserNoticesBlock: false,
		},
		{
			name: "upgrade notice in user-notices",
			userNotices: []UserNotice{
				{Type: "upgrade", Message: "v0.5.0 -> v0.5.1 available. Run: brew upgrade sageox"},
			},
			wantUserNoticesBlock: true,
			wantNoticeTypes:      []string{"upgrade"},
			wantNoticeMessages:   []string{"v0.5.0 -&gt; v0.5.1"},
		},
		{
			name: "restart notice in user-notices",
			userNotices: []UserNotice{
				{Type: "restart", Message: "SageOx hooks were just installed. Exit this session and start a new one so the hooks take effect."},
			},
			wantUserNoticesBlock: true,
			wantNoticeTypes:      []string{"restart"},
			wantNoticeMessages:   []string{"hooks were just installed"},
		},
		{
			name: "multiple notices",
			userNotices: []UserNotice{
				{Type: "upgrade", Message: "v0.5.0 -> v0.5.1 available"},
				{Type: "restart", Message: "Restart required"},
				{Type: "support", Message: "Agent not supported"},
			},
			wantUserNoticesBlock: true,
			wantNoticeTypes:      []string{"upgrade", "restart", "support"},
			wantNoticeMessages:   []string{"v0.5.0", "Restart", "not supported"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&buf)

			output := agentPrimeOutput{
				AgentID:     "test-agent",
				Status:      "fresh",
				UserNotices: tt.userNotices,
			}

			if _, err := outputAgentPrimeXML(cmd, output); err != nil {
				t.Fatalf("outputAgentPrimeXML() error = %v", err)
			}

			xml := buf.String()

			hasBlock := strings.Contains(xml, "<user-notices")
			if hasBlock != tt.wantUserNoticesBlock {
				t.Errorf("<user-notices> present = %v, want %v", hasBlock, tt.wantUserNoticesBlock)
			}

			if tt.wantUserNoticesBlock {
				if !strings.Contains(xml, `hint="Show each notice to the user"`) {
					t.Error("missing hint attribute on <user-notices>")
				}
			}

			for _, typ := range tt.wantNoticeTypes {
				wantAttr := `type="` + typ + `"`
				if !strings.Contains(xml, wantAttr) {
					t.Errorf("missing notice type=%q in output", typ)
				}
			}

			for _, msg := range tt.wantNoticeMessages {
				if !strings.Contains(xml, msg) {
					t.Errorf("missing notice message containing %q", msg)
				}
			}

			for _, s := range tt.wantNotInActions {
				// extract immediate-actions block
				start := strings.Index(xml, "<immediate-actions>")
				end := strings.Index(xml, "</immediate-actions>")
				if start >= 0 && end >= 0 {
					actionsBlock := xml[start:end]
					if strings.Contains(actionsBlock, s) {
						t.Errorf("%q should not be in <immediate-actions>, but found it", s)
					}
				}
			}

			for _, s := range tt.wantInActions {
				start := strings.Index(xml, "<immediate-actions>")
				end := strings.Index(xml, "</immediate-actions>")
				if start < 0 || end < 0 {
					t.Errorf("expected <immediate-actions> block for %q check", s)
				} else {
					actionsBlock := xml[start:end]
					if !strings.Contains(actionsBlock, s) {
						t.Errorf("%q should be in <immediate-actions>, but not found", s)
					}
				}
			}
		})
	}
}

func TestOutputAgentPrimeXML_DoctorStaysInActions(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	output := agentPrimeOutput{
		AgentID:          "test-agent",
		Status:           "fresh",
		NeedsDoctorAgent: true,
		DoctorHint:       "Run 'ox agent doctor' to finalize incomplete sessions",
	}

	if _, err := outputAgentPrimeXML(cmd, output); err != nil {
		t.Fatalf("outputAgentPrimeXML() error = %v", err)
	}

	xml := buf.String()

	// doctor hint must be in immediate-actions, not user-notices
	start := strings.Index(xml, "<immediate-actions>")
	end := strings.Index(xml, "</immediate-actions>")
	if start < 0 || end < 0 {
		t.Fatal("expected <immediate-actions> block")
	}
	actionsBlock := xml[start:end]
	if !strings.Contains(actionsBlock, "ox agent doctor") {
		t.Error("doctor hint not found in <immediate-actions>")
	}

	// should NOT have user-notices
	if strings.Contains(xml, "<user-notices") {
		t.Error("doctor-only output should not have <user-notices>")
	}
}

func TestOutputAgentPrimeXML_UpgradeNotInActions(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	output := agentPrimeOutput{
		AgentID:         "test-agent",
		Status:          "fresh",
		UpdateAvailable: true,
		UpdateHint:      "v0.5.0 -> v0.5.1 available. Run: brew upgrade sageox",
		UserNotices: []UserNotice{
			{Type: "upgrade", Message: "v0.5.0 -> v0.5.1 available. Run: brew upgrade sageox"},
		},
	}

	if _, err := outputAgentPrimeXML(cmd, output); err != nil {
		t.Fatalf("outputAgentPrimeXML() error = %v", err)
	}

	xml := buf.String()

	// upgrade must be in user-notices
	if !strings.Contains(xml, `<notice type="upgrade">`) {
		t.Error("upgrade notice not in <user-notices>")
	}

	// upgrade must NOT be in immediate-actions
	start := strings.Index(xml, "<immediate-actions>")
	end := strings.Index(xml, "</immediate-actions>")
	if start >= 0 && end >= 0 {
		actionsBlock := xml[start:end]
		if strings.Contains(actionsBlock, "brew upgrade") {
			t.Error("upgrade hint should not be in <immediate-actions>")
		}
	}
}

func TestOutputAgentPrimeXML_PRAttribution_UsesCorrectField(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	output := agentPrimeOutput{
		AgentID: "test-agent",
		Status:  "fresh",
		Attribution: config.ResolvedAttribution{
			Commit: "Co-Authored-By: SageOx <ox@sageox.ai>",
			PR:     "Co-Authored-By: SageOx <ox@sageox.ai>",
		},
	}

	if _, err := outputAgentPrimeXML(cmd, output); err != nil {
		t.Fatalf("outputAgentPrimeXML() error = %v", err)
	}

	xml := buf.String()

	// contribution score instruction must be present when commit is configured
	if !strings.Contains(xml, "SageOx contribution score") {
		t.Error("contribution score instruction missing")
	}
	if !strings.Contains(xml, "ox session score") {
		t.Error("ox session score command missing")
	}

	// PR attribution line must render the PR field value
	if !strings.Contains(xml, "add as last line of PR body: `Co-Authored-By: SageOx &lt;ox@sageox.ai&gt;`") {
		t.Error("PR attribution line missing or incorrect")
	}

	// commit hook instruction must be present
	if !strings.Contains(xml, "commit hook adds the trailer automatically") {
		t.Error("commit hook instruction missing")
	}
}

// TestOutputAgentPrimeXML_SurfacesOpenPlanFeedback verifies a new session learns
// about open human review feedback at prime — the PULL discovery path so feedback
// isn't lost when the push task missed. A zero count emits nothing (no nag).
func TestOutputAgentPrimeXML_SurfacesOpenPlanFeedback(t *testing.T) {
	render := func(n int) string {
		var buf bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&buf)
		if _, err := outputAgentPrimeXML(cmd, agentPrimeOutput{AgentID: "a", Status: "fresh", OpenPlanFeedback: n}); err != nil {
			t.Fatalf("outputAgentPrimeXML: %v", err)
		}
		return buf.String()
	}
	xml := render(2)
	if !strings.Contains(xml, "open human review feedback") || !strings.Contains(xml, "ox plan feedback show") {
		t.Errorf("prime must surface open feedback for a new session: %s", xml)
	}
	if strings.Contains(render(0), "open human review feedback") {
		t.Error("zero open feedback must not surface an action (no nag)")
	}
}

// TestOutputAgentPrimeXML_OrchestratorDirective verifies the buzz-conditional
// coordination guidance is emitted into prime output when set, and is a clean
// no-op when empty.
// Failure prevented: OrchestratorDirective is populated for a Buzz session but
// the XML writer silently drops it, so the agent never learns push-whispers are
// off and must be pulled with `ox agent <id> whisper`.
func TestOutputAgentPrimeXML_OrchestratorDirective(t *testing.T) {
	render := func(directive string) string {
		var buf bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&buf)
		if _, err := outputAgentPrimeXML(cmd, agentPrimeOutput{
			AgentID: "a", Status: "fresh", OrchestratorDirective: directive,
		}); err != nil {
			t.Fatalf("outputAgentPrimeXML: %v", err)
		}
		return buf.String()
	}
	xml := render("Running under Buzz (buzz-acp): push-whispers do not fire; pull with ox agent a whisper")
	if !strings.Contains(xml, "Running under Buzz") || !strings.Contains(xml, "ox agent a whisper") {
		t.Errorf("orchestrator directive must be emitted verbatim, got:\n%s", xml)
	}
	if strings.Contains(render(""), "Running under Buzz") {
		t.Error("an empty orchestrator directive must emit nothing")
	}
}

func TestOutputAgentPrimeXML_PRAttribution_DifferentValues(t *testing.T) {
	// ensures PR line renders output.Attribution.PR, not output.Attribution.Commit
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	output := agentPrimeOutput{
		AgentID: "test-agent",
		Status:  "fresh",
		Attribution: config.ResolvedAttribution{
			Commit: "commit-value",
			PR:     "pr-value",
		},
	}

	if _, err := outputAgentPrimeXML(cmd, output); err != nil {
		t.Fatalf("outputAgentPrimeXML() error = %v", err)
	}

	xml := buf.String()

	if !strings.Contains(xml, "add as last line of PR body: `pr-value`") {
		t.Errorf("PR line should render PR field value, got:\n%s", xml)
	}
	if strings.Contains(xml, "add as last line of PR body: `commit-value`") {
		t.Error("PR line is incorrectly rendering the Commit field")
	}
}

func TestOutputAgentPrimeXML_FullOutput(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	output := agentPrimeOutput{
		AgentID: "abc123",
		Status:  "fresh",
		Guidance: &agentGuidance{
			Hint: "scan first",
			Commands: []intentCommand{
				{Intent: "check health", Command: "ox doctor"},
			},
		},
		Attribution: config.ResolvedAttribution{
			Commit: "Co-Authored-By: SageOx <ox@sageox.ai>",
			PR:     "Co-Authored-By: SageOx <ox@sageox.ai>",
		},
		ProjectGuidance: &ProjectGuidance{
			Source:  "AGENTS.md",
			Content: "Use Go 1.24+",
		},
		TeamInstructions: &TeamInstructions{
			Content: "Follow team conventions",
		},
		TeamContext: &teamContextInfo{
			TeamID:   "team-1",
			TeamName: "TestTeam",
			Coworkers: []claude.Agent{
				{Name: "go-pro", Description: "Go expert", Model: "opus"},
			},
			CoworkerCommands: []claude.Command{
				{Name: "deploy", Trigger: "/deploy", Description: "Deploy to prod"},
			},
			MemoryContent: "Remember to use slog",
			ReadCommand:   "ox agent team-ctx",
		},
		Ledger: &ledgerInfo{Exists: true},
		Session: &sessionStatus{
			Recording:  true,
			Mode:       "auto",
			SessionURL: "https://sageox.ai/session/123",
		},
		NeedsDoctorAgent: true,
		DoctorHint:       "Run ox doctor",
		AgentTip:         "Use ox code search",
	}

	if _, err := outputAgentPrimeXML(cmd, output); err != nil {
		t.Fatalf("outputAgentPrimeXML() error = %v", err)
	}

	xml := buf.String()

	// verify structure
	required := []string{
		"<ox-prime>",
		"</ox-prime>",
		"<instructions>",
		"</instructions>",
		"<commands",
		"</commands>",
		"<attribution>",
		"</attribution>",
		"<project-guidance",
		"</project-guidance>",
		"<team-knowledge>",
		"</team-knowledge>",
		"<team-instructions>",
		"</team-instructions>",
		"<coworkers>",
		"</coworkers>",
		"<team-commands>",
		"</team-commands>",
		"<memory>",
		"</memory>",
		"<ledger>",
		"</ledger>",
		"<session-context",
		"</session-context>",
		"<immediate-actions>",
		"</immediate-actions>",
	}
	for _, tag := range required {
		if !strings.Contains(xml, tag) {
			t.Errorf("missing required tag: %s", tag)
		}
	}

	// verify content rendering
	if !strings.Contains(xml, "Use Go 1.24+") {
		t.Error("project guidance content missing")
	}
	if !strings.Contains(xml, "Follow team conventions") {
		t.Error("team instructions content missing")
	}
	if !strings.Contains(xml, "go-pro") {
		t.Error("coworker name missing")
	}
	if !strings.Contains(xml, "/deploy") {
		t.Error("team command trigger missing")
	}
	if !strings.Contains(xml, "Remember to use slog") {
		t.Error("memory content missing")
	}
}

func TestOutputAgentPrimeXML_CacheTierOrdering(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	output := agentPrimeOutput{
		AgentID: "test-agent",
		Status:  "fresh",
		Guidance: &agentGuidance{
			Hint:     "hint",
			Commands: []intentCommand{{Intent: "a", Command: "b"}},
		},
		TeamContext: &teamContextInfo{
			TeamID:        "team-1",
			TeamName:      "T",
			MemoryContent: "memory",
		},
		Session: &sessionStatus{
			Recording:  true,
			Mode:       "auto",
			SessionURL: "https://example.com",
		},
	}

	if _, err := outputAgentPrimeXML(cmd, output); err != nil {
		t.Fatalf("outputAgentPrimeXML() error = %v", err)
	}

	xml := buf.String()

	// cache tier ordering: static (instructions, commands, attribution)
	// must come before slow-changing (team-knowledge)
	// must come before per-session (session-context)
	instructionsIdx := strings.Index(xml, "<instructions>")
	commandsIdx := strings.Index(xml, "<commands")
	attributionIdx := strings.Index(xml, "<attribution>")
	teamKnowledgeIdx := strings.Index(xml, "<team-knowledge>")
	sessionIdx := strings.Index(xml, "<session-context")

	if instructionsIdx < 0 || commandsIdx < 0 || attributionIdx < 0 || teamKnowledgeIdx < 0 || sessionIdx < 0 {
		t.Fatal("missing expected XML blocks")
	}

	if instructionsIdx > commandsIdx {
		t.Error("instructions must come before commands")
	}
	if commandsIdx > attributionIdx {
		t.Error("commands must come before attribution")
	}
	if attributionIdx > teamKnowledgeIdx {
		t.Error("attribution (static) must come before team-knowledge (slow-changing)")
	}
	if teamKnowledgeIdx > sessionIdx {
		t.Error("team-knowledge (slow-changing) must come before session-context (per-session)")
	}
}

// TestOutputAgentPrimeXML_ConsultFirst verifies the consult-first block ships
// in every prime output, names the recency/anomaly cues, and routes each to a
// distinct corpus.
// Failure prevented: an agent reasons from priors instead of searching prior
// sessions/ledger — the exact regression that produced a confidently-wrong
// answer to a question already answered in a session from the day before.
func TestOutputAgentPrimeXML_ConsultFirst(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	// minimal output — consult-first is static, must appear with no team/ledger
	if _, err := outputAgentPrimeXML(cmd, agentPrimeOutput{AgentID: "a", Status: "fresh"}); err != nil {
		t.Fatalf("outputAgentPrimeXML() error = %v", err)
	}
	xml := buf.String()

	if !strings.Contains(xml, "<consult-first>") {
		t.Fatal("prime output missing <consult-first> block")
	}

	// the trigger: a confident wrong answer is worse than a slow one
	if !strings.Contains(xml, "first-principles") {
		t.Error("consult-first must warn against reasoning from first principles")
	}

	// recency cue routes to chronological session list, not semantic query
	if !strings.Contains(xml, "ox session list --limit 20 --json") {
		t.Error("consult-first must route recency cues to `ox session list`")
	}
	// conceptual cue routes to semantic query
	if !strings.Contains(xml, `ox query "<question>"`) {
		t.Error("consult-first must route conceptual cues to `ox query`")
	}
	// code-provenance cue routes to code search
	if !strings.Contains(xml, `ox code search "<pattern>"`) {
		t.Error("consult-first must route code-provenance cues to `ox code search`")
	}

	// the Layer-1 floor must stand ALONE on Codex/Droid: no Claude-only skill or
	// slash-command references. The consult reflex is hybrid — a thin `ox-consult`
	// skill adds Claude auto-activation ergonomics on top — but the floor block
	// must never point at it, or a Codex/Droid agent reads a dangling reference to
	// a surface it can't load. Assert the block carries neither "skill" nor "/ox-".
	consultStart := strings.Index(xml, "<consult-first>")
	consultEnd := strings.Index(xml, "</consult-first>")
	if consultStart < 0 || consultEnd < 0 || consultEnd < consultStart {
		t.Fatal("malformed <consult-first> block")
	}
	block := xml[consultStart:consultEnd]
	if strings.Contains(block, "skill") {
		t.Error("consult-first floor must not reference a Claude-only skill — it must stand alone on Codex/Droid")
	}
	if strings.Contains(block, "/ox-") {
		t.Error("consult-first floor must not reference a /ox- slash command — it must stand alone on Codex/Droid")
	}

	// must sit in the static (cacheable) tier — above all per-session content.
	// The cache boundary itself is a source comment (not emitted), so anchor on
	// <session-context>, the first per-session block in the output.
	consultIdx := strings.Index(xml, "<consult-first>")
	sessionIdx := strings.Index(xml, "<session-context")
	if sessionIdx < 0 {
		t.Fatal("missing <session-context> block")
	}
	if consultIdx > sessionIdx {
		t.Error("consult-first must be above the per-session block (static tier)")
	}
}

// TestOutputAgentPrimeXML_PlanEnrichmentGuidance verifies the
// <plan-enrichment-guidance> advisory renders on every prime payload and
// scales to the agent tier: full block (with the `ox plan review` loop)
// for Gold/Silver/unknown-baseline, lighter for Bronze.
// Failure prevented: the block silently dropping, or a Bronze agent being
// promised a review loop its lifecycle can't drive.
func TestOutputAgentPrimeXML_PlanEnrichmentGuidance(t *testing.T) {
	tests := []struct {
		name        string
		agentType   string
		wantFull    bool // full block includes the HTML-render recommendation
		wantCommand string
	}{
		{"gold claude-code", "claude-code", true, "`ox plan enrich`"},
		{"silver codex", "codex", true, "`ox plan enrich`"},
		{"bronze amp", "amp", false, "`ox plan enrich`"},
		{"unknown baseline", "some-future-agent", true, "`ox plan enrich`"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&buf)

			out := agentPrimeOutput{AgentID: "a", Status: "fresh", AgentType: tt.agentType}
			if _, err := outputAgentPrimeXML(cmd, out); err != nil {
				t.Fatalf("outputAgentPrimeXML() error = %v", err)
			}
			xml := buf.String()

			// block always present, on every payload
			if !strings.Contains(xml, "<plan-enrichment-guidance>") {
				t.Fatal("prime output missing <plan-enrichment-guidance> block")
			}
			if !strings.Contains(xml, "</plan-enrichment-guidance>") {
				t.Error("plan-enrichment-guidance block not closed")
			}

			// always routes to `ox plan enrich` as the (JSON-default) enrich verb
			if !strings.Contains(xml, tt.wantCommand) {
				t.Errorf("plan-enrichment-guidance must mention %s", tt.wantCommand)
			}

			// always points at `ox plan lint <slug>` as the pre-done verification
			// gate — full and Bronze tiers alike — so agents can discover the
			// (Hidden) lint command instead of guessing its flags.
			if !strings.Contains(xml, "ox plan lint") {
				t.Error("plan-enrichment-guidance must mention `ox plan lint` in every tier")
			}

			// the review loop is the differentiator: full tiers recommend the
			// `ox plan review` loop; Bronze (lighter) does not.
			hasReviewLoop := strings.Contains(xml, "ox plan review")
			if tt.wantFull && !hasReviewLoop {
				t.Error("full-tier block must recommend the `ox plan review` loop")
			}
			if !tt.wantFull {
				if hasReviewLoop {
					t.Error("bronze block must NOT promise the review loop")
				}
				if !strings.Contains(xml, "ox plan list") {
					t.Error("bronze block must point at `ox plan list` to browse prior plans")
				}
			}

			// must sit in the static (cacheable) tier — above per-session content
			blockIdx := strings.Index(xml, "<plan-enrichment-guidance>")
			sessionIdx := strings.Index(xml, "<session-context")
			if sessionIdx >= 0 && blockIdx > sessionIdx {
				t.Error("plan-enrichment-guidance must be above the per-session block")
			}
		})
	}
}

// TestConsultRoutes_NoDriftWithSkill is the conformance contract between the
// Layer-1 <consult-first> floor reminder and the additive `ox-consult` Claude
// skill: both render the SAME retrieval reflex, so the skill's activation
// description must name every corpus the floor table routes to. If a future
// edit drops a route from the skill's frontmatter (or adds one the floor
// doesn't carry), the two reflexes diverge — the exact drift the single-source
// capability table exists to prevent.
// Failure prevented: the consult reflex fires twice with different cue/route
// lists, or the skill silently stops covering a floor cue.
func TestConsultRoutes_NoDriftWithSkill(t *testing.T) {
	routes := consultRoutes()
	if len(routes) == 0 {
		t.Fatal("expected consult routes from the floor table; got none")
	}

	// read the additive skill's frontmatter description (YAML between the first
	// two --- fences). Path resolves from cmd/ox/ → repo root → extensions/...
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	skillPath := filepath.Join(root, "extensions", "claude", "skills", "ox-consult", "SKILL.md")
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read %s: %v", skillPath, err)
	}
	desc := skillFrontmatterDescription(t, string(raw))
	if desc == "" {
		t.Fatalf("ox-consult SKILL.md has no frontmatter description")
	}

	// every corpus command the floor table routes to must be named in the
	// skill's activation description, so the skill cannot drop a floor cue.
	// We compare on the leading `ox <subcommand>` token of each route — the
	// stable routing key, robust to flag/phrasing differences between the
	// (longer) floor line and the (terser) skill description.
	for _, r := range routes {
		corpus := leadingOxCommand(r.Command)
		if corpus == "" {
			t.Errorf("route %q has no leading `ox ...` corpus command in %q", r.Cue, r.Command)
			continue
		}
		if !strings.Contains(desc, corpus) {
			t.Errorf("ox-consult skill description drifted: floor routes to %q but the skill frontmatter does not name it.\n"+
				"floor cue: %s\nskill description: %s", corpus, r.Cue, desc)
		}
	}

	// reverse direction: the skill description must not name an `ox ...` corpus
	// the floor table doesn't carry. Without this, the skill could quietly add a
	// retrieval route the floor reminder never fires — the two reflexes diverge
	// in the other direction, which the forward loop above cannot catch.
	routeCorpora := map[string]struct{}{}
	for _, r := range routes {
		if c := leadingOxCommand(r.Command); c != "" {
			routeCorpora[c] = struct{}{}
		}
	}
	re := regexp.MustCompile("`ox[^`]*`")
	for _, token := range re.FindAllString(desc, -1) {
		c := leadingOxCommand(token)
		if c == "" {
			continue
		}
		if _, ok := routeCorpora[c]; !ok {
			t.Errorf("ox-consult skill description includes %q not present in floor consult routes", c)
		}
	}
}

// skillFrontmatterDescription returns the value of the `description:` key in a
// SKILL.md YAML frontmatter block, with folded-scalar line continuations joined
// into a single space-separated string. Tiny purpose-built parser — avoids a
// YAML dependency for one field in a test.
func skillFrontmatterDescription(t *testing.T, content string) string {
	t.Helper()
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		t.Fatalf("SKILL.md missing opening --- frontmatter fence")
	}
	var inDesc bool
	var parts []string
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			break // end of frontmatter
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "description:"):
			inDesc = true
			// strip key and any inline scalar marker (>- / | / quotes)
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "description:"))
			rest = strings.TrimLeft(rest, ">|-")
			rest = strings.TrimSpace(rest)
			if rest != "" {
				parts = append(parts, rest)
			}
		case inDesc && strings.HasPrefix(line, " "):
			// folded continuation line of the description value
			parts = append(parts, trimmed)
		case inDesc:
			// a new top-level key ends the description block
			inDesc = false
		}
	}
	return strings.Join(parts, " ")
}

// leadingOxCommand extracts the `ox <subcommand>` corpus token from the start of
// a routed-command string (e.g. "`ox session list --limit 20 ...`" → "ox session
// list"). Returns "" if the string does not begin with a backtick-quoted ox
// command. Used to compare the floor routing table against the skill description
// on the stable subcommand key, ignoring trailing flags/prose.
func leadingOxCommand(command string) string {
	command = strings.TrimSpace(command)
	if !strings.HasPrefix(command, "`ox ") {
		return ""
	}
	inner := command[1:] // drop leading backtick
	if end := strings.IndexByte(inner, '`'); end >= 0 {
		inner = inner[:end]
	}
	// keep the first three space-delimited tokens at most: `ox <verb> <noun>`
	fields := strings.Fields(inner)
	if len(fields) > 3 {
		fields = fields[:3]
	}
	// drop a trailing token that is a flag or a quoted arg placeholder
	for len(fields) > 2 {
		last := fields[len(fields)-1]
		if strings.HasPrefix(last, "-") || strings.HasPrefix(last, `"`) {
			fields = fields[:len(fields)-1]
			continue
		}
		break
	}
	return strings.Join(fields, " ")
}

func TestEscapeXML_AllSpecialChars(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{"<script>", "&lt;script&gt;"},
		{"a & b", "a &amp; b"},
		{`key="value"`, "key=&quot;value&quot;"},
		{"it's", "it&apos;s"},
		{`<a href="x">&</a>`, `&lt;a href=&quot;x&quot;&gt;&amp;&lt;/a&gt;`},
	}
	for _, tt := range tests {
		got := escapeXML(tt.input)
		if got != tt.want {
			t.Errorf("escapeXML(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestOutputAgentPrimeXML_MinimalOutput(t *testing.T) {
	// minimal output: no team context, no session, no guidance
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	output := agentPrimeOutput{
		AgentID: "min-agent",
		Status:  "fresh",
	}

	if _, err := outputAgentPrimeXML(cmd, output); err != nil {
		t.Fatalf("outputAgentPrimeXML() error = %v", err)
	}

	xml := buf.String()

	// must always have wrapper + instructions + attribution + session-context
	for _, tag := range []string{"<ox-prime>", "<instructions>", "<attribution>", "<session-context"} {
		if !strings.Contains(xml, tag) {
			t.Errorf("minimal output missing %s", tag)
		}
	}

	// must NOT have optional blocks
	for _, tag := range []string{"<team-knowledge>", "<commands", "<ledger>", "<immediate-actions>"} {
		if strings.Contains(xml, tag) {
			t.Errorf("minimal output should not have %s", tag)
		}
	}
}

func TestOutputAgentPrimeXML_WriteError(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetOut(failingWriter{})

	_, err := outputAgentPrimeXML(cmd, agentPrimeOutput{
		AgentID: "min-agent",
		Status:  "fresh",
	})
	if err == nil {
		t.Fatal("expected write error, got nil")
	}
}

// TestOutputAgentPrimeXML_RulePromotionGuidance verifies the static
// rule-promotion guidance block appears in every prime output, since it's
// what tells agents to ask whether new project-local rules should be
// published team-wide.
// Failure prevented: agents silently stop nudging users to share rules.
func TestOutputAgentPrimeXML_RulePromotionGuidance(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	if _, err := outputAgentPrimeXML(cmd, agentPrimeOutput{
		AgentID: "agent-1",
		Status:  "fresh",
	}); err != nil {
		t.Fatalf("outputAgentPrimeXML: %v", err)
	}

	xml := buf.String()
	must := []string{
		"<rule-promotion-guidance>",
		"</rule-promotion-guidance>",
		".claude/rules/",
		"agents/rules/",
		"ox guide team-rules",
		"every supported AI coding agent",
	}
	for _, s := range must {
		if !strings.Contains(xml, s) {
			t.Errorf("rule-promotion-guidance missing %q", s)
		}
	}
}

// TestOutputAgentPrimeXML_ContextBudget_Split verifies that prime emits a
// <context-budget> block, separates SageOx tool overhead from team and
// project content, returns a non-nil ContextBudget, and that team-content
// (memory, AGENTS.md) is charged to the team bucket — not to SageOx.
// Failure prevented: a regression that conflates buckets and makes SageOx
// look heavy when teams load big AGENTS.md files.
func TestOutputAgentPrimeXML_ContextBudget_Split(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	bigTeamMemory := strings.Repeat("Team-authored memory entry that ox does not control. ", 100)
	bigProjectGuidance := strings.Repeat("Project-authored AGENTS.md content. ", 100)

	output := agentPrimeOutput{
		AgentID: "agent-1",
		Status:  "fresh",
		ProjectGuidance: &ProjectGuidance{
			Source:  "AGENTS.md",
			Content: bigProjectGuidance,
		},
		TeamInstructions: &TeamInstructions{Content: "team conventions go here"},
		TeamContext: &teamContextInfo{
			TeamID:        "team-1",
			TeamName:      "TestTeam",
			MemoryContent: bigTeamMemory,
		},
	}

	budget, err := outputAgentPrimeXML(cmd, output)
	if err != nil {
		t.Fatalf("outputAgentPrimeXML: %v", err)
	}
	if budget == nil {
		t.Fatal("expected non-nil budget")
	}

	xml := buf.String()

	if !strings.Contains(xml, "<context-budget") {
		t.Error("missing <context-budget> block")
	}
	// per-source attributes derive from source names; assert each known
	// source we expect to see based on the input shape.
	for _, attr := range []string{"sageox=", "team=", "project=", "total="} {
		if !strings.Contains(xml, attr) {
			t.Errorf("budget block should report %s attribute", attr)
		}
	}

	memoryEstimate := len(bigTeamMemory) / 4
	guidanceEstimate := len(bigProjectGuidance) / 4

	teamTokens := budget.Get(prime.BudgetSourceTeam)
	projectTokens := budget.Get(prime.BudgetSourceProject)
	sageoxTokens := budget.Get(prime.BudgetSourceSageox)

	if teamTokens < memoryEstimate-50 {
		t.Errorf("team content (%d) much smaller than memory size estimate (%d) — likely miscategorized",
			teamTokens, memoryEstimate)
	}
	if projectTokens < guidanceEstimate-50 {
		t.Errorf("project content (%d) much smaller than project guidance estimate (%d) — likely miscategorized",
			projectTokens, guidanceEstimate)
	}

	// SageOx must not absorb team/project content. Big team memory (~5,000
	// chars / 4 ≈ 1,250 tokens) plus big project guidance (~3,600 chars / 4
	// ≈ 900 tokens) would inflate the sageox bucket if charged wrong;
	// 2,500 leaves headroom for the SageOx framing without false alarms.
	if sageoxTokens > 2500 {
		t.Errorf("SageOx overhead (%d) suspiciously large — team/project content may have leaked in",
			sageoxTokens)
	}

	if budget.Total() != sageoxTokens+teamTokens+projectTokens {
		t.Errorf("Total() %d != sum of known buckets (%d+%d+%d)",
			budget.Total(), sageoxTokens, teamTokens, projectTokens)
	}
}

// TestContextBudget_OpenSchema verifies that a future knowledge bubble
// — modeled here as a fictional "user" source — flows through the budget
// API without any field-shape changes. This is the test that protects the
// extensibility property: tagging an emit site with a new source string
// must Just Work end-to-end.
//
// Failure prevented: someone adds a fixed field for the next bubble and
// re-introduces the rigid schema we just refactored away from.
func TestContextBudget_OpenSchema(t *testing.T) {
	var b prime.ContextBudget
	b.Add(prime.BudgetSourceSageox, 100)
	b.Add(prime.BudgetSourceTeam, 200)
	b.Add(prime.BudgetSourceUser, 50)    // upcoming bubble
	b.Add("custom-knowledge-bubble", 25) // hypothetical future bubble

	if b.Total() != 375 {
		t.Errorf("Total() = %d, want 375", b.Total())
	}
	if b.Get("custom-knowledge-bubble") != 25 {
		t.Errorf("unknown source not preserved: got %d", b.Get("custom-knowledge-bubble"))
	}

	ordered := b.OrderedSources()
	// well-known sources first in canonical order, then unknown alpha
	wantPrefix := []string{prime.BudgetSourceSageox, prime.BudgetSourceTeam, prime.BudgetSourceUser}
	for i, want := range wantPrefix {
		if i >= len(ordered) || ordered[i] != want {
			t.Errorf("OrderedSources()[%d] = %v, want %v (full: %v)", i, ordered, want, ordered)
			break
		}
	}
	// unknown source should land after the well-known ones
	if len(ordered) < 4 || ordered[3] != "custom-knowledge-bubble" {
		t.Errorf("expected custom source after well-known, got %v", ordered)
	}
}

// TestOutputAgentPrimeXML_SageoxOverheadBudget_Regression guards against
// silent SageOx-overhead bloat. The threshold is a soft ceiling we can move
// when we deliberately add overhead; the test exists so a future PR that
// adds a 5K-token block to <instructions> blocks itself on review.
//
// Failure prevented: SageOx ships a feature that quietly makes every
// teammate's session more expensive in tokens without anyone noticing.
func TestOutputAgentPrimeXML_SageoxOverheadBudget_Regression(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	// Minimal output isolates the floor of what ox CLI itself injects.
	budget, err := outputAgentPrimeXML(cmd, agentPrimeOutput{
		AgentID: "agent-1",
		Status:  "fresh",
	})
	if err != nil {
		t.Fatalf("outputAgentPrimeXML: %v", err)
	}
	if budget == nil {
		t.Fatal("expected non-nil budget")
	}

	// As of this test landing, minimal sageox overhead measured roughly
	// 600 tokens (instructions + attribution + rule-promotion-guidance +
	// session-context + immediate-actions + budget block itself).
	// The ceiling gives ~2.5x headroom; crossing it should trigger an
	// explicit decision-and-review, not silent acceptance.
	//
	// Raised 1500 -> 1550 (ox-nut2): the <plan-enrichment-guidance> block gained
	// the explicit two-beat framing (enrich --json WHILE drafting, render a
	// "SageOx team-context-optimized plan" on present) AND the offer-the-live-
	// review-loop guidance (proactively offer `ox plan review`, run on the
	// human's yes) — the headline of that change. ~42 tokens on every Gold
	// session, deliberately accepted for a high-value collaboration loop.
	//
	// Raised 1550 -> 1610 (ox-0d2a): the guidance now tells agents NOT to
	// hand-author their own SageOx credit or footnote/ⓘ markers — `ox plan
	// render` deterministically owns the footer credit and auto-injects an OX
	// marker on references it surfaced context for; the rest use the
	// `ox plan viz ox-annotation` pattern. ~51 tokens, accepted to stop agents
	// shipping context-blind brand look-alikes that compete with ox's own.
	//
	// Raised 1610 -> 1950 (ox decision v1): the <decision-record-guidance>
	// floor block + its intent row + consult route (~290 tokens). NOTE the
	// block is corpus-gated (decision.CorpusDetected): repos without Decision
	// Records pay none of it — this test measures the ox repo itself, which
	// keeps docs/adr/, so the measured floor includes the block by design.
	// Deliberately accepted: consult-and-credit for permanent decision docs.
	//
	// Raised 1950 -> 2000 (ox-04cn.4): the <plan-enrichment-guidance> block now
	// teaches `ox plan lint <slug> [--strict]` — one clause in the full-tier
	// block next to `ox plan review`, a shorter one in the Bronze-tier block.
	// ~34 tokens, accepted so agents can discover the real `<slug>`-based
	// usage instead of guessing a `--file` flag by analogy with its siblings
	// `plan enrich`/`plan render` (both of which do take one).
	//
	// Raised 2000 -> 2100 (ox-r8lx): the <plan-enrichment-guidance> block now
	// teaches the two-layer plan structure — a minutiae-free decision layer up
	// top for the ~10-min human approver, then an "Implementation notes" section
	// at the END for the implementing agent — so agents RELOCATE implementation
	// detail rather than inlining it up top or deleting it. ~54 tokens, accepted:
	// it directly cuts reviewer cognitive load on every non-trivial plan.
	const sageoxOverheadCeiling = 2100
	sageoxTokens := budget.Get(prime.BudgetSourceSageox)
	if sageoxTokens > sageoxOverheadCeiling {
		t.Errorf("SageOx overhead floor for minimal prime = %d tokens, exceeds ceiling %d.\n"+
			"A recent change increased SageOx-controlled context. If intentional,\n"+
			"raise the ceiling and explain in the commit. If not, slim down whichever block grew.",
			sageoxTokens, sageoxOverheadCeiling)
	}

	if budget.Get(prime.BudgetSourceTeam) != 0 {
		t.Errorf("team content should be 0 in content-free prime, got %d", budget.Get(prime.BudgetSourceTeam))
	}
	if budget.Get(prime.BudgetSourceProject) != 0 {
		t.Errorf("project content should be 0 in content-free prime, got %d", budget.Get(prime.BudgetSourceProject))
	}
}

// TestOutputAgentPrimeXML_TeamRules_AlwaysAndIndexed verifies that
// always-tier rules are inlined with body, indexed-tier rules emit only
// metadata, and the budget block reports estimated token cost.
// Failure prevented: silently bloating prime context with full bodies, or
// dropping always-tier rule content that agents must see up front.
func TestOutputAgentPrimeXML_TeamRules_AlwaysAndIndexed(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	output := agentPrimeOutput{
		AgentID: "agent-1",
		Status:  "fresh",
		TeamContext: &teamContextInfo{
			TeamID:   "team-1",
			TeamName: "TestTeam",
			TeamRules: []teamdocs.TeamRule{
				{
					Name:            "escalation-policy",
					Description:     "Page humans on auth/payment issues.",
					RelPath:         "escalation.md",
					Visibility:      teamdocs.VisibilityAlways,
					Body:            "**Why:** Some calls need humans.\n",
					EstimatedTokens: 12,
				},
				{
					Name:        "postgres-uses-jsonb",
					Description: "Prefer JSONB metadata columns.",
					RelPath:     "backend/postgres.md",
					Visibility:  teamdocs.VisibilityIndexed,
				},
			},
		},
	}

	if _, err := outputAgentPrimeXML(cmd, output); err != nil {
		t.Fatalf("outputAgentPrimeXML: %v", err)
	}

	xml := buf.String()

	// always-tier rule: body inlined
	if !strings.Contains(xml, `<rule name="escalation-policy"`) {
		t.Error("missing always-tier <rule> with name attr")
	}
	if !strings.Contains(xml, "Some calls need humans") {
		t.Error("always-tier rule body should be inlined")
	}

	// indexed-tier rule: only metadata, NO body
	if !strings.Contains(xml, "<indexed") {
		t.Error("missing <indexed> table")
	}
	if !strings.Contains(xml, "postgres-uses-jsonb") {
		t.Error("indexed rule name should appear in catalog")
	}
	if !strings.Contains(xml, "backend/postgres.md") {
		t.Error("indexed rule path should appear so agent can read on demand")
	}

	// budget block surfaces always-tier cost
	if !strings.Contains(xml, "<team-rules-budget") {
		t.Error("missing <team-rules-budget> block")
	}
	if !strings.Contains(xml, `always_rules="1"`) {
		t.Error("budget should report 1 always-tier rule")
	}
	if !strings.Contains(xml, `indexed_rules="1"`) {
		t.Error("budget should report 1 indexed-tier rule")
	}
	if !strings.Contains(xml, `estimated_always_tokens="12"`) {
		t.Error("budget should report estimated tokens for always-tier")
	}
}

// TestOutputAgentPrimeXML_AgentTasksSurfaced verifies scheduled agent tasks are
// surfaced in <immediate-actions> at prime time — the universal delivery channel
// that reaches every adapter (not just those with a mid-session push hook).
func TestOutputAgentPrimeXML_AgentTasksSurfaced(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	if _, err := outputAgentPrimeXML(cmd, agentPrimeOutput{
		AgentID:         "test-agent",
		Status:          "fresh",
		AgentTasksReady: 3,
	}); err != nil {
		t.Fatalf("outputAgentPrimeXML: %v", err)
	}
	xml := buf.String()
	start := strings.Index(xml, "<immediate-actions>")
	end := strings.Index(xml, "</immediate-actions>")
	if start < 0 || end < 0 {
		t.Fatal("expected <immediate-actions> block when AgentTasksReady > 0")
	}
	block := xml[start:end]
	if !strings.Contains(block, "3 scheduled agent task") {
		t.Errorf("expected task count in actions, got: %s", block)
	}
	if !strings.Contains(block, "tasks next") {
		t.Errorf("expected claim instruction in actions, got: %s", block)
	}

	// zero ready → no task action
	buf.Reset()
	cmd2 := &cobra.Command{}
	cmd2.SetOut(&buf)
	if _, err := outputAgentPrimeXML(cmd2, agentPrimeOutput{AgentID: "a", Status: "fresh"}); err != nil {
		t.Fatalf("outputAgentPrimeXML: %v", err)
	}
	if strings.Contains(buf.String(), "scheduled agent task") {
		t.Error("no task action expected when AgentTasksReady == 0")
	}
}

// TestCountReadyAgentTasks_AgentTypeFiltered verifies prime's task count honors
// target_agent so an agent is only nudged about work it can actually claim.
func TestCountReadyAgentTasks_AgentTypeFiltered(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := agenttask.Enqueue(root, &agenttask.Task{Title: "anyone", Priority: 5}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := agenttask.Enqueue(root, &agenttask.Task{Title: "codex-only", TargetAgent: "codex", Priority: 1}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if got := countReadyAgentTasks(root, "claude"); got != 1 {
		t.Errorf("claude should see only the untargeted task, got %d", got)
	}
	if got := countReadyAgentTasks(root, "codex"); got != 2 {
		t.Errorf("codex should see both tasks, got %d", got)
	}
	// empty queue dir / no queue → 0, and must not create the dir
	empty := t.TempDir()
	if got := countReadyAgentTasks(empty, "claude"); got != 0 {
		t.Errorf("expected 0 for absent queue, got %d", got)
	}
}

// TestOutputAgentPrimeXML_KnowledgeBubbles verifies the <knowledge-bubbles>
// block (ox ADR-028 §6): when the KB envelope is non-empty the block carries
// the consumption guidance text plus one table row per bubble — slug in
// display form (#-prefixed), topics, and the mount path, with unmounted
// bubbles rendered as "(not mounted yet)" instead of a blank cell. When the
// envelope is empty the tag is absent entirely so flag-off accounts pay
// zero tokens for it.
// Failure prevented: agents either losing the "bubbles are data, consult
// read-only" guidance, or being handed a blank mount path they'd try to cd
// into.
func TestOutputAgentPrimeXML_KnowledgeBubbles(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	output := agentPrimeOutput{
		AgentID:    "test-agent",
		Status:     "fresh",
		KBGuidance: prime.KBGuidanceText,
		KB: []prime.KBInfo{
			{
				KBID:   "kb_mounted",
				Type:   "team",
				Slug:   "platform",
				Name:   "Platform",
				Topics: []string{"infra", "deploys"},
				Path:   "/data/sageox/sageox.ai/kb/kb_mounted",
			},
			{
				KBID: "kb_pending",
				Type: "team",
				Slug: "design-system",
				Name: "Design System",
				// no Path — not cloned yet
			},
		},
	}

	if _, err := outputAgentPrimeXML(cmd, output); err != nil {
		t.Fatalf("outputAgentPrimeXML: %v", err)
	}
	xml := buf.String()

	start := strings.Index(xml, "<knowledge-bubbles>")
	end := strings.Index(xml, "</knowledge-bubbles>")
	if start < 0 || end < 0 {
		t.Fatalf("expected <knowledge-bubbles> block, got:\n%s", xml)
	}
	block := xml[start:end]

	// guidance is XML-escaped at emit (it contains `<#slug>`); assert the
	// escaped form so raw-emission regressions fail this test.
	if !strings.Contains(block, escapeXML(prime.KBGuidanceText)) {
		t.Errorf("block must inline the XML-escaped KB consumption guidance, got:\n%s", block)
	}
	if strings.Contains(block, "describe <#slug>") {
		t.Errorf("guidance must not be emitted raw (unescaped '<'), got:\n%s", block)
	}
	for _, want := range []string{
		"#platform",
		"#design-system",
		"infra, deploys",
		"/data/sageox/sageox.ai/kb/kb_mounted",
		"(not mounted yet)",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("knowledge-bubbles block missing %q, got:\n%s", want, block)
		}
	}

	// zero KB rows → no tag at all
	var empty bytes.Buffer
	cmd2 := &cobra.Command{}
	cmd2.SetOut(&empty)
	if _, err := outputAgentPrimeXML(cmd2, agentPrimeOutput{AgentID: "a", Status: "fresh"}); err != nil {
		t.Fatalf("outputAgentPrimeXML: %v", err)
	}
	if strings.Contains(empty.String(), "<knowledge-bubbles>") {
		t.Error("no <knowledge-bubbles> tag expected when the KB envelope is empty")
	}
}

// TestOutputAgentPrimeXML_KnowledgeBubbles_PendingCheckoutNote verifies the
// knowledge-bubble block explains any row that has no local checkout, and
// stays quiet only when every bubble is mounted.
//
// The block's guidance sends the agent to AGENTS.md at the bubble's local
// path. Bubbles are cloned asynchronously by the daemon, so between the KB
// API returning rows and a clone landing, that path does not exist —
// following the guidance means a filesystem lookup on a missing directory
// with no explanation.
//
// The MIXED case is the load-bearing one: a single mounted bubble must not
// suppress the explanation for its pending siblings, since the agent is
// just as likely to pick the pending one.
//
// Failure prevented: an agent silently concluding the KB feature is broken
// (or inventing a path) during the normal pre-clone window.
func TestOutputAgentPrimeXML_KnowledgeBubbles_PendingCheckoutNote(t *testing.T) {
	const marker = "have no on-disk files"

	render := func(t *testing.T, kb []prime.KBInfo) string {
		t.Helper()
		out := agentPrimeOutput{
			AgentID:    "agent-kb-mount",
			KBGuidance: prime.KBGuidanceText,
			KB:         kb,
		}
		var buf bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&buf)
		if _, err := outputAgentPrimeXML(cmd, out); err != nil {
			t.Fatalf("outputAgentPrimeXML: %v", err)
		}
		return buf.String()
	}

	mounted := prime.KBInfo{Type: "team", Slug: "platform", Name: "Platform", Path: "/kb/kb_platform"}
	pending := prime.KBInfo{Type: "personal", Slug: "scratch", Name: "Scratch"}

	tests := []struct {
		name     string
		kb       []prime.KBInfo
		wantNote bool
	}{
		{"none mounted", []prime.KBInfo{pending, {Type: "team", Slug: "design", Name: "Design"}}, true},
		{"mixed: one mounted, one pending", []prime.KBInfo{mounted, pending}, true},
		{"all mounted", []prime.KBInfo{mounted, {Type: "team", Slug: "design", Name: "Design", Path: "/kb/kb_design"}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			xml := render(t, tt.kb)
			if got := strings.Contains(xml, marker); got != tt.wantNote {
				t.Errorf("pending-checkout note present = %v, want %v:\n%s", got, tt.wantNote, xml)
			}
			// the commands stay advertised in every state — they work unmounted
			if !strings.Contains(xml, "ox kb describe") {
				t.Errorf("ox kb describe must stay listed:\n%s", xml)
			}
		})
	}
}
