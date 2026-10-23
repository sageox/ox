package main

import (
	"log/slog"
	"regexp"
	"strings"

	friction "github.com/sageox/frictionax"
	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/daemon"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Friction events describe a failed invocation, and a failed invocation is
// mostly whatever the coworker typed: file paths, org and repo names, team IDs,
// search text, whole prompts. frictionax's default redactor passes all of it
// through, so the CLI reduces the event to its structure before it leaves the
// process.
//
// The rule is an allowlist taken from the command tree itself. Words that
// resolve as successive subcommands survive, flag names survive, and everything
// else (positionals, flag values, unresolved words) becomes <arg>. A tree-aware
// rule is more precise than pattern matching: free text is by definition
// anything the tree does not name, so there is no pattern to forget.

const (
	frictionArgPlaceholder = "<arg>"
	frictionIDPlaceholder  = "<id>"
)

var (
	// flag names stay so an unknown-flag event can still be diagnosed. The
	// shape check keeps free text that merely starts with a dash from riding
	// along as a "flag name".
	frictionLongFlagShape  = regexp.MustCompile(`^--[A-Za-z][A-Za-z0-9_-]{0,39}$`)
	frictionShortFlagShape = regexp.MustCompile(`^-[A-Za-z][A-Za-z0-9]{0,2}$`)

	// a bare flag name as cobra prints it in `required flag(s) "name" not set`
	frictionBareFlagShape = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,39}$`)

	// the agent dispatcher routes on plain lowercase words (session, log,
	// status, heartbeat, query, tasks). Cobra cannot resolve them, so they are
	// kept by shape; anything with a space, digit or capital is free text.
	frictionDispatchWordShape = regexp.MustCompile(`^[a-z][a-z-]{0,23}$`)

	frictionShorthandErrShape = regexp.MustCompile(`^unknown shorthand flag: '(.)' in`)
	frictionInvalidArgShape   = regexp.MustCompile(`^invalid argument "(?:.*?)" for "(--?[A-Za-z][A-Za-z0-9_-]{0,39})"`)
	frictionQuotedShape       = regexp.MustCompile(`"([^"]*)"`)
	frictionQuotedAnyShape    = regexp.MustCompile("\"[^\"]*\"?|'[^']*'?|`[^`]*`?")
)

// frictionErrorPhrases are the fixed wordings frictionax recognizes. When an
// error carries one of them in a shape the structural rules below do not
// handle, only the phrase is sent, never the rest of the message.
var frictionErrorPhrases = []string{
	"unknown command",
	"unknown shorthand flag",
	"unknown flag",
	"required flag",
	"invalid argument",
}

// redactedFrictionFields are the user-derived strings of a friction event after
// redaction.
type redactedFrictionFields struct {
	Command    string
	Subcommand string
	Input      string
	ErrorMsg   string
	Suggestion string
}

// redactFrictionEvent reduces the free-form fields of event to command
// structure. root is the cobra tree that decides which words are commands.
//
// It runs on the exit path of a failed command, where a panic would replace the
// coworker's real error with a stack trace, so it recovers and reports ok=false:
// the caller drops the event rather than send it unredacted.
func redactFrictionEvent(root *cobra.Command, event *friction.FrictionEvent) (fields redactedFrictionFields, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("friction redaction panicked", "panic", r)
			fields, ok = redactedFrictionFields{}, false
		}
	}()

	if root == nil {
		root = &cobra.Command{Use: "ox"}
	}
	// frictionax's cobra adapter fills Command with the parent's command path and
	// leaves Subcommand empty, but both are plain strings on the event, so they
	// get the same tree rule: Subcommand is relative to Command
	command, commandNode := walkCommandLine(root, root, event.Command)
	subcommand, _ := walkCommandLine(root, commandNode, event.Subcommand)
	return redactedFrictionFields{
		Command:    daemon.ScrubFrictionText(command),
		Subcommand: daemon.ScrubFrictionText(subcommand),
		Input:      daemon.ScrubFrictionText(redactCommandLine(root, event.Input)),
		ErrorMsg:   daemon.ScrubFrictionText(redactFrictionError(root, event.ErrorMsg)),
		Suggestion: daemon.ScrubFrictionText(redactCommandLine(root, event.Suggestion)),
	}, true
}

// redactCommandLine reduces a command line to its structure. See the file
// comment for the rule; examples, with `session log` and `plan list` real
// commands and --json a bool flag:
//
//	plan list --limit=5 --json      -> plan list --limit --json
//	session log fix the login bug   -> session log <arg>
//	agent OxAbCd session log --content hi -> agent <id> session log --content <arg>
func redactCommandLine(root *cobra.Command, line string) string {
	out, _ := walkCommandLine(root, root, line)
	return out
}

// walk modes: what the next positional token is allowed to be.
const (
	walkTree           = iota // resolving subcommands in the cobra tree
	walkDispatchFirst         // the word the agent dispatcher routes on
	walkDispatchSecond        // the sub-word of a dispatcher router (session <x>)
	walkDone                  // past the command path: positionals only
)

// walkCommandLine redacts line, resolving words against the tree from start. It
// also returns the deepest command that resolved, which callers use to place
// the arguments of an error message.
func walkCommandLine(root, start *cobra.Command, line string) (string, *cobra.Command) {
	tokens := strings.Fields(line)
	out := make([]string, 0, len(tokens))
	emit := func(s string) { out = append(out, s) }
	emitArg := func() {
		if len(out) == 0 || out[len(out)-1] != frictionArgPlaceholder {
			emit(frictionArgPlaceholder)
		}
	}

	cur := start
	mode := walkTree
	i := 0
	if cur == root && len(tokens) > 0 && tokens[0] == root.Name() {
		emit(tokens[0])
		i = 1
	}

	for ; i < len(tokens); i++ {
		tok := tokens[i]

		if tok == "--" {
			emit("--")
			if i+1 < len(tokens) {
				emitArg()
			}
			break
		}

		if len(tok) > 1 && tok[0] == '-' {
			name, _, hasValue := strings.Cut(tok, "=")
			if !frictionLongFlagShape.MatchString(name) && !frictionShortFlagShape.MatchString(name) {
				emitArg()
				continue
			}
			emit(name)
			if hasValue {
				continue
			}
			flag := lookupFrictionFlag(cur, name)
			switch {
			case flag == nil:
				// an unknown flag may or may not take a value, so nothing after
				// it can be trusted to be a command word
				mode = walkDone
			case flag.NoOptDefVal == "" && i+1 < len(tokens):
				emitArg()
				i++
			}
			continue
		}

		switch mode {
		case walkTree:
			if child := findFrictionChild(cur, tok); child != nil {
				emit(tok)
				cur = child
				continue
			}
			if cur.Name() == "agent" && cur.Parent() == root {
				if agentinstance.IsValidAgentID(tok) {
					emit(frictionIDPlaceholder)
					mode = walkDispatchFirst
					continue
				}
				// `ox agent session start`: no ID, but a word the dispatcher knows
				if isAgentSubcommand(tok) {
					emit(tok)
					mode = dispatchModeAfter(tok)
					continue
				}
			}
			emitArg()
			mode = walkDone
		case walkDispatchFirst:
			if frictionDispatchWordShape.MatchString(tok) {
				emit(tok)
				mode = dispatchModeAfter(tok)
			} else {
				emitArg()
				mode = walkDone
			}
		case walkDispatchSecond:
			if frictionDispatchWordShape.MatchString(tok) {
				emit(tok)
			} else {
				emitArg()
			}
			mode = walkDone
		default:
			emitArg()
		}
	}

	return strings.Join(out, " "), cur
}

// dispatchModeAfter says whether the agent dispatcher word just kept is a router
// with a sub-word of its own (`session log`, `whisper history`). Any other
// dispatched command takes arguments, which are never kept.
func dispatchModeAfter(word string) int {
	if word == "session" || word == "whisper" {
		return walkDispatchSecond
	}
	return walkDone
}

// findFrictionChild returns the subcommand of cmd that word names, by name or alias.
func findFrictionChild(cmd *cobra.Command, word string) *cobra.Command {
	for _, child := range cmd.Commands() {
		if child.Name() == word || child.HasAlias(word) {
			return child
		}
	}
	return nil
}

// lookupFrictionFlag finds the flag a `--name` or `-n` token refers to as seen
// from cmd: its own flags plus the persistent flags of it and its ancestors.
// Returns nil for flags the tree does not define, which is exactly what an
// unknown-flag event is about.
func lookupFrictionFlag(cmd *cobra.Command, token string) *pflag.Flag {
	find := func(set *pflag.FlagSet) *pflag.Flag {
		switch {
		case strings.HasPrefix(token, "--"):
			return set.Lookup(token[2:])
		case len(token) == 2:
			return set.ShorthandLookup(token[1:])
		}
		return nil // a cluster like -abc: pflag's lookup panics on it
	}
	if flag := find(cmd.Flags()); flag != nil {
		return flag
	}
	for c := cmd; c != nil; c = c.Parent() {
		if flag := find(c.PersistentFlags()); flag != nil {
			return flag
		}
	}
	return nil
}

// redactFrictionError keeps the shape of a cobra usage error and drops what the
// coworker typed into it. Only the first line is considered: cobra's own
// errors are one line, and ox appends hints after a blank line.
//
//	unknown command "conversation show cnv_x" for "ox" -> unknown command "conversation <arg>" for "ox"
//	unknown shorthand flag: 'x' in -x some text         -> unknown shorthand flag: 'x' in
//	unknown flag: --limit                               -> unknown flag: --limit
func redactFrictionError(root *cobra.Command, msg string) string {
	if i := strings.IndexAny(msg, "\r\n"); i >= 0 {
		msg = msg[:i]
	}
	msg = strings.TrimSpace(msg)

	switch {
	case msg == "":
		return ""
	case strings.HasPrefix(msg, `unknown command "`):
		return redactUnknownCommandError(root, msg)
	case strings.HasPrefix(msg, "unknown command or invalid agent_id: "):
		return "unknown command or invalid agent_id: " + redactDispatchWord(strings.TrimPrefix(msg, "unknown command or invalid agent_id: "))
	case strings.HasPrefix(msg, "unknown command: "):
		return "unknown command: " + redactDispatchWord(strings.TrimPrefix(msg, "unknown command: "))
	case strings.HasPrefix(msg, "unknown flag: "):
		name, _, _ := strings.Cut(strings.TrimPrefix(msg, "unknown flag: "), " ")
		name, _, _ = strings.Cut(name, "=")
		return "unknown flag: " + keepFlagToken(name)
	case strings.HasPrefix(msg, "unknown shorthand flag: "):
		// the text after " in " is the whole argument the flag was found in
		if m := frictionShorthandErrShape.FindStringSubmatch(msg); m != nil && frictionBareFlagShape.MatchString(m[1]) {
			return "unknown shorthand flag: '" + m[1] + "' in"
		}
		return "unknown shorthand flag: '" + frictionArgPlaceholder + "' in"
	case strings.HasPrefix(msg, "required flag"):
		// quoted items are flag names, kept only if they look like one
		return frictionQuotedShape.ReplaceAllStringFunc(msg, func(quoted string) string {
			name := strings.Trim(quoted, `"`)
			if frictionBareFlagShape.MatchString(name) {
				return quoted
			}
			return `"` + frictionArgPlaceholder + `"`
		})
	case strings.HasPrefix(msg, "invalid argument "):
		// the quoted value is what the coworker typed; pflag repeats it in the
		// parse error that follows the flag name, so the tail goes too
		if m := frictionInvalidArgShape.FindStringSubmatch(msg); m != nil {
			return `invalid argument "` + frictionArgPlaceholder + `" for "` + m[1] + `"`
		}
		return "invalid argument"
	}

	// a shape not handled above (an ox-authored error that merely contains one
	// of frictionax's trigger phrases): send the fixed phrase, never the text
	earliest, phrase := len(msg)+1, ""
	for _, p := range frictionErrorPhrases {
		if at := strings.Index(msg, p); at >= 0 && at < earliest {
			earliest, phrase = at, p
		}
	}
	if phrase != "" {
		return phrase
	}
	return frictionQuotedAnyShape.ReplaceAllString(msg, frictionArgPlaceholder)
}

// redactUnknownCommandError handles `unknown command "X" for "P"`, tolerating a
// message frictionax truncated mid-quote at 200 bytes.
func redactUnknownCommandError(root *cobra.Command, msg string) string {
	rest := strings.TrimPrefix(msg, `unknown command "`)
	typed, parent, hasParent := rest, "", false
	if at := strings.Index(rest, `" for "`); at >= 0 {
		typed, parent, hasParent = rest[:at], rest[at+len(`" for "`):], true
		parent, _, _ = strings.Cut(parent, `"`)
	}
	typed = strings.TrimSuffix(typed, `"`)

	// X is relative to P: cobra reports the unknown word under the command it
	// was typed after, so resolve P first and walk X from there
	redactedParent, parentCmd := walkCommandLine(root, root, parent)
	redactedTyped, _ := walkCommandLine(root, parentCmd, typed)
	if redactedTyped == "" {
		redactedTyped = frictionArgPlaceholder
	}

	out := `unknown command "` + redactedTyped + `"`
	if hasParent {
		out += ` for "` + redactedParent + `"`
	}
	return out
}

// redactDispatchWord keeps the unresolved word of an agent dispatcher error
// only when it has the shape of a command word.
func redactDispatchWord(word string) string {
	word = strings.TrimSpace(word)
	if frictionDispatchWordShape.MatchString(word) {
		return word
	}
	return frictionArgPlaceholder
}

// keepFlagToken returns token when it is shaped like a flag name.
func keepFlagToken(token string) string {
	if frictionLongFlagShape.MatchString(token) || frictionShortFlagShape.MatchString(token) {
		return token
	}
	return frictionArgPlaceholder
}
