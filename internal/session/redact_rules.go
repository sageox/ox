package session

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/paths"
)

// RedactRuleSource identifies where a redaction rule originated.
type RedactRuleSource string

const (
	RuleSourceBuiltin RedactRuleSource = "builtin"
	RuleSourceTeam    RedactRuleSource = "team"
	RuleSourceRepo    RedactRuleSource = "repo"
	RuleSourceUser    RedactRuleSource = "user"
)

// RedactRule is a single parsed rule from a REDACT.md file.
type RedactRule struct {
	Type        string           // "literal" or "regex"
	RawPattern  string           // original text from the file
	Replacement string           // e.g. "[REDACTED_CODENAME]"
	Source      RedactRuleSource // team, repo, or user
	SourcePath  string           // absolute path to the REDACT.md file
	LineNumber  int              // line number in the source file
}

// ParsedRedactFile holds all rules from a single REDACT.md file.
type ParsedRedactFile struct {
	Path   string
	Source RedactRuleSource
	Rules  []RedactRule
	Errors []RedactParseError
}

// RedactParseError describes a parse error in a REDACT.md file.
type RedactParseError struct {
	Path    string
	Line    int
	Message string
}

func (e RedactParseError) Error() string {
	return fmt.Sprintf("%s:%d: %s", e.Path, e.Line, e.Message)
}

// ParseRedactFile reads and parses a REDACT.md file.
// Returns nil, nil if the file does not exist.
// The parser is lenient: it collects errors per-line and continues parsing.
func ParseRedactFile(path string, source RedactRuleSource) (*ParsedRedactFile, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	result := &ParsedRedactFile{
		Path:   path,
		Source: source,
	}

	scanner := bufio.NewScanner(f)
	inRedactBlock := false
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// detect fenced code block boundaries
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inRedactBlock {
				// closing fence
				inRedactBlock = false
				continue
			}
			// check for opening ```redact fence
			tag := strings.TrimPrefix(trimmed, "```")
			if strings.TrimSpace(tag) == "redact" {
				inRedactBlock = true
				continue
			}
			continue
		}

		if !inRedactBlock {
			continue
		}

		// inside a redact block: parse rules
		// skip blank lines and comments
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		rule, parseErr := parseRuleLine(trimmed, lineNum, path, source)
		if parseErr != nil {
			result.Errors = append(result.Errors, *parseErr)
			continue
		}
		result.Rules = append(result.Rules, *rule)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	return result, nil
}

// parseRuleLine parses a single rule line inside a redact block.
// Expected formats:
//
//	literal "TEXT" -> [REPLACEMENT]
//	regex "PATTERN" -> [REPLACEMENT]
func parseRuleLine(line string, lineNum int, path string, source RedactRuleSource) (*RedactRule, *RedactParseError) {
	makeErr := func(msg string) *RedactParseError {
		return &RedactParseError{Path: path, Line: lineNum, Message: msg}
	}

	// split on "->" to get left and right sides
	parts := strings.SplitN(line, "->", 2)
	if len(parts) != 2 {
		return nil, makeErr("missing '->' separator")
	}

	left := strings.TrimSpace(parts[0])
	replacement := strings.TrimSpace(parts[1])

	if replacement == "" {
		return nil, makeErr("missing replacement token after '->'")
	}

	// parse left side: type "pattern"
	var ruleType, pattern string

	if strings.HasPrefix(left, "literal ") {
		ruleType = "literal"
		pattern = strings.TrimSpace(strings.TrimPrefix(left, "literal "))
	} else if strings.HasPrefix(left, "regex ") {
		ruleType = "regex"
		pattern = strings.TrimSpace(strings.TrimPrefix(left, "regex "))
	} else {
		return nil, makeErr("rule must start with 'literal' or 'regex'")
	}

	// strip surrounding quotes from pattern
	pattern = stripQuotes(pattern)
	if pattern == "" {
		return nil, makeErr("empty pattern")
	}

	return &RedactRule{
		Type:        ruleType,
		RawPattern:  pattern,
		Replacement: replacement,
		Source:      source,
		SourcePath:  path,
		LineNumber:  lineNum,
	}, nil
}

// stripQuotes removes surrounding double quotes from a string.
func stripQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// RulesToPatterns converts parsed RedactRules to SecretPatterns.
// For literal rules, the pattern is escaped via regexp.QuoteMeta.
// Invalid regex rules produce errors but don't stop conversion.
func RulesToPatterns(rules []RedactRule) ([]SecretPattern, []RedactParseError) {
	var patterns []SecretPattern
	var errors []RedactParseError

	for _, rule := range rules {
		var re *regexp.Regexp
		var err error

		switch rule.Type {
		case "literal":
			re, err = regexp.Compile(regexp.QuoteMeta(rule.RawPattern))
		case "regex":
			re, err = regexp.Compile(rule.RawPattern)
		default:
			errors = append(errors, RedactParseError{
				Path:    rule.SourcePath,
				Line:    rule.LineNumber,
				Message: fmt.Sprintf("unknown rule type: %s", rule.Type),
			})
			continue
		}

		if err != nil {
			errors = append(errors, RedactParseError{
				Path:    rule.SourcePath,
				Line:    rule.LineNumber,
				Message: fmt.Sprintf("invalid regex: %v", err),
			})
			continue
		}

		// generate a name from source + line number
		name := fmt.Sprintf("custom_%s_L%d", rule.Source, rule.LineNumber)

		patterns = append(patterns, SecretPattern{
			Name:    name,
			Pattern: re,
			Redact:  rule.Replacement,
			Source:  rule.Source,
		})
	}

	return patterns, errors
}

// RedactSourceInfo describes a REDACT.md source with its resolved path and rules.
type RedactSourceInfo struct {
	Source  RedactRuleSource
	Path    string // empty if file not found
	Rules   []RedactRule
	Errors  []RedactParseError
	IOError error // non-nil if file could not be read (permission denied, etc.)
}

// LoadCustomPatterns discovers and loads REDACT.md files from all custom levels
// (team, repo, user), merges them in order, and returns compiled patterns.
// Returns patterns plus any parse errors encountered.
func LoadCustomPatterns(projectRoot string) ([]SecretPattern, []RedactParseError) {
	sources := DiscoverRedactSources(projectRoot)

	var allRules []RedactRule
	var allErrors []RedactParseError

	for _, src := range sources {
		allRules = append(allRules, src.Rules...)
		allErrors = append(allErrors, src.Errors...)
	}

	if len(allRules) == 0 {
		return nil, allErrors
	}

	patterns, compileErrors := RulesToPatterns(allRules)
	allErrors = append(allErrors, compileErrors...)

	return patterns, allErrors
}

// DiscoverRedactSources finds REDACT.md files at all levels and parses them.
// Returns source info for each level (even if the file doesn't exist, for reporting).
func DiscoverRedactSources(projectRoot string) []RedactSourceInfo {
	var sources []RedactSourceInfo

	// 1. team-level: <team_context>/docs/REDACT.md
	teamSource := RedactSourceInfo{Source: RuleSourceTeam}
	if projectRoot != "" {
		if tc := config.FindRepoTeamContext(projectRoot); tc != nil && tc.Path != "" {
			teamPath := filepath.Join(tc.Path, "docs", "REDACT.md")
			teamSource.Path = teamPath
			parsed, err := ParseRedactFile(teamPath, RuleSourceTeam)
			if err != nil {
				teamSource.IOError = err
			} else if parsed != nil {
				teamSource.Rules = parsed.Rules
				teamSource.Errors = parsed.Errors
			}
		}
	}
	sources = append(sources, teamSource)

	// 2. repo-level: <project>/.sageox/REDACT.md
	repoSource := RedactSourceInfo{Source: RuleSourceRepo}
	if projectRoot != "" {
		repoPath := filepath.Join(projectRoot, ".sageox", "REDACT.md")
		repoSource.Path = repoPath
		parsed, err := ParseRedactFile(repoPath, RuleSourceRepo)
		if err != nil {
			repoSource.IOError = err
		} else if parsed != nil {
			repoSource.Rules = parsed.Rules
			repoSource.Errors = parsed.Errors
		}
	}
	sources = append(sources, repoSource)

	// 3. user-level: ~/.config/sageox/REDACT.md
	userSource := RedactSourceInfo{Source: RuleSourceUser}
	userPath := filepath.Join(paths.ConfigDir(), "REDACT.md")
	userSource.Path = userPath
	parsed, err := ParseRedactFile(userPath, RuleSourceUser)
	if err != nil {
		userSource.IOError = err
	} else if parsed != nil {
		userSource.Rules = parsed.Rules
		userSource.Errors = parsed.Errors
	}
	sources = append(sources, userSource)

	return sources
}

// NewRedactorWithCustomRules creates a Redactor with default patterns
// plus any user-defined patterns from REDACT.md files.
// Custom patterns are always additive -- built-in patterns cannot be removed.
// Parse errors are returned for logging/display but do not prevent redactor creation.
func NewRedactorWithCustomRules(projectRoot string) (*Redactor, []RedactParseError) {
	patterns := DefaultPatterns()
	customPatterns, parseErrors := LoadCustomPatterns(projectRoot)
	patterns = append(patterns, customPatterns...)
	return &Redactor{patterns: patterns}, parseErrors
}
