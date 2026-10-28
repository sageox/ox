// Package oxactivity classifies an AI coworker's tool calls and keeps the
// per-session counts the ox status line shows.
//
//   - Classify maps one harness tool call (a shell command or a SageOx MCP
//     tool) to an Activity: a label such as "pulled plan", its command words,
//     its kind, and — for item reads — the item's ID.
//   - State and RecordInfluence count the distinct discussions, plans, docs,
//     sessions and murmurs that influenced one harness session.
//   - Render draws the status line — "● ox  3 discussions · 1 plan" — with or
//     without color, fitted to a width.
//
// The package does no I/O beyond the per-session state file and never decides
// whether the line is enabled; callers gate on config.UserConfig's
// IsStatuslineEnabled.
package oxactivity

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/session/contextuse"
)

// Kind groups an activity by what it did with team knowledge.
type Kind string

const (
	// KindRetrieval pulls team knowledge into the AI coworker's context:
	// ox query, code search, reading sessions/plans/murmurs/discussions.
	KindRetrieval Kind = "retrieval"
	// KindWrite records something: posting a murmur, saving a plan,
	// reporting context use, scoring a session.
	KindWrite Kind = "write"
	// KindLifecycle starts, stops, or primes the AI coworker's session.
	KindLifecycle Kind = "lifecycle"
	// KindOther is any other ox call (doctor, status, ...).
	KindOther Kind = "other"
)

// Activity is one coworker-visible thing ox did for an AI coworker.
type Activity struct {
	// Label is the user-facing wording, lower case, past tense:
	// "loaded team context", "posted murmur", "ran ox doctor".
	Label string
	// Command is the matched ox subcommand words, e.g. "murmur list" or
	// "agent prime" (an `ox agent <id>` address is dropped). It is built only
	// from fixed command words — never from arguments, so a posted murmur is
	// "murmur" and its body never appears. SageOx MCP tools read "mcp <Tool>".
	Command string
	// Kind classifies the call; see the Kind constants.
	Kind Kind
	// Query is the search subject of `ox query` and `ox code search`: the
	// first positional argument, capped at MaxQueryLen. Empty for every other
	// call, including MCP tools (their arguments are not inspected).
	Query string
	// Ref is the item a read pulled — the plan slug of `ox plan view <slug>`,
	// the conversation ID of `ox conversation show <id>` — so a counter can
	// tell one item read twice from two items. It is only ever an ID or slug
	// (see itemRef), never free text, and empty for every other call.
	Ref string
}

// MaxQueryLen caps Activity.Query so a receipt stays short.
const MaxQueryLen = 200

// shellToolNames are the tool names harnesses use for "run a shell command".
// Claude Code and Codex hooks both report "Bash"; Codex's own transcript names
// the call "exec_command" (input field "cmd"); its older function tool is
// "shell"/"local_shell" (input field "command" as an argv array); Cursor
// reports "Shell".
var shellToolNames = map[string]bool{
	"Bash":            true,
	"bash":            true,
	"Shell":           true,
	"shell":           true,
	"local_shell":     true,
	"exec_command":    true,
	"run_command":     true,
	"execute_command": true,
}

// Classify maps a harness tool call to the activity a coworker should see.
// It reports false for anything that is not ox doing visible work: non-ox
// shell commands, ox plumbing (heartbeats, hook dispatch), non-SageOx MCP
// tools, and input it cannot parse.
func Classify(toolName string, toolInput json.RawMessage) (Activity, bool) {
	if isSageoxMCPTool(toolName) {
		return classifyMCP(toolName)
	}
	if !shellToolNames[toolName] {
		return Activity{}, false
	}
	cmd, ok := shellCommand(toolInput)
	if !ok {
		return Activity{}, false
	}
	return ClassifyCommand(cmd)
}

// ClassifyCommand classifies a raw shell command line. Leading `cd <dir> &&`
// segments and `VAR=value` assignments are skipped, and pipes and redirections
// are ignored, so `cd repo && OX_X=1 ox query "auth" 2>&1 | head` classifies
// as "searched team memory". The first remaining command must be ox.
func ClassifyCommand(command string) (Activity, bool) {
	words, ok := oxArgs(command)
	if !ok {
		return Activity{}, false
	}
	return classifyOxArgs(words)
}

// --- shell input ------------------------------------------------------------

// shellCommand extracts the command line from a shell tool's input. It accepts
// {"command": "..."}, {"cmd": "..."}, and Codex's argv form
// {"command": ["bash", "-lc", "..."]}.
func shellCommand(input json.RawMessage) (string, bool) {
	var fields struct {
		Command json.RawMessage `json:"command"`
		Cmd     string          `json:"cmd"`
	}
	if err := json.Unmarshal(input, &fields); err != nil {
		return "", false
	}
	if len(fields.Command) > 0 {
		var s string
		if err := json.Unmarshal(fields.Command, &s); err == nil && s != "" {
			return s, true
		}
		var argv []string
		if err := json.Unmarshal(fields.Command, &argv); err == nil && len(argv) > 0 {
			return argvCommand(argv), true
		}
	}
	if fields.Cmd != "" {
		return fields.Cmd, true
	}
	return "", false
}

// argvCommand unwraps `bash -lc "<script>"` style argv into the script, and
// otherwise joins argv words (none of which need re-quoting for classification).
func argvCommand(argv []string) string {
	if len(argv) >= 3 {
		switch path.Base(argv[0]) {
		case "bash", "sh", "zsh", "dash":
			if strings.HasPrefix(argv[1], "-") && strings.Contains(argv[1], "c") {
				return argv[2]
			}
		}
	}
	return strings.Join(argv, " ")
}

// oxArgs finds the first real command in a shell line and, when it is ox,
// returns the words after "ox".
func oxArgs(command string) ([]string, bool) {
	for _, seg := range splitCommands(command) {
		seg = stripAssignments(seg)
		if len(seg) == 0 {
			continue
		}
		switch seg[0] {
		case "cd", "pushd":
			continue
		}
		if path.Base(seg[0]) != "ox" {
			return nil, false
		}
		return seg[1:], true
	}
	return nil, false
}

var assignmentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// stripAssignments drops leading `VAR=value` words and an `env` prefix.
func stripAssignments(words []string) []string {
	for len(words) > 0 && (assignmentRe.MatchString(words[0]) || words[0] == "env") {
		words = words[1:]
	}
	return words
}

// splitCommands tokenizes a shell line into simple commands. Quotes and
// backslash escapes are honored; `&&`, `||`, `;`, `&` and newlines separate
// commands; everything after a `|` in a command is dropped (pipe tail); and
// redirections (`>`, `>>`, `<`, `2>&1`, `&>`) are removed with their targets.
// It is a classifier, not a shell: subshells and substitutions stay literal.
func splitCommands(line string) [][]string {
	var (
		cmds      [][]string
		cur       []string
		word      strings.Builder
		inWord    bool
		piped     bool // inside a pipe tail: drop words until the next separator
		skipNext  bool // next word is a redirection target
		runes     = []rune(line)
		emitWord  func()
		endSimple func()
	)
	emitWord = func() {
		if !inWord {
			return
		}
		w := word.String()
		word.Reset()
		inWord = false
		switch {
		case skipNext:
			skipNext = false
		case piped:
		default:
			cur = append(cur, w)
		}
	}
	endSimple = func() {
		emitWord()
		if len(cur) > 0 {
			cmds = append(cmds, cur)
		}
		cur = nil
		piped = false
		skipNext = false
	}
	redirect := func(i int) int {
		// A pending all-digit word is a file descriptor ("2>"), not an argument.
		if inWord && isDigits(word.String()) {
			word.Reset()
			inWord = false
		}
		emitWord()
		j := i + 1
		if j < len(runes) && runes[j] == runes[i] { // >> or <<
			j++
		}
		if j < len(runes) && runes[j] == '&' { // >&1
			j++
			for j < len(runes) && runes[j] >= '0' && runes[j] <= '9' {
				j++
			}
			return j - 1
		}
		skipNext = true
		return j - 1
	}

	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch r {
		case '\'':
			inWord = true
			for i++; i < len(runes) && runes[i] != '\''; i++ {
				word.WriteRune(runes[i])
			}
		case '"':
			inWord = true
			for i++; i < len(runes) && runes[i] != '"'; i++ {
				if runes[i] == '\\' && i+1 < len(runes) && strings.ContainsRune(`"\$`+"`", runes[i+1]) {
					i++
				}
				word.WriteRune(runes[i])
			}
		case '\\':
			if i+1 < len(runes) {
				i++
				if runes[i] == '\n' { // line continuation joins, it is not a word
					continue
				}
				inWord = true
				word.WriteRune(runes[i])
			}
		case ';', '\n':
			endSimple()
		case '&':
			if i+1 < len(runes) && runes[i+1] == '>' { // &> file
				emitWord()
				i = redirect(i + 1)
				continue
			}
			if i+1 < len(runes) && runes[i+1] == '&' {
				i++
			}
			endSimple()
		case '|':
			if i+1 < len(runes) && runes[i+1] == '|' {
				i++
				endSimple()
				continue
			}
			emitWord()
			piped = true
		case '>', '<':
			i = redirect(i)
		case ' ', '\t', '\r':
			emitWord()
		default:
			inWord = true
			word.WriteRune(r)
		}
	}
	endSimple()
	return cmds
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// --- ox subcommands ---------------------------------------------------------

// cliRule maps a run of positional ox subcommand words to a label. Rules are
// matched in order; the first whose words prefix the command wins.
type cliRule struct {
	words []string
	label string
	kind  Kind
	query bool // the first positional argument after words is a search subject
	ref   bool // the first positional argument after words names the item read
}

// cliRules is the label table for ox CLI invocations. Names match the cobra
// definitions in cmd/ox. Keep labels in sync with mcpLabels so both surfaces
// read the same.
var cliRules = []cliRule{
	{words: []string{"agent", "prime"}, label: "loaded team context", kind: KindLifecycle},
	{words: []string{"agent", "team-ctx"}, label: "loaded team context", kind: KindRetrieval},
	{words: []string{"agent", "session", "stop"}, label: "stopped recording", kind: KindLifecycle},
	{words: []string{"agent", "session", "context-trace"}, label: "reported context use", kind: KindWrite},
	{words: []string{"agent", "context", "used"}, label: "reported context use", kind: KindWrite},
	{words: []string{"agent", "session", "score"}, label: "scored session", kind: KindWrite},

	{words: []string{"murmur", "list"}, label: "checked murmurs", kind: KindRetrieval},

	{words: []string{"plan", "save"}, label: "saved plan", kind: KindWrite},
	{words: []string{"plan", "render"}, label: "rendered plan", kind: KindWrite},
	{words: []string{"plan", "view"}, label: "pulled plan", kind: KindRetrieval, ref: true},
	{words: []string{"plan", "show"}, label: "pulled plan", kind: KindRetrieval, ref: true},
	{words: []string{"plan", "get"}, label: "pulled plan", kind: KindRetrieval, ref: true},
	{words: []string{"plan", "enrich"}, label: "enriched plan", kind: KindWrite},

	{words: []string{"query"}, label: "searched team memory", kind: KindRetrieval, query: true},

	{words: []string{"code", "search"}, label: "searched code index", kind: KindRetrieval, query: true},
	{words: []string{"code", "defs"}, label: "searched code index", kind: KindRetrieval},
	{words: []string{"code", "callers"}, label: "searched code index", kind: KindRetrieval},
	{words: []string{"code", "callees"}, label: "searched code index", kind: KindRetrieval},
	{words: []string{"code", "refs"}, label: "searched code index", kind: KindRetrieval},
	{words: []string{"code", "log"}, label: "searched code index", kind: KindRetrieval},
	{words: []string{"code", "insights"}, label: "checked code hotspots", kind: KindRetrieval},

	{words: []string{"session", "list"}, label: "read sessions", kind: KindRetrieval},
	{words: []string{"session", "view"}, label: "read sessions", kind: KindRetrieval},
	{words: []string{"session", "show"}, label: "read sessions", kind: KindRetrieval},
	{words: []string{"session", "score"}, label: "scored session", kind: KindWrite},

	{words: []string{"conversation", "show"}, label: "read discussion", kind: KindRetrieval, ref: true},
	{words: []string{"conversation", "search"}, label: "read discussion", kind: KindRetrieval},
	{words: []string{"conversation", "list"}, label: "listed discussions", kind: KindRetrieval},
	{words: []string{"conversation", "transcript"}, label: "read discussion", kind: KindRetrieval, ref: true},
	{words: []string{"conversation", "topics"}, label: "read discussion", kind: KindRetrieval, ref: true},
	{words: []string{"conversation", "topic"}, label: "read discussion", kind: KindRetrieval, ref: true},
	{words: []string{"conversation", "walkthrough"}, label: "read discussion", kind: KindRetrieval, ref: true},

	{words: []string{"decision", "enrich"}, label: "checked decisions", kind: KindRetrieval},

	{words: []string{"session", "report"}, label: "checked session report", kind: KindOther},
	// `ox brief` is the hidden alias of `ox session report`, kept for one
	// release; `ox usage` is its older name, still found in recorded traces.
	{words: []string{"brief"}, label: "checked session report", kind: KindOther},
	{words: []string{"usage"}, label: "checked session report", kind: KindOther},
}

// murmurSubcommands are `ox murmur` children; any other first word is the
// content of a murmur being posted.
var murmurSubcommands = map[string]bool{
	"list": true, "status": true, "delete": true, "pause": true, "resume": true, "help": true,
}

var subcommandRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// recordingLifecycle are `ox agent [id] session <word>` calls that change the
// recording without a label of their own; they keep the generic label but
// report a lifecycle kind and their command words.
var recordingLifecycle = map[string]bool{"start": true, "pause": true, "resume": true}

func classifyOxArgs(args []string) (Activity, bool) {
	words := positional(args)
	if len(words) == 0 {
		return Activity{}, false
	}

	if words[0] == "agent" {
		rest := words[1:]
		// `ox agent <id> <cmd>` addresses one AI coworker; classify <cmd>.
		if len(rest) > 0 && agentinstance.IsValidAgentID(rest[0]) {
			rest = rest[1:]
		}
		if len(rest) > 0 && (rest[0] == "heartbeat" || rest[0] == "hook") {
			return Activity{}, false
		}
		// AI coworkers often address a top-level command through their agent
		// ID (`ox agent <id> murmur list`, `ox agent <id> query "x"`); credit
		// the command they meant.
		if len(rest) > 0 && topLevelUnderAgent[rest[0]] {
			return classifyWords(rest, args)
		}
		words = append([]string{"agent"}, rest...)
	}
	return classifyWords(words, args)
}

// topLevelUnderAgent are top-level ox commands an AI coworker may run as
// `ox agent <id> <cmd>`. Agent-scoped commands (session, prime, team-ctx,
// whisper, ...) are not here: they keep their `agent` rules.
var topLevelUnderAgent = map[string]bool{
	"murmur": true, "query": true, "plan": true, "code": true,
	"conversation": true, "decision": true, "brief": true, "usage": true,
}

// classifyWords matches positional ox words (agent ID already dropped) against
// the rule table; args are the original words, for the query subject.
func classifyWords(words, args []string) (Activity, bool) {
	for _, rule := range cliRules {
		if hasPrefix(words, rule.words) {
			act := Activity{Label: rule.label, Command: strings.Join(rule.words, " "), Kind: rule.kind}
			if rule.query {
				act.Query = querySubject(args, rule.words)
			}
			if rule.ref {
				act.Ref = itemRef(querySubject(args, rule.words))
			}
			return act, true
		}
	}

	switch words[0] {
	case "murmur":
		if len(words) == 1 || !murmurSubcommands[words[1]] {
			// The words after "murmur" are the murmur body: never copied.
			return Activity{Label: "posted murmur", Command: "murmur", Kind: KindWrite}, true
		}
		return Activity{Label: "ran ox murmur", Command: "murmur " + words[1], Kind: KindOther}, true
	case "coworker":
		if len(words) >= 3 && words[1] == "load" {
			if name := cleanText(words[2], 40); name != "" {
				return Activity{Label: "loaded coworker " + name, Command: "coworker load", Kind: KindOther}, true
			}
		}
	case "agent":
		if len(words) >= 3 && words[1] == "session" && recordingLifecycle[words[2]] {
			return Activity{Label: "ran ox agent", Command: "agent session " + words[2], Kind: KindLifecycle}, true
		}
	}

	if !subcommandRe.MatchString(words[0]) {
		return Activity{Label: "ran ox", Kind: KindOther}, true
	}
	command := words[0]
	// `ox agent` always takes a subcommand word; name it so the report can
	// tell `agent session import` from `agent whisper`.
	if words[0] == "agent" && len(words) >= 2 && subcommandRe.MatchString(words[1]) {
		command += " " + words[1]
	}
	return Activity{Label: "ran ox " + words[0], Command: command, Kind: KindOther}, true
}

// booleanFlags are common ox flags that take no value, so the word after one
// can still be a search subject (`ox query --json "auth"`).
var booleanFlags = map[string]bool{
	"--json": true, "--text": true, "--pretty": true, "--verbose": true, "-v": true, "--all": true,
}

// querySubject returns the search subject of a retrieval command: the first
// positional word after the command words that is not a flag's value. Since
// it is the only argument ever copied out of a command line, it is capped and
// limited to the rules that opt in (query, code search).
func querySubject(args, commandWords []string) string {
	matched := 0
	prevFlagTakesValue := false
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			prevFlagTakesValue = !strings.Contains(a, "=") && !booleanFlags[a]
			continue
		}
		if prevFlagTakesValue {
			prevFlagTakesValue = false
			continue
		}
		if matched < len(commandWords) {
			if a == commandWords[matched] {
				matched++
			}
			continue
		}
		return truncateQuery(strings.TrimSpace(a))
	}
	return ""
}

// slugRe is what a bare item ID or plan slug may look like. Anything else
// (a sentence, a path, a URI without a known ID) yields no Ref.
var slugRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// itemRef turns an item-read argument into a stable ID: the canonical cnv_
// form of an embedded conversation ID when there is one (so rec_<u> and
// cnv_<u> count once), else the argument itself when it looks like a slug,
// else "".
func itemRef(arg string) string {
	if id := contextuse.FindConversationID(arg); id != "" {
		return id
	}
	if slugRe.MatchString(arg) {
		return arg
	}
	return ""
}

func truncateQuery(s string) string {
	if len(s) <= MaxQueryLen {
		return s
	}
	cut := MaxQueryLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// positional drops flag words ("-x", "--json", "--topic=wip"). A flag's
// separate value word is kept; the rules only look at leading subcommand
// words, which precede any flag values in practice.
func positional(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		out = append(out, a)
	}
	return out
}

func hasPrefix(words, prefix []string) bool {
	if len(words) < len(prefix) {
		return false
	}
	for i, p := range prefix {
		if words[i] != p {
			return false
		}
	}
	return true
}

// --- SageOx MCP tools -------------------------------------------------------

// mcpTools maps the final segment of a SageOx MCP tool name to its label and
// kind.
var mcpTools = map[string]struct {
	label string
	kind  Kind
}{
	"Prime":                  {"loaded team context", KindLifecycle},
	"PublishMurmur":          {"posted murmur", KindWrite},
	"ListMurmurs":            {"checked murmurs", KindRetrieval},
	"SavePlan":               {"saved plan", KindWrite},
	"RevisePlan":             {"saved plan", KindWrite},
	"GetPlan":                {"pulled plan", KindRetrieval},
	"EnrichPlan":             {"enriched plan", KindWrite},
	"Search":                 {"searched team memory", KindRetrieval},
	"Conversation":           {"read discussion", KindRetrieval},
	"ConversationDigest":     {"read discussion", KindRetrieval},
	"ConversationTranscript": {"read discussion", KindRetrieval},
}

// isSageoxMCPTool reports whether a tool name is an MCP tool served by a
// SageOx server: "mcp__<server>__<Tool>" with "sageox" in the server segment
// (e.g. "mcp__claude_ai_sageox__PublishMurmur", "mcp__sageox__Prime").
func isSageoxMCPTool(name string) bool {
	if !strings.HasPrefix(name, "mcp__") {
		return false
	}
	rest := strings.TrimPrefix(name, "mcp__")
	idx := strings.LastIndex(rest, "__")
	if idx <= 0 {
		return false
	}
	return strings.Contains(strings.ToLower(rest[:idx]), "sageox")
}

func classifyMCP(name string) (Activity, bool) {
	tool := name[strings.LastIndex(name, "__")+2:]
	if tool == "" {
		return Activity{}, false
	}
	t := cleanText(tool, 40)
	if t == "" {
		return Activity{}, false
	}
	command := "mcp " + t
	if known, ok := mcpTools[tool]; ok {
		return Activity{Label: known.label, Command: command, Kind: known.kind}, true
	}
	if strings.HasPrefix(tool, "Conversation") {
		return Activity{Label: "read discussion", Command: command, Kind: KindRetrieval}, true
	}
	return Activity{Label: "used SageOx " + t, Command: command, Kind: KindOther}, true
}

// cleanText makes untrusted text safe for a single terminal line: control
// characters (including ESC) are removed, runs of whitespace collapse to one
// space, and the result is trimmed. max > 0 caps the width with an ellipsis.
func cleanText(s string, max int) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = b.Len() > 0
			continue
		case unicode.IsControl(r), r == '\u2028', r == '\u2029':
			continue
		case unicode.Is(unicode.Cf, r): // bidi overrides, zero-width joiners
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	out := b.String()
	if max > 0 {
		out = Truncate(out, max)
	}
	return out
}
