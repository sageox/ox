package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

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
	// The 1500-token ceiling gives ~2.5x headroom; crossing it should
	// trigger an explicit decision-and-review, not silent acceptance.
	const sageoxOverheadCeiling = 1500
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
