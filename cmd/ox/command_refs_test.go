package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ox must never tell an AI coworker, or a person, to run a command that fails.
// Hint strings, next-action lists, error remedies and shipped skill markdown
// all name `ox ...` commands. Nothing tied those strings to the command tree,
// so renames and removals left them behind: a replay of a month of real
// friction found `ox ledger sync`, `ox conventions`, `ox redaction verify`,
// bare `ox prime` and others in shipped output, each of which fails when
// followed.
//
// This test parses every non-test Go file under cmd/ and internal/, scans the
// STRING LITERALS (not comments), plus shipped skill and guide markdown, for
// `ox <words> [--flags]` references, and resolves each against the real command
// tree with every feature-gated command registered.

// commandRefSource is one scanned file: its text and how to turn an offset
// into a line number.
type commandRefSource struct {
	file string // slash-separated, relative to the repository root
	line int    // first source line of the text
	text string // one string literal or one markdown line
	// sourceNewlines is true when a newline in text is a newline in the source
	// (a raw string). An interpreted string's \n escapes stay on one line.
	sourceNewlines bool
}

// commandRef is one `ox ...` reference found in a source.
type commandRef struct {
	file   string
	line   int
	tokens []string // cleaned words after "ox"
	// quoted: opened by a quote, backtick, colon, "$", or a verb like "run", so
	// the author is plainly presenting a command
	quoted bool
	// lineStart: begins the text or a line. Prose messages start this way too
	// ("ox hooks not installed"), so it counts only when the tokens read as
	// command syntax (see commandSyntax)
	lineStart bool
}

func (r commandRef) text() string { return "ox " + strings.Join(r.tokens, " ") }

// commandSyntax reports whether the words after an unknown token read like a
// command line rather than a sentence: nothing follows it, or what follows is
// flags, flag values, and placeholders. A bare lowercase word with no flag
// before it ("is disabled", "renderer") is prose. A placeholder anywhere in
// the reference ("ox agent <id> ...") is itself proof of command syntax.
func commandSyntax(all, tail []string) bool {
	for _, tok := range all {
		if isPlaceholderToken(tok) {
			return true
		}
	}
	afterFlag := false
	for _, tok := range tail {
		switch {
		case strings.HasPrefix(tok, "-"):
			afterFlag = !strings.Contains(tok, "=")
		case isPlaceholderToken(tok), afterFlag:
			afterFlag = false
		default:
			return false
		}
	}
	return true
}

func isPlaceholderToken(tok string) bool {
	return strings.HasPrefix(tok, "<") || strings.HasPrefix(tok, "%") || strings.HasPrefix(tok, "$")
}

// presentedAsCommand decides whether an unresolved word is a defect or just
// English that happens to follow "ox <command>".
func (r commandRef) presentedAsCommand(tail []string) bool {
	return r.quoted || r.lineStart && commandSyntax(r.tokens, tail)
}

// commandRefAllowlist names references that deliberately do not resolve. Every
// entry carries its reason. An entry that matches nothing fails the test, so
// the list cannot rot, except entries marked pending, which wait on another
// slice of the same change.
var commandRefAllowlist = []commandRefAllowEntry{
	// Legacy matchers: these literals are compared against content that older
	// ox versions wrote into user files, so they must keep the old spelling.
	{file: "cmd/ox/doctor_hooks.go", ref: "ox prime", reason: "legacy hook command, matched and rewritten to 'ox agent prime' by the doctor"},
	{file: "cmd/ox/doctor_integration.go", ref: "ox prime", reason: "legacy marker text matched in user files, plus the 'Global ox prime' check name that doctor_integration_user_test.go asserts; neither is advice to run"},
	{file: "cmd/ox/init.go", ref: "ox prime", reason: "legacy marker text matched in user files, never shown as advice"},
	{file: "internal/uninstall/user_integrations.go", ref: "ox prime", reason: "legacy hook command matched so uninstall can remove it"},
	{file: "internal/uninstall/user_integrations.go", ref: "ox agent prime --user", reason: "legacy hook command matched so uninstall can remove it"},

	// Not commands.
	{file: "internal/plan/primary_html.go", ref: "ox plan markdown renderer", reason: "generator meta-tag value matched against stored plan HTML"},

	// Known gap, deliberately not fixed here: rewriting this README template
	// makes `ox doctor` regenerate .sageox/README.md in every initialized repo.
	// That fan-out is a separate decision from fixing command references.
	{file: "cmd/ox/init.go", ref: "ox agent <id> guidance", reason: "progressive-disclosure sketch in the generated .sageox/README.md; no such verb exists"},
}

type commandRefAllowEntry struct {
	file    string
	ref     string // matches the whole reference or a word-boundary prefix of it
	reason  string
	pending bool // waits on another slice of this change; may match nothing yet
}

func (e commandRefAllowEntry) matches(r commandRef) bool {
	if !strings.HasSuffix(r.file, e.file) {
		return false
	}
	text := r.text()
	return text == e.ref || strings.HasPrefix(text, e.ref+" ")
}

// retiredCommandRoots are first words that were documented or guessed at some
// point and never became (or no longer are) top-level commands. Without them a
// stale `ox ledger sync` is indistinguishable from prose like "ox does not".
var retiredCommandRoots = map[string]string{
	"ledger":      "no top-level ledger command; sync is 'ox sync', health is 'ox status'",
	"conventions": "replaced by 'ox agent team-ctx'",
	"redaction":   "lives under 'ox session redaction'",
	"policy":      "never shipped; redaction policy is 'ox session redaction policy'",
	"prime":       "bare prime is 'ox agent prime'; the friction catalog only redirects it",
}

// refChecker resolves references against a command tree and the agent
// dispatcher's table.
type refChecker struct {
	root         *cobra.Command
	agent        *cobra.Command
	verbs        map[string]bool // `ox agent [<id>] <verb>` handled by runWithAgentID
	sessionVerbs map[string]bool // `ox agent [<id>] session <verb>`
	firstWords   map[string]bool // first words that make a reference a command, not prose
}

var (
	commandWordPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	longFlagPattern    = regexp.MustCompile(`^--([a-z][a-z0-9-]*)(=.*)?$`)
	shortFlagPattern   = regexp.MustCompile(`^-([a-zA-Z])$`)
	oxWordPattern      = regexp.MustCompile(`(^|[^A-Za-z0-9_./$%@-])ox[ \t]+`)
	agentIDLikePattern = regexp.MustCompile(`^Ox[A-Za-z0-9]{3,8}$`)
)

// extractCommandRefs returns every `ox ...` reference in text.
func extractCommandRefs(text string) []commandRef {
	var refs []commandRef
	for _, loc := range oxWordPattern.FindAllStringSubmatchIndex(text, -1) {
		oxStart := loc[3] // group 1 is the preceding character; "ox" follows it
		segment := refSegment(text[loc[1]:])
		tokens := cleanRefTokens(segment)
		if len(tokens) == 0 {
			continue
		}
		quoted, lineStart := refContext(text[:oxStart])
		refs = append(refs, commandRef{
			line:      strings.Count(text[:oxStart], "\n"),
			tokens:    tokens,
			quoted:    quoted,
			lineStart: lineStart,
		})
	}
	return refs
}

// refSegment cuts text after "ox " at the first character that ends a command
// as written in prose: a closing quote, a pipe, a clause break, a newline, or
// the run of spaces that separates a help listing's command from its
// description.
func refSegment(s string) string {
	end := len(s)
	for i, r := range s {
		switch r {
		case '\n', '\'', '"', '`', '|', ';', '(', ')', '\\', '&', '\t':
			end = i
		case ' ':
			if strings.HasPrefix(s[i:], "  ") {
				end = i
			}
		}
		if end != len(s) {
			break
		}
	}
	s = s[:end]
	// a second reference on the same line starts its own segment
	if i := strings.Index(s, " ox "); i >= 0 {
		s = s[:i]
	}
	return s
}

// refStopWords end a reference: what follows is prose, not arguments.
var refStopWords = map[string]bool{
	"and": true, "or": true, "then": true, "but": true, "so": true,
	"—": true, "–": true, "-": true, "->": true, "=>": true, "→": true, "#": true,
}

func cleanRefTokens(segment string) []string {
	var tokens []string
	for _, raw := range strings.Fields(segment) {
		tok := strings.Trim(raw, "[]{}")
		last := false
		for strings.HasSuffix(tok, ".") || strings.HasSuffix(tok, ",") || strings.HasSuffix(tok, ":") ||
			strings.HasSuffix(tok, "!") || strings.HasSuffix(tok, "?") {
			tok = tok[:len(tok)-1]
			last = true
		}
		if tok == "" || refStopWords[tok] {
			break
		}
		tokens = append(tokens, tok)
		if last {
			break
		}
	}
	return tokens
}

var runVerbPattern = regexp.MustCompile(`(?i)(^|[\s:])(run|use|try|type|execute|invoke|call|rerun|re-run)$`)

// refContext classifies the text before "ox": see commandRef.quoted and
// commandRef.lineStart.
func refContext(before string) (quoted, lineStart bool) {
	trimmed := strings.TrimRight(before, " \t")
	if trimmed == "" || strings.HasSuffix(trimmed, "\n") {
		return false, true
	}
	switch trimmed[len(trimmed)-1] {
	case '\'', '"', '`', '(', ':', '$', '>':
		return true, false
	}
	return runVerbPattern.MatchString(trimmed), false
}

// check returns why the reference does not resolve, or nil.
func (c *refChecker) check(ref commandRef) []string {
	tokens := ref.tokens
	first := tokens[0]
	if !c.firstWords[first] {
		return nil // prose, not a command reference
	}
	if first == "help" || first == "completion" {
		return nil // cobra-provided; absent from the tree until first executed
	}

	cur := c.root
	var path []string
	i := 0
	for ; i < len(tokens); i++ {
		tok := tokens[i]
		if !commandWordPattern.MatchString(tok) {
			break
		}
		next := c.findChild(path, tok, cur)
		if next == nil {
			break
		}
		cur = next
		path = append(path, tok)
	}

	if i == 0 {
		// a retired name is wrong wherever it appears: even as a noun ("the ox
		// prime marker") it teaches the reader a command that fails. A name
		// known only from the friction catalog may be ordinary English
		// ("ox daemons running here"), so it must be presented as a command.
		if _, retired := retiredCommandRoots[first]; retired || ref.presentedAsCommand(tokens[1:]) {
			return []string{fmt.Sprintf("%q is not an ox command", first)}
		}
		return nil
	}
	// a token after `ox agent` that is not a cobra subcommand is served by the
	// custom dispatcher, whose verbs are not in the command tree
	if cur == c.agent {
		return c.checkAgentDispatch(ref, tokens[i:])
	}
	var problems []string
	if i < len(tokens) && !cur.Runnable() && commandWordPattern.MatchString(tokens[i]) &&
		ref.presentedAsCommand(tokens[i+1:]) {
		problems = append(problems, fmt.Sprintf("%q is not a subcommand of %q", tokens[i], cur.CommandPath()))
	}
	problems = append(problems, c.checkFlags(cur, tokens[i:])...)
	return problems
}

// findChild resolves tok as a subcommand of cur with the same rules cobra uses
// when executing.
func (c *refChecker) findChild(path []string, tok string, cur *cobra.Command) *cobra.Command {
	found, rest, err := c.root.Find(append(append([]string{}, path...), tok))
	if err != nil || len(rest) > 0 || found == nil || found == cur {
		return nil
	}
	return found
}

func (c *refChecker) checkFlags(cmd *cobra.Command, tokens []string) []string {
	if cmd.DisableFlagParsing {
		return nil
	}
	var problems []string
	for _, tok := range tokens {
		if tok == "--" {
			break
		}
		if m := longFlagPattern.FindStringSubmatch(tok); m != nil {
			if !commandHasFlag(cmd, m[1], false) {
				problems = append(problems, fmt.Sprintf("flag --%s is not defined on %q", m[1], cmd.CommandPath()))
			}
		} else if m := shortFlagPattern.FindStringSubmatch(tok); m != nil {
			if !commandHasFlag(cmd, m[1], true) {
				problems = append(problems, fmt.Sprintf("flag -%s is not defined on %q", m[1], cmd.CommandPath()))
			}
		}
	}
	return problems
}

func commandHasFlag(cmd *cobra.Command, name string, short bool) bool {
	if !short && (name == "help" || name == "version") || short && name == "h" {
		return true // cobra adds these lazily
	}
	for _, set := range []*pflag.FlagSet{cmd.LocalFlags(), cmd.InheritedFlags()} {
		if short && set.ShorthandLookup(name) != nil || !short && set.Lookup(name) != nil {
			return true
		}
	}
	return false
}

// checkAgentDispatch validates what follows `ox agent` when it is not a cobra
// subcommand: `[<id>] <verb> [<session verb>]` plus flags, against the table
// runWithAgentID switches on.
func (c *refChecker) checkAgentDispatch(ref commandRef, rest []string) []string {
	if len(rest) == 0 {
		return nil
	}
	i := 0
	switch first := rest[0]; {
	case c.verbs[first]:
		// implicit-ID form: ox agent session start
	case agentIDPlaceholder(first):
		i = 1
	case commandWordPattern.MatchString(first):
		if ref.presentedAsCommand(rest[1:]) {
			return []string{fmt.Sprintf("%q is neither an 'ox agent' subcommand nor an agent-ID verb", first)}
		}
		return nil
	default:
		return nil
	}
	if i >= len(rest) {
		return nil
	}
	var problems []string
	verb := rest[i]
	if commandWordPattern.MatchString(verb) {
		if !c.verbs[verb] {
			if ref.presentedAsCommand(rest[i+1:]) {
				return []string{fmt.Sprintf("%q is not an agent verb (have: %s)", verb, sortedVerbList(c.verbs))}
			}
			return nil
		}
		if verb == "session" && i+1 < len(rest) && commandWordPattern.MatchString(rest[i+1]) &&
			!c.sessionVerbs[rest[i+1]] && ref.presentedAsCommand(rest[i+2:]) {
			problems = append(problems, fmt.Sprintf("%q is not a session verb (have: %s)", rest[i+1], sortedVerbList(c.sessionVerbs)))
		}
	}
	return append(problems, c.checkFlags(c.agent, rest[i:])...)
}

// agentIDPlaceholder reports whether tok stands for an agent ID: a real ID, a
// <placeholder>, a %s verb, or a shell variable.
func agentIDPlaceholder(tok string) bool {
	if agentinstance.IsValidAgentID(tok) || agentIDLikePattern.MatchString(tok) {
		return true
	}
	return strings.HasPrefix(tok, "<") || strings.HasPrefix(tok, "%") || strings.HasPrefix(tok, "$")
}

func sortedVerbList(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// dispatcherTable reads the verbs runWithAgentID routes by parsing its switch
// statements, so the table can never drift from the code that serves it.
func dispatcherTable(t *testing.T) (verbs, sessionVerbs map[string]bool) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), repoPath("agent.go"), nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	verbs, sessionVerbs = map[string]bool{}, map[string]bool{}
	caseStrings := func(clause *ast.CaseClause) []string {
		var out []string
		for _, expr := range clause.List {
			if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					out = append(out, s)
				}
			}
		}
		return out
	}
	switchOn := func(sw *ast.SwitchStmt, name string) bool {
		id, ok := sw.Tag.(*ast.Ident)
		return ok && id.Name == name
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "runWithAgentID" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			switch {
			case switchOn(sw, "subcommand"):
				for _, stmt := range sw.Body.List {
					for _, s := range caseStrings(stmt.(*ast.CaseClause)) {
						verbs[s] = true
					}
				}
			case switchOn(sw, "sessionCmd"):
				for _, stmt := range sw.Body.List {
					for _, s := range caseStrings(stmt.(*ast.CaseClause)) {
						sessionVerbs[s] = true
					}
				}
			}
			return true
		})
	}
	require.NotEmpty(t, verbs, "could not find the agent dispatcher's verb switch in agent.go")
	require.NotEmpty(t, sessionVerbs, "could not find the agent dispatcher's session switch in agent.go")
	// the removed verb still has a case that explains it
	delete(sessionVerbs, "html")
	return verbs, sessionVerbs
}

// catalogFirstWords returns the first word of every plain pattern in the
// friction catalog: the invalid commands people and AI coworkers actually type.
func catalogFirstWords(t *testing.T) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(repoPath("default_catalog.json"))
	require.NoError(t, err)
	var catalog struct {
		Commands []struct {
			Pattern  string `json:"pattern"`
			HasRegex bool   `json:"has_regex"`
		} `json:"commands"`
	}
	require.NoError(t, json.Unmarshal(data, &catalog))
	words := map[string]bool{}
	for _, entry := range catalog.Commands {
		if entry.HasRegex {
			continue
		}
		if fields := strings.Fields(entry.Pattern); len(fields) > 0 && commandWordPattern.MatchString(fields[0]) {
			words[fields[0]] = true
		}
	}
	return words
}

// allFeatureCommandsRegistered registers every feature-gated command for the
// duration of the test, so references to them resolve.
func allFeatureCommandsRegistered(t *testing.T) {
	t.Helper()
	type gate struct{ parent, command *cobra.Command }
	gates := []gate{
		{rootCmd, scoutCmd}, {rootCmd, bulletinCmd}, {rootCmd, cartsCmd},
		{rootCmd, memoryCmd}, {sessionCmd, sessionTraceCmd},
	}
	for _, g := range gates {
		was := commandRegistered(g.parent, g.command)
		setCommandRegistered(g.parent, g.command, true)
		t.Cleanup(func() { setCommandRegistered(g.parent, g.command, was) })
	}
}

func newRealRefChecker(t *testing.T) *refChecker {
	t.Helper()
	allFeatureCommandsRegistered(t)
	verbs, sessionVerbs := dispatcherTable(t)

	firstWords := map[string]bool{}
	for _, child := range rootCmd.Commands() {
		firstWords[child.Name()] = true
		for _, alias := range child.Aliases {
			firstWords[alias] = true
		}
	}
	for word := range catalogFirstWords(t) {
		firstWords[word] = true
	}
	for word := range retiredCommandRoots {
		firstWords[word] = true
	}
	return &refChecker{root: rootCmd, agent: agentCmd, verbs: verbs, sessionVerbs: sessionVerbs, firstWords: firstWords}
}

// collectCommandRefSources gathers every string literal in non-test Go files
// under cmd/ and internal/ and every line of shipped markdown.
func collectCommandRefSources(t *testing.T, repoRoot string) []commandRefSource {
	t.Helper()
	var sources []commandRefSource
	skipDir := map[string]bool{"testdata": true, "vendor": true, ".git": true, "node_modules": true}

	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if skipDir[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(repoRoot, path)
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return fmt.Errorf("parse %s: %w", rel, err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				sources = append(sources, commandRefSource{
					file:           filepath.ToSlash(rel),
					line:           fset.Position(lit.Pos()).Line,
					text:           value,
					sourceNewlines: strings.HasPrefix(lit.Value, "`"),
				})
				return true
			})
			return nil
		})
		require.NoError(t, err)
	}

	// shipped markdown is scanned line by line
	for _, dir := range []string{"claude-plugin/skills", "claude-plugin/commands"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".md") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(repoRoot, path)
			for i, line := range strings.Split(string(data), "\n") {
				sources = append(sources, commandRefSource{file: filepath.ToSlash(rel), line: i + 1, text: line})
			}
			return nil
		})
		require.NoError(t, err)
	}
	return sources
}

// refsInSources turns sources into references with file:line positions.
func refsInSources(sources []commandRefSource) []commandRef {
	var refs []commandRef
	for _, src := range sources {
		for _, ref := range extractCommandRefs(src.text) {
			ref.file = src.file
			if src.sourceNewlines {
				ref.line += src.line
			} else {
				ref.line = src.line
			}
			refs = append(refs, ref)
		}
	}
	return refs
}

// unresolvedRefs returns one message per reference that does not resolve and is
// not allowlisted, plus the allowlist entries nothing matched.
func unresolvedRefs(refs []commandRef, checker *refChecker, allow []commandRefAllowEntry) (failures, unused []string) {
	used := make([]bool, len(allow))
	for _, ref := range refs {
		problems := checker.check(ref)
		if len(problems) == 0 {
			continue
		}
		allowed := false
		for i, entry := range allow {
			if entry.matches(ref) {
				used[i], allowed = true, true
			}
		}
		if !allowed {
			failures = append(failures, fmt.Sprintf("%s:%d: %q: %s", ref.file, ref.line, ref.text(), strings.Join(problems, "; ")))
		}
	}
	for i, entry := range allow {
		if !used[i] && !entry.pending {
			unused = append(unused, fmt.Sprintf("%s %q (%s)", entry.file, entry.ref, entry.reason))
		}
	}
	sort.Strings(failures)
	return failures, unused
}

// TestCommandRefs_ShippedTextNamesRealCommands is the gate: every `ox ...`
// reference in shipped strings must resolve against the real command tree.
//
// Failure prevented: ox's own output tells a coworker to run a command or pass
// a flag that does not exist, so the very next step of its guidance fails.
func TestCommandRefs_ShippedTextNamesRealCommands(t *testing.T) {
	checker := newRealRefChecker(t)
	repoRoot := filepath.Join(packageDir, "..", "..")
	refs := refsInSources(collectCommandRefSources(t, repoRoot))
	require.Greater(t, len(refs), 300, "scan found suspiciously few references; the extractor is broken")

	failures, unused := unresolvedRefs(refs, checker, commandRefAllowlist)
	if len(failures) > 0 {
		t.Errorf("%d shipped string(s) name an ox command or flag that does not exist.\n"+
			"Fix the string to name a real command. If the text is deliberately not a command, add it\n"+
			"to commandRefAllowlist in command_refs_test.go with a one-line reason.\n\n%s",
			len(failures), strings.Join(failures, "\n"))
	}
	if len(unused) > 0 {
		t.Errorf("allowlist entries matched nothing; delete them:\n%s", strings.Join(unused, "\n"))
	}
}

// fakeRefTree is a small command tree shaped like ox's: a group with children,
// a leaf with flags, and an agent dispatcher with cobra subcommands.
func fakeRefTree() *refChecker {
	root := &cobra.Command{Use: "ox"}
	root.PersistentFlags().Bool("json", false, "")

	doctor := &cobra.Command{Use: "doctor", Run: func(*cobra.Command, []string) {}}
	doctor.Flags().Bool("fix", false, "")
	doctor.Flags().BoolP("yes", "y", false, "")

	group := &cobra.Command{Use: "plan"} // non-runnable group
	list := &cobra.Command{Use: "list", Run: func(*cobra.Command, []string) {}}
	list.Flags().Int("limit", 0, "")
	group.AddCommand(list)

	agent := &cobra.Command{Use: "agent", Args: cobra.ArbitraryArgs, Run: func(*cobra.Command, []string) {}}
	agent.PersistentFlags().String("role", "", "")
	agent.AddCommand(&cobra.Command{Use: "prime", Run: func(*cobra.Command, []string) {}})

	passthrough := &cobra.Command{Use: "run", DisableFlagParsing: true, Run: func(*cobra.Command, []string) {}}
	root.AddCommand(doctor, group, agent, passthrough)

	return &refChecker{
		root: root, agent: agent,
		verbs:        map[string]bool{"session": true, "heartbeat": true},
		sessionVerbs: map[string]bool{"start": true, "log": true},
		firstWords: map[string]bool{
			"doctor": true, "plan": true, "agent": true, "run": true,
			"ledger": true, "prime": true, "help": true, "daemons": true,
		},
	}
}

// TestCommandRefs_CheckerRejectsWhatItShould proves the checker can fail. A
// gate that has only ever been green is untested: each row here is a reference
// shape the real scan relies on being caught or ignored.
func TestCommandRefs_CheckerRejectsWhatItShould(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string // substrings of problems, in order; nil = resolves
	}{
		{"real command and flag", "Run 'ox doctor --fix' to repair", nil},
		{"short flag", "`ox doctor -y`", nil},
		{"inherited flag", "ox plan list --json", nil},
		{"local flag on nested command", "ox plan list --limit 5", nil},
		{"unknown flag", "Run 'ox doctor --run-autofix-now'", []string{"flag --run-autofix-now is not defined"}},
		{"unknown short flag", "`ox doctor -z`", []string{"flag -z is not defined"}},
		{"flag of a sibling command", "ox plan list --fix", []string{"flag --fix is not defined"}},
		{"retired command root", "Pull may be needed. Run: ox ledger sync", []string{`"ledger" is not an ox command`}},
		{"retired root in a command", "`ox prime`", []string{`"prime" is not an ox command`}},
		{"retired root as a noun is still wrong", "AGENTS.md does not contain ox prime marker.", []string{`"prime" is not an ox command`}},
		{"catalog-only name in prose is not flagged", "List all running ox daemons", nil},
		{"catalog-only name presented as a command", "Run 'ox daemons' to see them", []string{`"daemons" is not an ox command`}},
		{"line-start prose with a plain tail", "ox plan markdown renderer", nil},
		{"line-start prose after a real command", "ox doctor is a command", nil},
		{"help listing row with a description column", "  ox plan frobnicate        Do the thing", []string{`"frobnicate" is not a subcommand`}},
		{"help listing row that resolves", "  ox plan list              List plans", nil},
		{"unknown subcommand of a group", "Use 'ox plan frobnicate' to", []string{`"frobnicate" is not a subcommand of "ox plan"`}},
		{"prose is not a command", "ox does not support that", nil},
		{"prose with a capital", "SageOx tracks sessions; ox is a CLI", nil},
		{"positional arguments", "ox plan list <slug> extra", nil},
		{"unknown word after a leaf is an argument", "ox doctor now", nil},
		{"pass-through flags are not checked", "`ox run --anything`", nil},
		{"agent id placeholder", "ox agent <id> session log --role user", nil},
		{"agent percent-s placeholder", "ox agent %s session start", nil},
		{"agent real id", "ox agent Oxa7b3 heartbeat", nil},
		{"agent implicit id", "ox agent session start", nil},
		{"agent cobra subcommand", "ox agent prime", nil},
		{"agent unknown verb", "ox agent <id> frobnicate", []string{`"frobnicate" is not an agent verb`}},
		{"agent unknown session verb", "ox agent <id> session frobnicate", []string{`"frobnicate" is not a session verb`}},
		{"agent unknown flag", "ox agent <id> session log --bogus x", []string{"flag --bogus is not defined"}},
		{"agent unknown first word", "ox agent frobnicate", []string{`"frobnicate" is neither`}},
		{"quote ends the reference", `ox doctor --fix "--bogus"`, nil},
		{"clause break ends the reference", "Run ox doctor, then git push --force", nil},
		{"second reference on the line", "ox doctor --fix or ox plan list --bogus", []string{"flag --bogus is not defined"}},
		{"bracketed optional flag", "`ox plan list [--limit 5]`", nil},
		{"no scan inside identifiers", "see ox-hash and box doctor --bogus and tox plan", nil},
	}
	checker := fakeRefTree()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var problems []string
			for _, ref := range extractCommandRefs(tt.text) {
				problems = append(problems, checker.check(ref)...)
			}
			if tt.want == nil {
				assert.Empty(t, problems)
				return
			}
			require.Len(t, problems, len(tt.want), "problems: %v", problems)
			for i, want := range tt.want {
				assert.Contains(t, problems[i], want)
			}
		})
	}
}

// TestCommandRefs_ReportsPositionsAndHonorsAllowlist checks the plumbing the
// failure message depends on: file:line positions for raw and interpreted
// literals, allowlist matching, and rejection of an allowlist that went stale.
func TestCommandRefs_ReportsPositionsAndHonorsAllowlist(t *testing.T) {
	checker := fakeRefTree()
	sources := []commandRefSource{
		{file: "cmd/ox/raw.go", line: 10, text: "first line\nsecond line\nRun: ox ledger sync", sourceNewlines: true},
		{file: "cmd/ox/interp.go", line: 20, text: "a\nb\nRun: ox ledger sync"},
		{file: "claude-plugin/skills/x/SKILL.md", line: 7, text: "| `ox doctor --nope` | x |"},
	}
	refs := refsInSources(sources)
	require.Len(t, refs, 3)

	failures, unused := unresolvedRefs(refs, checker, nil)
	require.Len(t, failures, 3)
	assert.Contains(t, failures[0], "claude-plugin/skills/x/SKILL.md:7:")
	assert.Contains(t, failures[1], "cmd/ox/interp.go:20:")
	assert.Contains(t, failures[2], "cmd/ox/raw.go:12:")
	assert.Empty(t, unused)

	allow := []commandRefAllowEntry{
		{file: "cmd/ox/raw.go", ref: "ox ledger sync", reason: "test"},
		{file: "cmd/ox/never.go", ref: "ox gone", reason: "matches nothing"},
		{file: "cmd/ox/later.go", ref: "ox gone", reason: "waits on a sibling slice", pending: true},
	}
	failures, unused = unresolvedRefs(refs, checker, allow)
	assert.Len(t, failures, 2, "the allowlisted reference must drop out")
	require.Len(t, unused, 1, "a stale allowlist entry must be reported; a pending one must not")
	assert.Contains(t, unused[0], "cmd/ox/never.go")
}

// TestCommandRefs_ScanReachesRealStrings guards against the scan silently
// matching nothing: known shipped strings must be found at their positions.
func TestCommandRefs_ScanReachesRealStrings(t *testing.T) {
	repoRoot := filepath.Join(packageDir, "..", "..")
	refs := refsInSources(collectCommandRefSources(t, repoRoot))

	found := map[string]bool{}
	for _, ref := range refs {
		found[ref.file+"|"+ref.text()] = true
	}
	for _, want := range []string{
		"claude-plugin/skills/ox/SKILL.md|ox agent team-ctx",
		"cmd/ox/session_helpers.go|ox session redaction verify",
		"internal/daemon/status_display.go|ox sync",
		// the guidance that sent AI coworkers to a rejected command
		"cmd/ox/agent_session.go|ox agent %s session log --role user --content",
		"cmd/ox/agent_session.go|ox agent %s session record",
	} {
		assert.True(t, found[want], "scan did not find %q", want)
	}
}
