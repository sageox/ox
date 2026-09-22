package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/sageox/ox/internal/cli"
)

// skills_out.go — rendering and encoding helpers shared by every `ox skills …`
// command. One implementation each, so the human and --json renderings stay
// byte-for-byte consistent across list, status, publish, approve, and revoke
// rather than drifting one command at a time.

// skillsPrintf returns a line-printer bound to w: every call appends a
// trailing newline, matching how each `ox skills` command renders its
// human-readable output.
func skillsPrintf(w io.Writer) func(format string, args ...any) {
	return func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }
}

// encodeSkillsJSON is the one `--json` encoder for every `ox skills` and
// `ox addons` command. It delegates to cli.PrintJSONTo so these payloads get
// the same treatment as the rest of ox: two-space indent, one trailing
// newline, and syntax color when a human is reading at a terminal — never when
// the output is piped, redirected, or captured by an AI coworker.
func encodeSkillsJSON(w io.Writer, payload any) error {
	return cli.PrintJSONTo(w, payload)
}

// dedupeNames removes duplicates, preserving first-seen order. Order matters
// to more than one caller: a multi-name publish reports "the earlier name"
// positionally, so a dedupe that reordered would misreport it.
func dedupeNames(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// allOrNothingSuffix explains, only when more than one name was given, that a
// failure partway through a batch left nothing changed — the only case where a
// reader could reasonably wonder whether the earlier names took effect.
func allOrNothingSuffix(names []string, verb string) string {
	if len(names) < 2 {
		return ""
	}
	return "; nothing was " + verb
}

// skillsTableWidth bounds every wrapped line this command family prints, so no
// line runs past a plain 80-column terminal.
const skillsTableWidth = 80

// writeWrapped prints text broken on spaces so no line runs past
// skillsTableWidth, with first prefixing the opening line and cont every
// continuation.
//
// A long word is never split: a path, a filename, or a backticked command
// broken across two lines cannot be copied out of the terminal, and every
// guidance string in this family exists to be copied.
func writeWrapped(w io.Writer, first, cont, text string) {
	prefix := first
	line := ""
	flush := func() {
		fmt.Fprintf(w, "%s%s\n", prefix, line)
		prefix, line = cont, ""
	}
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case len([]rune(prefix))+len([]rune(line))+1+len([]rune(word)) <= skillsTableWidth:
			line += " " + word
		default:
			flush()
			line = word
		}
	}
	if line != "" {
		flush()
	}
}

// writeSkillsProblems renders a styled header followed by one wrapped bullet
// per problem — the one shape `ox skills list` and `ox skills status` both use
// when something needs a human's attention. header is pre-styled by the
// caller, since list's read failures and status's diagnostics warrant
// different severities.
func writeSkillsProblems(w io.Writer, header string, problems []string) {
	if len(problems) == 0 {
		return
	}
	p := skillsPrintf(w)
	p("%s", header)
	for _, problem := range problems {
		writeWrapped(w, "  • ", "    ", problem)
	}
}
