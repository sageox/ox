package plan

import (
	"strings"
	"testing"
)

// enrichedResult is a Result that carries SageOx enrichment (one deterministic
// badge). un-enriched plans use the zero Result.
func enrichedResult() Result {
	// a deterministic, ox-computed badge — the kind that renders an anchored OX
	// marker (so a render without the marker is correctly flagged).
	return Result{Annotations: []Annotation{{Kind: BadgeDeterministic, Type: BadgeCollision, Why: "contended"}}}
}

// contextOnlyResult carries enrichment via the context bundle but no badges.
func contextOnlyResult() Result {
	return Result{Context: []ContextItem{{Kind: "session", Title: "prior work"}}}
}

// the canonical attribution fragments the html-plan skill is spec'd to emit.
const (
	footerCredit = `<footer>Team context enriched by SageOx</footer>`
	oxMarker     = `<button aria-label="SageOx insight">…</button>`
	inlineAvatar = `<img src="data:image/png;base64,AAAA">`
	remoteAvatar = `<img src="https://avatars.githubusercontent.com/u/224450799?s=64">`
)

// TestLintBranding_EarnedCreditRequired verifies the core guarantee: an
// enriched plan whose render omits the footer credit is flagged, and a fully
// attributed render is clean.
// Failure prevented: SageOx silently loses credit on a team-context-aware plan.
func TestLintBranding_EarnedCreditRequired(t *testing.T) {
	tests := []struct {
		name      string
		html      string
		res       Result
		wantRules []string
	}{
		{
			name: "enriched + credit + marker is clean",
			html: footerCredit + oxMarker + inlineAvatar,
			res:  enrichedResult(),
		},
		{
			name:      "enriched but no credit is flagged",
			html:      oxMarker + inlineAvatar,
			res:       enrichedResult(),
			wantRules: []string{"branding.footer-credit"},
		},
		{
			name:      "enriched with badges but no OX marker is flagged",
			html:      footerCredit,
			res:       enrichedResult(),
			wantRules: []string{"branding.ox-marker"},
		},
		{
			name: "context-only enrichment needs credit, not a marker",
			html: footerCredit,
			res:  contextOnlyResult(),
		},
		{
			name:      "context-only enrichment without credit is flagged",
			html:      "<p>plan</p>",
			res:       contextOnlyResult(),
			wantRules: []string{"branding.footer-credit"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertRules(t, LintBranding([]byte(tt.html), tt.res), tt.wantRules)
		})
	}
}

// TestLintBranding_NoOverclaim verifies an un-enriched plan must NOT credit
// SageOx — there is nothing to credit.
// Failure prevented: marketing-y credit appears on plans ox did not enrich.
func TestLintBranding_NoOverclaim(t *testing.T) {
	t.Run("un-enriched + no credit is clean", func(t *testing.T) {
		assertRules(t, LintBranding([]byte("<p>greenfield plan</p>"), Result{}), nil)
	})
	t.Run("un-enriched + credit is overclaim", func(t *testing.T) {
		assertRules(t, LintBranding([]byte(footerCredit), Result{}), []string{"branding.overclaim"})
	})
}

// TestLintBranding_RemoteAvatarBanned verifies the self-contained invariant: a
// live remote avatar is always flagged, even on an otherwise-correct render.
// Failure prevented: the page needs network to show the SageOx mark, breaking
// file:// rendering for the reviewer.
func TestLintBranding_RemoteAvatarBanned(t *testing.T) {
	html := footerCredit + `<button aria-label="SageOx insight">` + remoteAvatar + `</button>`
	got := LintBranding([]byte(html), enrichedResult())
	assertRules(t, got, []string{"branding.remote-avatar"})
}

// TestLintBranding_EmptyHTMLNoFindings verifies linting is a no-op when there is
// no render to check (fail-open: a save without --html must not be flagged).
func TestLintBranding_EmptyHTMLNoFindings(t *testing.T) {
	if got := LintBranding(nil, enrichedResult()); got != nil {
		t.Errorf("expected no findings on empty html, got %+v", got)
	}
}

// assertRules checks the finding rule-ids match exactly (order-independent).
func assertRules(t *testing.T, got []BrandingFinding, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d finding(s) %v, want %d %v", len(got), ruleIDs(got), len(want), want)
	}
	have := make(map[string]bool, len(got))
	for _, f := range got {
		have[f.Rule] = true
		// every message must be non-empty and name something actionable.
		if strings.TrimSpace(f.Message) == "" {
			t.Errorf("finding %s has empty message", f.Rule)
		}
	}
	for _, r := range want {
		if !have[r] {
			t.Errorf("missing expected finding %q; got %v", r, ruleIDs(got))
		}
	}
}

func ruleIDs(fs []BrandingFinding) []string {
	ids := make([]string, len(fs))
	for i, f := range fs {
		ids[i] = f.Rule
	}
	return ids
}
