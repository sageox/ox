package teamdocs

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// ScopeKind says what a rule's frontmatter declares about where it applies.
type ScopeKind int

const (
	// ScopeInvalid is the zero value on purpose. "Everywhere" must be something a
	// parse concludes, never what an unset Scope happens to read as.
	ScopeInvalid ScopeKind = iota
	// ScopeUnscoped means the frontmatter was read and limits nothing.
	ScopeUnscoped
	// ScopeScoped means at least one repo or glob limits the rule.
	ScopeScoped
)

// Scope is a rule's reach as its frontmatter declares it. Invalid exists because
// an empty repos: or globs: means "everywhere": any form the reader could not
// understand used to read as empty, and so widened the rule without a word.
type Scope struct {
	Kind   ScopeKind
	Repos  []string
	Globs  []string
	Reason string // why Kind is ScopeInvalid; bounded, safe to show a person
}

// maxScopeReasonRunes bounds a reason because parser errors quote team-authored
// text, and each reason is paid in every session until someone fixes the source.
const maxScopeReasonRunes = 120

func invalidScope(format string, args ...any) Scope {
	reason := fmt.Sprintf(format, args...)
	if utf8.RuneCountInString(reason) > maxScopeReasonRunes {
		reason = string([]rune(reason)[:maxScopeReasonRunes]) + "…"
	}
	return Scope{Kind: ScopeInvalid, Reason: reason}
}

// ParseRuleScope reads a Team Rule's scope from its full text.
func ParseRuleScope(manifest string) Scope {
	return ruleScope(parseManifest(manifest))
}

// ruleScope adds the check only rules need. A rule copied from .claude/rules keeps
// its paths: key, which ox does not read, so it would quietly apply everywhere. A
// skill may carry paths: legitimately, because ox installs the manifest verbatim
// and the agent reads that key itself.
func ruleScope(fm ruleFrontmatter) Scope {
	if fm.Scope.Kind != ScopeInvalid && fm.foreignScopeKey != "" {
		return invalidScope("uses %s:, which ox does not read; write globs: or repos:", fm.foreignScopeKey)
	}
	return fm.Scope
}

// foreignScopeKeys are the spellings other tools use for a rule's scope. An author
// who wrote one meant to limit the rule, so reading it as absent is the same silent
// widening as failing to parse it.
var foreignScopeKeys = map[string]bool{
	"paths": true, "path": true, "glob": true, "repo": true,
	"applyto": true, "filematchpattern": true,
}

// publicationKeys decide whether a manifest is published at all.
var publicationKeys = map[string]bool{"status": true, "audience": true, "visibility": true}

// gatingKey reports whether a key decides who receives a manifest. A value under
// one that cannot be read, or that appears twice, withholds the manifest instead of
// defaulting to "publish".
func gatingKey(key string) bool {
	return key == "repos" || key == "globs" || publicationKeys[key]
}

type frontmatterState int

const (
	frontmatterAbsent frontmatterState = iota
	frontmatterClosed
	frontmatterUnclosed
)

// splitFrontmatter returns the text between the fences, using the same fence rule
// as ManifestBody so the two can never disagree about where a rule's body starts.
func splitFrontmatter(text string) (string, frontmatterState) {
	first, rest, found := strings.Cut(trimBOM(text), "\n")
	if !IsFrontmatterFence(first) {
		return "", frontmatterAbsent
	}
	if !found {
		return "", frontmatterUnclosed
	}
	for offset := 0; offset <= len(rest); {
		line, _, _ := strings.Cut(rest[offset:], "\n")
		if IsFrontmatterFence(line) {
			return rest[:offset], frontmatterClosed
		}
		offset += len(line) + 1
	}
	return rest, frontmatterUnclosed
}

// scopeKeyLine finds a scope key with a value on its own line at any indentation.
var scopeKeyLine = regexp.MustCompile(`(?im)^[ \t]*["']?(repos|globs)["']?[ \t]*:[ \t]*[^\s#]`)

// yamlLineBreaks spells out every break YAML honors. Anything reading this text by line
// has to agree with the parser, or a key after a bare carriage return is one line to
// the reader and its own key to YAML.
var yamlLineBreaks = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\u0085", "\n", "\u2028", "\n", "\u2029", "\n")

func mentionsScopeKey(block string) bool {
	return scopeKeyLine.MatchString(yamlLineBreaks.Replace(block))
}

// parseManifest reads one rule or skill manifest. It never fails: what it cannot
// read comes back as an Invalid scope, which withholds the manifest.
func parseManifest(text string) ruleFrontmatter {
	block, state := splitFrontmatter(text)
	switch state {
	case frontmatterAbsent:
		return ruleFrontmatter{Scope: scopeWithoutFrontmatter(text)}
	case frontmatterUnclosed:
		fm := ruleFrontmatter{Scope: invalidScope("frontmatter has no closing ---")}
		fm.Requires, fm.RequiresDeclared = requiresFromLines(block)
		return fm
	}

	root, repaired, err := decodeFrontmatter(block)
	if err != nil {
		fm := ruleFrontmatter{Scope: invalidScope("frontmatter is not valid YAML: %v", err)}
		fm.Requires, fm.RequiresDeclared = requiresFromLines(block)
		return fm
	}
	fm := readFrontmatter(root)
	// A repair quotes single lines, so it cannot see a scope key sitting inside a
	// multi-line value. Concluding "everywhere" needs a parse nothing had to help.
	if repaired && fm.Scope.Kind == ScopeUnscoped && mentionsScopeKey(block) {
		fm.Scope = invalidScope("frontmatter is not valid YAML and mentions a scope key ox could not read")
	}
	return fm
}

// scopeWithoutFrontmatter treats a file with no frontmatter as unscoped, unless it
// plainly tried to have some. A stray blank line above the fence hides the block
// from every reader, including the agent, and the author's repos: with it.
func scopeWithoutFrontmatter(text string) Scope {
	trimmed := strings.TrimLeft(yamlLineBreaks.Replace(trimBOM(text)), " \t\n")
	first, rest, _ := strings.Cut(trimmed, "\n")
	if strings.TrimSpace(first) == "---" {
		if block, _, closed := strings.Cut(rest, "\n---"); closed && mentionsScopeKey(block) {
			return invalidScope("frontmatter must start on the first line of the file and use LF or CRLF line endings")
		}
	}
	return Scope{Kind: ScopeUnscoped}
}

// maxRepairableFrontmatter bounds the line-by-line retry, which parses every line again
// on the session-start path. Real frontmatter is a few hundred bytes, and a block past
// this that YAML also rejects is withheld.
const maxRepairableFrontmatter = 64 * 1024

// decodeFrontmatter parses the block with a real YAML parser. Only when that fails
// does it retry with repairFrontmatter, and repaired says so. The result is the
// document's root node, or nil for a block that holds no content.
func decodeFrontmatter(block string) (root *yaml.Node, repaired bool, err error) {
	root, err = decodeYAMLDocument(block)
	if err == nil {
		return root, false, nil
	}
	if len(block) > maxRepairableFrontmatter {
		return nil, false, err
	}
	fixed := repairFrontmatter(block)
	if fixed == block {
		return nil, false, err
	}
	root, retryErr := decodeYAMLDocument(fixed)
	if retryErr != nil {
		return nil, false, err // the author's own line numbers, not the repaired text's
	}
	return root, true, nil
}

// decodeYAMLDocument refuses a second document. A fence with a trailing comment is
// not a fence to IsFrontmatterFence but is a document separator to YAML, and every
// key below it would otherwise vanish from a first-document-only read.
//
// The recover is here because this runs on team-authored text on the session-start
// path, where prime must never fail over a rule file, and the parser signals
// internal errors by panicking.
func decodeYAMLDocument(block string) (root *yaml.Node, err error) {
	defer func() {
		if r := recover(); r != nil {
			root, err = nil, fmt.Errorf("parser failed: %v", r)
		}
	}()
	decoder := yaml.NewDecoder(strings.NewReader(block))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	var next yaml.Node
	if err := decoder.Decode(&next); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("more than one YAML document")
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	return doc.Content[0], nil
}

// repairableValue splits `key: value` for the keys a repair may quote.
var repairableValue = regexp.MustCompile(`^([A-Za-z_][\w.-]*)([ \t]*:[ \t]+)(.+)$`)

// repairFrontmatter quotes the values YAML rejects and teams write all the time: a
// description holding a colon, and a bare glob, which YAML reads as an alias. The
// bare comma form is one ox documents, because that is how a rule looks when copied
// out of .cursor/rules. Refusing these would withhold rules that carry no scope
// problem at all.
//
// A repair can only turn a value into a string, and a string scope narrows a rule.
// So it cannot widen one, and whatever it cannot fix stays Invalid.
func repairFrontmatter(block string) string {
	lines := foldFreeTextValues(strings.Split(yamlLineBreaks.Replace(block), "\n"))
	for i, line := range lines {
		lines[i] = repairLine(line)
	}
	return strings.Join(lines, "\n")
}

// foldFreeTextValues joins a value with the indented lines below it, the way YAML
// folds a multi-line plain scalar. A description that wraps onto a line holding a
// colon is the case: "and also: Y" reads as a stray mapping, and quoting only the
// first line leaves a quoted scalar with text after it.
func foldFreeTextValues(lines []string) []string {
	folded := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if below := foldableLinesBelow(line, lines[i+1:]); below != nil {
			parts := []string{strings.TrimRight(line, " \t")}
			for _, next := range below {
				if text := strings.TrimSpace(next); text != "" {
					parts = append(parts, text)
				}
			}
			line = strings.Join(parts, " ")
			i += len(below)
		}
		folded = append(folded, line)
	}
	return folded
}

// topLevelKey splits a line that starts a top-level key. Being anchored at a letter is
// what keeps the fold linear: only these lines ever scan the indented run below them.
var topLevelKey = regexp.MustCompile(`^([A-Za-z_][\w.-]*)[ \t]*:(?:[ \t]+(.*))?$`)

// foldableLinesBelow returns the lines to fold into line, or nil to leave it alone.
//
// A key that limits delivery never folds, and neither does a value below which one
// appears: swallowed into a description, that line would quietly drop a limit the
// author wrote, so the block stays Invalid and the warning says so. Block scalars and
// anchors already mean something across lines, and an anchor's list must survive for
// the alias that reads it.
func foldableLinesBelow(line string, rest []string) []string {
	match := topLevelKey.FindStringSubmatch(line)
	if match == nil || limitsDelivery(strings.ToLower(match[1])) {
		return nil
	}
	value := strings.TrimSpace(match[2])
	if value != "" && strings.ContainsRune("|>&*![{#", rune(value[0])) {
		return nil
	}
	end := 0
	for i, next := range rest {
		if strings.TrimSpace(next) == "" {
			continue // blank lines inside a plain scalar fold too, but never trail one
		}
		if next[0] != ' ' && next[0] != '\t' {
			break
		}
		if namesDeliveryKey(next) {
			return nil
		}
		end = i + 1
	}
	if end == 0 {
		return nil
	}
	below := rest[:end]
	// A key with nothing after it usually holds a nested value, which is left alone
	// unless YAML cannot read it as one.
	if value == "" && yamlLineParses(strings.Join(below, "\n")) {
		return nil
	}
	return below
}

// limitsDelivery reports whether a key decides who receives a manifest, or was
// plainly meant to.
func limitsDelivery(key string) bool {
	return gatingKey(key) || foreignScopeKeys[key] || key == "requires"
}

// namesDeliveryKey reports whether an indented line reads as such a key, in any
// spelling a mistake could give it.
func namesDeliveryKey(line string) bool {
	key, _, found := strings.Cut(strings.TrimLeft(line, " \t-"), ":")
	return found && limitsDelivery(strings.ToLower(strings.Trim(key, "\"' \t")))
}

func repairLine(line string) string {
	body := strings.TrimLeft(line, " \t")
	// YAML forbids tab indentation, and tabs change nothing about what was meant.
	indent := strings.ReplaceAll(line[:len(line)-len(body)], "\t", "  ")
	if body == "" || body[0] == '#' {
		return indent + body
	}
	var prefix, value string
	scopeValue := true
	if rest, isItem := strings.CutPrefix(body, "- "); isItem {
		prefix, value = "- ", strings.TrimLeft(rest, " ")
	} else if match := repairableValue.FindStringSubmatch(body); match != nil {
		key := strings.ToLower(match[1])
		// A repaired scope can only narrow, but `status: *draft` read as a string
		// would publish a draft. That one stays broken and withholds the manifest.
		if publicationKeys[key] {
			return indent + body
		}
		scopeValue = key == "repos" || key == "globs" || key == "requires"
		prefix, value = match[1]+match[2], match[3]
	} else if indent != "" {
		value = body
	} else {
		return indent + body
	}
	// An opening bracket under a scope key is a flow list. Quoting one would turn a
	// list that never closed into a pattern, hiding the mistake the warning reports.
	// Elsewhere it is ordinary text: `argument-hint: [owner/repo] [--max N]`.
	if value == "" || (scopeValue && value[0] == '[') || yamlLineParses(prefix+value) {
		return indent + body
	}
	// Only a plain trailing comment comes off. A leading quote or bracket says nothing
	// here, because YAML just refused to read the value that way, and trusting it would
	// keep `"Use" this` as "Use" and lose the rest.
	clean, _, _ := strings.Cut(value, " #")
	clean = strings.TrimSpace(clean)
	return indent + prefix + "'" + strings.ReplaceAll(clean, "'", "''") + "'"
}

func yamlLineParses(line string) bool {
	var node yaml.Node
	return yaml.Unmarshal([]byte(line), &node) == nil
}

// resolveAlias follows one YAML alias. Anchors cannot chain to another alias, so one
// step is all there is, and walking only what a key needs means an alias bomb is
// never expanded.
func resolveAlias(node *yaml.Node) *yaml.Node {
	if node != nil && node.Kind == yaml.AliasNode && node.Alias != nil {
		return node.Alias
	}
	return node
}

func isNullNode(node *yaml.Node) bool {
	return node.Kind == yaml.ScalarNode && node.ShortTag() == "!!null"
}

// readFrontmatter turns the parsed block into the fields ox acts on.
func readFrontmatter(root *yaml.Node) ruleFrontmatter {
	unscoped := ruleFrontmatter{Scope: Scope{Kind: ScopeUnscoped}}
	root = resolveAlias(root)
	if root == nil || isNullNode(root) {
		return unscoped
	}
	if root.Kind != yaml.MappingNode {
		return ruleFrontmatter{Scope: invalidScope("frontmatter is not a YAML mapping")}
	}

	fm := unscoped
	var repos, globs []string
	seen := make(map[string]bool, len(root.Content)/2)
	// Every key is read even after one proves unreadable. requires: has to survive a
	// withheld scope, because `ox skills publish` asks about it directly and an absent
	// answer reads as "runs anywhere". The warning names the first problem.
	var problem error
	note := func(err error) {
		if problem == nil {
			problem = err
		}
	}

	for i := 0; i+1 < len(root.Content); i += 2 {
		keyNode, value := root.Content[i], resolveAlias(root.Content[i+1])
		// A merge or complex key can hold repos: where no key lookup would see it.
		if keyNode.Kind != yaml.ScalarNode || keyNode.ShortTag() == "!!merge" {
			note(errors.New("frontmatter uses a YAML merge or a non-scalar key"))
			continue
		}
		key := keyNode.Value
		// Stray quotes belong with wrong case: a near miss on a key that limits who
		// gets this rule is a mistake to report, not a different key to ignore.
		lower := strings.ToLower(strings.Trim(key, "\"' "))
		if key != lower && gatingKey(lower) {
			note(fmt.Errorf("%s: must be written in lowercase, with no stray quotes", lower))
			continue
		}
		if foreignScopeKeys[lower] {
			fm.foreignScopeKey = key
			continue
		}
		if gatingKey(key) && seen[key] {
			note(fmt.Errorf("%s: is declared twice", key))
			continue
		}
		seen[key] = true

		var err error
		switch key {
		case "repos":
			repos, err = scopeList(key, value, singleScopeValue)
		case "globs":
			globs, err = scopeList(key, value, parseGlobs)
		case "requires":
			fm.Requires, fm.RequiresDeclared = requiresFromNode(value)
		case "status":
			fm.Status, err = gatingScalar(key, value)
		case "audience":
			fm.Audience, err = gatingScalar(key, value)
		case "visibility":
			fm.Visibility, err = gatingScalar(key, value)
		case "name":
			fm.Name = scalarText(value)
		case "description":
			fm.Description = scalarText(value)
		case "valid-through":
			fm.ValidThrough = scalarText(value)
		case "from-discussion":
			fm.FromDiscussion = scalarText(value)
		}
		if err != nil {
			note(err)
		}
	}

	switch {
	case problem != nil:
		fm.Scope = invalidScope("%v", problem)
	case len(repos) > 0 || len(globs) > 0:
		fm.Scope = Scope{Kind: ScopeScoped, Repos: repos, Globs: globs}
	}
	return fm
}

func scalarText(node *yaml.Node) string {
	if node.Kind != yaml.ScalarNode || isNullNode(node) {
		return ""
	}
	return strings.TrimSpace(node.Value)
}

func gatingScalar(key string, node *yaml.Node) (string, error) {
	if node.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("%s: must be a single value", key)
	}
	return scalarText(node), nil
}

func singleScopeValue(value string) []string {
	return []string{value}
}

// scopeList reads repos: or globs:. Null and empty mean "no limit", the documented
// meaning. A value that holds text but yields no entry does not: the author limited
// something that cannot be read, and dropping it would apply the rule everywhere.
func scopeList(key string, node *yaml.Node, fromScalar func(string) []string) ([]string, error) {
	switch node.Kind {
	case yaml.ScalarNode:
		text := scalarText(node)
		if text == "" {
			return nil, nil
		}
		values := fromScalar(text)
		if len(values) == 0 {
			return nil, fmt.Errorf("%s: holds no usable value", key)
		}
		return values, nil
	case yaml.SequenceNode:
		values := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			item = resolveAlias(item)
			if item.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("%s: holds an entry that is not a single value", key)
			}
			if text := scalarText(item); text != "" {
				values = append(values, text)
			}
		}
		if len(values) == 0 {
			return nil, nil
		}
		return values, nil
	default:
		return nil, fmt.Errorf("%s: must be a list or a single value", key)
	}
}

// requiresFromNode reads requires: with the semantics SkillRequirements documents.
// An explicit [] is a declaration ("runs anywhere"), and a key with nothing under it
// is not, because reading it as "needs nothing" would make a skill that runs
// commands look portable. Anything else that holds text is a declaration, so a value
// nobody recognizes keeps the skill to coding agents rather than passing it as
// portable.
func requiresFromNode(node *yaml.Node) (values []string, declared bool) {
	switch node.Kind {
	case yaml.ScalarNode:
		if isNullNode(node) {
			return nil, false
		}
		if text := scalarText(node); text != "" {
			return []string{text}, true
		}
		return nil, true
	case yaml.SequenceNode:
		if len(node.Content) == 0 {
			return nil, true
		}
		for _, item := range node.Content {
			item = resolveAlias(item)
			switch {
			case item.Kind != yaml.ScalarNode:
				values = append(values, "unreadable")
			case !isNullNode(item) && scalarText(item) != "":
				values = append(values, scalarText(item))
			}
		}
		return values, len(values) > 0
	default:
		return []string{"unreadable"}, true
	}
}

// requiresFromLines reads requires: from text YAML rejected. A broken line elsewhere
// in the block must not erase a declaration: a skill whose author said it needs a
// shell would then be offered to hosts that have none.
func requiresFromLines(block string) (values []string, declared bool) {
	inBlockList := false
	for line := range strings.SplitSeq(strings.ReplaceAll(block, "\r\n", "\n"), "\n") {
		if inBlockList {
			if item, isItem := blockListItem(line); isItem {
				if item != "" {
					values = append(values, item)
					declared = true
				}
				continue
			}
			inBlockList = false
		}
		rest, isRequires := strings.CutPrefix(line, "requires:")
		if !isRequires {
			continue
		}
		value := stripYAMLComment(strings.TrimSpace(rest))
		if value == "" {
			inBlockList = true
			continue
		}
		declared = true
		values = parseInlineList(value)
	}
	return values, declared
}
