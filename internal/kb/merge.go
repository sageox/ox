package kb

// merge.go — three-source merger that fans out in parallel to:
//
//  1. /api/v1/kb (the new knowledge-bubble API; see internal/api/kb.go)
//  2. /api/v1/cli/repos (legacy team-context endpoint)
//  3. local ledger registry (filesystem + project state)
//
// The output is a unified list of Bubbles. The three sources are queried
// concurrently — failure of one is non-fatal and surfaces as a per-source
// SourceWarning. This file is the single helper consumed by `ox kb list`,
// `ox status`, prime, and the daemon sync loop, per the plan
// "Three-source merger" bullet.
//
// Dedup precedence (highest to lowest):
//
//	kb_id  >  repo_id  >  (slug + endpoint)
//
// When a kb-API row collides with a legacy row by any of these keys, the
// kb-API row wins and the legacy synthesis is suppressed. As the backend
// migrates legacy rows into the kb table, the dedup naturally takes effect
// without a CLI release; legacy fan-out can be retired in a much later
// release.
//
// Escape hatch: OX_KB_DISABLE=1 skips the kb-API source entirely. Mirrors
// the OX_XDG_DISABLE pattern. Used for debugging rollout issues, not daily
// operation.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/endpoint"
)

// Source identifies which of the three fan-out sources produced a Bubble or
// a warning. Stable strings — surfaced in JSON output and logs.
type Source string

const (
	SourceKB         Source = "kb"
	SourceTeamLegacy Source = "team_legacy"
	SourceLedger     Source = "ledger_legacy"
)

// envDisableKBSource is the escape hatch from the plan — when set to "1"
// (or any non-empty truthy value) the merger skips the kb-API source
// entirely and returns only legacy rows.
const envDisableKBSource = "OX_KB_DISABLE"

// Bubble is the unified row returned by Merge. It's a superset of the
// fields produced by the three sources — empty values are normal for any
// field that the source row didn't supply.
type Bubble struct {
	// KBID is the immutable kb identifier from /api/v1/kb. Empty for
	// legacy rows that haven't been migrated into the kb table yet.
	KBID string

	// Type is the kb_type bucket. Legacy team rows synthesize KBTypeTeam;
	// legacy ledger rows synthesize KBTypeRepo. Unknown server values are
	// already collapsed to KBTypeUnknown by the kb client.
	Type api.KBType

	// Slug is the human-readable slug (kebab-case). Used as the secondary
	// dedup key together with Endpoint when neither KBID nor RepoID matches.
	Slug string

	// Name is the display name.
	Name string

	// ViewerRole is the caller's role on this bubble ("owner", "member",
	// "viewer"). Only kb-API rows populate this today.
	ViewerRole string

	// LocalPath is the on-disk checkout path. For kb-API rows this is the
	// canonical KBDir(kb_id); for legacy rows it's whatever path the
	// synthesizer reports (team context dir or ledger dir).
	LocalPath string

	// RepoURL is the git clone URL when known.
	RepoURL string

	// RepoID is the SageOx repo_id, populated for legacy ledger rows and
	// for kb-API rows when the server supplies it. Used as the secondary
	// dedup key.
	RepoID string

	// Endpoint is the SageOx API endpoint this row belongs to (normalized
	// via endpoint.NormalizeEndpoint). Used together with Slug as the
	// tertiary dedup key.
	Endpoint string

	// Source identifies which fan-out branch produced this row.
	Source Source

	// Legacy is true for synthesized rows from /api/v1/cli/repos and the
	// local ledger registry. kb-API rows have Legacy=false.
	Legacy bool
}

// SourceWarning is a non-fatal error from one of the three sources. The
// merger collects these into MergeResult.Warnings so the caller can render
// them without losing the rows from sources that did succeed.
type SourceWarning struct {
	Source Source
	Err    string
}

// MergeResult is the return value of Merge. Bubbles is the deduped union;
// Warnings is one entry per source that errored (non-fatally).
type MergeResult struct {
	Bubbles  []Bubble
	Warnings []SourceWarning
}

// KBSource is the contract for fetching new-API kb rows. Defined as an
// interface so tests can supply fakes without spinning up an httptest
// server when they only care about the merge logic.
type KBSource interface {
	ListBubbles(ctx context.Context) ([]api.KB, error)
}

// LegacyTeamSource is the contract for fetching legacy team-context rows
// from /api/v1/cli/repos. The merger only needs the repo map and the
// endpoint the rows came from — the rest of ReposResponse is unused.
type LegacyTeamSource interface {
	ListTeamContexts(ctx context.Context) (rows []LegacyTeamRow, endpoint string, err error)
}

// LegacyTeamRow is the merger-facing projection of a /api/v1/cli/repos
// team-context entry. Decoupled from api.RepoInfo so a future API shape
// change doesn't ripple into the merger.
type LegacyTeamRow struct {
	TeamID   string // team_xxx — used as RepoID-equivalent for dedup
	Name     string
	Slug     string
	URL      string // git clone URL
	LocalDir string // on-disk team-context checkout (may be empty if not yet cloned)
}

// LedgerSource is the contract for enumerating local ledger registry
// entries. Implementations typically scan paths.LedgersDataDir(...) for
// each known endpoint and read each ledger's project config.
type LedgerSource interface {
	ListLedgers(ctx context.Context) ([]LegacyLedgerRow, error)
}

// LegacyLedgerRow is the merger-facing projection of a local ledger.
type LegacyLedgerRow struct {
	RepoID   string // SageOx repo_id (extracted from path or project config)
	Name     string // display name (typically the host repo's name)
	Slug     string // optional kebab-case slug
	LocalDir string // on-disk ledger checkout path
	Endpoint string // SageOx API endpoint this ledger is bound to
	URL      string // git clone URL when known (rare for legacy ledgers)
}

// Merger fans out to the three sources and merges the results. Construct
// via NewMerger; the zero value is not usable.
type Merger struct {
	kb     KBSource
	teams  LegacyTeamSource
	ledger LedgerSource

	// disableKB short-circuits the kb-API source. Populated from the
	// OX_KB_DISABLE env var at construction time so tests can override
	// via direct field assignment if needed.
	disableKB bool
}

// NewMerger constructs a Merger. Any of the three sources may be nil — a
// nil source contributes zero rows and never produces a warning, which is
// the expected shape during daemon startup or in narrow tests that only
// exercise one branch.
func NewMerger(kb KBSource, teams LegacyTeamSource, ledger LedgerSource) *Merger {
	return &Merger{
		kb:        kb,
		teams:     teams,
		ledger:    ledger,
		disableKB: kbDisabledByEnv(),
	}
}

// kbDisabledByEnv returns true when OX_KB_DISABLE is set to a value
// commonly understood as "on". Empty / "0" / "false" all leave the kb
// source enabled.
func kbDisabledByEnv() bool {
	v := strings.TrimSpace(os.Getenv(envDisableKBSource))
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// Merge fans out in parallel to all three sources, deduplicates by stable
// identifier, and returns the unified result. The returned error is
// reserved for catastrophic failures unrelated to any single source —
// today it's always nil; per-source failures land in Warnings.
func (m *Merger) Merge(ctx context.Context) (MergeResult, error) {
	var (
		wg sync.WaitGroup

		mu       sync.Mutex
		kbRows   []api.KB
		teamRows []LegacyTeamRow
		teamEP   string
		ledRows  []LegacyLedgerRow
		warnings []SourceWarning
	)

	addWarning := func(src Source, err error) {
		mu.Lock()
		defer mu.Unlock()
		warnings = append(warnings, SourceWarning{Source: src, Err: err.Error()})
	}

	// --- source 1: /api/v1/kb (new) ---
	if m.kb != nil && !m.disableKB {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, err := m.kb.ListBubbles(ctx)
			if err != nil {
				// ErrKBAPIUnavailable is the "flag off / endpoint
				// missing" path; per the plan it is NOT a warning,
				// just zero rows from this source.
				if errors.Is(err, api.ErrKBAPIUnavailable) {
					slog.Debug("kb merge: kb source unavailable, treating as 0 rows", "err", err)
					return
				}
				addWarning(SourceKB, err)
				slog.Warn("kb merge: kb source failed", "err", err)
				return
			}
			mu.Lock()
			kbRows = rows
			mu.Unlock()
		}()
	} else if m.disableKB {
		slog.Debug("kb merge: kb source disabled via OX_KB_DISABLE")
	}

	// --- source 2: /api/v1/cli/repos (legacy team contexts) ---
	if m.teams != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, ep, err := m.teams.ListTeamContexts(ctx)
			if err != nil {
				addWarning(SourceTeamLegacy, err)
				slog.Warn("kb merge: legacy team source failed", "err", err)
				return
			}
			mu.Lock()
			teamRows = rows
			teamEP = ep
			mu.Unlock()
		}()
	}

	// --- source 3: local ledger registry ---
	if m.ledger != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, err := m.ledger.ListLedgers(ctx)
			if err != nil {
				addWarning(SourceLedger, err)
				slog.Warn("kb merge: ledger source failed", "err", err)
				return
			}
			mu.Lock()
			ledRows = rows
			mu.Unlock()
		}()
	}

	wg.Wait()

	// honor cancellation — a context that fired between fan-out and merge
	// should report cleanly even if every goroutine still managed to run.
	if err := ctx.Err(); err != nil {
		warnings = append(warnings, SourceWarning{Source: SourceKB, Err: fmt.Sprintf("context: %v", err)})
	}

	bubbles := mergeAndDedup(kbRows, teamRows, teamEP, ledRows)
	slog.Debug("kb merge: result",
		"kb_rows", len(kbRows),
		"team_rows", len(teamRows),
		"ledger_rows", len(ledRows),
		"merged", len(bubbles),
		"warnings", len(warnings))

	return MergeResult{Bubbles: bubbles, Warnings: warnings}, nil
}

// mergeAndDedup performs the actual fan-in. It runs in three passes so the
// dedup precedence is unambiguous: kb-API rows are inserted first and own
// their identifier keys; legacy rows are then admitted only if none of
// their keys collide with an already-claimed kb-API key.
func mergeAndDedup(kbRows []api.KB, teamRows []LegacyTeamRow, teamEP string, ledRows []LegacyLedgerRow) []Bubble {
	out := make([]Bubble, 0, len(kbRows)+len(teamRows)+len(ledRows))

	// claimed-key sets, one per dedup tier
	claimedByKBID := make(map[string]bool)
	claimedByRepoID := make(map[string]bool)
	claimedBySlugEP := make(map[string]bool)

	teamEPNorm := endpoint.NormalizeEndpoint(teamEP)

	// pass 1: kb-API rows always win
	for _, r := range kbRows {
		b := Bubble{
			KBID:       r.KBID,
			Type:       r.KBType, // already normalized by the kb client
			Slug:       r.Slug,
			Name:       r.Name,
			ViewerRole: r.ViewerRole,
			RepoURL:    r.RepoURL,
			Source:     SourceKB,
			Legacy:     false,
			// LocalPath / Endpoint are populated by the caller from
			// project context — the kb API doesn't return them today.
		}
		if r.KBID != "" {
			claimedByKBID[r.KBID] = true
		}
		if r.Slug != "" {
			claimedBySlugEP[slugEPKey(r.Slug, "")] = true
		}
		out = append(out, b)
	}

	// pass 2: legacy team contexts
	for _, t := range teamRows {
		if t.TeamID != "" && claimedByRepoID[t.TeamID] {
			continue
		}
		key := slugEPKey(t.Slug, teamEPNorm)
		if t.Slug != "" && claimedBySlugEP[key] {
			continue
		}
		// also suppress when an empty-endpoint kb row already claimed
		// this slug — server doesn't yet return endpoint per row, so
		// matching on slug alone is the safest forward-compat behavior.
		if t.Slug != "" && claimedBySlugEP[slugEPKey(t.Slug, "")] {
			continue
		}
		out = append(out, Bubble{
			Type:      api.KBTypeTeam,
			Slug:      t.Slug,
			Name:      t.Name,
			LocalPath: t.LocalDir,
			RepoURL:   t.URL,
			RepoID:    t.TeamID,
			Endpoint:  teamEPNorm,
			Source:    SourceTeamLegacy,
			Legacy:    true,
		})
		if t.TeamID != "" {
			claimedByRepoID[t.TeamID] = true
		}
		if t.Slug != "" {
			claimedBySlugEP[key] = true
		}
	}

	// pass 3: legacy ledger rows
	for _, l := range ledRows {
		if l.RepoID != "" && claimedByRepoID[l.RepoID] {
			continue
		}
		epNorm := endpoint.NormalizeEndpoint(l.Endpoint)
		key := slugEPKey(l.Slug, epNorm)
		if l.Slug != "" && claimedBySlugEP[key] {
			continue
		}
		// also suppress when an empty-endpoint kb row already claimed
		// this slug — server doesn't yet return endpoint per row, so
		// matching on slug alone is the safest forward-compat behavior.
		// Mirrors the same fallback in pass 2 above so kb-API rows win
		// across BOTH legacy sources, not just legacy team contexts.
		if l.Slug != "" && claimedBySlugEP[slugEPKey(l.Slug, "")] {
			continue
		}
		out = append(out, Bubble{
			Type:      api.KBTypeRepo,
			Slug:      l.Slug,
			Name:      l.Name,
			LocalPath: l.LocalDir,
			RepoURL:   l.URL,
			RepoID:    l.RepoID,
			Endpoint:  epNorm,
			Source:    SourceLedger,
			Legacy:    true,
		})
		if l.RepoID != "" {
			claimedByRepoID[l.RepoID] = true
		}
		if l.Slug != "" {
			claimedBySlugEP[key] = true
		}
	}

	return out
}

// slugEPKey is the composite key used for the (slug, endpoint) tier of
// dedup. Endpoint is normalized before this is called.
func slugEPKey(slug, ep string) string {
	return slug + "@" + ep
}
