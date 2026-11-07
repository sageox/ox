package consultscan

import (
	"path/filepath"
	"strings"
)

// This file finds an `ox` invocation inside a recorded shell command. Matching
// is by shell word, not substring, so "ox plan view" never matches inside
// "ox plan viewer", "box plan view", or an echoed string, and each match ends
// at the next command separator so "ox query auth && echo done" is a query
// for "auth" only.

// shellWord is one word of a shell command with quotes removed, and its byte
// span in the command. sep marks a command separator: &&, ||, ;, |, &, newline.
type shellWord struct {
	text       string
	start, end int
	sep        bool
}

// lexShell splits a shell command into words and separators. It understands
// single and double quotes and backslash escapes — enough to read the
// arguments of an `ox` call — and nothing more (no expansion, no subshells).
// An unterminated quote runs to the end of the command.
func lexShell(cmd string) []shellWord {
	var words []shellWord
	for i := 0; i < len(cmd); {
		c := cmd[i]
		switch c {
		case ' ', '\t', '\r':
			i++
		case '\n', ';':
			words = append(words, shellWord{text: cmd[i : i+1], start: i, end: i + 1, sep: true})
			i++
		case '&', '|':
			j := i + 1
			if j < len(cmd) && cmd[j] == c {
				j++
			}
			words = append(words, shellWord{text: cmd[i:j], start: i, end: j, sep: true})
			i = j
		default:
			start := i
			var b strings.Builder
			for i < len(cmd) && !isWordBreak(cmd[i]) {
				switch cmd[i] {
				case '"', '\'':
					q := cmd[i]
					i++
					for i < len(cmd) && cmd[i] != q {
						if q == '"' && cmd[i] == '\\' && i+1 < len(cmd) {
							i++
						}
						b.WriteByte(cmd[i])
						i++
					}
					i = min(i+1, len(cmd)) // past the closing quote
				case '\\':
					if i+1 < len(cmd) {
						i++
					}
					b.WriteByte(cmd[i])
					i++
				default:
					b.WriteByte(cmd[i])
					i++
				}
			}
			words = append(words, shellWord{text: b.String(), start: start, end: i})
		}
	}
	return words
}

func isWordBreak(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == ';' || c == '&' || c == '|'
}

// oxCall is one matched `ox` invocation: its arguments after the matched
// subcommand words, and the byte span of those arguments in the command
// (from the end of the subcommand to the next separator).
type oxCall struct {
	args       []shellWord
	start, end int
}

// findOxCall finds the first invocation of the `ox` subcommand named by
// prefix ("ox plan view") in cmd. The binary may be named by path (./bin/ox),
// and an agent id may sit between `agent` and its subcommand
// (`ox agent Ox1a2b team-ctx` matches "ox agent team-ctx").
func findOxCall(cmd, prefix string) (oxCall, bool) {
	want := strings.Fields(prefix)
	if len(want) == 0 || want[0] != "ox" {
		return oxCall{}, false
	}
	words := lexShell(cmd)
	for i, w := range words {
		if w.sep || filepath.Base(w.text) != "ox" {
			continue
		}
		j, ok := matchSubcommand(words, i+1, want[1:])
		if !ok {
			continue
		}
		call := oxCall{start: words[j-1].end, end: len(cmd)}
		for _, a := range words[j:] {
			if a.sep {
				call.end = a.start
				break
			}
			call.args = append(call.args, a)
		}
		return call, true
	}
	return oxCall{}, false
}

// matchSubcommand matches want against words from index j, allowing one
// non-flag agent id right after an `agent` word. It returns the index just
// past the last matched word.
func matchSubcommand(words []shellWord, j int, want []string) (int, bool) {
	skippedID := false
	for k := 0; k < len(want); {
		if j >= len(words) || words[j].sep {
			return 0, false
		}
		switch {
		case words[j].text == want[k]:
			k++
		case !skippedID && k > 0 && want[k-1] == "agent" && !strings.HasPrefix(words[j].text, "-"):
			skippedID = true
		default:
			return 0, false
		}
		j++
	}
	return j, true
}
