package secretguard

import (
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// --- Rescans of already-scanned text (sageox/ox#1457, part 2) ---

// TestInputMarkerDoesNotArmARule verifies a marker an earlier scan left in the
// text does not count as a rule's keyword, while any other literal still runs
// the rule over the marker.
// Failure prevented: "[REDACTED_SECRET]" re-arms the secret/key detectors over
// every byte of a redacted multi-MB session on each of its rescans.
func TestInputMarkerDoesNotArmARule(t *testing.T) {
	s, err := NewRules([]byte("[[rules]]\nid='marker-adjacent'\nregex='(?i)secret[^a-z]{0,12}:([a-z]{8})'\n"))
	if err != nil {
		t.Fatal(err)
	}
	redacted := "earlier output [REDACTED_SECRET]:abcdefgh"
	if out, found := s.RedactText(redacted); out != redacted || len(found) > 0 {
		t.Fatalf("a marker from an earlier scan armed the rule: %v", found)
	}
	// Negative control: the same bytes with "secret" outside the marker scan.
	armed := "the secret was " + redacted
	if out, found := s.RedactText(armed); strings.Contains(out, "abcdefgh") || !slices.Contains(found, "marker-adjacent") {
		t.Fatalf("a literal outside the marker did not arm the rule: %v", found)
	}
}

// TestCleanScanIsRememberedPerCatalog verifies the clean-scan memo skips only
// the same bytes under the same catalog, and never a text with a finding.
// Failure prevented: a memo keyed on text alone lets one catalog's clean
// result skip another catalog's finding, or a cached refusal stops rescanning.
func TestCleanScanIsRememberedPerCatalog(t *testing.T) {
	alpha, err := NewRules([]byte("[[rules]]\nid='alpha'\nregex='alpha_[a-z]{8}'\n"))
	if err != nil {
		t.Fatal(err)
	}
	beta, err := NewRules([]byte("[[rules]]\nid='beta'\nregex='beta_[a-z]{8}'\n"))
	if err != nil {
		t.Fatal(err)
	}
	text := "tool output mentions beta_qzxwvjkp once"
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if out, found := alpha.RedactText(text); out != text || len(found) > 0 {
				t.Error("clean text changed")
			}
		})
	}
	wg.Wait()
	if !cleanScans.has(alpha.cleanKey(text)) {
		t.Fatal("clean scan not remembered")
	}
	if cleanScans.has(beta.cleanKey(text)) {
		t.Fatal("clean scan remembered for another catalog")
	}
	for range 2 {
		out, found := beta.RedactText(text)
		if strings.Contains(out, "qzxwvjkp") || !slices.Contains(found, "beta") {
			t.Fatal("another catalog's finding was skipped")
		}
		if cleanScans.has(beta.cleanKey(text)) {
			t.Fatal("a text with a finding was remembered")
		}
	}
	if err := beta.ValidateContent("raw.jsonl", []byte(text)); err == nil {
		t.Fatal("publication accepted a text the catalog refuses")
	}
	if _, names, _ := RedactTextWithReplacements("API_KEY=opaque-sentinel-replacements", nil); len(names) == 0 {
		t.Fatal("guarded rendering skipped a finding")
	}
}

// TestCleanScanMemoIsBoundedAndHoldsNoContent verifies the memo starts over at
// its cap and stores fixed-size digests only.
// Failure prevented: a long-running daemon grows the memo without bound, or a
// refactor starts keeping scanned session text in process memory.
func TestCleanScanMemoIsBoundedAndHoldsNoContent(t *testing.T) {
	if typ := reflect.TypeOf(cleanScans.seen); typ.Key() != reflect.TypeOf([32]byte{}) || typ.Elem().Size() != 0 {
		t.Fatalf("memo stores more than digests: %v", typ)
	}
	m := &scanMemo{seen: map[[32]byte]struct{}{}}
	for i := range cleanScanLimit {
		m.add(bundled.cleanKey(strings.Repeat("x", i)))
	}
	first := bundled.cleanKey("")
	if len(m.seen) != cleanScanLimit || !m.has(first) {
		t.Fatalf("memo holds %d of %d entries", len(m.seen), cleanScanLimit)
	}
	next := bundled.cleanKey("one more")
	m.add(next)
	if len(m.seen) != 1 || !m.has(next) || m.has(first) {
		t.Fatalf("memo did not start over at its cap: %d entries", len(m.seen))
	}
}

// deltaCatalogs is a base and a later catalog that share the full bundled
// catalog. The later one changes rule x, and adds content rule y and path rule z.
func deltaCatalogs(t *testing.T) (base, latest *RuleSet) {
	t.Helper()
	catalog := string(bundledCatalog)
	base, err := NewRules([]byte(catalog + "\n[[rules]]\nid='x'\nregex='alpha_[a-z]{8}'\n"))
	if err != nil {
		t.Fatal(err)
	}
	latest, err = NewRules([]byte(catalog + "\n[[rules]]\nid='x'\nregex='beta_[a-z]{8}'\n" +
		"[[rules]]\nid='y'\nregex='gamma_[a-z]{8}'\n[[rules]]\nid='z'\npath='\\.futurekey$'\n"))
	if err != nil {
		t.Fatal(err)
	}
	return base, latest
}

// TestDeltaKeepsOnlyRulesTheBaseLacks verifies Delta drops rules identical to
// the base's and keeps changed and new ones, and that an empty delta passes.
// Failure prevented: an upstream fix to an existing rule, which keeps its id,
// is dropped from the second check and its credentials publish.
func TestDeltaKeepsOnlyRulesTheBaseLacks(t *testing.T) {
	base, latest := deltaCatalogs(t)
	delta := latest.Delta(base)
	var ids []string
	for _, r := range delta.rules {
		ids = append(ids, r.id)
	}
	if !slices.Equal(ids, []string{"x", "y", "z"}) {
		t.Fatalf("delta rules = %v, want the changed and new rules only", ids)
	}
	if delta.CatalogHash() == latest.CatalogHash() || delta.CatalogHash() == base.CatalogHash() {
		t.Fatal("delta shares a catalog identity, and so its clean-scan memo")
	}
	same, err := NewRules(bundledCatalog)
	if err != nil {
		t.Fatal(err)
	}
	empty := same.Delta(Bundled())
	if empty.RuleCount() != 0 || empty.ValidateContent("raw.jsonl", []byte("ordinary notes")) != nil {
		t.Fatal("a catalog equal to the bundled one left rules to check")
	}
	var missing *RuleSet
	if missing.Delta(Bundled()).ValidateContent("raw.jsonl", []byte("ordinary notes")) == nil {
		t.Fatal("a missing catalog passed as an empty delta")
	}
}

// TestDeltaValidatesLikeTheFullCatalog verifies that, on content the base
// accepted, the delta refuses exactly what the full later catalog refuses.
// Failure prevented: the cheaper second check accepts something the complete
// current catalog would have refused.
func TestDeltaValidatesLikeTheFullCatalog(t *testing.T) {
	base, latest := deltaCatalogs(t)
	delta := latest.Delta(base)
	prose := strings.Repeat("ordinary tool output describing a completed operation.\n", 2000)
	cases := []struct{ name, content string }{
		{"raw.jsonl", "ordinary notes"},
		{"raw.jsonl", "alpha was renamed beta in the docs"},
		{"raw.jsonl", "changed rule: beta_qzxwvjkp"},
		{"raw.jsonl", `{"content":"new rule: gamma_qzxwvjkp"}`},
		{"raw.jsonl", "{\"content\":\"[REDACTED_SECRET]\"}\n{\"content\":\"gamma_qzxwvjkp\"}\n"},
		{"raw.jsonl", "API_KEY=[REDACTED_SECRET] then [REDACTED_SECRET]:beta_qzxwvjkp"},
		{"raw.jsonl", prose + "beta_qzxwvjkp"},
		{"raw.jsonl", prose},
		{"keys/x.futurekey", "ordinary notes"},
		{"keys/x.txt", "commit 0123456789abcdef0123456789abcdef01234567"},
	}
	refused := 0
	for _, c := range cases {
		if err := base.ValidateContent(c.name, []byte(c.content)); err != nil {
			t.Fatalf("%s %.40q: the base must accept every case: %v", c.name, c.content, err)
		}
		full := latest.ValidateContent(c.name, []byte(c.content))
		if got := delta.ValidateContent(c.name, []byte(c.content)); (got == nil) != (full == nil) {
			t.Errorf("%s %.40q: delta refused=%t, full catalog refused=%t", c.name, c.content, got != nil, full != nil)
		}
		if full != nil {
			refused++
		}
	}
	if refused == 0 || refused == len(cases) {
		t.Fatalf("%d of %d cases refused; the comparison needs both outcomes", refused, len(cases))
	}
}
