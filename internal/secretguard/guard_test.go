package secretguard

import (
	"encoding/json"
	"fmt"
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
