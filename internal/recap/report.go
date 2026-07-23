package recap

import (
	"context"
	"time"
)

// Build mines the ledger and team context into the evidence bundle. It is pure
// reads and fail-open throughout: any missing store, unreadable file, or git
// error degrades a section to empty rather than failing the report. A thin
// bundle is a valid, useful result — it drives the cold-start prescriptions.
func Build(in BuildInput) *Output {
	// Scan the whole ledger once (up to `until`) so we can report both the
	// window and the user's all-time ledger depth — the latter is the core
	// solo-value signal ("your accumulated, searchable memory").
	allMine := mineOnly(ScanSessions(in.LedgerPath, time.Time{}, in.Until, in.Identity))
	mine := inWindowSessions(allMine, in.Since, in.Until)

	traces := scanTraces(in.LedgerPath, mine)
	artifacts := buildArtifactReaches(traces, in.TeamPath)
	decisions := gatherDecisions(in.LedgerPath, mine)
	plans := gatherPlans(in.ProjectRoot, in.Since, in.Until, in.Identity)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	work := gatherWork(ctx, in.ProjectRoot, in.Since, mine)

	team := gatherTeamContext(in.TeamPath)

	out := &Output{
		User:  in.Identity.DisplayName,
		Scope: "personal",
		Since: in.Since,
		Until: in.Until,
		Repo:  in.RepoName,
		Team:  in.TeamName,
		Coverage: Coverage{
			SessionsInWindow: len(mine),
			LedgerAllTime:    len(allMine),
			WithTraces:       traces.withTraces,
			TracesDehydrated: traces.tracesDehydrated,
		},
		ArtifactsReached: artifacts,
		KnowledgeFlow:    knowledgeFlow(),
		SettledDecisions: decisions,
		PlansEnriched:    plans,
		YourWork:         work,
		TeamContextBuilt: team.artifacts,
		NextActions: nextActions(nextActionInput{
			hasLedger:     len(allMine) > 0,
			hasWindowWork: len(mine) > 0,
			artifactCount: len(artifacts),
			decisionCount: len(decisions),
			planCount:     len(plans),
			team:          team,
		}),
		WindowLabel: humaneWindow(in.Since, in.Now),
		Guidance:    guidanceText,
		Hints: &Hints{
			Drilldown: "Read any cited session with `ox session view <name> --context` to see the full provided/influenced trace.",
			Verify:    "Cross-check shipped commits with `git log --since=" + sinceLabel(in.Since, in.Now) + " --grep 'SageOx-Session:'`.",
		},
	}
	return out
}

// knowledgeFlow returns the influence-flow section. Today it is always a
// placeholder: the causal signals it needs (influenced/consulted context-trace
// events, distilled-discussion injection traces, cross-session recall) are not
// recorded on-disk yet, so there is nothing honest to show. When that
// instrumentation lands, this function mines the real chains and flips
// Available — until then it names the vision without faking a receipt.
func knowledgeFlow() *KnowledgeFlow {
	return &KnowledgeFlow{
		Available: false,
		Pending: "Coming: the causal chains — a team decision from a discussion that shaped " +
			"one of your plans, a teammate's session that steered your implementation, a " +
			"knowledge-bubble distillation that influenced the work inside a session. This is " +
			"the most valuable view and it needs influence instrumentation (consulted/influenced " +
			"events) that isn't recorded yet — so it's named here, never faked. Today the report " +
			"shows what team knowledge reached you, not yet how it changed what you built.",
	}
}

// nextActionInput carries the signal presence the prescription logic reasons
// over.
type nextActionInput struct {
	hasLedger     bool // any recorded sessions ever (solo value exists)
	hasWindowWork bool // recorded sessions in the reporting window
	artifactCount int  // team-context docs that reached the user
	decisionCount int
	planCount     int
	team          teamBuilt
}

// nextActions returns the concrete steps that would start or increase value,
// keyed to what's missing. Ordered most-impactful first. For a solo player the
// social axis (teammate, discussion) is framed as the NEXT unlock, never as a
// deficiency. Returns nil when the user is already getting clear value on both
// axes.
func nextActions(in nextActionInput) []NextAction {
	var actions []NextAction

	// True cold start — nothing recorded yet on either axis.
	if !in.hasLedger {
		actions = append(actions, NextAction{
			Action: "ox agent prime",
			Why:    "Starts a recorded session and loads available context — your searchable ledger, and your value, begin here.",
		})
		if !in.team.populated() {
			actions = append(actions, NextAction{
				Action: "Record a discussion at sageox.ai (or add a teammate)",
				Why:    "Unlocks the second value axis — decisions and conventions that flow automatically into every future session.",
			})
		}
		return actions
	}

	// Solo player getting temporal value but no social value yet — invite it,
	// don't scold. This is the on-ramp, not a gap.
	if in.artifactCount == 0 && !in.team.populated() {
		actions = append(actions, NextAction{
			Action: "Record a discussion at sageox.ai, or invite a teammate",
			Why:    "Your ledger is already compounding your own memory. Adding team context unlocks the second axis: a teammate's decisions reaching your work automatically.",
		})
	}

	// Team context exists but isn't reaching sessions — prime is the delivery
	// moment.
	if in.artifactCount == 0 && in.team.populated() {
		actions = append(actions, NextAction{
			Action: "Run `ox agent prime` at the start of each session",
			Why:    "Your team has context built, but it only reaches work when prime loads it — that's the moment value is delivered.",
		})
	}

	// Capturing decisions is the highest-leverage habit on both axes.
	if in.hasWindowWork && in.decisionCount == 0 {
		actions = append(actions, NextAction{
			Action: "Record decisions as you make them (score the session at stop, or `ox agent <id> session context-trace`)",
			Why:    "Captured decisions resurface in later sessions instead of being re-litigated — the core of compounding memory.",
		})
	}

	// Plan mode surfaces collisions/prior-art from your own history — pure solo
	// value that many miss.
	if in.hasWindowWork && in.planCount == 0 {
		actions = append(actions, NextAction{
			Action: "Draft in plan mode, then `ox plan enrich`",
			Why:    "Flags collisions with your own open work and surfaces your prior art before you write code — even solo.",
		})
	}
	return actions
}

// inWindowSessions filters an already-scanned set to the half-open window.
func inWindowSessions(facts []SessionFacts, since, until time.Time) []SessionFacts {
	var out []SessionFacts
	for _, f := range facts {
		if inWindow(f.CreatedAt, since, until) {
			out = append(out, f)
		}
	}
	return out
}

// humaneWindow renders the reporting window as a human phrase, e.g. "last 30
// days". Falls back to an absolute since-date when the span can't be computed.
func humaneWindow(since, now time.Time) string {
	if now.IsZero() || since.IsZero() || !since.Before(now) {
		return "since " + since.Format("2006-01-02")
	}
	days := int(now.Sub(since).Hours()/24 + 0.5)
	if days <= 1 {
		return "last 24 hours"
	}
	return "last " + itoa(days) + " days"
}

// sinceLabel renders a git-friendly relative window for the verify hint, e.g.
// "30.days". Falls back to an absolute date when the span is unusual.
func sinceLabel(since, now time.Time) string {
	if now.IsZero() || since.IsZero() || !since.Before(now) {
		return since.Format("2006-01-02")
	}
	days := int(now.Sub(since).Hours()/24 + 0.5)
	if days < 1 {
		days = 1
	}
	return itoa(days) + ".days"
}

// itoa is a tiny int→string without pulling strconv into this file's surface.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
