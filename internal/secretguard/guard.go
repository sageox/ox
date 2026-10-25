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
// All upstream regex/path rules and entropy thresholds are loaded. Upstream
// repository allowlists are not suppressors for captured tool output. Explicit
// sensitive environment context is always redacted, independent of entropy.
type RuleSet struct {
	rules []rule
	hash  string
}
type rule struct {
	id               string
	expression, path *regexp.Regexp
	keywords         []string
	group            int
	entropy          float64
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
		for _, k := range cfg.Keywords {
			if k != "" {
				r.keywords = append(r.keywords, strings.ToLower(k))
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
		}

		s.rules = append(s.rules, r)
	}
	h := sha256.Sum256(data)
	s.hash = hex.EncodeToString(h[:])
	return s, nil
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

var redactionMarker = regexp.MustCompile(`^\[REDACTED(?:_[A-Z0-9_-]+)?\]$`)

func masked(v string) bool { return v == "" || redactionMarker.MatchString(strings.Trim(v, "\"'")) }

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
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "{") || strings.HasPrefix(trim, "[") || strings.HasPrefix(trim, "\"") {
				var v any
				if json.Unmarshal([]byte(trim), &v) == nil {
					out := s.redact(line, found, depth+1, values)
					changed = changed || out != line
					lines[i] = out
				}
			}
		}

		if changed {
			text = strings.Join(lines, "\n")
		}
	}

	out := text
	// Pair by the complete indexed Helm path, never by adjacency or provider
	// prefix; opaque service keys are just as sensitive as recognizable tokens.
	names := map[string]bool{}
	for _, m := range helmEnv.FindAllStringSubmatch(out, -1) {
		if strings.EqualFold(m[2], "name") && sensitiveName(strings.Trim(m[3], "\"'")) {
			names[strings.ToLower(m[1])] = true
		}
	}
	out = helmEnv.ReplaceAllStringFunc(out, func(m string) string {
		p := helmEnv.FindStringSubmatch(m)
		if strings.EqualFold(p[2], "value") && names[strings.ToLower(p[1])] && !masked(p[3]) {
			rememberValue(values, strings.Trim(p[3], "\"'"))
			found["sensitive-env-value"] = true
			return p[1] + ".value=" + replacement
		}
		return m
	})
	out = directEnv.ReplaceAllStringFunc(out, func(m string) string {
		p := directEnv.FindStringSubmatch(m)
		if sensitiveName(p[1]) && !masked(p[2]) {
			rememberValue(values, strings.Trim(p[2], "\"'"))
			found["sensitive-env-value"] = true
			return p[1] + "=" + replacement
		}
		return m
	})
	// Capture contextual values before any standalone-token masking can erase a
	// credential-shaped environment name. Both the name and value must be safe.

	for _, local := range []struct {
		expression *regexp.Regexp
		name       string
	}{{sageoxToken, "sageox-bearer-token"}, {sageoxAPIKey, "sageox-api-key"}} {
		out = local.expression.ReplaceAllStringFunc(out, func(match string) string { rememberValue(values, match); found[local.name] = true; return replacement })
	}

	// Only syntax-proven mandatory keywords may screen regex work. Airtable
	// demonstrates why unverified upstream hints cannot suppress scans.
	lower := strings.ReplaceAll(strings.ToLower(out), "ſ", "s")
	for _, r := range s.rules {
		if r.expression == nil {
			continue
		}
		if len(r.keywords) > 0 {
			matched := false
			for _, k := range r.keywords {
				if strings.Contains(lower, k) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		indexes := r.expression.FindAllStringSubmatchIndex(out, -1)
		for i := len(indexes) - 1; i >= 0; i-- {
			a, b := indexes[i][0], indexes[i][1]
			if r.group > 0 {
				a, b = indexes[i][2*r.group], indexes[i][2*r.group+1]
			}
			if a < 0 || b <= a || masked(out[a:b]) || r.entropy > 0 && shannonEntropy(out[a:b]) < r.entropy {
				continue
			}
			rememberValue(values, out[a:b])
			found[r.id] = true
			out = out[:a] + replacement + out[b:]
		}
	}
	return out
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
			if val, ok := x["value"].(string); ok && !masked(val) {
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
				if str, ok := val.(string); ok && !masked(str) {
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
