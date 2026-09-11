package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/paths"
)

// Claude Code persists any hook output longer than this many characters to a
// file under its config dir and injects only a 2,000-character preview plus
// the path (persistHookOutput, threshold 1e4 in 2.1.263). The cap applies to
// every hook channel — plain stdout, systemMessage, additionalContext,
// initialUserMessage — so there is no channel to route around it.
//
// A full prime is 14–24 KB. Before this trimmer the model saw the first 2 KB
// of <instructions> and a file path, and every delivery test stayed green
// (eval case 09, 2026-09-11: "output was too large (19KB) and got truncated …
// Team rules I can see: none", 3 of 3 runs). See docs/specs/agent-evals.md.
const claudeHookOutputCap = 10000

// primeHookBudget is what prime may emit on a hook. The margin below the cap
// covers the ~150-char `{"systemMessage": …}` banner `ox agent hook` prints
// into the same stdout ahead of prime, plus the trim. Claude measures UTF-16
// units and we measure bytes, so multi-byte characters only ever make us
// more careful.
const primeHookBudget = 9700

// hookCapSectionPriority lists top-level <ox-prime> elements in the order
// they are KEPT when the output must shrink: the first entries survive
// longest, the last are deferred first. Anything not listed is deferred
// before anything listed. The full bundle is always written to disk, so a
// deferred section costs the agent one Read, never the information.
//
// The order is a product judgment: what the coworker must see before its
// first action (identity, what to do now, when to consult, the team's
// always-rules and memory) outranks reference material it can fetch when a
// task calls for it (command table, attribution and plan guidance, ledger and
// bubble inventories, the budget footer).
var hookCapSectionPriority = []string{
	"instructions",
	"session-context",
	"immediate-actions",
	"user-notices",
	"consult-first",
	// <team-knowledge> is trimmed by child, not as a block: the always-rules
	// and memory are the product, the catalogs are one Read away.
	"team-knowledge/team-rules",
	"team-knowledge/memory",
	"team-knowledge/team-instructions",
	"team-knowledge/docs",
	"team-knowledge/coworkers",
	"team-knowledge/indexed",
	"team-knowledge/team-commands",
	"team-knowledge/team-rules-budget",
	"capture-prior",
	"ledger",
	"attribution",
	"commands",
	"plan-enrichment-guidance",
	"decision-record-guidance",
	"code-search",
	"rule-promotion-guidance",
	"visualization-guidance",
	"knowledge-bubbles",
	"other-teams",
	"project-guidance",
	"context-budget",
}

// hookCapDeferredHints tells the agent, per deferred section, when the file
// is worth opening. Kept short: these lines are paid on every capped prime.
var hookCapDeferredHints = map[string]string{
	"commands":                         "intent→ox command table",
	"attribution":                      "commit/PR attribution rules — read before your first commit or PR",
	"plan-enrichment-guidance":         "how to enrich and render plans — read before presenting a plan",
	"decision-record-guidance":         "ADR/DDR workflow — read before touching a decision record",
	"code-search":                      "ox code search/insights usage",
	"rule-promotion-guidance":          "when to offer a local rule to the team",
	"visualization-guidance":           "diagram and viz conventions for PRs and plans",
	"knowledge-bubbles":                "knowledge bubble inventory",
	"other-teams":                      "other team contexts available",
	"project-guidance":                 "project AGENTS.md body",
	"ledger":                           "how to browse prior sessions",
	"capture-prior":                    "capturing pre-prime conversation",
	"context-budget":                   "token accounting for this prime",
	"team-knowledge/docs":              "team doc catalog (names, when to read, directory)",
	"team-knowledge/coworkers":         "expert coworker roster — `ox coworker list`",
	"team-knowledge/indexed":           "indexed team rules (read on demand)",
	"team-knowledge/team-commands":     "team command files",
	"team-knowledge/team-rules-budget": "per-rule token estimates",
	"team-knowledge/team-instructions": "team CLAUDE.md/AGENTS.md pointers",
	"team-knowledge/memory":            "team memory (MEMORY.md)",
	"team-knowledge/team-rules":        "always-visible team rules",
}

// hookCapSplitParents are wrappers whose children are trimmed individually.
// The wrapper itself is never dropped; an emptied wrapper costs two lines.
var hookCapSplitParents = map[string]bool{"team-knowledge": true}

// openTag matches an element opening tag alone on its line, which is how the
// prime emitter writes every top-level section.
var openTag = regexp.MustCompile(`^<([a-z][a-z-]*)(?:\s[^>]*)?>$`)

// primeSection is one top-level element of the prime document as a byte span
// covering its opening line through its closing line (newline included).
type primeSection struct {
	name       string
	start, end int
}

// topLevelSections scans the document line by line, tracking only depth 0/1:
// a section opens on an opening tag seen at top level and closes on the
// matching `</name>` line. Nested tags (a <rule> inside <team-knowledge>)
// never reach top level, so RE2's lack of backreferences is not a problem.
func topLevelSections(xml string) []primeSection {
	var out []primeSection
	current := ""
	start := 0
	offset := 0
	for _, line := range strings.SplitAfter(xml, "\n") {
		trimmed := strings.TrimRight(line, "\n")
		switch {
		case current == "":
			if m := openTag.FindStringSubmatch(trimmed); m != nil && m[1] != "ox-prime" {
				current, start = m[1], offset
			}
		case trimmed == "</"+current+">":
			out = append(out, primeSection{name: current, start: start, end: offset + len(line)})
			current = ""
		}
		offset += len(line)
	}
	return out
}

// trimCandidates returns the droppable sections of xml: every top-level
// element, except that a split parent contributes its children (named
// parent/child) instead of itself.
func trimCandidates(xml string) []primeSection {
	var out []primeSection
	for _, s := range topLevelSections(xml) {
		if !hookCapSplitParents[s.name] {
			out = append(out, s)
			continue
		}
		openEnd := strings.Index(xml[s.start:], "\n")
		closeStart := strings.LastIndex(xml[:s.end], "\n</"+s.name+">")
		if openEnd < 0 || closeStart < 0 {
			out = append(out, s)
			continue
		}
		innerStart := s.start + openEnd + 1
		for _, c := range topLevelSections(xml[innerStart:closeStart]) {
			out = append(out, primeSection{name: s.name + "/" + c.name, start: innerStart + c.start, end: innerStart + c.end})
		}
	}
	return out
}

// fitPrimeToHookCap returns xml unchanged when it fits budget. Otherwise it
// removes whole top-level sections in reverse priority until the document
// fits, appends a <deferred> element naming what was removed and where the
// complete bundle lives, and returns the deferred section names. The result
// stays well-formed XML because only complete elements are removed.
func fitPrimeToHookCap(xml string, budget int, fullPath string) (string, []string) {
	if len(xml) <= budget {
		return xml, nil
	}

	sections := trimCandidates(xml)

	rank := make(map[string]int, len(hookCapSectionPriority))
	for i, name := range hookCapSectionPriority {
		rank[name] = i
	}
	// higher rank = deferred sooner; unlisted sections rank past the end
	rankOf := func(name string) int {
		if r, ok := rank[name]; ok {
			return r
		}
		return len(hookCapSectionPriority)
	}

	header := fmt.Sprintf("\n<deferred path=\"%s\" hint=\"Claude Code injects at most %d characters of hook output; these sections were left out of this prime to stay under it. The complete prime is at path — Read it when a line below applies.\">\n",
		escapeXMLText(fullPath), claudeHookOutputCap)
	const footer = "</deferred>\n"

	// drop candidates from the lowest-priority section upward until the
	// document fits;
	// the pointer element is paid for up front so the result really fits
	dropped := make(map[int]bool)
	var deferred []string
	size := len(xml) + len(header) + len(footer)
	for size > budget {
		best := -1
		for i, s := range sections {
			if dropped[i] {
				continue
			}
			if best == -1 || rankOf(s.name) > rankOf(sections[best].name) {
				best = i
			}
		}
		if best == -1 {
			break // nothing left to drop; emit what we have
		}
		dropped[best] = true
		deferred = append(deferred, sections[best].name)
		size -= sections[best].end - sections[best].start
		size += len(deferredLine(sections[best].name))
	}
	if len(deferred) == 0 {
		return xml, nil
	}

	// backfill: the last drop usually overshoots (one big section), leaving
	// slack that other sections can use. Re-admit in priority order — the
	// most valuable dropped section first — whatever fits, so the budget is
	// spent on what matters, not wasted.
	order := make([]int, 0, len(sections))
	for i := range sections {
		if dropped[i] {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool { return rankOf(sections[order[a]].name) < rankOf(sections[order[b]].name) })
	for _, i := range order {
		s := sections[i]
		span := s.end - s.start
		if size+span-len(deferredLine(s.name)) <= budget {
			dropped[i] = false
			size += span - len(deferredLine(s.name))
		}
	}
	// swap: a large high-priority section (the docs catalog) can stay out
	// while several small low-priority ones got back in. For each section
	// still out, in priority order, evict kept lower-priority sections from
	// the bottom up until it fits; if it never fits, undo the evictions.
	cost := func(i int) int { return sections[i].end - sections[i].start - len(deferredLine(sections[i].name)) }
	for _, i := range order {
		if !dropped[i] {
			continue
		}
		need := size + cost(i) - budget
		if need <= 0 {
			dropped[i], size = false, size+cost(i)
			continue
		}
		var evict []int
		freed := 0
		for j := len(sections) - 1; j >= 0 && freed < need; j-- {
			// candidates: kept, lower priority than i, scanned lowest-priority first
			if dropped[j] || rankOf(sections[j].name) <= rankOf(sections[i].name) {
				continue
			}
			evict = append(evict, j)
			freed += cost(j)
		}
		if freed < need {
			continue
		}
		for _, j := range evict {
			dropped[j] = true
			size -= cost(j)
		}
		dropped[i], size = false, size+cost(i)
	}

	deferred = deferred[:0]
	for i, s := range sections {
		if dropped[i] {
			deferred = append(deferred, s.name)
		}
	}
	if len(deferred) == 0 {
		return xml, nil
	}

	var sb strings.Builder
	sb.Grow(size + 512)
	cursor := 0
	for i, s := range sections {
		if !dropped[i] {
			continue
		}
		sb.WriteString(xml[cursor:s.start])
		cursor = s.end
	}
	sb.WriteString(xml[cursor:])
	out := sb.String()

	// the pointer goes just before the closing tag, so the document stays
	// well-formed and the agent reads it last, knowing what it did not get
	closing := strings.LastIndex(out, "\n</ox-prime>")
	if closing < 0 {
		return out, deferred
	}
	var ptr strings.Builder
	ptr.WriteString(header)
	for _, name := range deferred {
		ptr.WriteString(deferredLine(name))
	}
	ptr.WriteString(footer)
	return out[:closing] + ptr.String() + out[closing:], deferred
}

// deferredLine is one row of the <deferred> pointer.
func deferredLine(name string) string {
	if hint, ok := hookCapDeferredHints[name]; ok {
		return fmt.Sprintf("- %s: %s\n", name, hint)
	}
	return fmt.Sprintf("- %s\n", name)
}

// primeFullBundlePath is where the untrimmed prime for agentID is written
// when a hook cap forces trimming. Under the cache dir: derived data, safe
// to lose, never in the repo.
func primeFullBundlePath(agentID string) string {
	if agentID == "" {
		agentID = "unknown"
	}
	return filepath.Join(paths.CacheDir(), "prime", agentID+"-full.xml")
}

// writePrimeFullBundle persists the untrimmed prime so the <deferred>
// pointer resolves. Best-effort: a write failure leaves the pointer dangling
// but the trimmed prime is still worth more than a 2 KB preview.
func writePrimeFullBundle(path, xml string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(xml), 0o600)
}
