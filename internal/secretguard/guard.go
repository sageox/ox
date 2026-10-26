// Package secretguard is the mandatory, dependency-light publication guard.
package secretguard

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"regexp/syntax"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Gitleaks rules intentionally always follow the latest upstream automatically.
// This complete catalog is only the offline fallback; publication refreshes it,
// never a manually selected or hand-ported subset of upstream detectors.
//
//go:embed gitleaks.toml
var bundledCatalog []byte

// RuleSet is immutable after construction and safe for concurrent scans.
// All upstream regex/path rules load with the filters that define each
// detector: entropy, keywords, and the allowlists shipped inside the rule.
// Keywords gate a rule only when its expression proves no literal; a rule
// allowlist's path condition never holds, because captured content has no
// repository location. The catalog-wide allowlist is a repository-scanning
// policy, not a suppressor for captured tool output. Explicit sensitive
// environment context is always redacted, independent of entropy, unless the
// value is a syntactic placeholder.
type RuleSet struct {
	rules []rule
	hash  string
}
type rule struct {
	id               string
	expression, path *regexp.Regexp
	keywords         []string // literals the expression provably contains
	gate             []string // upstream keywords, required when keywords is empty
	allowlists       []ruleAllowlist
	group            int
	entropy          float64
}

// ruleAllowlist is the content side of an upstream [[rules.allowlists]] entry.
type ruleAllowlist struct {
	target    string // "secret", "match" or "line"
	all       bool   // condition AND
	located   bool   // has a path condition, which captured content never meets
	regexes   []*regexp.Regexp
	stopwords []string
}
type config struct {
	Title      string       `toml:"title"`
	MinVersion string       `toml:"minVersion"`
	Allowlist  allowlist    `toml:"allowlist"`
	Allowlists []allowlist  `toml:"allowlists"`
	Rules      []ruleConfig `toml:"rules"`
}
type allowlist struct {
	Description string   `toml:"description"`
	Condition   string   `toml:"condition"`
	Paths       []string `toml:"paths"`
	RegexTarget string   `toml:"regexTarget"`
	Regexes     []string `toml:"regexes"`
	Stopwords   []string `toml:"stopwords"`
}
type ruleConfig struct {
	ID          string      `toml:"id"`
	Description string      `toml:"description"`
	Regex       string      `toml:"regex"`
	Path        string      `toml:"path"`
	Entropy     float64     `toml:"entropy"`
	SecretGroup int         `toml:"secretGroup"`
	Keywords    []string    `toml:"keywords"`
	Allowlists  []allowlist `toml:"allowlists"`
}

// NewRules rejects unknown configuration features instead of silently dropping
// future upstream protections. Errors never include config or matched values.
func NewRules(data []byte) (*RuleSet, error) {
	var c config
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&c); err != nil {
		return nil, errors.New("secret guard: unsupported or malformed rule catalog")
	}
	if len(c.Rules) == 0 {
		return nil, errors.New("secret guard: empty rule catalog")
	}
	seen := map[string]bool{}
	s := &RuleSet{}
	for _, cfg := range c.Rules {
		if cfg.ID == "" || seen[cfg.ID] || cfg.Regex == "" && cfg.Path == "" {
			return nil, errors.New("secret guard: invalid rule catalog")
		}
		seen[cfg.ID] = true
		r := rule{id: cfg.ID, group: cfg.SecretGroup, entropy: cfg.Entropy}
		var err error
		if cfg.Regex != "" {
			r.expression, err = regexp.Compile(cfg.Regex)
			if err != nil || r.group < 0 || r.group > r.expression.NumSubexp() {
				return nil, errors.New("secret guard: unsupported rule expression")
			}
		}
		if r.expression != nil && r.group == 0 && r.expression.NumSubexp() > 0 {
			r.group = 1
		}
		if cfg.Path != "" {
			r.path, err = regexp.Compile(cfg.Path)
			if err != nil {
				return nil, errors.New("secret guard: unsupported rule path")
			}
		}
		var hints []string
		for _, k := range cfg.Keywords {
			if k != "" {
				hints = append(hints, strings.ToLower(k))
			}
		}
		if r.expression != nil {
			parsed, err := syntax.Parse(cfg.Regex, syntax.Perl)
			if err != nil {
				return nil, errors.New("secret guard: unsupported rule syntax")
			}
			// Derive mandatory literals from the expression itself. The upstream
			// keywords are only hints and sometimes absent from valid credential text.
			// This keeps large tool results cheap without introducing token omissions.
			r.keywords = requiredKeywords(parsed)
			// Without a provable literal the expression alone matches ordinary
			// text: Sourcegraph's bare alternative is any 40-hex commit SHA. Its
			// keywords are then the detector's precision gate, as upstream applies them.
			if len(r.keywords) == 0 {
				r.gate = hints
			}
		}
		for _, a := range cfg.Allowlists {
			compiled, err := newRuleAllowlist(a)
			if err != nil {
				return nil, err
			}
			r.allowlists = append(r.allowlists, compiled)
		}

		s.rules = append(s.rules, r)
	}
	h := sha256.Sum256(data)
	s.hash = hex.EncodeToString(h[:])
	return s, nil
}

// newRuleAllowlist mirrors upstream allowlist validation; unknown targets or
// conditions are refused rather than guessed.
func newRuleAllowlist(cfg allowlist) (ruleAllowlist, error) {
	unsupported := errors.New("secret guard: unsupported rule allowlist")
	a := ruleAllowlist{target: cfg.RegexTarget, located: len(cfg.Paths) > 0}
	switch a.target {
	case "":
		a.target = "secret"
	case "secret", "match", "line":
	default:
		return a, unsupported
	}
	switch strings.ToUpper(cfg.Condition) {
	case "", "OR", "||":
	case "AND", "&&":
		a.all = true
	default:
		return a, unsupported
	}
	if len(cfg.Paths) == 0 && len(cfg.Regexes) == 0 && len(cfg.Stopwords) == 0 {
		return a, unsupported
	}
	for _, p := range cfg.Paths {
		if _, err := regexp.Compile(p); err != nil {
			return a, unsupported
		}
	}
	for _, expression := range cfg.Regexes {
		re, err := regexp.Compile(expression)
		if err != nil {
			return a, unsupported
		}
		a.regexes = append(a.regexes, re)
	}
	// Upstream matches stopwords case-insensitively against the secret.
	for _, w := range cfg.Stopwords {
		if w != "" {
			a.stopwords = append(a.stopwords, strings.ToLower(w))
		}
	}
	return a, nil
}

// allows reports whether upstream would drop the finding. Regexes test the
// configured target and stopwords the secret; a path condition never holds.
func (a ruleAllowlist) allows(secret, match, line string) bool {
	target := secret
	switch a.target {
	case "match":
		target = match
	case "line":
		target = line
	}
	regex := false
	for _, re := range a.regexes {
		if target != "" && re.MatchString(target) {
			regex = true
			break
		}
	}
	stopword := false
	lower := strings.ToLower(secret)
	for _, w := range a.stopwords {
		if strings.Contains(lower, w) {
			stopword = true
			break
		}
	}
	if a.all {
		return !a.located && len(a.regexes)+len(a.stopwords) > 0 &&
			(len(a.regexes) == 0 || regex) && (len(a.stopwords) == 0 || stopword)
	}
	return regex || stopword
}

var bundled = mustBundled()

func mustBundled() *RuleSet {
	s, err := NewRules(bundledCatalog)
	if err != nil {
		panic(err)
	}
	return s
}

// CatalogHash identifies every upstream catalog byte, including future additions.
func CatalogHash() string { return bundled.hash }

// CatalogHash returns the immutable catalog fingerprint.
func (s *RuleSet) CatalogHash() string { return s.hash }

// RuleCount reports all upstream rules, including path-only rules.
func (s *RuleSet) RuleCount() int { return len(s.rules) }

// RedactText applies the complete bundled guard.
func RedactText(text string) (string, []string) { return bundled.RedactText(text) }

// ValidateContent rejects detected secrets without returning their values.
func ValidateContent(name string, data []byte) error { return bundled.ValidateContent(name, data) }
func (s *RuleSet) ValidateContent(name string, data []byte) error {
	if s == nil || len(s.rules) == 0 {
		return errors.New("secret guard: missing rule catalog")
	}
	for _, r := range s.rules {
		if r.expression == nil && r.path.MatchString(name) {
			return errors.New("secret guard: sensitive file content refused")
		}
	}
	_, found := s.RedactText(string(data))
	if len(found) > 0 {
		return fmt.Errorf("secret guard: content contains credentials (%d detectors)", len(found))
	}
	return nil
}

// These private product prefixes are additive to every upstream catalog;
// Gitleaks cannot discover SageOx-issued formats from its public rule set.
var sageoxToken = regexp.MustCompile(`\box[a-z]_[A-Za-z0-9_\-]{8,}`)
var sageoxAPIKey = regexp.MustCompile(`\bmk_[A-Za-z0-9]{20,}`)

var envName = regexp.MustCompile(`(?i)(?:^|_)(?:key|token|secret|password|passwd|credential|credentials)(?:_|$)`)
var directEnv = regexp.MustCompile(`(?i)\b([A-Z][A-Z0-9_]*(?:KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIALS)[A-Z0-9_]*)\s*[:=]\s*('[^']*'|"[^"]*"|[^\s,;]+)`)
var helmEnv = regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*extraEnv\[[0-9]+\])\.(name|value)=('[^']*'|"[^"]*"|[^\s,]+)`)

const replacement = "[REDACTED_SECRET]"

// builtins run in this order, before the catalog: contextual environment values
// are captured before any standalone-token masking can erase a credential-shaped
// environment name. Each is screened like a catalog rule, by literals its own
// expression proves; unscreened, each would run its NFA across every byte of a
// multi-MB string (sageox/ox#1457).
var builtins = []struct {
	keywords []string
	redact   func(text string, found, values map[string]bool) string
}{
	{expressionKeywords(helmEnv), redactHelmEnv},
	{expressionKeywords(directEnv), redactDirectEnv},
	{expressionKeywords(sageoxToken), tokenRedactor(sageoxToken, "sageox-bearer-token")},
	{expressionKeywords(sageoxAPIKey), tokenRedactor(sageoxAPIKey, "sageox-api-key")},
}

// expressionKeywords is nil, meaning unscreened, when no literal is provable.
func expressionKeywords(expression *regexp.Regexp) []string {
	parsed, err := syntax.Parse(expression.String(), syntax.Perl)
	if err != nil {
		return nil
	}
	return requiredKeywords(parsed)
}

var redactionMarker = regexp.MustCompile(`^\[REDACTED(?:_[A-Z0-9_-]+)?\]$`)

func masked(v string) bool { return v == "" || redactionMarker.MatchString(strings.Trim(v, "\"'")) }

// envPlaceholder matches values that name where a credential goes and hold
// none: a words-only <slot>, or a ${NAME} / $NAME reference (unbraced only in
// the single-case shape upstream's own allowlist uses, so "$Passw0rd" stays a
// literal). Natural-language values such as "your-api-key" are not syntax and
// stay sensitive: a letters-only password is what this detector exists for.
var envPlaceholder = regexp.MustCompile(`^(?:<[A-Za-z]+(?:[ _.-][A-Za-z]+)*>|\$\{[A-Za-z_][A-Za-z0-9_]*\}|\$(?:[A-Z_]+|[a-z_]+))$`)

// unassigned reports a sensitive environment value that carries no credential.
func unassigned(v string) bool {
	return masked(v) || envPlaceholder.MatchString(strings.Trim(v, "\"'"))
}

func sensitiveName(n string) bool {
	return envName.MatchString(n)
}
func (s *RuleSet) RedactText(text string) (string, []string) {
	if s == nil || len(s.rules) == 0 {
		return "[REDACTED_INVALID_SECRET_GUARD]", []string{"invalid-secret-guard"}
	}
	found := map[string]bool{}
	out := s.redact(text, found, 0, nil)
	names := make([]string, 0, len(found))
	for n := range found {
		names = append(names, n)
	}
	sort.Strings(names)
	return out, names
}
func (s *RuleSet) redact(text string, found map[string]bool, depth int, values map[string]bool) string {
	// JSON decoding is necessary before scanning strings: escaped quotes and
	// Unicode escapes are data, not credential boundaries. Nested JSON is common
	// in adapter tool results. Beyond this limit refuse the field, never skip it.
	if depth > 64 {
		rememberValue(values, text)
		found["nested-content-limit"] = true
		return replacement
	}
	var value any
	d := json.NewDecoder(strings.NewReader(text))
	d.UseNumber()
	if d.Decode(&value) == nil {
		var extra any
		if d.Decode(&extra) == io.EOF {
			// A repeated key hides its earlier value from the decoded walk, and
			// parsers disagree on which one wins: refuse the field, never skip it.
			if duplicateKeys(text) {
				rememberValue(values, text)
				found["duplicate-json-key"] = true
				return replacement
			}
			before := len(found)
			changed := false
			value, changed = s.walk(value, found, depth+1, values)
			if changed || len(found) > before {
				b, err := json.Marshal(value)
				if err == nil {
					return string(b)
				}
				found["invalid-json"] = true
				return replacement
			}
			return text
		}
	}
	// JSONL must decode each line independently; otherwise a Unicode-escaped
	// value can evade scanning simply by appending a second session turn.
	if strings.Contains(text, "\n") {
		lines := strings.Split(text, "\n")
		changed := false
		for i, line := range lines {
			if jsonRecord(line) {
				out := s.redact(line, found, depth+1, values)
				changed = changed || out != line
				lines[i] = out
			}
		}

		if changed {
			text = strings.Join(lines, "\n")
		}
	}

	out := text
	lower := canonicalLower(out)
	for _, b := range builtins {
		if !screened(lower, b.keywords) {
			continue
		}
		// A replacement inserts marker text, so later screens must see it.
		if next := b.redact(out, found, values); next != out {
			out, lower = next, canonicalLower(next)
		}
	}

	// Syntax-proven mandatory keywords screen regex work; upstream hints screen
	// only rules with no such proof. Airtable demonstrates why unverified hints
	// cannot suppress a rule that is anchored on its own literal.
	for _, r := range s.rules {
		if r.expression == nil || !screened(lower, r.keywords) || !screened(lower, r.gate) {
			continue
		}
		indexes := r.expression.FindAllStringSubmatchIndex(out, -1)
		for i := len(indexes) - 1; i >= 0; i-- {
			m := indexes[i]
			a, b := m[0], m[1]
			if r.group > 0 {
				a, b = m[2*r.group], m[2*r.group+1]
			}
			if a < 0 || b <= a || masked(out[a:b]) || r.entropy > 0 && shannonEntropy(out[a:b]) < r.entropy {
				continue
			}
			if r.excused(out, m[0], m[1], out[a:b]) {
				continue
			}
			rememberValue(values, out[a:b])
			found[r.id] = true
			out = out[:a] + replacement + out[b:]
		}
	}
	return out
}

// redactHelmEnv pairs by the complete indexed Helm path, never by adjacency or
// provider prefix; opaque service keys are just as sensitive as recognizable tokens.
func redactHelmEnv(text string, found, values map[string]bool) string {
	names := map[string]bool{}
	for _, m := range helmEnv.FindAllStringSubmatch(text, -1) {
		if strings.EqualFold(m[2], "name") && sensitiveName(strings.Trim(m[3], "\"'")) {
			names[strings.ToLower(m[1])] = true
		}
	}
	return helmEnv.ReplaceAllStringFunc(text, func(m string) string {
		p := helmEnv.FindStringSubmatch(m)
		if strings.EqualFold(p[2], "value") && names[strings.ToLower(p[1])] && !unassigned(p[3]) {
			rememberValue(values, strings.Trim(p[3], "\"'"))
			found["sensitive-env-value"] = true
			return p[1] + ".value=" + replacement
		}
		return m
	})
}

// redactDirectEnv masks NAME=value pairs. Such a pair lives inside one JSON
// string, which the record pass has already scanned decoded. The raw record
// line only adds escapes glued to the value (\" or \n after [REDACTED_SECRET]),
// which would refuse at publication what capture accepted entry by entry.
func redactDirectEnv(text string, found, values map[string]bool) string {
	out := text
	envMatches := directEnv.FindAllStringSubmatchIndex(out, -1)
	for i := len(envMatches) - 1; i >= 0; i-- {
		m := envMatches[i]
		name, value := out[m[2]:m[3]], out[m[4]:m[5]]
		if !sensitiveName(name) || unassigned(value) || jsonRecord(matchLines(out, m[0], m[1])) {
			continue
		}
		rememberValue(values, strings.Trim(value, "\"'"))
		found["sensitive-env-value"] = true
		out = out[:m[0]] + name + "=" + replacement + out[m[1]:]
	}
	return out
}

// tokenRedactor masks every standalone match of a SageOx-issued format.
func tokenRedactor(expression *regexp.Regexp, finding string) func(string, map[string]bool, map[string]bool) string {
	return func(text string, found, values map[string]bool) string {
		return expression.ReplaceAllStringFunc(text, func(match string) string {
			rememberValue(values, match)
			found[finding] = true
			return replacement
		})
	}
}

func (s *RuleSet) walk(v any, found map[string]bool, depth int, values map[string]bool) (any, bool) {
	switch x := v.(type) {
	case string:
		out := s.redact(x, found, depth, values)
		return out, out != x
	case map[string]any:
		changed := false
		name, _ := x["name"].(string)
		if sensitiveName(name) {
			if val, ok := x["value"].(string); ok && !unassigned(val) {
				rememberValue(values, val)
				x["value"] = replacement
				found["sensitive-env-value"] = true
				changed = true
			}
		}
		for k, val := range x {
			sensitiveKey := sensitiveName(k)
			safeKey := s.redact(k, found, depth+1, values)
			if safeKey != k {
				delete(x, k)
				x[safeKey] = val
				k = safeKey
				changed = true
			}
			if sensitiveKey {
				if str, ok := val.(string); ok && !unassigned(str) {
					rememberValue(values, str)
					x[k] = replacement
					found["sensitive-env-value"] = true
					changed = true
					continue
				}
			}
			out, c := s.walk(val, found, depth+1, values)
			x[k] = out
			changed = changed || c
		}
		return x, changed
	case []any:
		changed := false
		for i, val := range x {
			out, c := s.walk(val, found, depth+1, values)
			x[i] = out
			changed = changed || c
		}
		return x, changed
	}
	return v, false
}

// excused applies a rule's own upstream filters to one finding: its keywords
// when the expression proved no literal, then its allowlists. The keyword
// fragment is the matched line, so a mention elsewhere in a long session
// cannot arm a bare-token rule. A JSON record line is left to its decoded
// strings, already scanned under the same filters, so publication of a JSONL
// file agrees with capture redacting it entry by entry.
func (r rule) excused(text string, start, end int, secret string) bool {
	if len(r.gate) == 0 && len(r.allowlists) == 0 {
		return false
	}
	line := matchLines(text, start, end)
	if len(r.gate) > 0 && (jsonRecord(line) || !screened(canonicalLower(line), r.gate)) {
		return true
	}
	match := strings.Trim(text[start:end], "\n")
	for _, a := range r.allowlists {
		if a.allows(secret, match, line) {
			return true
		}
	}
	return false
}

// matchLines returns the complete line or lines a match occupies, the unit
// upstream reports as a finding's line.
func matchLines(text string, start, end int) string {
	for end > start && text[end-1] == '\n' {
		end--
	}
	from := strings.LastIndexByte(text[:start], '\n') + 1
	to := len(text)
	if i := strings.IndexByte(text[end:], '\n'); i >= 0 {
		to = end + i
	}
	return text[from:to]
}

// duplicateKeys reports a JSON object, at any depth, that repeats a key.
// Keys compare decoded, so an escaped spelling is the same key.
func duplicateKeys(text string) bool {
	type frame struct {
		keys   map[string]bool // nil for arrays
		tokens int
	}
	var stack []*frame
	d := json.NewDecoder(strings.NewReader(text))
	for {
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, isDelim := token.(json.Delim)
		if isDelim && (delim == '}' || delim == ']') {
			stack = stack[:len(stack)-1]
			continue
		}
		if len(stack) > 0 {
			top := stack[len(stack)-1]
			if top.keys != nil && top.tokens%2 == 0 {
				key, _ := token.(string)
				if top.keys[key] {
					return true
				}
				top.keys[key] = true
			}
			top.tokens++
		}
		if isDelim {
			f := &frame{}
			if delim == '{' {
				f.keys = map[string]bool{}
			}
			stack = append(stack, f)
		}
	}
}

// jsonRecord reports a line the JSONL pass decodes and scans string by string.
func jsonRecord(line string) bool {
	trim := strings.TrimSpace(line)
	return (strings.HasPrefix(trim, "{") || strings.HasPrefix(trim, "[") || strings.HasPrefix(trim, "\"")) && json.Valid([]byte(trim))
}

// canonicalLower folds case for literal screens. Go's case folding also accepts
// long s for ASCII s, so screens canonicalize it explicitly.
func canonicalLower(text string) string {
	return strings.ReplaceAll(strings.ToLower(text), "ſ", "s")
}

// screened reports whether text can match: no screen, or a screening literal appears.
func screened(lower string, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}
	for _, k := range keywords {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

// requiredKeywords returns an OR-set containing a literal on every accepting
// regex path, or nil when none is provable. Optional branches never qualify.
// Only ASCII literals screen work; Go's case folding also accepts long s for
// ASCII s, which the input pre-screen canonicalizes explicitly.
func requiredKeywords(expression *syntax.Regexp) []string {
	switch expression.Op {
	case syntax.OpLiteral:
		text := strings.ToLower(string(expression.Rune))
		if len(text) < 2 || strings.TrimSpace(text) == "" {
			return nil
		}
		for _, r := range text {
			if r > 127 {
				return nil
			}
		}
		return []string{text}
	case syntax.OpConcat:
		var best []string
		score := 0
		for _, child := range expression.Sub {
			candidate := requiredKeywords(child)
			if len(candidate) == 0 {
				continue
			}
			minimum := len(candidate[0])
			for _, keyword := range candidate {
				if len(keyword) < minimum {
					minimum = len(keyword)
				}
			}
			if minimum > score || minimum == score && len(candidate) < len(best) {
				best = candidate
				score = minimum
			}
		}
		return best
	case syntax.OpAlternate:
		var all []string
		for _, child := range expression.Sub {
			candidate := requiredKeywords(child)
			if len(candidate) == 0 {
				return nil
			}
			all = append(all, candidate...)
		}
		return all
	case syntax.OpCapture, syntax.OpPlus:
		return requiredKeywords(expression.Sub[0])
	case syntax.OpRepeat:
		if expression.Min > 0 {
			return requiredKeywords(expression.Sub[0])
		}
	}
	return nil
}

func shannonEntropy(text string) float64 {
	if len(text) == 0 {
		return 0
	}
	counts := map[rune]int{}
	n := 0
	for _, c := range text {
		counts[c]++
		n++
	}
	entropy := 0.0
	for _, count := range counts {
		p := float64(count) / float64(n)
		entropy -= p * math.Log2(p)
	}
	return entropy
}

func rememberValue(values map[string]bool, value string) {
	if values != nil && value != "" {
		values[value] = true
	}
}

// TextReplacement can only remove an original span and emit a fixed marker.
// It cannot disable detection, supply arbitrary replacement text, or expose
// matched values. Unprovable coverage falls back to the full mandatory result.
type TextReplacement struct {
	Start, End int
	Marker     string
}

// RedactTextWithReplacements preserves existing trusted rendering when every
// detected original value is wholly removed. The original is always scanned
// first; partial matches and encoded values never receive an assumed proof.
func RedactTextWithReplacements(text string, edits []TextReplacement) (string, []string, bool) {
	found := map[string]bool{}
	values := map[string]bool{}
	mandatory := bundled.redact(text, found, 0, values)
	fallback := func() (string, []string, bool) {
		names := make([]string, 0, len(found))
		for name := range found {
			names = append(names, name)
		}
		sort.Strings(names)
		return mandatory, names, false
	}
	if len(edits) == 0 {
		return fallback()
	}
	edits = append([]TextReplacement(nil), edits...)
	sort.Slice(edits, func(i, j int) bool { return edits[i].Start < edits[j].Start })
	previous := 0
	for _, edit := range edits {
		if edit.Start < previous || edit.Start < 0 || edit.End <= edit.Start || edit.End > len(text) || !IsFixedRedactionMarker(edit.Marker) {
			return fallback()
		}
		previous = edit.End
	}
	for value := range values {
		occurrences := 0
		for offset := 0; offset < len(text); {
			i := strings.Index(text[offset:], value)
			if i < 0 {
				break
			}
			start := offset + i
			end := start + len(value)
			occurrences++
			covered := false
			for _, edit := range edits {
				if edit.Start <= start && edit.End >= end {
					covered = true
					break
				}
			}
			if !covered {
				return fallback()
			}
			offset = end
		}
		if occurrences == 0 {
			return fallback()
		}
	}
	out := text
	for i := len(edits) - 1; i >= 0; i-- {
		edit := edits[i]
		out = out[:edit.Start] + edit.Marker + out[edit.End:]
	}
	out, names := RedactText(out)
	return out, names, true
}

// IsFixedRedactionMarker reports the sole replacement grammar accepted by the
// guarded rendering API; arbitrary replacement text is never a removal proof.
func IsFixedRedactionMarker(marker string) bool {
	if redactionMarker.MatchString(marker) {
		return true
	}
	// These are existing fixed protocol renderings, not caller-defined prefixes.
	// No arbitrary string may be prepended to a marker and treated as a proof.
	switch marker {
	case "Authorization: Bearer [REDACTED_BEARER_TOKEN]", "Authorization: Basic [REDACTED_BASIC_AUTH]", "sox_share_session=[REDACTED_SOX_SHARE_SESSION]", "Ocp-Apim-Subscription-Key: [REDACTED_AZURE_SUBSCRIPTION_KEY]":
		return true
	}
	return false
}
