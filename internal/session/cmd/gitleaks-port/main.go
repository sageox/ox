// Command gitleaks-port translates the gitleaks default rule catalog
// (config/gitleaks.toml from the gitleaks repo) into Go SecretPattern
// entries for ox's session.RawWriter chokepoint.
//
// Run via `make sync-gitleaks-rules`. The generator reads the pinned
// TOML alongside this file and writes
// internal/session/gitleaks_generated.go.
//
// Why generate instead of import the gitleaks Go module: the gitleaks
// runtime pulls in a wazero WASM dependency and other heavy transitive
// imports. The rules themselves are static MIT-licensed data; the most
// honest dependency-minimal path is to translate them at build time
// and ship the generated Go file in the repo. The generator pin is
// updated by re-running `make sync-gitleaks-rules` with a newer toml.
//
// Skipped at translation time:
//   - Rules whose regex doesn't compile with Go's regexp/RE2 (rare;
//     gitleaks targets the same engine but a handful of patterns use
//     features RE2 rejects).
//   - Rules duplicated by ox's existing DefaultPatterns or hand-ported
//     DefaultExtraDetectors. Hand-ported wins because the ox version
//     carries class-specific [REDACTED_*] slugs that consumers grep for.
//   - Rules that capture broad classes of content with no clear
//     credential boundary (e.g. "generic-api-key" with very loose
//     regex — those produce too many false positives in coding-agent
//     session content, where keys, ids, and tokens of all shapes show
//     up in legitimate prose).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// gitleaksConfig is the subset of gitleaks.toml we actually consume.
type gitleaksConfig struct {
	Rules []gitleaksRule `toml:"rules"`
}

type gitleaksRule struct {
	ID          string   `toml:"id"`
	Description string   `toml:"description"`
	Regex       string   `toml:"regex"`
	Keywords    []string `toml:"keywords"`
	Entropy     float64  `toml:"entropy"`
}

// skipRuleIDs lists gitleaks rule IDs that ox handles via a hand-ported
// detector (or via the built-in DefaultPatterns set). The hand-ported
// version typically has a class-specific [REDACTED_*] slug that grep
// consumers already depend on; the generated version would carry a
// generic slug.
var skipRuleIDs = map[string]bool{
	// Covered by internal/session/secrets.go DefaultPatterns:
	"aws-access-token":                   true, // we have aws_access_key / aws_sts_session_key
	"aws-secret-token":                   true, // we have aws_secret_key
	"github-pat":                         true, // we have github_token + github_fine_grained_pat
	"github-fine-grained-pat":            true,
	"github-oauth":                       true,
	"github-app-token":                   true,
	"github-refresh-token":               true,
	"gitlab-pat":                         true, // we have gitlab_token
	"gitlab-oat":                         true, // gitlab_oauth_token
	"gitlab-deploy-token":                true, // gitlab_deploy_token
	"gitlab-runner-token":                true, // gitlab_runner_token
	"gitlab-feed-token":                  true, // gitlab_feed_token
	"slack-bot-token":                    true, // we have slack_token (covers all xox*)
	"slack-user-token":                   true,
	"slack-app-token":                    true,
	"slack-config-access-token":          true,
	"slack-config-refresh-token":         true,
	"slack-legacy-bot-token":             true,
	"slack-legacy-token":                 true,
	"slack-legacy-workspace-token":       true,
	"slack-webhook-url":                  true, // hand-ported (slack_webhook_url)
	"stripe-access-token":                true, // we have stripe_key
	"twilio-api-key":                     true, // we have twilio_key
	"sendgrid-api-token":                 true, // we have sendgrid_key
	"mailchimp-api-key":                  true, // we have mailchimp_key
	"npm-access-token":                   true, // we have npm_token
	"pypi-upload-token":                  true, // we have pypi_token
	"heroku-api-key":                     true, // hand-ported
	"private-key":                        true, // private_key_header
	"jwt":                                true, // jwt_token
	"jwt-base64":                         true,
	"openai-api-key":                     true, // hand-ported
	"anthropic-api-key":                  true, // hand-ported
	"gcp-api-key":                        true, // hand-ported
	"gcp-service-account":                true, // hand-ported
	"datadog-access-token":               true, // hand-ported
	"pagerduty-api-key":                  true, // hand-ported
	"sentry-access-token":                true, // hand-ported (sentry_auth_token)
	"sentry-org-token":                   true,
	"sentry-user-token":                  true,
	"new-relic-user-api-key":             true, // hand-ported
	"new-relic-ingest-browser-api-token": true,
	"grafana-cloud-api-token":            true, // hand-ported
	"grafana-service-account-token":      true,
	"vault-service-token":                true, // hand-ported (vault_root_token)
	"vault-batch-token":                  true,
	"doppler-api-token":                  true, // hand-ported
	"square-access-token":                true, // hand-ported
	"shopify-shared-secret":              true,
	"shopify-access-token":               true, // hand-ported
	"shopify-custom-access-token":        true,
	"shopify-private-app-access-token":   true,
	"discord-api-token":                  true, // hand-ported (discord_bot_token)
	"discord-client-secret":              true,
	"discord-client-id":                  true,
	"linear-api-key":                     true, // hand-ported
	"linear-client-secret":               true,
	"notion-api-key":                     true, // hand-ported (notion_integration_token)
	"postman-api-token":                  true, // hand-ported
	"cloudflare-api-key":                 true, // hand-ported
	"cloudflare-api-token":               true,
	"cloudflare-origin-ca-key":           true,
	"cloudflare-global-api-key":          true,
	"digitalocean-pat":                   true, // hand-ported
	"digitalocean-access-token":          true,
	"digitalocean-refresh-token":         true,
	"dynatrace-api-token":                true, // hand-ported
	"snyk-api-token":                     true, // hand-ported
	"cloudinary-credentials":             true, // hand-ported (cloudinary_url)
	"facebook-access-token":              true, // hand-ported
	"facebook-secret":                    true,
	"dropbox-access-token":               true, // hand-ported
	"dropbox-api-token":                  true,
	"dropbox-short-lived-access-token":   true,
	"airtable-api-key":                   true, // hand-ported
	"vercel-api-token":                   true, // hand-ported
	"netlify-access-token":               true, // hand-ported
	"planetscale-password":               true, // hand-ported
	"planetscale-api-token":              true,
	"mailgun-private-api-token":          true, // hand-ported (mailgun_api_key)
	"mailgun-pub-key":                    true,
	"mailgun-signing-key":                true,
	"sendinblue-api-token":               true, // hand-ported (brevo_api_key)
	"rubygems-api-token":                 true, // hand-ported
	"clerk-secret-key":                   true, // hand-ported
	"clerk-publishable-key":              true,
	"clerk-frontend-api":                 true,
	"braintree-access-token":             true, // hand-ported

	// Skipped because too broad / false-positive-prone for coding agent
	// content (these regexes match too much "looks-like-a-token" text
	// without a clear credential boundary):
	"generic-api-key":        true,
	"generic-bearer":         true,
	"hashicorp-tf-api-token": true, // overlaps with vault-service-token in shape
}

// classFromID derives a [REDACTED_<CLASS>] slug from a gitleaks rule ID.
// e.g. "1password-secret-key" → "1PASSWORD_SECRET_KEY"; rule names with
// digits at the start are prefixed to keep the Go identifier valid.
func classFromID(id string) string {
	s := strings.ToUpper(id)
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, ".", "_")
	return s
}

// nameFromID derives the Go SecretPattern.Name field (lowercase, _).
func nameFromID(id string) string {
	s := strings.ToLower(id)
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, ".", "_")
	return s
}

// goIdentFromID derives a Go variable identifier prefix from the rule ID
// so identifiers don't collide. Currently unused (we emit only the slice
// literal in the generated file), but kept for future extensibility.
func goIdentFromID(id string) string {
	parts := strings.FieldsFunc(id, func(r rune) bool { return r == '-' || r == '.' || r == '_' })
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "")
}

// validateRegex returns true if pattern compiles with Go's regexp
// engine. gitleaks targets RE2 too, so most rules pass. The handful
// that don't get dropped with a logged warning.
func validateRegex(pattern string) error {
	_, err := regexp.Compile(pattern)
	return err
}

// minKeywordLen is the shortest synthetic keyword we'll emit. Anything
// shorter (e.g. a single dash or hyphen from a regex literal) screens
// out almost nothing and just bloats the keyword list, so we skip it.
const minKeywordLen = 3

// guaranteedAlternatives walks a parsed regex AST and returns a set of
// lowercase literal substrings such that every match contains at least
// one element of the set (substring, case-insensitively). Returns nil
// when no such guarantee can be made — the caller treats this as "no
// safe screen, run the regex unconditionally."
//
// This is the load-bearing function for keyword pre-screen correctness:
// the chokepoint OR-matches the set against lower(input) and skips the
// regex when no element appears, so the set must be sound for every
// possible match.
//
// We derive this from the regex AST (not from gitleaks' shipped
// keywords field) because many gitleaks rules use a vendor name as the
// keyword ("airtable") while the regex matches a context-free token
// (\b(pat[[:alnum:]]{14}\.[a-f0-9]{64})\b). A leaked token in a
// context-poor tool dump wouldn't contain "airtable" and would bypass
// the screen — a real security false-negative.
//
// FoldCase is honored implicitly: the returned literals are always
// lowercase, matching how the chokepoint lowercases its input.
func guaranteedAlternatives(re *syntax.Regexp) []string {
	if re == nil {
		return nil
	}
	switch re.Op {
	case syntax.OpLiteral:
		if len(re.Rune) == 0 {
			return nil
		}
		lit := strings.ToLower(string(re.Rune))
		// Apply the length floor here, inside the walker, so that
		// OpAlternate's "every branch must contribute" rule
		// correctly returns nil when a branch's only literal is
		// shorter than minKeywordLen. Doing the filter at the top
		// level would break alternation safety.
		if len(lit) < minKeywordLen {
			return nil
		}
		return []string{lit}

	case syntax.OpConcat:
		// Every child's match is in every match. Pick the single
		// child whose guarantee set has the longest single literal
		// (most selective). The OR-union of THAT child's set is
		// sufficient: a match contains a match of that child, hence
		// contains one of its alternatives.
		var best []string
		bestLen := 0
		for _, sub := range re.Sub {
			subG := guaranteedAlternatives(sub)
			if len(subG) == 0 {
				continue
			}
			maxLen := 0
			for _, l := range subG {
				if len(l) > maxLen {
					maxLen = len(l)
				}
			}
			if maxLen > bestLen {
				bestLen = maxLen
				best = subG
			}
		}
		return best

	case syntax.OpAlternate:
		// A match takes exactly one branch. For the OR-screen to be
		// sound, EVERY branch must contribute at least one
		// alternative; otherwise some branch could match without any
		// of our keywords being present.
		var out []string
		for _, sub := range re.Sub {
			subG := guaranteedAlternatives(sub)
			if len(subG) == 0 {
				return nil
			}
			out = append(out, subG...)
		}
		return out

	case syntax.OpCapture:
		if len(re.Sub) == 0 {
			return nil
		}
		return guaranteedAlternatives(re.Sub[0])

	case syntax.OpPlus:
		// 1+ repetition: the body matches at least once.
		if len(re.Sub) == 0 {
			return nil
		}
		return guaranteedAlternatives(re.Sub[0])

	case syntax.OpRepeat:
		if re.Min > 0 && len(re.Sub) > 0 {
			return guaranteedAlternatives(re.Sub[0])
		}
		return nil
	}
	// OpStar / OpQuest / OpAnyChar / OpCharClass / OpBegin*  etc:
	// nothing guaranteed.
	return nil
}

// deriveKeywords returns the keyword set the generator should emit for
// a rule. Filters short literals (under minKeywordLen) and dedupes.
// Returns nil when the result is empty — caller omits Keywords so the
// regex runs unconditionally (the safe fallback for context-free
// tokens like sourcegraph's bare 40-char hex).
func deriveKeywords(rx string) []string {
	parsed, err := syntax.Parse(rx, syntax.Perl)
	if err != nil {
		return nil
	}
	// We deliberately do NOT call Simplify(): it can factor common
	// prefixes out of alternations (e.g. fo1_|fm1[ar]_|fm2_ becomes
	// f(?:o1_|m1[ar]_|m2_)), which leaves one branch with no literal
	// of length >= minKeywordLen and forces the whole rule into the
	// always-run fallback. The unsimplified tree usually has longer
	// per-branch literals, which makes the screen sharper.
	alts := guaranteedAlternatives(parsed)
	seen := map[string]bool{}
	var out []string
	for _, l := range alts {
		if seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

func main() {
	var (
		input  = flag.String("in", "gitleaks-v8.30.1.toml", "gitleaks.toml source path")
		output = flag.String("out", "../../gitleaks_generated.go", "Go file to write")
		ver    = flag.String("gitleaks-version", "v8.30.1", "gitleaks version pin for the header comment")
	)
	flag.Parse()

	cfgBytes, err := os.ReadFile(*input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", *input, err)
		os.Exit(1)
	}
	var cfg gitleaksConfig
	if err := toml.Unmarshal(cfgBytes, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "parse toml: %v\n", err)
		os.Exit(1)
	}

	// Deterministic order: sort by rule id so generated output is stable.
	sort.Slice(cfg.Rules, func(i, j int) bool { return cfg.Rules[i].ID < cfg.Rules[j].ID })

	var b strings.Builder
	fmt.Fprintf(&b, `// Code generated by internal/session/cmd/gitleaks-port. DO NOT EDIT.
//
// Source: github.com/gitleaks/gitleaks @ %s (config/gitleaks.toml)
// License (source): MIT
//
// Run %sin internal/session/cmd/gitleaks-port:
//   go run . -in gitleaks-v8.30.1.toml -out ../../gitleaks_generated.go
//
// Or via the Makefile: make sync-gitleaks-rules.
//
// Rules whose regex doesn't compile under Go's regexp/RE2 engine are
// skipped (rare; logged at generation time). Rules duplicated by ox's
// hand-ported detectors (see gitleaks_detectors.go and secrets.go) are
// also skipped — the hand-ported versions carry class-specific
// [REDACTED_*] slugs that downstream consumers grep for.

package session

import "regexp"

// generatedGitleaksDetectors returns the auto-translated subset of the
// gitleaks default rule catalog. Combined with DefaultExtraDetectors,
// these provide ox's third-layer detection in RawWriter.
func generatedGitleaksDetectors() []SecretPattern {
	return []SecretPattern{
`, *ver, "`")

	emitted, skippedRE2, skippedDup := 0, 0, 0
	for _, r := range cfg.Rules {
		if r.ID == "" || r.Regex == "" {
			continue
		}
		if skipRuleIDs[r.ID] {
			skippedDup++
			continue
		}
		if err := validateRegex(r.Regex); err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: regex did not compile under RE2: %v\n", r.ID, err)
			skippedRE2++
			continue
		}
		class := classFromID(r.ID)
		// Keywords are derived from the regex AST (NOT from gitleaks'
		// shipped keywords). A keyword in the emitted SecretPattern
		// must be a guaranteed substring of every possible match,
		// because the chokepoint quick-screen skips the regex when no
		// keyword appears in the input. gitleaks' shipped keywords
		// often name the vendor ("airtable") while the regex matches a
		// context-free token ("pat[alnum]{14}.{hex64}"), which would
		// false-negative on a leaked bare token. When the AST yields
		// no literal of length >= minKeywordLen, Keywords is omitted
		// and the regex runs unconditionally — slower but correct.
		kw := deriveKeywords(r.Regex)
		fmt.Fprintf(&b, "\t\t{\n")
		fmt.Fprintf(&b, "\t\t\tName:    %q,\n", nameFromID(r.ID))
		fmt.Fprintf(&b, "\t\t\tPattern: regexp.MustCompile(%s),\n", goRawString(r.Regex))
		fmt.Fprintf(&b, "\t\t\tRedact:  %q,\n", "[REDACTED_"+class+"]")
		if len(kw) > 0 {
			fmt.Fprintf(&b, "\t\t\tKeywords: []string{")
			for i, k := range kw {
				if i > 0 {
					fmt.Fprintf(&b, ", ")
				}
				fmt.Fprintf(&b, "%q", k)
			}
			fmt.Fprintf(&b, "},\n")
		}
		fmt.Fprintf(&b, "\t\t},\n")
		emitted++
	}
	fmt.Fprintf(&b, "\t}\n}\n")

	if err := os.WriteFile(*output, []byte(b.String()), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", *output, err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr,
		"gitleaks-port: emitted %d patterns, skipped %d duplicates of hand-ported rules, %d incompatible regexes\n",
		emitted, skippedDup, skippedRE2)
	fmt.Fprintf(os.Stderr, "wrote %s\n", filepath.Clean(*output))
	_ = goIdentFromID // reserved for future use
}

// goRawString quotes s as a Go raw string literal when possible
// (backtick-delimited), falling back to a double-quoted literal when s
// contains a backtick. Backticks are rare in regex bodies — most
// gitleaks rules use them as the literal pattern delimiter in TOML,
// stripped during parsing.
func goRawString(s string) string {
	if !strings.Contains(s, "`") {
		return "`" + s + "`"
	}
	// fallback: replace ` with `+"`"+` inside a string literal
	parts := strings.Split(s, "`")
	var qb strings.Builder
	for i, p := range parts {
		if i > 0 {
			qb.WriteString("+ \"`\" +")
		}
		qb.WriteString("`")
		qb.WriteString(p)
		qb.WriteString("`")
	}
	return qb.String()
}
