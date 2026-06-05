package plan

import (
	"context"
	"testing"
	"time"

	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/ledgersearch"
	"github.com/sageox/ox/internal/teamdocs"
)

// --- A. Murmur match + scoring ---

// TestScoreMurmur verifies a murmur is bundled only when its files or topic
// overlap the plan, with file matches outranking topic matches.
// Failure prevented: an unrelated murmur leaking into the client's context
// budget, or a relevant one (explicit file ref) being dropped.
func TestScoreMurmur(t *testing.T) {
	want := map[string]struct{}{"internal/auth/token.go": {}}
	headings := []string{"Refactor OAuth refresh"}

	tests := []struct {
		name      string
		murmur    ledger.MurmurFile
		wantMatch bool
		wantScore float64
	}{
		{
			name: "explicit file reference scores highest",
			murmur: ledger.MurmurFile{
				Metadata: map[string]string{"files": "internal/auth/token.go"},
			},
			wantMatch: true,
			wantScore: 1.0,
		},
		{
			name: "directory-style file reference matches",
			murmur: ledger.MurmurFile{
				Metadata: map[string]string{"files": "internal/auth/"},
			},
			wantMatch: true,
			wantScore: 1.0,
		},
		{
			name: "topic overlap is a softer match",
			murmur: ledger.MurmurFile{
				Topic: "oauth provider wiring",
			},
			wantMatch: true,
			wantScore: 0.6,
		},
		{
			name: "no file and unrelated topic does not match",
			murmur: ledger.MurmurFile{
				Topic:    "unrelated billing work",
				Metadata: map[string]string{"files": "internal/billing/charge.go"},
			},
			wantMatch: false,
			wantScore: 0,
		},
		{
			name:      "empty murmur does not match",
			murmur:    ledger.MurmurFile{},
			wantMatch: false,
			wantScore: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMatch, gotScore := scoreMurmur(tt.murmur, want, headings)
			if gotMatch != tt.wantMatch {
				t.Fatalf("scoreMurmur() match = %v, want %v", gotMatch, tt.wantMatch)
			}
			if gotScore != tt.wantScore {
				t.Fatalf("scoreMurmur() score = %v, want %v", gotScore, tt.wantScore)
			}
		})
	}
}

// TestMurmurContextItems verifies relevant murmurs become well-formed bundle
// items and irrelevant ones are excluded.
// Failure prevented: malformed ContextItem (missing kind/ref/author) reaching
// the client agent, or noise murmurs inflating the bundle.
func TestMurmurContextItems(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	files := []string{"internal/auth/token.go"}
	headings := []string{"OAuth refresh"}

	murmurs := []ledger.MurmurFile{
		{
			ID:          "m-relevant",
			PrincipalID: "alice",
			Topic:       "auth token cleanup",
			Content:     "editing the token refresh path",
			Timestamp:   now.Add(-2 * time.Hour),
			Metadata:    map[string]string{"files": "internal/auth/token.go"},
		},
		{
			ID:        "m-unrelated",
			Topic:     "billing invoices",
			Content:   "nothing to do with the plan",
			Timestamp: now.Add(-1 * time.Hour),
			Metadata:  map[string]string{"files": "internal/billing/charge.go"},
		},
	}

	items := murmurContextItems(murmurs, files, headings, now)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	got := items[0]
	if got.Kind != "murmur" {
		t.Errorf("kind = %q, want murmur", got.Kind)
	}
	if got.Ref != "m-relevant" {
		t.Errorf("ref = %q, want m-relevant", got.Ref)
	}
	if got.Author != "alice" {
		t.Errorf("author = %q, want alice", got.Author)
	}
	if got.When != "2026-06-03" {
		t.Errorf("when = %q, want 2026-06-03", got.When)
	}
	if got.Score != weightMurmur*1.0 {
		t.Errorf("score = %v, want %v", got.Score, weightMurmur*1.0)
	}
}

// --- B. Session items from ledgersearch ---

// TestSessionContextItems verifies session results become session items with a
// `ox session view`-usable ref, and that murmur-typed results are skipped (the
// dedicated murmur pass owns those with richer scoring).
// Failure prevented: double-surfacing a murmur as both murmur and session, or a
// session ref the client can't pass to `ox session view`.
func TestSessionContextItems(t *testing.T) {
	results := []ledgersearch.Result{
		{
			DocType:   "session",
			SourceID:  "2026-05-01T09-30-bob-agent7",
			Text:      "implemented oauth token refresh and caching",
			Score:     0.9,
			CreatedAt: "2026-05-01T09:30:00Z",
		},
		{
			DocType:  "murmur",
			SourceID: "m-skip",
			Text:     "should not appear as a session",
			Score:    0.8,
		},
	}

	items := sessionContextItems(results)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 (murmur must be skipped)", len(items))
	}
	got := items[0]
	if got.Kind != "session" {
		t.Errorf("kind = %q, want session", got.Kind)
	}
	if got.Ref != "2026-05-01T09-30-bob-agent7" {
		t.Errorf("ref = %q, want the session folder name", got.Ref)
	}
	if got.Author != "bob" {
		t.Errorf("author = %q, want bob", got.Author)
	}
	if got.Score != weightSession*0.9 {
		t.Errorf("score = %v, want %v", got.Score, weightSession*0.9)
	}
}

// --- C. Team docs: matching + kind classification ---

// TestClassifyDocKind verifies team docs are mapped to allowed bundle kinds.
// Failure prevented: an ADR surfacing under the wrong kind, breaking the client
// agent's aligns/conflicts reasoning which keys off adr/decision.
func TestClassifyDocKind(t *testing.T) {
	tests := []struct {
		name, title, want string
	}{
		{"adr-018-perf.md", "ADR 018: Performance", "adr"},
		{"architecture-decision-log.md", "", "adr"},
		{"endpoint-decision.md", "Endpoint Decision", "decision"},
		{"api-conventions.md", "API Conventions", "discussion"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyDocKind(tt.name, tt.title); got != tt.want {
				t.Fatalf("classifyDocKind(%q, %q) = %q, want %q", tt.name, tt.title, got, tt.want)
			}
		})
	}
}

// TestTeamDocContextItems verifies only topic-matching docs become items and that
// non-matching docs are excluded.
// Failure prevented: every team doc flooding the bundle regardless of relevance.
func TestTeamDocContextItems(t *testing.T) {
	terms := map[string]struct{}{"oauth": {}, "token": {}, "refresh": {}}
	docs := []teamdocs.TeamDoc{
		{
			Name:        "adr-009-oauth.md",
			Title:       "ADR 009: OAuth token strategy",
			Description: "how we refresh tokens",
			Path:        "/team/docs/adr-009-oauth.md",
		},
		{
			Name:        "deploy-runbook.md",
			Title:       "Deploy Runbook",
			Description: "kubernetes rollout steps",
			Path:        "/team/docs/deploy-runbook.md",
		},
	}

	items := teamDocContextItems(docs, terms)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 (only the matching doc)", len(items))
	}
	got := items[0]
	if got.Kind != "adr" {
		t.Errorf("kind = %q, want adr", got.Kind)
	}
	if got.Ref != "/team/docs/adr-009-oauth.md" {
		t.Errorf("ref = %q, want the doc path", got.Ref)
	}
	if got.Score <= 0 {
		t.Errorf("score = %v, want > 0", got.Score)
	}
}

// TestTopicTerms verifies salient terms are drawn from query+headings with
// stopwords and short tokens dropped.
// Failure prevented: stopwords matching every doc, or an empty topic silently
// matching nothing meaningful.
func TestTopicTerms(t *testing.T) {
	terms := topicTerms("oauth token", []string{"Add the refresh flow"})
	for _, want := range []string{"oauth", "token", "refresh", "flow"} {
		if _, ok := terms[want]; !ok {
			t.Errorf("missing term %q", want)
		}
	}
	// "the" is a stopword, "add" is a stopword — must be absent.
	for _, banned := range []string{"the", "add"} {
		if _, ok := terms[banned]; ok {
			t.Errorf("stopword %q leaked into terms", banned)
		}
	}
}

// --- D. Ranking + capping ---

// TestRankAndCap verifies above-floor items are ordered by score desc and the
// bundle is truncated to the cap.
// Failure prevented: an unbounded bundle blowing the client agent's context
// budget, or low-relevance items crowding out strong ones.
func TestRankAndCap(t *testing.T) {
	items := []ContextItem{
		{Kind: "session", Ref: "weak", Score: 0.6},
		{Kind: "murmur", Ref: "high", Score: 0.9},
		{Kind: "decision", Ref: "mid", Score: 0.7},
	}
	got := rankAndCap(items, 2)
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2 after cap", len(got))
	}
	if got[0].Ref != "high" || got[1].Ref != "mid" {
		t.Fatalf("order = [%s,%s], want [high,mid]", got[0].Ref, got[1].Ref)
	}
}

// TestRankAndCapScoreFloor verifies items below minBundleScore are dropped.
// Failure prevented: weak chatter (a single-token doc match, a barely-relevant
// session) leaking into the bundle as a low-quality citation — the exact
// "noise" failure mode this fix targets.
func TestRankAndCapScoreFloor(t *testing.T) {
	items := []ContextItem{
		{Kind: "session", Ref: "barely", Score: 0.48},  // below floor
		{Kind: "discussion", Ref: "weak", Score: 0.43}, // below floor (single-token doc)
		{Kind: "murmur", Ref: "strong", Score: 0.60},   // clears floor
	}
	got := rankAndCap(items, 0)
	if len(got) != 1 || got[0].Ref != "strong" {
		t.Fatalf("got %v, want only [strong] after floor", refs(got))
	}
}

// TestRankAndCapDedup verifies duplicate (kind,ref) items collapse to the
// highest-scoring one.
// Failure prevented: the same session/doc surfacing twice in the bundle,
// wasting the agent's tokens and double-counting a single source.
func TestRankAndCapDedup(t *testing.T) {
	items := []ContextItem{
		{Kind: "session", Ref: "dup", Score: 0.6},
		{Kind: "session", Ref: "dup", Score: 0.9}, // higher — should win
		{Kind: "session", Ref: "other", Score: 0.7},
	}
	got := rankAndCap(items, 0)
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2 after dedup (refs: %v)", len(got), refs(got))
	}
	if got[0].Ref != "dup" || got[0].Score != 0.9 {
		t.Fatalf("got[0] = {%s, %.2f}, want {dup, 0.90} (kept highest)", got[0].Ref, got[0].Score)
	}
}

// TestRankAndCapDeterministic verifies equal scores break ties deterministically
// (kind then ref) so JSON output is stable across runs.
// Failure prevented: non-deterministic bundle ordering that breaks output diffs
// and flakes tests downstream.
func TestRankAndCapDeterministic(t *testing.T) {
	items := []ContextItem{
		{Kind: "session", Ref: "b", Score: 0.6},
		{Kind: "adr", Ref: "a", Score: 0.6},
		{Kind: "adr", Ref: "z", Score: 0.6},
	}
	got := rankAndCap(items, 0) // 0 = no cap
	wantRefs := []string{"a", "z", "b"}
	for i, w := range wantRefs {
		if got[i].Ref != w {
			t.Fatalf("got[%d].Ref = %q, want %q (order: %v)", i, got[i].Ref, w, refs(got))
		}
	}
}

func refs(items []ContextItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Ref
	}
	return out
}

// --- E. Snippet truncation ---

// TestSnippet verifies whitespace collapse and rune-safe truncation.
// Failure prevented: a giant murmur body bloating one item, or truncation
// splitting a multi-byte rune.
func TestSnippet(t *testing.T) {
	if got := snippet("  hello   world\n\tagain  ", 200); got != "hello world again" {
		t.Errorf("whitespace collapse = %q", got)
	}
	long := snippet("aaaaaaaaaa", 5)
	if []rune(long)[len([]rune(long))-1] != '…' {
		t.Errorf("expected ellipsis on truncation, got %q", long)
	}
	// multi-byte runes must not be split mid-byte.
	multi := snippet("日本語のテキストが長い場合", 5)
	if len([]rune(multi)) > 5 {
		t.Errorf("truncated length = %d runes, want <= 5", len([]rune(multi)))
	}
}

// --- F. Fail-open behavior ---

// TestRetrieveFailOpen verifies the retriever returns (nil, nil) — never an
// error — when there is no project/ledger to read from.
// Failure prevented: a missing ledger aborting plan enrichment instead of
// degrading gracefully.
func TestRetrieveFailOpen(t *testing.T) {
	r := &contextBundleRetriever{}

	// empty git root: nothing resolvable.
	items, err := r.Retrieve(context.Background(), Input{}, "")
	if err != nil {
		t.Fatalf("err = %v, want nil (fail-open)", err)
	}
	if items != nil {
		t.Fatalf("items = %v, want nil for empty input", items)
	}

	// non-existent git root with a populated-looking plan: still fail-open.
	in := Input{Sections: []Section{
		{Heading: "OAuth token refresh", Files: []string{"internal/auth/token.go"}},
	}}
	items, err = r.Retrieve(context.Background(), in, "/nonexistent/git/root")
	if err != nil {
		t.Fatalf("err = %v, want nil (fail-open)", err)
	}
	if items != nil {
		t.Fatalf("items = %v, want nil when sources are unreadable", items)
	}
}

// TestRetrieverRegistered verifies the retriever self-registers via init() so the
// orchestrator picks it up.
// Failure prevented: the bundle silently never being produced because the
// retriever wasn't registered.
func TestRetrieverRegistered(t *testing.T) {
	_, rs := snapshotRegistry()
	for _, r := range rs {
		if r.Name() == "context-bundle" {
			return
		}
	}
	t.Fatal("context-bundle retriever not registered")
}
