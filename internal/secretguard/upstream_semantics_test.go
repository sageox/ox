package secretguard

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp/syntax"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// Fixtures are derived at runtime so no credential-shaped literal lives in
// source, where the repo's own secret scan and session recordings would see it.
func syntheticHex(seed string, n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", seed, i)))
		b.WriteString(hex.EncodeToString(sum[:]))
	}
	return b.String()[:n]
}

// syntheticToken returns n characters from [A-Za-z0-9_-].
func syntheticToken(seed string, n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", seed, i)))
		b.WriteString(base64.RawURLEncoding.EncodeToString(sum[:]))
	}
	return b.String()[:n]
}

// syntheticAlnum returns n characters from [A-Za-z0-9].
func syntheticAlnum(seed string, n int) string {
	return strings.NewReplacer("-", "x", "_", "Q").Replace(syntheticToken(seed, n))
}

func commitSHA(seed string) string { return syntheticHex("commit/"+seed, 40) }

func assertPublishable(t *testing.T, label, input string) {
	t.Helper()
	if out, found := RedactText(input); out != input || len(found) > 0 {
		t.Errorf("%s: benign content treated as credentials: %v", label, found)
	}
	if err := ValidateContent("sessions/2026-10-21T23-49-devon-OxAbCd/meta.json", []byte(input)); err != nil {
		t.Errorf("%s: publication refused: %v", label, err)
	}
}

// --- A. Upstream keyword prefilter ---

// TestCommitSHAsInSessionArtifactsAreNotCredentials verifies 40-hex git commit
// SHAs pass the guard in every session artifact that carries them.
// Failure prevented: a session meta listing produced_commits is refused as a
// Sourcegraph token, and because the daemon commits the whole Ledger workdir,
// that one file blocks every later automatic Ledger commit on the machine.
func TestCommitSHAsInSessionArtifactsAreNotCredentials(t *testing.T) {
	shas := []string{commitSHA("a"), commitSHA("b"), commitSHA("c")}
	meta, _ := json.MarshalIndent(map[string]any{"agent_id": "OxAbCd", "title": "Fix ledger sync", "produced_commits": shas}, "", "  ")
	entry, _ := json.Marshal(map[string]any{"type": "tool_result", "content": strings.Join(shas, "\n")})
	cases := map[string]string{
		"session meta":           string(meta),
		"raw.jsonl tool output":  `{"type":"user","content":"git log --format=%H -3"}` + "\n" + string(entry) + "\n",
		"git log default format": "commit " + shas[0] + "\nAuthor: Devon <devon@example.com>\n\n    fix(ledger): recover a moved branch\n",
		"commit link":            "Merged in https://github.com/example/repo/commit/" + shas[1] + " today.",
		"rev-parse output":       shas[2],
	}
	for name, input := range cases {
		assertPublishable(t, name, input)
	}
}

// TestKeywordElsewhereInARecordDoesNotArmBareTokenRules verifies a rule that
// relies on upstream keywords needs one on the matched line, and inside a JSON
// record on the same decoded string — the unit capture redacts entry by entry.
// Failure prevented: a session that discusses Sourcegraph anywhere has every
// commit SHA in it refused at publication although capture accepted each
// entry, so the Ledger wedges on the very sessions that discuss this rule.
func TestKeywordElsewhereInARecordDoesNotArmBareTokenRules(t *testing.T) {
	sha := commitSHA("elsewhere")
	discuss, _ := json.Marshal(map[string]any{"type": "user", "content": "why does sourcegraph-access-token fire without sgp_?"})
	output, _ := json.Marshal(map[string]any{"type": "tool_result", "content": "commit " + sha})
	both, _ := json.Marshal(map[string]any{"input": "check the sourcegraph rule", "output": "commit " + sha})
	cases := map[string]string{
		"markdown transcript":              "# Fix the sourcegraph false positive\n\ncommit " + sha + "\n",
		"separate JSONL entries":           string(discuss) + "\n" + string(output) + "\n",
		"one JSONL entry, separate fields": `{"type":"user","content":"hi"}` + "\n" + string(both) + "\n",
		"one JSON record":                  string(both),
		"git log body":                     "commit " + sha + "\n\n    fix(secretguard): gate the sourcegraph rule on its keywords\n",
	}
	for name, input := range cases {
		assertPublishable(t, name, input)
	}
}

// TestSourcegraphTokensStillCaughtBesideCommitSHAs verifies the keyword
// prefilter removes only the bare-hex false positive: real-shaped Sourcegraph
// tokens are still redacted, and commit SHAs next to them survive.
// Failure prevented: a leaked Sourcegraph token reaches the Ledger, or
// redacting one also destroys the session's commit history.
func TestSourcegraphTokensStillCaughtBesideCommitSHAs(t *testing.T) {
	sha := commitSHA("beside-token")
	tokens := map[string]string{
		"sgp_ token":          "sgp_" + syntheticHex("sg/plain", 40),
		"sgp_ instance token": "sgp_" + syntheticHex("sg/instance", 16) + "_" + syntheticHex("sg/secret", 40),
		"sgp_local token":     "sgp_local_" + syntheticHex("sg/local", 40),
		"legacy hex token":    syntheticHex("sg/legacy", 40),
	}
	for name, token := range tokens {
		line := "src login --token " + token + " against our sourcegraph instance"
		meta, _ := json.Marshal(map[string]any{"produced_commits": []string{sha}, "note": line})
		escaped := strings.Replace(string(meta), "sourcegraph", `sourcegraph`, 1)
		for i, input := range []string{line + "\ncommit " + sha, string(meta), escaped} {
			out, found := RedactText(input)
			if !slices.Contains(found, "sourcegraph-access-token") || strings.Contains(out, token) {
				t.Errorf("%s case %d: token not redacted (%v)", name, i, found)
			}
			if !strings.Contains(out, sha) {
				t.Errorf("%s case %d: commit SHA beside the token was redacted", name, i)
			}
			if ValidateContent("sessions/x/meta.json", []byte(input)) == nil {
				t.Errorf("%s case %d: publication accepted a token", name, i)
			}
		}
	}
}

// TestBundledCatalogAgreesWithUpstreamKeywordPrefilter runs the complete
// bundled catalog over a fixture and checks every catalog finding against
// upstream Gitleaks keyword semantics: a rule with keywords runs only on a
// fragment containing one of them. The guard is stricter only where the
// rule's own expression proves a literal (TestUpstreamKeywordsCannotSuppressBareToken).
// Failure prevented: a rule with a bare alternative (any 40-hex, any
// digits|alnum pair) fires on text upstream never scans and wedges
// publication — including one a later catalog refresh adds unnoticed.
func TestBundledCatalogAgreesWithUpstreamKeywordPrefilter(t *testing.T) {
	var c config
	if err := toml.Unmarshal(bundledCatalog, &c); err != nil {
		t.Fatal(err)
	}
	keywords := map[string][]string{}
	anchored := map[string]bool{}
	var keywordOnly []string
	for _, r := range c.Rules {
		if r.Regex == "" {
			continue
		}
		parsed, err := syntax.Parse(r.Regex, syntax.Perl)
		if err != nil {
			t.Fatal(err)
		}
		keywords[r.ID] = r.Keywords
		anchored[r.ID] = len(requiredKeywords(parsed)) > 0
		if !anchored[r.ID] && len(r.Keywords) > 0 {
			keywordOnly = append(keywordOnly, r.ID)
		}
	}
	// One fragment per line, as upstream sees a one-line file.
	fixture := []struct{ line, want string }{
		{"produced_commits: " + commitSHA("catalog"), ""},
		{"run 202610251813390|ci-linux-amd64-go-test-shard-3 passed", ""},
		{"src login sgp_" + syntheticHex("catalog/sg", 40), "sourcegraph-access-token"},
		{"sourcegraph token " + syntheticHex("catalog/legacy", 40), "sourcegraph-access-token"},
		{"facebook app token 202610251813390|" + syntheticHex("catalog/fb", 32), "facebook-access-token"},
		{"fly deploy --access-token fo1_" + syntheticToken("catalog/fly", 43), "flyio-access-token"},
	}
	fired := map[string]bool{}
	for i, f := range fixture {
		_, found := RedactText(f.line)
		lower := strings.ToLower(f.line)
		for _, id := range found {
			kws, catalogRule := keywords[id]
			if !catalogRule {
				continue
			}
			upstreamScans := len(kws) == 0
			for _, k := range kws {
				upstreamScans = upstreamScans || strings.Contains(lower, strings.ToLower(k))
			}
			if !upstreamScans && !anchored[id] {
				t.Errorf("fixture %d: %s fired on a fragment upstream never scans", i, id)
			}
			fired[id] = true
		}
		if f.want != "" && !slices.Contains(found, f.want) {
			t.Errorf("fixture %d: %s missed a fragment upstream scans", i, f.want)
		}
	}
	for _, id := range keywordOnly {
		if !fired[id] {
			t.Errorf("%s relies on its upstream keywords alone; add a fixture line it must fire on", id)
		}
	}
}

// --- B. Rule-level allowlists ---

// TestGenericAPIKeyHonorsItsUpstreamAllowlists verifies the guard applies the
// allowlists shipped inside a catalog rule — upstream's letters-only, stopword
// and import-line filters for generic-api-key — while key-shaped values are
// still caught and repository path allowlists still excuse nothing.
// Failure prevented: an ordinary stage list or import line is refused as an
// API key, wedging every later automatic Ledger commit.
func TestGenericAPIKeyHonorsItsUpstreamAllowlists(t *testing.T) {
	for name, input := range map[string]string{
		"stage list":  "make ready: cheap-mcp, go-lint-api, go-vet-shadow-check",
		"import list": `import { apiKey, deviceAuthorization } from "better-auth/plugins";`,
	} {
		assertPublishable(t, name, input)
	}
	key := syntheticToken("generic/key", 32)
	// A JSON record line defers only keyword-gated rules to its decoded
	// strings; literal-anchored generic-api-key still reads key/value structure.
	camel := `{"type":"user","content":"hi"}` + "\n" + `{"apiKey":"` + key + `"}` + "\n"
	for i, input := range []string{`api_key = "` + key + `"`, "auth: " + key, `{"config":"client_secret=` + key + `"}`, camel} {
		if out, found := RedactText(input); len(found) == 0 || strings.Contains(out, key) {
			t.Errorf("key-shaped case %d not redacted", i)
		}
	}
	// github-pat ships a path-only allowlist; captured content has no
	// repository location that could excuse a token.
	pat := "ghp_" + syntheticAlnum("generic/pat", 36)
	if ValidateContent("node_modules/@octokit/auth-token/README.md", []byte("token "+pat)) == nil {
		t.Fatal("repository path allowlist excused a token")
	}
}

// TestRuleAllowlistsApplyContentConditionsOnly pins the allowlist semantics
// on a custom catalog: content conditions (secret, match and line regexes,
// stopwords) suppress like upstream; a path condition never holds, so an AND
// allowlist that needs one never suppresses; unknown semantics fail closed.
// Failure prevented: a refreshed catalog's allowlist either silently stops
// filtering (false positives wedge publication) or a repository path excuses
// a session credential.
func TestRuleAllowlistsApplyContentConditionsOnly(t *testing.T) {
	const head = "[[rules]]\nid='vendor'\nregex='''vendor_([a-z0-9]{12})'''\n[[rules.allowlists]]\n"
	cases := []struct {
		name, allowlist, input string
		suppressed             bool
	}{
		{"secret regex", "regexes=['''^0+$''']", "vendor_000000000000", true},
		{"secret regex miss", "regexes=['''^0+$''']", "vendor_0000000000x1", false},
		{"match regex", "regexTarget='match'\nregexes=['''^vendor_9''']", "vendor_9abcdefghijk", true},
		{"line regex", "regexTarget='line'\nregexes=['''^# example:''']", "# example: vendor_abcdefghijk1", true},
		{"line regex other line", "regexTarget='line'\nregexes=['''^# example:''']", "# example:\nvendor_abcdefghijk1", false},
		{"stopword", "stopwords=['sample']", "vendor_samplexyz1234", true},
		{"stopword any case", "stopwords=['Sample']", "vendor_samplexyz1234", true},
		{"AND both hold", "condition='AND'\nregexes=['''^sample''']\nstopwords=['sample']", "vendor_samplexyz1234", true},
		{"AND one fails", "condition='AND'\nregexes=['''^0+$''']\nstopwords=['sample']", "vendor_samplexyz1234", false},
		{"path only", "paths=['''.*''']", "vendor_abcdefghijk1", false},
		{"AND with path", "condition='AND'\npaths=['''.*''']\nregexes=['''.*''']", "vendor_abcdefghijk1", false},
	}
	for _, tc := range cases {
		s, err := NewRules([]byte(head + tc.allowlist + "\n"))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		_, found := s.RedactText(tc.input)
		if got := len(found) == 0; got != tc.suppressed {
			t.Errorf("%s: suppressed=%t, want %t", tc.name, got, tc.suppressed)
		}
	}
	for _, bad := range []string{"regexTarget='future'\nregexes=['''x''']", "condition='XOR'\nregexes=['''x''']", "regexes=['''[''']", "description='empty'"} {
		if _, err := NewRules([]byte(head + bad + "\n")); err == nil {
			t.Errorf("unsupported allowlist accepted: %q", bad)
		}
	}
}

// --- C. Capture and publication agree on JSONL ---

// TestRedactedSessionEntriesPassPublicationAsJSONL verifies entries the guard
// accepted or redacted one at a time, as capture writes them, also pass when
// publication validates the assembled raw.jsonl.
// Failure prevented: publication re-reads a JSON line in its escaped raw form,
// sees \" or \n glued to a redacted or placeholder value, and refuses the file
// capture produced, wedging every later automatic Ledger commit.
func TestRedactedSessionEntriesPassPublicationAsJSONL(t *testing.T) {
	secret := syntheticToken("roundtrip/secret", 24)
	var captured []string
	for _, content := range []string{
		"API_KEY=" + secret + "\nmore output",
		"MAILLAB_API_KEY='" + secret + "'\nREGION=us-example-1",
		`export API_TOKEN="` + secret + `" && make deploy`,
		"// OPENAI_PLUGIN_VERIFICATION_TOKEN=<verification-token>\n// more doc",
		`set OPENAI_PLUGIN_VERIFICATION_TOKEN="<verification-token>" in the environment`,
	} {
		entry, _ := json.Marshal(map[string]any{"type": "tool_result", "content": content})
		out, _ := RedactText(string(entry))
		if strings.Contains(out, secret) {
			t.Fatal("capture kept a secret")
		}
		captured = append(captured, out)
	}
	file := strings.Join(captured, "\n") + "\n"
	if out, found := RedactText(file); out != file || len(found) > 0 {
		t.Errorf("publication disagrees with per-entry capture: %v", found)
	}
	if err := ValidateContent("sessions/x/raw.jsonl", []byte(file)); err != nil {
		t.Errorf("publication refused captured content: %v", err)
	}
	// A plaintext line between records is still scanned in raw form.
	if ValidateContent("sessions/x/raw.jsonl", []byte(file+"AWS_SECRET_ACCESS_KEY="+secret+"\n")) == nil {
		t.Error("plaintext line between JSON records skipped")
	}
}

// TestDuplicateJSONKeyCannotHideAValue verifies a repeated key at any depth,
// including an escaped spelling of the same key, refuses the document rather
// than letting the decoded walk see only the last value.
// Failure prevented: {"content":"<secret>","content":"x"} publishes the
// secret, because a decoded map keeps only the last duplicate and the guard
// never scans the first.
func TestDuplicateJSONKeyCannotHideAValue(t *testing.T) {
	key := "AKIA" + "QWERTYUIOPASDFGH"
	for i, input := range []string{
		`{"content":"` + key + `","content":"x"}`,
		`{"turn":{"content":"` + key + `","content":"x"}}`,
		`[{"a":1},{"content":"` + key + `","content":"x"}]`,
		`{"name":"API_KEY","value":"opaque-sentinel-shadowed","value":"[REDACTED]"}`,
		`{"type":"user","content":"hi"}` + "\n" + `{"content":"` + key + `","content":"x"}` + "\n",
	} {
		out, found := RedactText(input)
		if len(found) == 0 || strings.Contains(out, key) || strings.Contains(out, "opaque-sentinel") {
			t.Errorf("case %d: duplicate key hid a value (%v)", i, found)
		}
		if ValidateContent("sessions/x/meta.json", []byte(input)) == nil {
			t.Errorf("case %d: publication accepted a shadowed value", i)
		}
	}
	// Distinct keys, and the same key in sibling objects, are ordinary JSON.
	assertPublishable(t, "sibling objects", `[{"content":"a"},{"content":"b"}]`)
	assertPublishable(t, "nested same name", `{"content":"a","turn":{"content":"b"}}`)
}

// --- D. Sensitive environment placeholders ---

// TestSensitiveEnvPlaceholdersAreNotCredentialValues verifies a sensitive
// name assigned a syntactic placeholder — a <word-slot> or a ${NAME} / $NAME
// reference — is not a credential value, while literal values, including
// natural-language "your-..." text, still are.
// Failure prevented: documentation such as TOKEN=<token> is refused and
// wedges the Ledger, or a placeholder rule wide enough to excuse a real value.
func TestSensitiveEnvPlaceholdersAreNotCredentialValues(t *testing.T) {
	for name, input := range map[string]string{
		"doc comment slot":    "// OPENAI_PLUGIN_VERIFICATION_TOKEN=<verification-token>",
		"slot then prose":     "// OPENAI_PLUGIN_VERIFICATION_TOKEN=<verification-token> placeholder-from-dashboard value",
		"braced reference":    "export OPENAI_API_KEY=${OPENAI_API_KEY}",
		"bare reference":      "GITHUB_TOKEN=$GH_TOKEN gh pr list",
		"helm slot":           "helm upgrade app chart --set extraEnv[0].name=API_TOKEN --set extraEnv[0].value='<api-token>'",
		"JSON name/value ref": `{"name":"API_KEY","value":"${API_KEY}"}`,
		"JSON sensitive key":  `{"api_key":"<api-key>"}`,
	} {
		assertPublishable(t, name, input)
	}
	opaque := syntheticToken("env/opaque", 24)
	for i, input := range []string{
		"API_KEY=your-api-key-here",
		"API_KEY=<" + syntheticHex("env/wrapped", 24) + ">",
		"API_TOKEN='<" + opaque + "$!>'",
		"API_KEY=${API_KEY:-" + opaque + "}",
		"DB_PASSWORD=$Passw0rdLiteral",
		`{"name":"API_KEY","value":"<` + syntheticHex("env/json", 24) + `>"}`,
	} {
		if _, found := RedactText(input); !slices.Contains(found, "sensitive-env-value") {
			t.Errorf("literal case %d accepted as a placeholder", i)
		}
	}
}
