package secretguard

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestCompleteUpstreamCatalog(t *testing.T) {
	var c config
	if err := toml.Unmarshal(bundledCatalog, &c); err != nil {
		t.Fatal(err)
	}
	if bundled.RuleCount() != len(c.Rules) || len(c.Rules) == 0 {
		t.Fatal("upstream rule missing")
	}
	seen := map[string]bool{}
	for _, r := range bundled.rules {
		seen[r.id] = true
	}
	for _, id := range []string{"generic-api-key", "pkcs12-file", "aws-access-token", "gcp-api-key", "openai-api-key", "airtable-personnal-access-token"} {
		if !seen[id] {
			t.Fatalf("rule %s missing", id)
		}
	}
}
func TestRulesFailClosed(t *testing.T) {
	for _, cfg := range []string{"", "[[rules]]\nid='bad'", "[[rules]]\nid='bad'\nregex='['", "[[rules]]\nid='bad'\nregex='ok'\nfutureMode=true", "[[rules]]\nid='bad'\nregex='ok'\nsecretGroup=2", "[[rules]]\nid='same'\nregex='a'\n[[rules]]\nid='same'\nregex='b'"} {
		if _, err := NewRules([]byte(cfg)); err == nil {
			t.Fatal("invalid catalog accepted")
		}
	}
	var missing *RuleSet
	if missing.ValidateContent("raw.jsonl", []byte("clean")) == nil {
		t.Fatal("missing guard accepted")
	}
}
func TestContextualSecretsAndJSONEncoding(t *testing.T) {
	secret := "syntheticOpaqueSentinel-73xyAZ/+=-not-a-provider-prefix"
	plain := fmt.Sprintf("helm template chart --set extraEnv[2].value='%s' --set extraEnv[2].name=MAILLAB_API_KEY --set extraEnv[3].name=REGION --set extraEnv[3].value=us-example-1", secret)
	values := []string{plain, "MAILLAB_API_KEY=" + secret, fmt.Sprintf(`{"name":"MAILLAB_API_KEY","value":%q,"region":"us-example-1"}`, secret)}
	for i := 0; i < 4; i++ {
		encoded, _ := json.Marshal(map[string]any{"output": values[len(values)-1]})
		values = append(values, string(encoded))
	}
	unicode := strings.ReplaceAll(values[2], "synthetic", `\u0073ynthetic`)
	values = append(values, unicode)
	for i, input := range values {
		out, found := RedactText(input)
		if len(found) == 0 || strings.Contains(out, secret) || strings.Contains(out, `\u0073ynthetic`) {
			t.Errorf("case %d: secret not removed", i)
		}
		if ValidateContent("raw.jsonl", []byte(input)) == nil {
			t.Errorf("case %d: publication accepted", i)
		}
		if err := ValidateContent("raw.jsonl", []byte(out)); err != nil {
			t.Errorf("case %d: redacted output rejected: %v", i, err)
		}
	}
	out, _ := RedactText(plain)
	if !strings.Contains(out, "us-example-1") {
		t.Fatal("benign Helm field lost")
	}
}
func TestUpstreamKeywordsCannotSuppressBareToken(t *testing.T) {
	token := "patABCDEFGHIJKLMN." + strings.Repeat("0123456789abcdef", 4)
	out, names := RedactText("standalone " + token)
	if strings.Contains(out, token) || len(names) == 0 {
		t.Fatal("keyword hint bypassed detector")
	}
}
func TestRefreshedCatalogIsAdditiveAndImmutable(t *testing.T) {
	rule := []byte("[[rules]]\nid='new-provider'\nregex='newkey_[A-Z0-9]{12}'\n")
	s, err := NewRules(rule)
	if err != nil {
		t.Fatal(err)
	}
	for i := range rule {
		rule[i] = 'x'
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, names := s.RedactText("newkey_ABCDEFGHIJ12")
			if strings.Contains(out, "newkey_") || len(names) != 1 {
				t.Error("new rule not enforced")
			}
		}()
	}
	wg.Wait()
	if s.CatalogHash() == CatalogHash() {
		t.Fatal("catalog identities equal")
	}
}
func TestPathOnlyRuleAndErrorPrivacy(t *testing.T) {
	if err := ValidateContent("archive.p12", []byte("opaque binary")); err == nil {
		t.Fatal("path-only upstream rule bypassed")
	}
	secret := "opaqueSentinel72w89qq"
	err := ValidateContent("sensitive-name-"+secret, []byte("MAILLAB_API_KEY="+secret))
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("error exposed content or accepted secret")
	}
}
func TestBenignJSONFramingAndNumbersPreserved(t *testing.T) {
	input := `{"seq":9007199254740993,"type":"tool","call_id":"call-1","name":"REGION","value":"us-example-1"}`
	out, found := RedactText(input)
	if out != input || len(found) != 0 {
		t.Fatal("benign wire payload changed")
	}
}

func TestJSONLDoesNotSkipEncodedSecretsOrKeys(t *testing.T) {
	token := "oxp_" + strings.Repeat("Ab3z", 8)
	encoded := strings.ReplaceAll(token, "oxp_", `\u006fxp_`)
	data := fmt.Sprintf("{\"type\":\"user\",\"content\":\"safe\"}\n{\"content\":\"%s\"}\n", encoded)
	if ValidateContent("raw.jsonl", []byte(data)) == nil {
		t.Fatal("JSONL Unicode escape bypass")
	}
	keyed := fmt.Sprintf(`{%q:"benign"}`, token)
	if ValidateContent("raw.jsonl", []byte(keyed)) == nil {
		t.Fatal("JSON key bypass")
	}
	out, _ := RedactText(data)
	if ValidateContent("raw.jsonl", []byte(out)) != nil || strings.Contains(out, "Ab3z") {
		t.Fatal("JSONL redaction failed")
	}
}
func TestEntropyPreservesBenignAssignmentsButNotSecretEnv(t *testing.T) {
	input := "key2=allowed_value"
	out, found := RedactText(input)
	if out != input || len(found) != 0 {
		t.Fatal("upstream entropy boundary lost")
	}
	out, found = RedactText("MAILLAB_API_KEY=" + strings.Repeat("a", 30))
	if len(found) == 0 || strings.Contains(out, strings.Repeat("a", 30)) {
		t.Fatal("entropy bypassed explicit sensitive context")
	}
}
func TestKeywordProofNeverTrustsOptionalOrAlternativeHint(t *testing.T) {
	cases := []string{`(?:vendor)?bare_[A-Z]{8}`, `vendor_[A-Z]{8}|bare_[A-Z]{8}`}
	for _, expression := range cases {
		s, err := NewRules([]byte("[[rules]]\nid='keyword-proof'\nregex='" + expression + "'\nkeywords=['vendor']\n"))
		if err != nil {
			t.Fatal(err)
		}
		out, found := s.RedactText("bare_ABCDEFGH")
		if out == "bare_ABCDEFGH" || len(found) == 0 {
			t.Fatal("unsafe keyword proof")
		}
	}
}

func BenchmarkRedactText(b *testing.B) {
	for _, size := range []int{8 << 10, 100 << 10} {
		for _, withSecret := range []bool{false, true} {
			input := strings.Repeat("ordinary tool output describing a completed operation.\n", size/54)
			if withSecret {
				input += "\nMAILLAB_API_KEY=syntheticOpaqueBenchmarkSentinel91xyAZ"
			}
			b.Run(fmt.Sprintf("bytes=%d/secret=%t", size, withSecret), func(b *testing.B) {
				b.SetBytes(int64(len(input)))
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					RedactText(input)
				}
			})
		}
	}
}

func TestKeywordProofPreservesRE2UnicodeCaseFolding(t *testing.T) {
	rules, err := NewRules([]byte("[[rules]]\nid='folded'\nregex='(?i)secret_[a-z]{8}'\n"))
	if err != nil {
		t.Fatal(err)
	}
	out, found := rules.RedactText("ſecret_abcdefgh")
	if len(found) == 0 || strings.Contains(out, "abcdefgh") {
		t.Fatal("Unicode case folding bypassed required-literal screen")
	}
}

func TestMarkerSubstringCannotHideOpaqueCredential(t *testing.T) {
	secret := "[REDACTED_SECRET] plus newly-issued-opaque-sentinel"
	for _, input := range []string{"AWS_SECRET_ACCESS_KEY='" + secret + "'", fmt.Sprintf(`{"name":"AWS_SECRET_ACCESS_KEY","value":%q}`, secret)} {
		out, found := RedactText(input)
		if len(found) == 0 || strings.Contains(out, "newly-issued") {
			t.Fatal("marker substring bypassed contextual protection")
		}
	}
}
func TestMixedJSONLDoesNotSkipPlaintextSecrets(t *testing.T) {
	first := "opaque-first-sentinel93xY"
	second := "opaque-second-sentinel94xZ"
	input := fmt.Sprintf("{\"name\":\"API_KEY\",\"value\":%q}\nAWS_SECRET_ACCESS_KEY=%s\n", first, second)
	out, found := RedactText(input)
	if len(found) == 0 || strings.Contains(out, first) || strings.Contains(out, second) {
		t.Fatal("mixed framing skipped a credential")
	}
}

func TestGuardedRenderingCannotSuppressUncoveredOrEncodedSecret(t *testing.T) {
	const secret = "opaque-sentinel-guarded-rendering"
	input := "API_KEY=" + secret
	for _, edits := range [][]TextReplacement{
		{{Start: 0, End: 7, Marker: "[REDACTED_NAME]"}},
		{{Start: 8, End: 14, Marker: "[REDACTED_PREFIX]"}},
		{{Start: 8, End: len(input), Marker: "unsafe replacement"}},
	} {
		out, _, used := RedactTextWithReplacements(input, edits)
		if used || strings.Contains(out, secret) || strings.Contains(out, "unsafe replacement") {
			t.Fatal("unsafe rendering bypassed mandatory guard")
		}
	}
	out, _, used := RedactTextWithReplacements(input, []TextReplacement{{Start: 8, End: len(input), Marker: "[REDACTED_CUSTOM]"}})
	if !used || out != "API_KEY=[REDACTED_CUSTOM]" {
		t.Fatal("complete fixed-marker removal did not preserve rendering")
	}
	encoded := `{"API_KEY":"\u006fpaque-sentinel-guarded-rendering"}`
	out, _, used = RedactTextWithReplacements(encoded, []TextReplacement{{Start: 0, End: 5, Marker: "[REDACTED_PREFIX]"}})
	if used || strings.Contains(out, "sentinel") {
		t.Fatal("encoded value received an assumed coverage proof")
	}
}

// --- Built-in expression screens (sageox/ox#1457) ---

// TestBuiltinScreensStayProvable pins the literal each built-in expression
// proves. Failure prevented: a regex edit that loses its literal silently
// unscreens the built-in, and every large string pays its full NFA pass again.
func TestBuiltinScreensStayProvable(t *testing.T) {
	want := [][]string{
		{"extraenv["},
		{"key", "token", "secret", "passw", "credentials"},
		{"ox"},
		{"mk_"},
	}
	if len(builtins) != len(want) {
		t.Fatalf("%d built-ins, want %d", len(builtins), len(want))
	}
	for i, b := range builtins {
		if !slices.Equal(b.keywords, want[i]) {
			t.Errorf("built-in %d screen = %q, want %q", i, b.keywords, want[i])
		}
	}
}

// TestBuiltinScreensKeepRE2CaseFolding covers spellings the (?i) expressions
// match but a byte-wise ASCII screen would miss. Failure prevented: a screen
// that skips text its own expression would redact.
func TestBuiltinScreensKeepRE2CaseFolding(t *testing.T) {
	for name, input := range map[string]string{
		"kelvin sign":     "API_\u212AEY=opaqueSentinel93xY",
		"long s":          "MAILLAB_\u017FECRET=opaqueSentinel93xY",
		"upper-case helm": "--set EXTRAENV[0].name=API_TOKEN --set EXTRAENV[0].value=opaqueSentinel93xY",
	} {
		out, found := RedactText(input)
		if strings.Contains(out, "opaqueSentinel93xY") || !slices.Contains(found, "sensitive-env-value") {
			t.Errorf("%s: value not redacted: %v", name, found)
		}
	}
}

// TestBuiltinsFireInsideLargeUnrelatedText puts each built-in's credential at
// the start, middle and end of text that proves none of their literals.
// Failure prevented: a screen that drops a real credential in a large session.
func TestBuiltinsFireInsideLargeUnrelatedText(t *testing.T) {
	filler := strings.Repeat("review this request ", 1000)
	for _, tc := range []struct{ credential, secret, finding string }{
		{"--set extraEnv[0].name=API_TOKEN --set extraEnv[0].value=opaqueSentinel95xQ", "opaqueSentinel95xQ", "sensitive-env-value"},
		{"MAILLAB_SECRET=opaqueSentinel96xR", "opaqueSentinel96xR", "sensitive-env-value"},
		{"oxp_Ab3zAb3zAb3zAb3z", "Ab3zAb3zAb3zAb3z", "sageox-bearer-token"},
		{"mk_" + strings.Repeat("Ab3z", 6), strings.Repeat("Ab3z", 6), "sageox-api-key"},
	} {
		for where, input := range map[string]string{"start": tc.credential + " " + filler, "middle": filler + tc.credential + " " + filler, "end": filler + tc.credential} {
			out, found := RedactText(input)
			if strings.Contains(out, tc.secret) || !slices.Contains(found, tc.finding) {
				t.Errorf("%s at %s: not redacted: %v", tc.finding, where, found)
			}
		}
	}
}

// TestBuiltinMarkerIsVisibleToLaterScreens arms a rule only with text a
// built-in's replacement inserted. Failure prevented: a screen computed before
// the replacement skips a rule that scans the replaced text.
func TestBuiltinMarkerIsVisibleToLaterScreens(t *testing.T) {
	s, err := NewRules([]byte("[[rules]]\nid='marker-adjacent'\nregex='(?i)secret\\]:[a-z]{8}'\n"))
	if err != nil {
		t.Fatal(err)
	}
	out, found := s.RedactText("MAILLAB_API_KEY='opaque':abcdefgh")
	if strings.Contains(out, "abcdefgh") || !slices.Contains(found, "marker-adjacent") {
		t.Fatalf("rule armed by marker text skipped: %q %v", out, found)
	}
}

// BenchmarkRedactTextLargePrompt is the 7 MB prompt from the large-session
// import test, which proves no built-in or catalog literal.
func BenchmarkRedactTextLargePrompt(b *testing.B) {
	input := strings.Repeat("review this request ", 350000)
	b.SetBytes(int64(len(input)))
	for i := 0; i < b.N; i++ {
		RedactText(input)
	}
}
