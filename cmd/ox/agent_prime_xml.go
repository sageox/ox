package main

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/spf13/cobra"
)

// outputAgentPrimeXML renders prime output as structured XML tags.
//
// This format follows Claude's prompting best practices for structured context:
//
//	https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/claude-prompting-best-practices#structure-prompts-with-xml-tags
//
// Design principles:
//   - Each content type gets its own semantically-named XML tag
//   - Use kebab-case tag names (<team-knowledge>, <immediate-actions>)
//   - Nest tags when content has natural hierarchy
//   - Use markdown tables inside tags for catalogs (coworkers, commands, docs)
//   - Use XML attributes for machine-readable IDs (agent_id, status, url)
//   - Plain prose for behavioral instructions — no JSON escaping
//   - OMIT telemetry/debug fields — they waste agent context tokens
//   - Only include content that influences agent behavior
//
// Prompt caching optimization (IMPORTANT):
//
//	Claude's prompt caching matches on prefix — identical leading tokens are cached
//	and reused across sessions. Output is ordered in three tiers:
//
//	1. STATIC (top)         — instructions, commands, attribution. Identical across
//	                          all sessions for the same repo+team. Fully cacheable.
//	2. SLOW-CHANGING (mid)  — coworker catalog, docs catalog, team memory. Only
//	                          changes when team context syncs (~hours/days).
//	3. PER-SESSION (bottom) — agent_id, session URL, immediate-actions. Unique
//	                          every session but small (~200-400 tokens).
//
//	Dynamic values (team name, sync status, session URLs, agent_id) MUST go in
//	the per-session block at the bottom. Even seemingly stable values like team
//	name should be pushed down — if they appear in a tag attribute at the top
//	(e.g., <team-knowledge team="SageOx">), a team rename busts the entire cache.
//	Prefer bare tags at the top and bind dynamic values at the end.
//
//	The output contains an explicit CACHE BOUNDARY comment in the XML. Everything
//	above it is cacheable; everything below is per-session. This makes the boundary
//	visible to anyone editing the output template. If you're unsure where new content
//	goes, ask: "Does this change between sessions?" If yes → below the boundary.
//	"Does this change between syncs?" → above boundary, in slow-changing block.
//	"Never changes?" → static block at the very top.
//
// When adding new fields to prime output:
//  1. Ask: "Does an LLM need this to behave correctly?" If no → omit or put in <!-- debug -->
//  2. Choose the right cache tier (static / slow-changing / per-session)
//  3. Choose the right tag: instructions, commands, session, team-knowledge, attribution
//  4. Use the natural format for the content (prose, table, key=value)
//  5. Keep tag names descriptive — they ARE the documentation for the model
//  6. Test with `make test-integration` to verify agent comprehension
func outputAgentPrimeXML(cmd *cobra.Command, output agentPrimeOutput) error {
	var sb strings.Builder

	sb.WriteString("<ox-prime>\n")

	// ════════════════════════════════════════════════════════════
	// cache-tier: static — identical across all sessions for this repo+team
	// ════════════════════════════════════════════════════════════

	// instructions: core orientation for the agent
	sb.WriteString("\n<instructions>\n")
	sb.WriteString("You are connected to SageOx, which provides team context and session recording.\n")
	sb.WriteString("SageOx has two SEPARATE knowledge sources:\n")
	sb.WriteString("(1) TEAM CONTEXT: team-wide meetings, architecture decisions, conventions (shared across ALL repos)\n")
	sb.WriteString("(2) SESSIONS/LEDGER: repo-specific archive of prior AI coworker coding sessions (THIS repo only)\n")
	sb.WriteString("These are unrelated — sessions are NOT discussions, and the ledger is NOT team context.\n")
	sb.WriteString("</instructions>\n")

	// commands: intent-to-command lookup table
	if output.Guidance != nil && len(output.Guidance.Commands) > 0 {
		sb.WriteString("\n<commands")
		if output.Guidance.Hint != "" {
			sb.WriteString(fmt.Sprintf(" hint=%q", output.Guidance.Hint))
		}
		sb.WriteString(">\n")
		sb.WriteString("| Intent | Command |\n")
		sb.WriteString("|--------|---------|\n")
		for _, ic := range output.Guidance.Commands {
			sb.WriteString(fmt.Sprintf("| %s | `%s` |\n", ic.Intent, ic.Command))
		}
		sb.WriteString("</commands>\n")
	}

	// attribution: how to credit SageOx in commits/PRs
	sb.WriteString("\n<attribution>\n")
	sb.WriteString("When SageOx guidance influences your approach, attribute it:\n")
	sb.WriteString("- \"Based on SageOx domain guidance, I'll...\"\n")
	sb.WriteString("- \"Following SageOx best practices for [topic]...\"\n")
	sb.WriteString("\nPlan footer (required for team-guided plans):\n")
	sb.WriteString("> Guided by SageOx\n")
	if output.Attribution.Commit != "" {
		sb.WriteString(fmt.Sprintf("\nCommit: `%s`\n", output.Attribution.Commit))
	}
	if output.Attribution.PR != "" {
		sb.WriteString(fmt.Sprintf("PR body (last line): `%s`\n", output.Attribution.Commit))
	}
	sb.WriteString("</attribution>\n")

	// project guidance: AGENTS.md from the project root
	if output.ProjectGuidance != nil && !output.ProjectGuidance.Skipped && output.ProjectGuidance.Content != "" {
		sb.WriteString(fmt.Sprintf("\n<project-guidance source=%q>\n", output.ProjectGuidance.Source))
		sb.WriteString(output.ProjectGuidance.Content)
		if !strings.HasSuffix(output.ProjectGuidance.Content, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("</project-guidance>\n")
	}

	// ════════════════════════════════════════════════════════════
	// cache-tier: slow — changes on team context sync, not per-session
	// NOTE: No dynamic attributes on these tags. Team name, sync status,
	// etc. are bound in the per-session block below to maximize prefix
	// cache hits.
	// ════════════════════════════════════════════════════════════

	if output.TeamContext != nil {
		sb.WriteString("\n<team-knowledge>\n")

		// team instructions (AGENTS.md / CLAUDE.md from team context root)
		if output.TeamInstructions != nil && output.TeamInstructions.Content != "" {
			sb.WriteString("\n<team-instructions>\n")
			sb.WriteString(output.TeamInstructions.Content)
			if !strings.HasSuffix(output.TeamInstructions.Content, "\n") {
				sb.WriteString("\n")
			}
			sb.WriteString("</team-instructions>\n")
		}

		// agents-level AGENTS.md (coworkers/agents/AGENTS.md)
		if output.TeamContext.AgentsAgentsMDContent != "" {
			sb.WriteString("\n<coworker-instructions>\n")
			sb.WriteString(output.TeamContext.AgentsAgentsMDContent)
			if !strings.HasSuffix(output.TeamContext.AgentsAgentsMDContent, "\n") {
				sb.WriteString("\n")
			}
			sb.WriteString("</coworker-instructions>\n")
		}

		// coworkers catalog
		if len(output.TeamContext.Coworkers) > 0 {
			sb.WriteString("\n<coworkers>\n")
			sb.WriteString("| Name | Specialty | Model |\n")
			sb.WriteString("|------|-----------|-------|\n")
			for _, cw := range output.TeamContext.Coworkers {
				desc := cw.Description
				if desc == "" {
					desc = "(no description)"
				}
				model := cw.Model
				if model == "" {
					model = "inherit"
				}
				sb.WriteString(fmt.Sprintf("| %s | %s | %s |\n", cw.Name, desc, model))
			}
			sb.WriteString("\nLoad: `ox coworker load <name>`\n")
			sb.WriteString("</coworkers>\n")
		}

		// team commands
		if len(output.TeamContext.CoworkerCommands) > 0 {
			sb.WriteString("\n<team-commands>\n")
			sb.WriteString("| Command | Trigger | Description |\n")
			sb.WriteString("|---------|---------|-------------|\n")
			for _, tcmd := range output.TeamContext.CoworkerCommands {
				desc := tcmd.Description
				if desc == "" {
					desc = "(no description)"
				}
				sb.WriteString(fmt.Sprintf("| %s | %s | %s |\n", tcmd.Name, tcmd.Trigger, desc))
			}
			sb.WriteString("</team-commands>\n")
		}

		// docs catalog (progressive disclosure — paths only, not content)
		if len(output.TeamContext.TeamDocs) > 0 {
			sb.WriteString("\n<docs hint=\"read on demand, not preloaded\">\n")
			sb.WriteString("| Name | When to Read |\n")
			sb.WriteString("|------|--------------|\n")
			for _, doc := range output.TeamContext.TeamDocs {
				title := doc.Title
				if title == "" {
					title = doc.Name
				}
				when := doc.When
				if when == "" {
					when = title
				}
				sb.WriteString(fmt.Sprintf("| %s | %s |\n", doc.Name, when))
			}
			if output.TeamContext.ReadCommand != "" {
				sb.WriteString(fmt.Sprintf("\nRead: `%s`\n", output.TeamContext.ReadCommand))
			}
			sb.WriteString("</docs>\n")
		}

		// team memory (inlined content)
		if output.TeamContext.MemoryContent != "" {
			sb.WriteString("\n<memory>\n")
			sb.WriteString(output.TeamContext.MemoryContent)
			if !strings.HasSuffix(output.TeamContext.MemoryContent, "\n") {
				sb.WriteString("\n")
			}
			sb.WriteString("</memory>\n")
		}

		sb.WriteString("\n</team-knowledge>\n")
	}

	// ledger info
	if output.Ledger != nil && output.Ledger.Exists {
		sb.WriteString("\n<ledger>\n")
		sb.WriteString("Repo-specific archive of prior AI coworker coding sessions.\n")
		sb.WriteString("NOT team context. Use `ox session list` to browse, `ox session view <name> --text` to view.\n")
		sb.WriteString("Do not read ledger files directly (LFS stubs).\n")
		sb.WriteString("</ledger>\n")
	}

	// other teams
	if output.OtherTeams != nil && len(output.OtherTeams.Teams) > 0 {
		sb.WriteString("\n<other-teams hint=\"Only read when user asks about a specific team by name\">\n")
		sb.WriteString("| Slug | Age |\n")
		sb.WriteString("|------|-----|\n")
		for _, t := range output.OtherTeams.Teams {
			age := t.Age
			if age == "" {
				age = "unknown"
			}
			sb.WriteString(fmt.Sprintf("| %s | %s |\n", t.Slug, age))
		}
		sb.WriteString("\nRead: `ox agent team-ctx <slug>`\n")
		sb.WriteString("</other-teams>\n")
	}

	// ════════════════════════════════════════════════════════════
	// CACHE BOUNDARY — everything below here is unique per session.
	// Adding content above this line? It MUST be identical across
	// all sessions for the same repo+team, or you'll bust the cache
	// for every user on every session start.
	// ════════════════════════════════════════════════════════════
	// cache-tier: session — unique every session

	// session context: binds all per-session dynamic values
	sb.WriteString("\n<session-context")
	sb.WriteString(fmt.Sprintf(" agent_id=%q", output.AgentID))
	sb.WriteString(fmt.Sprintf(" status=%q", output.Status))
	if output.TeamContext != nil {
		teamName := output.TeamContext.TeamName
		if teamName == "" {
			teamName = output.TeamContext.TeamID
		}
		sb.WriteString(fmt.Sprintf(" team=%q", teamName))
		if output.TeamContext.Stale {
			sb.WriteString(fmt.Sprintf(" team_stale=%q", output.TeamContext.StaleSince))
		} else {
			sb.WriteString(" team_synced=\"true\"")
		}
	}
	if output.Session != nil {
		if output.Session.Recording {
			sb.WriteString(" recording=\"true\"")
			if output.Session.Mode != "" {
				sb.WriteString(fmt.Sprintf(" mode=%q", output.Session.Mode))
			}
			if output.Session.SessionURL != "" {
				sb.WriteString(fmt.Sprintf(" url=%q", output.Session.SessionURL))
			}
		}
	}
	sb.WriteString(">\n")
	// session notification
	if output.Session != nil && output.Session.Recording {
		if output.Session.UserNotification != "" {
			sb.WriteString(output.Session.UserNotification)
			sb.WriteString("\n")
		} else {
			sb.WriteString("Recording active. Discussions may be shared with your team.\n")
		}
	}
	// observation directive
	if output.ObservationDirective != "" {
		sb.WriteString(output.ObservationDirective)
		sb.WriteString("\n")
	}
	sb.WriteString("</session-context>\n")

	// immediate actions: time-sensitive directives the agent should act on now
	var actions []string
	if output.NeedsDoctorAgent && output.DoctorHint != "" {
		actions = append(actions, fmt.Sprintf("<action priority=\"high\">%s</action>", output.DoctorHint))
	}
	if output.HooksInstalled && output.HooksRestartNotice != "" {
		actions = append(actions, fmt.Sprintf("<action priority=\"high\">%s</action>", output.HooksRestartNotice))
	}
	if output.UpdateAvailable && output.UpdateHint != "" {
		actions = append(actions, fmt.Sprintf("<action priority=\"warn\">%s</action>", output.UpdateHint))
	}
	if output.SupportNotice != "" {
		actions = append(actions, fmt.Sprintf("<action priority=\"warn\">%s</action>", output.SupportNotice))
	}
	if output.PrimeExcessiveNotice != "" {
		actions = append(actions, fmt.Sprintf("<action priority=\"warn\">%s</action>", output.PrimeExcessiveNotice))
	}
	if output.UserNotification != "" {
		actions = append(actions, fmt.Sprintf("<action priority=\"info\">Relay to user: %s</action>", output.UserNotification))
	}
	if output.AgentTip != "" {
		actions = append(actions, fmt.Sprintf("<action priority=\"info\">%s</action>", output.AgentTip))
	}

	if len(actions) > 0 {
		sb.WriteString("\n<immediate-actions>\n")
		for _, a := range actions {
			sb.WriteString(a)
			sb.WriteString("\n")
		}
		sb.WriteString("</immediate-actions>\n")
	}

	// capture-prior: instructions for capturing planning history (session-specific)
	if output.CapturePrior != nil {
		sb.WriteString(fmt.Sprintf("\n<capture-prior agent_id=%q>\n", output.AgentID))
		sb.WriteString(output.CapturePrior.Description)
		sb.WriteString("\n")
		for _, instr := range output.CapturePrior.Instructions {
			sb.WriteString(fmt.Sprintf("- %s\n", instr))
		}
		if output.CapturePrior.Example != "" {
			sb.WriteString(fmt.Sprintf("\nExample:\n%s\n", output.CapturePrior.Example))
		}
		sb.WriteString("</capture-prior>\n")
	}

	sb.WriteString("\n</ox-prime>\n")

	// write output
	cw := agentinstance.NewCountingWriter(cmd.OutOrStdout())
	_, err := cw.Write([]byte(sb.String()))
	if err != nil {
		return err
	}

	// send context heartbeat
	if bytes := cw.BytesWritten(); bytes > 0 && output.AgentID != "" {
		sendContextHeartbeat(output.AgentID, bytes, "prime")
	}
	return nil
}
