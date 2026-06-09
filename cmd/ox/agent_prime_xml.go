package main

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/prime"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/spf13/cobra"
)

// bookkeeper tracks per-source byte counts as XML is written into a
// strings.Builder. It uses snapshot-mark accounting: charge(source)
// attributes every byte written since the previous charge() call to that
// source. Token estimates use the same len/4 heuristic as
// tokens.EstimateTokens to avoid pulling in a tokenizer dependency.
//
// Sources are identified by string constants in the prime package
// (prime.BudgetSourceSageox, ...Team, ...Project, ...User, ...). Tagging
// an emit site with a new source name is the only code change needed to
// account for content from a future knowledge bubble — the budget,
// heartbeat, daemon, and status layers all carry the source string
// verbatim through the rest of the pipeline.
type bookkeeper struct {
	sb     *strings.Builder
	budget prime.ContextBudget
	mark   int
}

func newBookkeeper(sb *strings.Builder) *bookkeeper {
	return &bookkeeper{sb: sb, mark: sb.Len()}
}

func (b *bookkeeper) charge(source string) {
	delta := b.sb.Len() - b.mark
	if delta < 0 {
		delta = 0
	}
	b.budget.Add(source, delta/4)
	b.mark = b.sb.Len()
}

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
func outputAgentPrimeXML(cmd *cobra.Command, output agentPrimeOutput) (*prime.ContextBudget, error) {
	var sb strings.Builder
	bk := newBookkeeper(&sb)

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
	sb.WriteString("When you find relevant prior sessions or discussions, attribute insights to teammates by name:\n")
	sb.WriteString("- \"SageOx found [name]'s session on [topic] — they solved a similar problem by...\"\n")
	sb.WriteString("- \"SageOx surfaced a discussion where [name] and [name] decided on [approach]...\"\n")
	sb.WriteString("Use `ox session list` and `ox query` to find related work from teammates.\n")
	sb.WriteString("Your user's identity is in the you= and you_aliases= attributes below.\n")
	sb.WriteString("Match against ALL aliases — the same person may appear as different names across sessions,\n")
	sb.WriteString("murmurs, and discussions. Do NOT credit them as a teammate — say \"Building on your earlier session...\" instead.\n")
	sb.WriteString("</instructions>\n")

	// consult-first: the root failure SageOx exists to prevent is an agent
	// reasoning from first principles when the answer already lives in team
	// memory — and getting it backwards. This block names the cues that must
	// trigger a search BEFORE reasoning, and routes each cue to the right
	// corpus (chronological vs semantic vs code are different retrieval modes).
	// Static (no per-session/per-team variance) so it's fully cacheable.
	sb.WriteString("\n<consult-first>\n")
	sb.WriteString("Before answering from first-principles reasoning, check whether SageOx already has the answer.\n")
	sb.WriteString("A confident answer that prior work contradicts is worse than a slow one. These cues mean STOP and search first:\n")
	sb.WriteString("- The user references recent or specific work they did: \"I just pushed...\", \"this request\", \"did X fix Y?\", \"is the alert gone now?\"\n")
	sb.WriteString("- A prior decision, a prod anomaly, or a metric/cost change — anything with a before/after.\n")
	sb.WriteString("Route the cue to the right corpus — these are DIFFERENT retrieval modes, not interchangeable:\n")
	sb.WriteString("- Recency / \"I just did X\" → `ox session list --limit 20 --json` (chronological; the summary is in the list, not in view). A specific recent session is the likeliest source; semantic search can rank it below older fuzzy matches and miss it.\n")
	sb.WriteString("- Conceptual / \"did we decide or discuss X?\" → `ox query \"<question>\"` (semantic; default `--source=team` covers discussions, docs, and session history). Add `--source=all` to include code.\n")
	sb.WriteString("- \"Who or what touched this code?\" → `ox code search \"<pattern>\"` / `ox code insights`.\n")
	sb.WriteString("</consult-first>\n")

	// rule-promotion-guidance: nudge the agent to ask whether a project-local
	// rule should also become a team rule when one looks generally applicable.
	// Static (no per-session/per-team variance) so it's fully cacheable.
	sb.WriteString("\n<rule-promotion-guidance>\n")
	sb.WriteString("When the user adds or edits a project-local rule (CLAUDE.md, AGENTS.md, .claude/rules/*.md, .cursorrules, .windsurfrules, etc.) that looks generally applicable — not specific to this repo's code, paths, or services — ask: \"This looks like it could apply to your whole team. Want me to also publish it to your SageOx team context as agents/rules/<name>.md?\" If yes, run `ox guide team-rules` for the file format and current manual workflow. Default to asking; do not silently publish. Skip the prompt for repo-specific rules (paths, services, schemas unique to this codebase).\n")
	sb.WriteString("Team rules apply to every supported AI coding agent (Claude, Codex, Amp, etc.) used by teammates running ox — but only for teammates running ox. Project-local .claude/rules/ only reaches Claude users.\n")
	sb.WriteString("</rule-promotion-guidance>\n")

	// plan-enrichment-guidance: when the agent produces a plan for non-trivial
	// work, nudge it to enrich the plan with team context via `ox plan` and
	// recommend an HTML render. Tier-aware (Gold/Silver/Bronze) so we only
	// promise what the agent can deliver — Bronze agents have no real-time
	// hook, so they get a lighter note. Parallels <rule-promotion-guidance>.
	writePlanEnrichmentGuidance(&sb, output.AgentType)

	// code-search: behavioral instruction to prefer ox code search over built-in tools
	if output.CodeDBAvailable {
		sb.WriteString("\n<code-search status=\"indexed\">\n")
		sb.WriteString("This repo has a live code search index. PREFER `ox code search \"&lt;query&gt;\"` over Grep/Glob/ripgrep for:\n")
		sb.WriteString("- Cross-file symbol search, function lookup, type definitions\n")
		sb.WriteString("- Git history, diffs, and blame queries\n")
		sb.WriteString("- Exploratory searches where you don't know the exact file\n")
		sb.WriteString("Use `ox code insights` before planning multi-file changes (shows hotspots, contention, open PRs).\n")
		sb.WriteString("Reserve Grep/Glob for: exact-string matches in a known file, or when ox code search returns no results.\n")
		sb.WriteString("</code-search>\n")
	}

	// commands: intent-to-command lookup table
	if output.Guidance != nil && len(output.Guidance.Commands) > 0 {
		sb.WriteString("\n<commands")
		if output.Guidance.Hint != "" {
			fmt.Fprintf(&sb, " hint=%q", output.Guidance.Hint)
		}
		sb.WriteString(">\n")
		sb.WriteString("| Intent | Command |\n")
		sb.WriteString("|--------|---------|\n")
		for _, ic := range output.Guidance.Commands {
			fmt.Fprintf(&sb, "| %s | `%s` |\n", ic.Intent, ic.Command)
		}
		sb.WriteString("</commands>\n")
	}

	// attribution: how to credit SageOx in commits/PRs
	sb.WriteString("\n<attribution>\n")
	sb.WriteString("When SageOx guidance influences your approach, attribute it:\n")
	sb.WriteString("- \"Based on SageOx domain guidance, I'll...\"\n")
	sb.WriteString("- \"Following SageOx best practices for [topic]...\"\n")
	sb.WriteString("\nWhen a teammate's contribution influences your approach, credit BOTH the person AND SageOx:\n")
	sb.WriteString("- \"SageOx surfaced [name]'s discussion about [topic] — I'll follow that approach\"\n")
	sb.WriteString("- \"Thanks to SageOx, I found [name]'s earlier session that solved a similar problem\"\n")
	sb.WriteString("- \"SageOx delivered context from [name]'s [discussion title] — applying their pattern here\"\n")
	sb.WriteString("Prefer naming teammates when you can trace the insight to a specific person. SageOx is the enabler.\n")
	sb.WriteString("\nIMPORTANT: The you= and you_aliases= attributes identify the current user.\n")
	sb.WriteString("Match against ALL aliases (the same person appears under different names in different contexts).\n")
	sb.WriteString("Do NOT credit the current user as a \"teammate\" — that is their own prior work.\n")
	sb.WriteString("For their own prior sessions/discussions: \"Building on your earlier work on [topic]...\"\n")
	sb.WriteString("For actual teammates: \"SageOx surfaced [name]'s discussion about [topic]...\"\n")
	sb.WriteString("\nPlan footer (required for team-guided plans):\n")
	sb.WriteString("> Guided by SageOx\n")
	if output.Attribution.Commit != "" {
		sb.WriteString("\nSageOx contribution score (required when commit attribution is configured):\n")
		sb.WriteString("Score reflects how much SageOx team context shaped your work — NOT that a session was recorded.\n")
		sb.WriteString("Categories:\n")
		sb.WriteString("  none         — no influence, worked independently of team context\n")
		sb.WriteString("  minor        — confirmed an approach you already planned\n")
		sb.WriteString("  moderate     — team context guided decisions\n")
		sb.WriteString("  significant  — domain knowledge you would not have had otherwise\n")
		sb.WriteString("  critical     — team context entirely shaped the approach\n")
		sb.WriteString("Report: `ox session score --score &lt;category&gt; --reason \"&lt;detailed paragraph&gt;\"`\n")
		sb.WriteString("Reason must cite specific sources: SOUL.md sections, meeting titles, team-ctx slugs, prior sessions.\n")
		fmt.Fprintf(&sb, "Score >= %g earns commit attribution.\n", output.Attribution.ScoreThreshold)
		sb.WriteString("The commit hook adds the trailer automatically — do NOT add it manually.\n")
	}
	if output.Attribution.PR != "" {
		fmt.Fprintf(&sb, "\nPR attribution: check `git log` for `%s` trailers.\n", escapeXML(output.Attribution.Commit))
		fmt.Fprintf(&sb, "If any commit has one, add as last line of PR body: `%s`\n", escapeXML(output.Attribution.PR))
	}
	sb.WriteString("</attribution>\n")

	// charge: everything above is SageOx framing/instructions/commands/attribution
	bk.charge(prime.BudgetSourceSageox)

	// project guidance: AGENTS.md from the project root
	if output.ProjectGuidance != nil && !output.ProjectGuidance.Skipped && output.ProjectGuidance.Content != "" {
		fmt.Fprintf(&sb, "\n<project-guidance source=%q>\n", output.ProjectGuidance.Source)
		bk.charge(prime.BudgetSourceSageox) // <project-guidance> tag is our framing
		sb.WriteString(output.ProjectGuidance.Content)
		if !strings.HasSuffix(output.ProjectGuidance.Content, "\n") {
			sb.WriteString("\n")
		}
		bk.charge(prime.BudgetSourceProject) // body comes from the repo's AGENTS.md
		sb.WriteString("</project-guidance>\n")
		bk.charge(prime.BudgetSourceSageox)
	}

	// ════════════════════════════════════════════════════════════
	// cache-tier: slow — changes on team context sync, not per-session
	// NOTE: No dynamic attributes on these tags. Team name, sync status,
	// etc. are bound in the per-session block below to maximize prefix
	// cache hits.
	// ════════════════════════════════════════════════════════════

	if output.TeamContext != nil {
		sb.WriteString("\n<team-knowledge>\n")
		bk.charge(prime.BudgetSourceSageox)

		// team instructions (AGENTS.md / CLAUDE.md from team context root)
		if output.TeamInstructions != nil && output.TeamInstructions.Content != "" {
			sb.WriteString("\n<team-instructions>\n")
			bk.charge(prime.BudgetSourceSageox)
			sb.WriteString(output.TeamInstructions.Content)
			if !strings.HasSuffix(output.TeamInstructions.Content, "\n") {
				sb.WriteString("\n")
			}
			bk.charge(prime.BudgetSourceTeam)
			sb.WriteString("</team-instructions>\n")
			bk.charge(prime.BudgetSourceSageox)
		}

		// agents-level AGENTS.md (coworkers/agents/AGENTS.md)
		if output.TeamContext.AgentsAgentsMDContent != "" {
			sb.WriteString("\n<coworker-instructions>\n")
			bk.charge(prime.BudgetSourceSageox)
			sb.WriteString(output.TeamContext.AgentsAgentsMDContent)
			if !strings.HasSuffix(output.TeamContext.AgentsAgentsMDContent, "\n") {
				sb.WriteString("\n")
			}
			bk.charge(prime.BudgetSourceTeam)
			sb.WriteString("</coworker-instructions>\n")
			bk.charge(prime.BudgetSourceSageox)
		}

		// coworkers catalog: framing is ours, rows are team data
		if len(output.TeamContext.Coworkers) > 0 {
			sb.WriteString("\n<coworkers>\n")
			sb.WriteString("| Name | Specialty | Model |\n")
			sb.WriteString("|------|-----------|-------|\n")
			bk.charge(prime.BudgetSourceSageox)
			for _, cw := range output.TeamContext.Coworkers {
				desc := cw.Description
				if desc == "" {
					desc = "(no description)"
				}
				model := cw.Model
				if model == "" {
					model = "inherit"
				}
				fmt.Fprintf(&sb, "| %s | %s | %s |\n", cw.Name, desc, model)
			}
			bk.charge(prime.BudgetSourceTeam)
			sb.WriteString("\nLoad: `ox coworker load <name>`\n")
			sb.WriteString("</coworkers>\n")
			bk.charge(prime.BudgetSourceSageox)
		}

		// team commands: framing is ours, rows are team data
		if len(output.TeamContext.CoworkerCommands) > 0 {
			sb.WriteString("\n<team-commands>\n")
			sb.WriteString("| Command | Trigger | Description |\n")
			sb.WriteString("|---------|---------|-------------|\n")
			bk.charge(prime.BudgetSourceSageox)
			for _, tcmd := range output.TeamContext.CoworkerCommands {
				desc := tcmd.Description
				if desc == "" {
					desc = "(no description)"
				}
				fmt.Fprintf(&sb, "| %s | %s | %s |\n", tcmd.Name, tcmd.Trigger, desc)
			}
			bk.charge(prime.BudgetSourceTeam)
			sb.WriteString("</team-commands>\n")
			bk.charge(prime.BudgetSourceSageox)
		}

		// docs catalog (progressive disclosure — paths only, not content)
		if len(output.TeamContext.TeamDocs) > 0 {
			sb.WriteString("\n<docs hint=\"read on demand, not preloaded\">\n")
			sb.WriteString("| Name | When to Read |\n")
			sb.WriteString("|------|--------------|\n")
			bk.charge(prime.BudgetSourceSageox)
			for _, doc := range output.TeamContext.TeamDocs {
				title := doc.Title
				if title == "" {
					title = doc.Name
				}
				when := doc.When
				if when == "" {
					when = title
				}
				fmt.Fprintf(&sb, "| %s | %s |\n", doc.Name, when)
			}
			bk.charge(prime.BudgetSourceTeam)
			if output.TeamContext.ReadCommand != "" {
				fmt.Fprintf(&sb, "\nRead: `%s`\n", output.TeamContext.ReadCommand)
			}
			sb.WriteString("</docs>\n")
			bk.charge(prime.BudgetSourceSageox)
		}

		// team rules: framing is ours, bodies and rows are team data.
		// emitTeamRules charges its own buckets through the bookkeeper.
		if len(output.TeamContext.TeamRules) > 0 {
			emitTeamRules(&sb, bk, output.TeamContext.TeamRules)
		}

		// team memory (inlined content): framing ours, body is team's
		if output.TeamContext.MemoryContent != "" {
			sb.WriteString("\n<memory>\n")
			bk.charge(prime.BudgetSourceSageox)
			sb.WriteString(output.TeamContext.MemoryContent)
			if !strings.HasSuffix(output.TeamContext.MemoryContent, "\n") {
				sb.WriteString("\n")
			}
			bk.charge(prime.BudgetSourceTeam)
			sb.WriteString("</memory>\n")
			bk.charge(prime.BudgetSourceSageox)
		}

		sb.WriteString("\n</team-knowledge>\n")
		bk.charge(prime.BudgetSourceSageox)
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
			fmt.Fprintf(&sb, "| %s | %s |\n", t.Slug, age)
		}
		sb.WriteString("\nList all: `ox teams`\n")
		sb.WriteString("Read: `ox agent team-ctx <slug>`\n")
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
	fmt.Fprintf(&sb, " agent_id=%q", output.AgentID)
	fmt.Fprintf(&sb, " status=%q", output.Status)
	if output.CurrentUserName != "" {
		fmt.Fprintf(&sb, " you=\"%s\"", escapeXML(output.CurrentUserName))
		if len(output.CurrentUserAliases) > 0 {
			fmt.Fprintf(&sb, " you_aliases=\"%s\"", escapeXML(strings.Join(output.CurrentUserAliases, ", ")))
		}
	}
	if output.TeamContext != nil {
		teamName := output.TeamContext.TeamName
		if teamName == "" {
			teamName = output.TeamContext.TeamID
		}
		fmt.Fprintf(&sb, " team=%q", teamName)
		if output.TeamContext.Stale {
			fmt.Fprintf(&sb, " team_stale=%q", output.TeamContext.StaleSince)
		} else {
			sb.WriteString(" team_synced=\"true\"")
		}
	}
	if output.Session != nil {
		if output.Session.Recording {
			sb.WriteString(" recording=\"true\"")
			if output.Session.Mode != "" {
				fmt.Fprintf(&sb, " mode=%q", output.Session.Mode)
			}
			if output.Session.SessionURL != "" {
				fmt.Fprintf(&sb, " url=%q", output.Session.SessionURL)
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
		sb.WriteString(escapeXML(output.ObservationDirective))
		sb.WriteString("\n")
	}
	// murmur directive — when auto-murmuring is configured, tell the agent to proactively publish WIP
	if output.MurmurDirective != "" {
		sb.WriteString(escapeXML(output.MurmurDirective))
		sb.WriteString("\n")
	}
	sb.WriteString("</session-context>\n")

	// user notices: messages that agents must relay to the user (upgrade, restart, support)
	if len(output.UserNotices) > 0 {
		sb.WriteString("\n<user-notices hint=\"Show each notice to the user\">\n")
		for _, n := range output.UserNotices {
			fmt.Fprintf(&sb,
				"<notice type=\"%s\">%s</notice>\n",
				escapeXML(n.Type),
				escapeXML(n.Message),
			)
		}
		sb.WriteString("</user-notices>\n")
	}

	// immediate actions: agent-only directives (doctor, excessive prime, tips)
	var actions []string
	if output.NeedsDoctorAgent && output.DoctorHint != "" {
		actions = append(actions, fmt.Sprintf("<action priority=\"high\">%s</action>", output.DoctorHint))
	}
	if output.AgentTasksReady > 0 {
		actions = append(actions, fmt.Sprintf("<action priority=\"high\">%d scheduled agent task(s) ready. These are SageOx background chores (NOT the user's request) — claim and run each in a SUBAGENT with fresh context: 'ox agent &lt;id&gt; tasks next', do the work, then 'ox agent &lt;id&gt; tasks done &lt;task-id&gt;'.</action>", output.AgentTasksReady))
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
		fmt.Fprintf(&sb, "\n<capture-prior agent_id=%q>\n", output.AgentID)
		sb.WriteString(output.CapturePrior.Description)
		sb.WriteString("\n")
		for _, instr := range output.CapturePrior.Instructions {
			fmt.Fprintf(&sb, "- %s\n", instr)
		}
		if output.CapturePrior.Example != "" {
			fmt.Fprintf(&sb, "\nExample:\n%s\n", output.CapturePrior.Example)
		}
		sb.WriteString("</capture-prior>\n")
	}

	// final SageOx-overhead charge BEFORE the budget block, so the budget
	// emitted below reports the totals of everything that came before it.
	bk.charge(prime.BudgetSourceSageox)

	// context-budget: split of estimated token cost by who controls each
	// emit site. Each known source becomes an attribute on the open tag;
	// the schema is open — adding a new knowledge bubble (e.g. "user")
	// adds an attribute without breaking existing parsers. Total is the
	// sum across all sources.
	sb.WriteString("\n<context-budget hint=\"who controls each token in this prime output\"")
	for _, src := range bk.budget.OrderedSources() {
		fmt.Fprintf(&sb, " %s=\"%d\"", src, bk.budget.Get(src))
	}
	fmt.Fprintf(&sb, " total=\"%d\">\n", bk.budget.Total())
	sb.WriteString("SageOx is judged on the sageox bucket — every word in it is our product decision.\n")
	sb.WriteString("Other sources (team, project, user, ...) reflect content authored elsewhere; SageOx delivers them but does not control their size.\n")
	sb.WriteString("</context-budget>\n")
	bk.charge(prime.BudgetSourceSageox)

	sb.WriteString("\n</ox-prime>\n")
	bk.charge(prime.BudgetSourceSageox)

	// write output
	cw := agentinstance.NewCountingWriter(cmd.OutOrStdout())
	_, err := cw.Write([]byte(sb.String()))
	if err != nil {
		return &bk.budget, err
	}

	// send context heartbeat with per-bucket breakdown so the daemon can
	// track cumulative tokens by category, not just a rolled-up total.
	if bytes := cw.BytesWritten(); bytes > 0 && output.AgentID != "" {
		sendContextHeartbeatSplit(output.AgentID, bytes, "prime", &bk.budget)
	}
	return &bk.budget, nil
}

// xmlEscaper replaces XML-special characters with their entity references.
var xmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	"\"", "&quot;",
	"'", "&apos;",
)

func escapeXML(s string) string {
	return xmlEscaper.Replace(s)
}

// writePlanEnrichmentGuidance emits the <plan-enrichment-guidance> advisory
// block, scaled to the agent's plan-enrichment tier. Gold (claude-code) and
// Silver (codex/gemini) and the unknown-agent baseline get the full block;
// Bronze (amp/opencode/pi) gets a lighter note that only points at `ox plan`
// and `ox plan list` without promising a real-time nudge the tier can't fire.
//
// Token-lean by design: this ships on every prime payload (see
// docs/ai/specs/agent-ux-principles.md — every token competes with the
// developer's own context).
func writePlanEnrichmentGuidance(sb *strings.Builder, agentType string) {
	sb.WriteString("\n<plan-enrichment-guidance>\n")
	if prime.ClassifyAgentTier(agentType) == prime.TierBronze {
		// lighter tier: surface the surface, don't promise nudges.
		sb.WriteString("When you produce a plan for non-trivial work, `ox plan` enriches it with team context (collisions, prior art, expert routing). Run `ox plan list` to browse prior plans for this repo.\n")
		sb.WriteString("</plan-enrichment-guidance>\n")
		return
	}
	sb.WriteString("When you produce a plan for non-trivial work (multi-file OR architectural OR touches a hotspot/open-PR OR ~5+ steps), run `ox plan` to enrich it with team context — collisions (incl. teammates actively murmuring on these files), prior art, and expert routing.\n")
	sb.WriteString("For non-trivial plans, render a human-review HTML page with the `html-plan` skill (it writes a self-contained file and opens it); re-open any saved render with `ox plan --open` or `ox plan view <slug> --open`. Render only after the human confirms, unless plan.html is always; skip if plan.html is off.\n")
	sb.WriteString("</plan-enrichment-guidance>\n")
}

// emitTeamRules writes <team-rules> and <team-rules-budget> blocks for the
// discovered, filtered set of team rules. Charges its bookkeeper as it goes:
// framing (tags, table headers, attribute names) is sageox-overhead; the
// rule names, descriptions, paths, and bodies are team-content (the team
// authored them). This keeps the rolled-up <context-budget> accurate even
// when teams accumulate large always-tier rule libraries.
func emitTeamRules(sb *strings.Builder, bk *bookkeeper, rules []teamdocs.TeamRule) {
	var alwaysRules []teamdocs.TeamRule
	var indexedRules []teamdocs.TeamRule
	totalAlwaysTokens := 0

	for _, r := range rules {
		switch r.Visibility {
		case teamdocs.VisibilityAlways:
			alwaysRules = append(alwaysRules, r)
			totalAlwaysTokens += r.EstimatedTokens
		default:
			indexedRules = append(indexedRules, r)
		}
	}

	sb.WriteString("\n<team-rules hint=\"agents/rules/<topic>.md — modular AI-coworker rules. See `ox guide team-rules`.\">\n")
	bk.charge(prime.BudgetSourceSageox)

	// always-tier rules: framing is ours, body+name+desc+path are team's
	for _, r := range alwaysRules {
		sb.WriteString("\n<rule")
		bk.charge(prime.BudgetSourceSageox)
		fmt.Fprintf(sb, " name=%q", r.Name)
		bk.charge(prime.BudgetSourceTeam)
		sb.WriteString(" visibility=\"always\"")
		bk.charge(prime.BudgetSourceSageox)
		if r.Description != "" {
			fmt.Fprintf(sb, " description=%q", r.Description)
			bk.charge(prime.BudgetSourceTeam)
		}
		fmt.Fprintf(sb, " path=%q", r.RelPath)
		bk.charge(prime.BudgetSourceTeam)
		sb.WriteString(">\n")
		bk.charge(prime.BudgetSourceSageox)
		sb.WriteString(r.Body)
		if !strings.HasSuffix(r.Body, "\n") {
			sb.WriteString("\n")
		}
		bk.charge(prime.BudgetSourceTeam)
		sb.WriteString("</rule>\n")
		bk.charge(prime.BudgetSourceSageox)
	}

	// indexed-tier rules: framing/headers ours, rows are team's
	if len(indexedRules) > 0 {
		sb.WriteString("\n<indexed hint=\"read on demand by path\">\n")
		sb.WriteString("| Name | Description | Path |\n")
		sb.WriteString("|------|-------------|------|\n")
		bk.charge(prime.BudgetSourceSageox)
		for _, r := range indexedRules {
			desc := r.Description
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Fprintf(sb, "| %s | %s | %s |\n", r.Name, desc, r.RelPath)
		}
		bk.charge(prime.BudgetSourceTeam)
		sb.WriteString("</indexed>\n")
		bk.charge(prime.BudgetSourceSageox)
	}

	sb.WriteString("</team-rules>\n")
	bk.charge(prime.BudgetSourceSageox)

	// per-rule budget: framing ours, the rule names listed are team-derived
	// but small (just the names) — round to sageox to keep this simple
	if len(alwaysRules) > 0 {
		fmt.Fprintf(sb,
			"\n<team-rules-budget always_rules=\"%d\" indexed_rules=\"%d\" estimated_always_tokens=\"%d\">\n",
			len(alwaysRules), len(indexedRules), totalAlwaysTokens)
		sb.WriteString("Per-rule token estimates (always-tier only; indexed rules cost zero until read):\n")
		sb.WriteString("| Rule | ~Tokens |\n")
		sb.WriteString("|------|---------|\n")
		for _, r := range alwaysRules {
			fmt.Fprintf(sb, "| %s | %d |\n", r.Name, r.EstimatedTokens)
		}
		sb.WriteString("</team-rules-budget>\n")
		bk.charge(prime.BudgetSourceSageox)
	}
}
