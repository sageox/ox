package main

// status_bubbles.go — `ox status` knowledge-bubbles section.
//
// Sourced from the KB API (the only source of bubble rows under ox
// ADR-028). Team contexts and ledgers are conversation stores with their
// own status sections — they are not bubbles and do not appear here.
//
// The section opens with a scannable summary line:
//
//	Knowledge bubbles: <total> (<n> personal, <n> profile, <n> team, <n> repo[, <n> custom][, <n> unknown])
//
// followed by one card per bubble (personal-scope rows first, then the
// project team's) showing the local mount path (paths.KBDir) and its sync
// status, mirroring the Ledger / Team Context cards. Zero buckets are
// omitted from the summary. Total=0 collapses to `Knowledge bubbles: 0`
// (no parens, no cards). Fetch errors degrade gracefully to
// `(unavailable)` so the rest of `ox status` still renders.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/kb"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/status"
)

// statusBubblesSummary is the small intermediate value that renderers and
// JSON emitters both consume. Keeps presentation logic out of the fetch.
type statusBubblesSummary struct {
	// Total is the sum of all per-type counts.
	Total int

	// ByType is keyed by kb_type slug ("personal", "profile", ...). Only
	// non-zero buckets appear so order-of-iteration callers can skip the
	// is-zero check.
	ByType map[string]int

	// Bubbles are the raw fetched rows, kept so the section renderer and
	// JSON emitter can show per-bubble cards (path + sync status), not
	// just counts.
	Bubbles []kb.Bubble

	// Warnings are non-fatal errors from the KB fetch.
	Warnings []kb.Warning

	// Unavailable reports that the fetch could not run at all. Renderers
	// show "(unavailable)" and skip the breakdown.
	Unavailable bool
}

// statusBubblesTypeOrder is the canonical render order for the by-type
// breakdown. Matches kb_list.go's kbTypePriority so `ox status` and
// `ox kb list` agree on which bucket appears first.
var statusBubblesTypeOrder = []api.KBType{
	api.KBTypePersonal,
	api.KBTypeProfile,
	api.KBTypeTeam,
	api.KBTypeRepo,
	api.KBTypeCustom,
	api.KBType("channel"),
	api.KBTypeUnknown,
}

// summarizeBubbles tallies per-type counts from a KB fetch. Empty/unknown
// types collapse to "unknown" so a forward-compat row never silently
// vanishes from the count.
func summarizeBubbles(res kb.ListResult) statusBubblesSummary {
	by := make(map[string]int)
	for _, b := range res.Bubbles {
		key := string(b.Type)
		if b.Type == "" || b.Type == api.KBTypeUnknown {
			key = string(api.KBTypeUnknown)
		}
		by[key]++
	}
	// drop zero entries (defensive — should never happen since we only
	// increment, but keeps the shape predictable for JSON consumers)
	for k, v := range by {
		if v == 0 {
			delete(by, k)
		}
	}
	return statusBubblesSummary{
		Total:    len(res.Bubbles),
		ByType:   by,
		Bubbles:  res.Bubbles,
		Warnings: res.Warnings,
	}
}

// formatBubblesLine builds the human-readable summary string with no
// styling applied. Splitting style off makes this trivially testable:
// the assertion is the literal string the user sees.
func formatBubblesLine(s statusBubblesSummary) string {
	if s.Unavailable {
		return "Knowledge bubbles: (unavailable)"
	}
	if s.Total == 0 {
		return "Knowledge bubbles: 0"
	}
	parts := make([]string, 0, len(s.ByType))
	for _, t := range statusBubblesTypeOrder {
		k := string(t)
		if n, ok := s.ByType[k]; ok && n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, k))
		}
	}
	// stable order for any custom keys not in statusBubblesTypeOrder
	// (forward-compat: server adds a new type before the CLI knows about
	// it). Sort by key so output stays deterministic.
	if len(parts) < nonZeroBuckets(s.ByType) {
		extras := make([]string, 0)
		known := knownTypeSet()
		for k, n := range s.ByType {
			if _, ok := known[k]; ok {
				continue
			}
			if n > 0 {
				extras = append(extras, fmt.Sprintf("%d %s", n, k))
			}
		}
		sort.Strings(extras)
		parts = append(parts, extras...)
	}
	return fmt.Sprintf("Knowledge bubbles: %d (%s)", s.Total, strings.Join(parts, ", "))
}

// nonZeroBuckets counts entries with value > 0. Used to detect when
// statusBubblesTypeOrder didn't cover every key.
func nonZeroBuckets(m map[string]int) int {
	n := 0
	for _, v := range m {
		if v > 0 {
			n++
		}
	}
	return n
}

// knownTypeSet returns the set of type slugs the CLI knows about, used to
// detect forward-compat extras in formatBubblesLine.
func knownTypeSet() map[string]struct{} {
	out := make(map[string]struct{}, len(statusBubblesTypeOrder))
	for _, t := range statusBubblesTypeOrder {
		out[string(t)] = struct{}{}
	}
	return out
}

// renderBubblesLine produces the styled `ox status` block: one main line
// plus an optional warnings hint. Returns an empty string only if the
// caller passed a zero-value summary (defensive).
func renderBubblesLine(s statusBubblesSummary) string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(statusLabelStyle.Render("Knowledge bubbles"))
	main := formatBubblesLine(s)
	// strip the "Knowledge bubbles: " prefix — the label column already
	// shows the field name, so the value column shouldn't repeat it.
	value := strings.TrimPrefix(main, "Knowledge bubbles: ")
	if s.Unavailable {
		b.WriteString(statusMutedStyle.Render(value))
	} else {
		b.WriteString(statusValueStyle.Render(value))
	}
	if len(s.Warnings) > 0 && !s.Unavailable {
		b.WriteString(" ")
		b.WriteString(statusWarningStyle.Render("(warnings: see ox doctor)"))
	}
	b.WriteString("\n")
	return b.String()
}

// statusBubbleRow is one bubble plus its derived local state: where the
// checkout lives (canonical paths.KBDir keyed by kb_id) and what git says
// about it. Computed once so the human renderer and JSON emitter agree.
type statusBubbleRow struct {
	Bubble kb.Bubble
	Path   string
	Cloned bool
	Git    gitRepoStatus
}

// statusKBDirForBubble resolves a bubble's canonical checkout directory.
// Seam variable so tests can point rows at a temp dir without endpoint
// plumbing; production is paths.KBDir.
var statusKBDirForBubble = func(kbID string) string {
	return paths.KBDir(kbID)
}

// collectBubbleRows derives per-bubble local state from a summary. Sync
// times layer the same way as the team-context cards: daemon (freshest)
// via LastSyncForPath, else none. Rows sort personal-scope first, then by
// type priority, then slug, so a user's own bubbles lead the section.
// Safe with a nil daemonStatus (JSON path).
func collectBubbleRows(s statusBubblesSummary, daemonStatus *daemon.StatusData) []statusBubbleRow {
	rows := make([]statusBubbleRow, 0, len(s.Bubbles))
	for _, bub := range s.Bubbles {
		row := statusBubbleRow{Bubble: bub, Path: bub.LocalPath}
		if row.Path == "" && bub.KBID != "" {
			row.Path = statusKBDirForBubble(bub.KBID)
		}
		if row.Path != "" {
			if _, err := os.Stat(filepath.Join(row.Path, ".git")); err == nil {
				row.Cloned = true
				lastSync, hasSync := daemonStatus.LastSyncForPath(row.Path)
				row.Git = getGitRepoStatus(row.Path, lastSync, hasSync)
			}
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		bi, bj := rows[i].Bubble, rows[j].Bubble
		if ri, rj := bubbleScopeRank(bi.ScopeType), bubbleScopeRank(bj.ScopeType); ri != rj {
			return ri < rj
		}
		if pi, pj := kbTypePriority(bi.Type), kbTypePriority(bj.Type); pi != pj {
			return pi < pj
		}
		return bi.Slug < bj.Slug
	})
	return rows
}

// bubbleScopeRank orders personal-scope ("user") bubbles ahead of
// team-scope ones. Unknown scope types trail with team.
func bubbleScopeRank(scopeType string) int {
	if scopeType == "user" {
		return 0
	}
	return 1
}

// kbLifecycleProvisioning is the lifecycle_state for a bubble whose
// server-side repo is still being created. The failed counterpart
// (kbLifecycleProvisionFailed) is declared in doctor_kb.go; "active" and
// unknown/empty values are treated as cloneable.
const kbLifecycleProvisioning = "provisioning"

// bubbleCloneable reports whether an unmounted bubble can actually be
// cloned — i.e. the server has (or should have) a repo for it. While
// provisioning is in flight or has failed there is nothing for
// `ox doctor --fix` to clone, so the repair hint must not appear.
func bubbleCloneable(b kb.Bubble) bool {
	return b.LifecycleState != kbLifecycleProvisioning &&
		b.LifecycleState != kbLifecycleProvisionFailed
}

// renderBubblesSection renders the full knowledge-bubbles block: the
// summary line, then one card per bubble in the Ledger / Team Context
// card style — name, type + slug, mount path, and sync status, with the
// same staleness warning and not-cloned repair hint the team-context
// cards show.
func renderBubblesSection(s statusBubblesSummary, daemonStatus *daemon.StatusData) string {
	var b strings.Builder
	b.WriteString(renderBubblesLine(s))
	if s.Unavailable || len(s.Bubbles) == 0 {
		return b.String()
	}

	bootstrapping := daemonStatus.IsBootstrapping()
	for _, r := range collectBubbleRows(s, daemonStatus) {
		bub := r.Bubble
		name := bub.Name
		if name == "" {
			name = bub.Slug
		}
		if name == "" {
			name = bub.KBID
		}
		b.WriteString("\n")
		b.WriteString(statusLabelStyle.Render("KB"))
		b.WriteString(statusValueStyle.Render(name))
		b.WriteString("\n")

		b.WriteString(statusLabelStyle.Render("  Type"))
		b.WriteString(statusValueStyle.Render(formatKBType(bub.Type)))
		if bub.Slug != "" {
			b.WriteString(" ")
			b.WriteString(renderSlugRef("#", bub.Slug))
		}
		b.WriteString("\n")

		if r.Path != "" {
			b.WriteString(statusLabelStyle.Render("  Path"))
			b.WriteString(statusMutedStyle.Render(shortenHome(r.Path)))
			b.WriteString("\n")
		}

		b.WriteString(statusLabelStyle.Render("  Status"))
		switch {
		case !r.Cloned && bub.LifecycleState == kbLifecycleProvisioning:
			// no checkout AND the server hasn't finished provisioning the
			// repo — a clone hint would send doctor after a repo that does
			// not exist yet.
			b.WriteString(statusMutedStyle.Render("⟳ provisioning"))
		case !r.Cloned && bub.LifecycleState == kbLifecycleProvisionFailed:
			b.WriteString(statusErrorStyle.Render("✗ provisioning failed"))
		default:
			b.WriteString(renderBubbleStatus(r.Git, r.Cloned, bootstrapping))
		}
		b.WriteString("\n")

		if r.Cloned {
			// staleness warning, same threshold as the team-context cards
			syncState := daemon.LoadSyncState(r.Path)
			if syncState.IsStale(daemon.DefaultStalenessThreshold) && !syncState.LastSync.IsZero() {
				b.WriteString(statusLabelStyle.Render(""))
				b.WriteString(statusWarningStyle.Render(fmt.Sprintf("⚠ stale (last sync %s)", status.FormatTimeAgo(syncState.LastSync))))
				b.WriteString("\n")
			}
		} else if !bootstrapping && bubbleCloneable(bub) {
			if bub.RepoURL != "" {
				b.WriteString(statusLabelStyle.Render("  Remote"))
				b.WriteString(statusMutedStyle.Render(bub.RepoURL))
				b.WriteString("\n")
			}
			b.WriteString(statusLabelStyle.Render(""))
			b.WriteString(statusMutedStyle.Render("Run 'ox doctor --fix' to clone"))
			b.WriteString("\n")
		}
	}
	return b.String()
}

// buildBubblesJSON converts a summary into the BubblesJSON payload. Uses
// status.BubblesJSON directly so the shape stays in sync with the type
// declared in internal/status/types.go.
func buildBubblesJSON(s statusBubblesSummary) *status.BubblesJSON {
	if s.Unavailable {
		// surface unavailability as zero counts + a synthetic warning
		// rather than omitting the field — JSON consumers should see
		// "the fetch ran, it produced nothing" explicitly.
		return &status.BubblesJSON{
			Total: 0,
			Warnings: []status.BubbleWarningJSON{
				{Error: "kb fetch unavailable"},
			},
		}
	}
	out := &status.BubblesJSON{
		Total: s.Total,
	}
	if len(s.ByType) > 0 {
		out.ByType = make(map[string]int, len(s.ByType))
		for k, v := range s.ByType {
			out.ByType[k] = v
		}
	}
	if len(s.Bubbles) > 0 {
		// nil daemonStatus: JSON reports git-derived state; the freshest
		// daemon sync time only refines the human status cell.
		rows := collectBubbleRows(s, nil)
		out.Bubbles = make([]status.BubbleJSON, 0, len(rows))
		for _, r := range rows {
			var syncStatus string
			switch {
			case r.Cloned:
				syncStatus, _ = status.FormatGitRepoStatus(r.Git)
			case r.Bubble.LifecycleState == kbLifecycleProvisioning:
				syncStatus = "provisioning"
			case r.Bubble.LifecycleState == kbLifecycleProvisionFailed:
				// same wording as the human card ("✗ provisioning failed")
				// minus the glyph — one status vocabulary across formats.
				syncStatus = "provisioning failed"
			default:
				syncStatus = "not cloned"
			}
			out.Bubbles = append(out.Bubbles, status.BubbleJSON{
				KBID:       r.Bubble.KBID,
				Type:       jsonTypeForBubble(r.Bubble.Type),
				Slug:       r.Bubble.Slug,
				Name:       r.Bubble.Name,
				ScopeType:  r.Bubble.ScopeType,
				Path:       r.Path,
				Cloned:     r.Cloned,
				SyncStatus: syncStatus,
			})
		}
	}
	if len(s.Warnings) > 0 {
		out.Warnings = make([]status.BubbleWarningJSON, 0, len(s.Warnings))
		for _, w := range s.Warnings {
			out.Warnings = append(out.Warnings, status.BubbleWarningJSON{Error: w.Err})
		}
	}
	return out
}

// collectBubblesSummary fetches bubbles with a short timeout and returns a
// summary. Fetch problems never propagate upward — `ox status` must keep
// rendering the rest of the report.
func collectBubblesSummary(fetch statusBubblesFetch) statusBubblesSummary {
	if fetch == nil {
		return statusBubblesSummary{Unavailable: true}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return summarizeBubbles(fetch(ctx))
}

// statusBubblesFetch is the seam between status rendering and the KB fetch,
// so tests can inject canned results without auth/endpoint plumbing.
type statusBubblesFetch func(ctx context.Context) kb.ListResult

// statusBubblesFetchForRoot is the production wiring: same client
// construction as `ox kb list` so the two surfaces cannot disagree.
// Tests assign a fake.
var statusBubblesFetchForRoot = func(projectRoot string) statusBubblesFetch {
	source, ep := newDefaultKBListSource(projectRoot)
	scopes := ambientKBScopes(projectRoot)
	return func(ctx context.Context) kb.ListResult {
		return kb.FetchBubbles(ctx, source, ep, scopes)
	}
}

// commonDir returns the longest shared leading directory of a and b.
func commonDir(a, b string) string {
	if a == b {
		return a
	}
	sep := string(os.PathSeparator)
	as, bs := strings.Split(a, sep), strings.Split(b, sep)
	n := len(as)
	if len(bs) < n {
		n = len(bs)
	}
	i := 0
	for i < n && as[i] == bs[i] {
		i++
	}
	return strings.Join(as[:i], sep)
}

// renderSlugRef styles a slug reference: the sigil (@ for an owner, # for a
// bubble) is muted so the slug itself stands out — e.g. dim("@") + bright("sageox").
func renderSlugRef(sigil, slug string) string {
	return statusMutedStyle.Render(sigil) + statusValueStyle.Render(slug)
}

// padCell right-pads a (possibly ANSI-styled) cell to width w using its
// visible width, so color codes don't break column alignment.
func padCell(cell string, w int) string {
	if gap := w - lipgloss.Width(cell); gap > 0 {
		return cell + strings.Repeat(" ", gap)
	}
	return cell
}

// renderBubbleStatus renders the dense status cell for a bubble's local
// checkout: a crisp freshness age when clean ("✓ 2h"), the actionable count
// when dirty ("⚠ 6 uncommitted"), a red ⚠ when wedged, or a clone hint.
func renderBubbleStatus(st gitRepoStatus, cloned, bootstrapping bool) string {
	switch {
	case !cloned:
		if bootstrapping {
			return statusMutedStyle.Render("⟳ setting up")
		}
		return statusWarningStyle.Render("⚠ not cloned")
	case st.Error != "":
		return statusErrorStyle.Render("✗ " + st.Error)
	case st.IsWedged():
		// ⚠ glyph in error color — wedged needs eyes like uncommitted, but is worse
		if st.RebaseInProgress {
			return statusErrorStyle.Render("⚠ rebase wedged")
		}
		return statusErrorStyle.Render("⚠ diverged")
	case st.UncommittedCount > 0:
		return statusWarningStyle.Render(fmt.Sprintf("⚠ %d uncommitted", st.UncommittedCount))
	case st.HasLastSync:
		return statusSuccessStyle.Render("✓ " + status.CompactAge(st.LastSync))
	default:
		return statusSuccessStyle.Render("✓ synced")
	}
}

// shortenHome replaces the user's home directory prefix with ~ for display.
func shortenHome(path string) string {
	if path == "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}
