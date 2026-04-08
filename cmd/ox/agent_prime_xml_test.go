package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/claude"
	"github.com/sageox/ox/internal/config"
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

			if err := outputAgentPrimeXML(cmd, output); err != nil {
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

	if err := outputAgentPrimeXML(cmd, output); err != nil {
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

	if err := outputAgentPrimeXML(cmd, output); err != nil {
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

	if err := outputAgentPrimeXML(cmd, output); err != nil {
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

	if err := outputAgentPrimeXML(cmd, output); err != nil {
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

	if err := outputAgentPrimeXML(cmd, output); err != nil {
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

	if err := outputAgentPrimeXML(cmd, output); err != nil {
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

	if err := outputAgentPrimeXML(cmd, output); err != nil {
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

	err := outputAgentPrimeXML(cmd, agentPrimeOutput{
		AgentID: "min-agent",
		Status:  "fresh",
	})
	if err == nil {
		t.Fatal("expected write error, got nil")
	}
}
