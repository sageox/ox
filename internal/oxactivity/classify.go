// Package oxactivity turns an AI coworker's tool calls into the short,
// human-readable "ox is doing work" line that harnesses show to coworkers.
//
// Every harness renders the same wording and the same styling, so the pieces
// they share live here:
//
//   - Classify maps one harness tool call (a shell command or a SageOx MCP
//     tool) to an Activity label such as "posted murmur".
//   - Render draws one line — "● ox  posted murmur · whisper from ryan [wip] …"
//     — with or without color.
//   - State, LoadState and SaveState persist the latest action and whisper per
//     AI coworker, so a later process (a status-line refresh, a hook) can render
//     them.
//
// The package does no I/O beyond the per-agent state file and never decides
// whether the line is enabled; callers gate on config.UserConfig's
// IsStatuslineEnabled.
package oxactivity

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"

	"github.com/sageox/ox/internal/agentinstance"
)

// Activity is one coworker-visible thing ox did for an AI coworker.
type Activity struct {
	// Label is the user-facing wording, lower case, past tense:
	// "loaded team context", "posted murmur", "ran ox doctor".
	Label string
}

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
}

// cliRules is the label table for ox CLI invocations. Names match the cobra
// definitions in cmd/ox. Keep labels in sync with mcpLabels so both surfaces
// read the same.
var cliRules = []cliRule{
	{[]string{"agent", "prime"}, "loaded team context"},
	{[]string{"agent", "team-ctx"}, "loaded team context"},
	{[]string{"agent", "session", "stop"}, "stopped recording"},

	{[]string{"murmur", "list"}, "checked murmurs"},

	{[]string{"plan", "save"}, "saved plan"},
	{[]string{"plan", "render"}, "rendered plan"},
	{[]string{"plan", "view"}, "pulled plan"},
	{[]string{"plan", "show"}, "pulled plan"},
	{[]string{"plan", "get"}, "pulled plan"},
	{[]string{"plan", "enrich"}, "enriched plan"},

	{[]string{"query"}, "searched team memory"},

	{[]string{"code", "search"}, "searched code index"},
	{[]string{"code", "defs"}, "searched code index"},
	{[]string{"code", "callers"}, "searched code index"},
	{[]string{"code", "callees"}, "searched code index"},
	{[]string{"code", "refs"}, "searched code index"},
	{[]string{"code", "log"}, "searched code index"},
	{[]string{"code", "insights"}, "checked code hotspots"},

	{[]string{"session", "list"}, "read sessions"},
	{[]string{"session", "view"}, "read sessions"},
	{[]string{"session", "show"}, "read sessions"},

	{[]string{"conversation", "show"}, "read discussion"},
	{[]string{"conversation", "search"}, "read discussion"},
	{[]string{"conversation", "transcript"}, "read discussion"},
	{[]string{"conversation", "topics"}, "read discussion"},
	{[]string{"conversation", "topic"}, "read discussion"},

	{[]string{"decision", "enrich"}, "checked decisions"},
}

// murmurSubcommands are `ox murmur` children; any other first word is the
// content of a murmur being posted.
var murmurSubcommands = map[string]bool{
	"list": true, "status": true, "delete": true, "pause": true, "resume": true, "help": true,
}

var subcommandRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

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
		words = append([]string{"agent"}, rest...)
	}

	for _, rule := range cliRules {
		if hasPrefix(words, rule.words) {
			return Activity{Label: rule.label}, true
		}
	}

	switch words[0] {
	case "murmur":
		if len(words) == 1 || !murmurSubcommands[words[1]] {
			return Activity{Label: "posted murmur"}, true
		}
	case "coworker":
		if len(words) >= 3 && words[1] == "load" {
			if name := cleanText(words[2], 40); name != "" {
				return Activity{Label: "loaded coworker " + name}, true
			}
		}
	}

	if !subcommandRe.MatchString(words[0]) {
		return Activity{Label: "ran ox"}, true
	}
	return Activity{Label: "ran ox " + words[0]}, true
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

// mcpLabels maps the final segment of a SageOx MCP tool name to its label.
var mcpLabels = map[string]string{
	"Prime":                  "loaded team context",
	"PublishMurmur":          "posted murmur",
	"ListMurmurs":            "checked murmurs",
	"SavePlan":               "saved plan",
	"RevisePlan":             "saved plan",
	"GetPlan":                "pulled plan",
	"EnrichPlan":             "enriched plan",
	"Search":                 "searched team memory",
	"Conversation":           "read discussion",
	"ConversationDigest":     "read discussion",
	"ConversationTranscript": "read discussion",
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
	if label, ok := mcpLabels[tool]; ok {
		return Activity{Label: label}, true
	}
	if strings.HasPrefix(tool, "Conversation") {
		return Activity{Label: "read discussion"}, true
	}
	if t := cleanText(tool, 40); t != "" {
		return Activity{Label: "used SageOx " + t}, true
	}
	return Activity{}, false
}
