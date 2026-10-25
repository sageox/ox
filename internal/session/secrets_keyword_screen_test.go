package session

import (
	"regexp/syntax"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Keywords pre-screen skips a pattern's regex entirely when no keyword is
// present, so a keyword that the regex does not actually require silently
// disables the detector for every input that would otherwise match. No
// positive-sample test catches that: the samples contain the keyword by
// construction. This file decides the question from the parsed regex instead,
// so the answer holds for all inputs rather than the ones someone wrote down.
//
// A `branch` is the ordered list of literal runs guaranteed on one path through
// the regex. Anything that is not a literal — a char class, an optional group,
// an anchor — ends the current run, because no literal survives across it.
// Adjacent literals merge into one run, which is what makes this agree with the
// parser: `syntax.Parse` factors shared prefixes out of an alternation, turning
// `(glpat-|gloas-)` into `gl(pat-|oas-)`, and only the merged run contains the
// keyword the catalog declares.
type branch []string

// literalBranchBudget caps the cross-product an alternation-heavy pattern can
// produce. Exceeding it yields an uncovered branch, so the gate fails loudly
// rather than passing a pattern it stopped analyzing.
const literalBranchBudget = 4096

func literalBranches(re *syntax.Regexp) []branch {
	switch re.Op {
	case syntax.OpLiteral:
		// Fold-case literals are stored unfolded; MatchesKeyword lowercases its
		// input, so the lowercased literal is the thing to compare against.
		return []branch{{strings.ToLower(string(re.Rune))}}

	case syntax.OpCapture, syntax.OpPlus:
		return literalBranches(re.Sub[0])

	case syntax.OpRepeat:
		if re.Min >= 1 {
			return literalBranches(re.Sub[0])
		}
		return []branch{{""}}

	case syntax.OpAlternate:
		var out []branch
		for _, sub := range re.Sub {
			out = append(out, literalBranches(sub)...)
			if len(out) > literalBranchBudget {
				return []branch{{""}}
			}
		}
		return out

	case syntax.OpConcat:
		out := []branch{{}}
		for _, sub := range re.Sub {
			next := make([]branch, 0, len(out))
			for _, head := range out {
				for _, tail := range literalBranches(sub) {
					next = append(next, joinBranch(head, tail))
				}
			}
			if len(next) > literalBranchBudget {
				return []branch{{""}}
			}
			out = next
		}
		return out

	default:
		// Char classes, anchors, `.`, `*`, `?`: no literal is guaranteed.
		return []branch{{""}}
	}
}

func joinBranch(a, b branch) branch {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	out := append(branch{}, a[:len(a)-1]...)
	last, first := a[len(a)-1], b[0]
	if last != "" && first != "" {
		out = append(out, last+first)
	} else {
		out = append(out, last, first)
	}
	return append(out, b[1:]...)
}

// mustContainOneOf reports whether every string the regex matches contains at
// least one of `lower`, which is exactly the condition under which skipping the
// regex on a keyword miss cannot lose a detection.
func mustContainOneOf(re *syntax.Regexp, lower []string) bool {
	branches := literalBranches(re)
	if len(branches) == 0 {
		return false
	}
	for _, b := range branches {
		covered := false
		for _, run := range b {
			for _, kw := range lower {
				if strings.Contains(run, kw) {
					covered = true
					break
				}
			}
			if covered {
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

// minAnchorLen is the shortest literal run treated as a usable anchor. A
// one- or two-byte run ("." in the Discord bot-token shape, "Q~" in the Azure
// one) is present in almost every line, so screening on it would claim a
// filter that does not filter.
const minAnchorLen = 3

// screenable reports whether some keyword set could screen this regex without
// losing detections: every path through it must carry a literal run long enough
// to anchor on. Patterns that fail this — a bare 40-hex token, a token shape
// built entirely from char classes — cannot be screened at all, and the gate
// below exempts them by this test rather than by a hand-maintained list.
func screenable(re *syntax.Regexp) bool {
	for _, b := range literalBranches(re) {
		anchored := false
		for _, run := range b {
			if len(run) >= minAnchorLen {
				anchored = true
				break
			}
		}
		if !anchored {
			return false
		}
	}
	return true
}

// productionCatalog is every pattern RawWriter.WriteEntry applies, in the order
// it applies them. The chokepoint runs on every session entry write, so an
// unscreened pattern here costs a full regex pass over every byte of every
// tool output a coworker's agent produces.
func productionCatalog() []SecretPattern {
	all := DefaultPatterns()
	all = append(all, DefaultExtraDetectors()...)
	return append(all, generatedGitleaksDetectors()...)
}

// heroku_key's keyword is a policy narrowing, not a speed screen: its regex is a
// bare UUID, and gating on "heroku" is what stops it redacting every UUID in a
// session. The keyword is deliberately NOT implied by the regex, so the
// soundness gate below cannot apply to it. Any other pattern landing here is a
// detector someone turned off by accident.
var keywordScreenPolicyExceptions = map[string]string{
	"heroku_key": "bare-UUID regex; the keyword narrows an over-broad pattern rather than screening it",
}

// A speed screen must be implied by the pattern it screens. Without this gate,
// narrowing a regex or widening a keyword turns a detector off for a whole class
// of inputs while every existing test still passes.
func TestSecretPatternKeywords_AreRequiredByTheirRegex(t *testing.T) {
	t.Parallel()

	for _, p := range productionCatalog() {
		if len(p.Keywords) == 0 {
			continue
		}
		t.Run(p.Name, func(t *testing.T) {
			t.Parallel()

			lower := make([]string, len(p.Keywords))
			for i, kw := range p.Keywords {
				assert.Equal(t, strings.ToLower(kw), kw,
					"keyword %q must be lowercase — MatchesKeyword compares against a lowercased input", kw)
				lower[i] = strings.ToLower(kw)
			}

			re, err := syntax.Parse(p.Pattern.String(), syntax.Perl)
			require.NoError(t, err)
			required := mustContainOneOf(re.Simplify(), lower)

			if why, exempt := keywordScreenPolicyExceptions[p.Name]; exempt {
				assert.False(t, required,
					"%s is listed as a policy exception (%s) but its keywords ARE implied by its regex — drop the exception", p.Name, why)
				return
			}
			assert.True(t, required,
				"pattern %s can match a string containing none of %v, so the pre-screen would skip it and the secret would ship",
				p.Name, p.Keywords)
		})
	}
}

// The gate above is only load-bearing if it actually rejects a bad keyword.
func TestMustContainOneOf_RejectsKeywordsTheRegexDoesNotRequire(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		pattern  string
		keywords []string
		want     bool
	}{
		{"literal prefix is required", `AKIA[0-9A-Z]{16}`, []string{"akia"}, true},
		{"keyword absent from the pattern", `AKIA[0-9A-Z]{16}`, []string{"asia"}, false},
		{"every branch carries the keyword", `(access_token|auth_token)=x`, []string{"token"}, true},
		{"one branch lacks the keyword", `(access_token|api_key)=x`, []string{"token"}, false},
		{"per-branch keywords survive prefix factoring", `(glpat-|gloas-)x`, []string{"glpat-", "gloas-"}, true},
		{"keyword spanning a factored prefix and a branch", `(aws_secret_key|aws_secret_id)=x`, []string{"aws_secret_key", "aws_secret_id"}, true},
		{"optional group cannot be relied on", `(secret)?[a-z]+`, []string{"secret"}, false},
		{"one-or-more group is required", `(secret)+[a-z]+`, []string{"secret"}, true},
		{"star group cannot be relied on", `(secret)*[a-z]+`, []string{"secret"}, false},
		{"char class breaks the literal run", `sec[a-z]ret`, []string{"secret"}, false},
		{"fold-case literal matches a lowercase keyword", `(?i)Authorization:\s*Bearer\s+\w+`, []string{"authorization:"}, true},
		{"char class carries no literal", `[a-f0-9]{32}-us[0-9]`, []string{"heroku"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			re, err := syntax.Parse(tc.pattern, syntax.Perl)
			require.NoError(t, err)
			assert.Equal(t, tc.want, mustContainOneOf(re.Simplify(), tc.keywords))
		})
	}
}

// generatorDerivationGaps are screenable patterns that nonetheless ship without
// a screen, because gitleaks_generated.go is generated and its deriveKeywords
// walker cannot recover them. Go's regex parser factors shared prefixes out of
// an alternation — `fo1_|fm1[ar]_|fm2_` parses as `f(?:o1_|m1[ar]_|m2_)` — which
// leaves the `m1` branch below the generator's 3-byte floor and drops the whole
// rule to the always-run fallback. Costs ~11ms per MiB scanned. Teaching the
// generator to merge a factored prefix back into each branch is the fix; it
// means regenerating and re-validating all 147 rules, so it is deliberately not
// bundled here.
var generatorDerivationGaps = map[string]string{
	"flyio_access_token": "generated rule; deriveKeywords loses the prefix the parser factored out of its alternation",
}

// Every pattern that CAN be screened must be. An unscreened pattern runs its
// regex over every byte of every input: that is what made the pre-push secret
// gate cost ~0.9s per MiB, and both the gate and the write-time chokepoint run
// over whole megabyte-scale tool-output lines.
func TestProductionCatalog_EveryScreenablePatternIsScreened(t *testing.T) {
	t.Parallel()

	var unscreened []string
	for _, p := range productionCatalog() {
		if len(p.Keywords) > 0 {
			continue
		}
		re, err := syntax.Parse(p.Pattern.String(), syntax.Perl)
		require.NoError(t, err)
		if screenable(re.Simplify()) && generatorDerivationGaps[p.Name] == "" {
			unscreened = append(unscreened, p.Name)
		}
	}
	assert.Empty(t, unscreened,
		"these patterns run their regex on every input; give each one the literal its regex already requires")
}

// Pattern names must be unique across the three catalog tiers: the screen and
// the redaction slug are both looked up by name, and a duplicate would make
// audit records ambiguous about which detector fired.
func TestProductionCatalog_NamesAreUnique(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for _, p := range productionCatalog() {
		assert.False(t, seen[p.Name], "duplicate detector name %q", p.Name)
		seen[p.Name] = true
	}
}

// withoutDerivedScreens returns the catalog with every speed screen removed,
// leaving the policy narrowings in place — dropping heroku_key's keyword would
// legitimately change what it redacts, so it is not part of the property below.
func withoutDerivedScreens(patterns []SecretPattern) []SecretPattern {
	out := make([]SecretPattern, len(patterns))
	copy(out, patterns)
	for i := range out {
		if keywordScreenPolicyExceptions[out[i].Name] == "" {
			out[i].Keywords = nil
		}
	}
	return out
}

// The screen decides whether to run a pattern by looking at the ORIGINAL input,
// but RedactString then runs that pattern against `output`, which earlier
// patterns have already rewritten. Redaction placeholders are bytes the input
// never had, so a match can exist in `output` that the screen never saw — and
// the structural gate above cannot detect that, because it only reasons about
// the regex. Compare the screened catalog against an unscreened one and require
// them to agree on both the redacted text and the detectors that fired.
func FuzzRedactString_ScreenIsTransparent(f *testing.F) {
	seeds := []string{
		"",
		"no secrets in this line at all",
		"AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE",
		`{"token":"ghp_1234567890abcdefghijklmnopqrstuvwxyz"}`,
		"Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789",
		"export GITHUB_TOKEN=ghp_1234567890abcdefghijklmnopqrstuvwxyz",
		"postgres://user:hunter2hunter2@db.internal:5432/app",
		`password="hunter2hunter2" api_key=abcdefghijklmnopqrstuvwx`,
		"-----BEGIN PRIVATE KEY-----",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abcdef",
		"sox_share_session=abcdefghijklmnopqrst",
		"https://u:supersecret@example.com/path",
		// Adjacent secrets of different classes: the first redaction rewrites
		// the very bytes the next pattern is matched against.
		"AKIAIOSFODNN7EXAMPLEghp_1234567890abcdefghijklmnopqrstuvwxyz",
		"secret=aaaaaaaaaaaaaaaaaaaa token=bbbbbbbbbbbbbbbbbbbbbb",
		// A placeholder shape appearing in the input, so a later pattern sees
		// text that looks like something an earlier one produced.
		"[REDACTED_GITHUB_TOKEN] password=\"hunter2hunter2\"",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	catalog := productionCatalog()
	screened := NewRedactorWithPatterns(catalog)
	unscreened := NewRedactorWithPatterns(withoutDerivedScreens(catalog))

	f.Fuzz(func(t *testing.T, in string) {
		gotOut, gotFound := screened.RedactString(in)
		wantOut, wantFound := unscreened.RedactString(in)

		if gotOut != wantOut {
			t.Fatalf("screen changed redaction of %q:\n screened: %q\nunscreened: %q", in, gotOut, wantOut)
		}
		sort.Strings(gotFound)
		sort.Strings(wantFound)
		if !slices.Equal(gotFound, wantFound) {
			t.Fatalf("screen changed detectors for %q: screened=%v unscreened=%v", in, gotFound, wantFound)
		}
	})
}
