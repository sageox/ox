package teamskills

import (
	"regexp"
	"strings"
)

// runtime.go decides whether a team skill's BODY expects a coding agent with a
// terminal, for skills whose author did not say so with a `requires:` key.
//
// It is the reference definition of that inference. The SageOx server mirrors
// it to classify a skill as coding-agent only (kept out of chat hosts and out of
// the Claude organization export), and the two are compared against the shared
// vectors in testdata/runtime_signal_vectors.json. Change the vectors and both
// sides together, or the CLI and the server will disagree about the same file.
//
// The definition is deliberately NARROW. It flags only what is unambiguously an
// instruction to run something:
//
//   - runs-ox-commands: an inline code span that starts an ox invocation
//     (`ox sync`), anywhere outside a fenced block; or, INSIDE a fenced block of
//     any language (untagged included), a line that is itself an ox invocation,
//     optionally behind a `$ ` prompt.
//   - shell-fence: a fenced block opened with a shell language tag: bash, sh,
//     shell, or zsh (case-insensitive, ``` or ~~~).
//
// It does NOT flag prose that mentions ox ("ox keeps your team in sync",
// "the ox CLI"), an untagged fence on its own, or a fence in another language.
// A false positive here costs a coworker the skill in chat and a misleading
// warning at publish time; a false negative is recovered by the author adding
// `requires: [shell]`. The narrow side is the one an author can fix.

// Signal codes. They match the server's reason codes so a warning printed by
// `ox skills publish` names the same thing the console shows.
const (
	SignalRunsOxCommands = "runs-ox-commands"
	SignalShellFence     = "shell-fence"
)

// RuntimeSignal is one piece of evidence that a skill's body expects a
// terminal. Evidence is the matched text, trimmed, so a human can find it.
type RuntimeSignal struct {
	Code     string
	Evidence string
}

var (
	// fenceOpen matches a fenced-code opener: any leading indentation (a fence
	// inside a list item is still a fence), three or more backticks or tildes,
	// then the info string.
	fenceOpen = regexp.MustCompile("^[ \\t]*(`{3,}|~{3,})(.*)$")

	// shellInfo matches an info string whose language is a shell. The \b lets
	// `shell-session` and `sh {.copy}` count while `bashrc` does not.
	shellInfo = regexp.MustCompile(`(?i)^[ \t]*(?:bash|sh|shell|zsh)\b`)

	// oxInlineSpan matches an inline code span that starts an ox invocation. A
	// bare `ox` (the CLI's name used as a noun) needs a lowercase verb after it
	// to count.
	oxInlineSpan = regexp.MustCompile("`ox [a-z][a-z0-9-]*")

	// oxCommandLine matches a line inside a fence that IS an ox invocation.
	oxCommandLine = regexp.MustCompile(`^[ \t]*(?:\$[ \t]+)?ox[ \t]+[a-z][a-z0-9-]*`)
)

// BodyRuntimeSignals returns the first runs-ox-commands signal and the first
// shell-fence signal in body, in that order, or nil when there are none.
//
// body is the manifest WITHOUT its frontmatter (teamdocs.ManifestBody): a
// description that mentions `ox plan` describes the skill, it does not run it.
func BodyRuntimeSignals(body string) []RuntimeSignal {
	var runsOx, shellFence string

	inFence := false
	var fenceMarker string // the run of ` or ~ that opened the current fence
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if inFence {
			if closesFence(line, fenceMarker) {
				inFence = false
				continue
			}
			if runsOx == "" {
				if m := oxCommandLine.FindString(line); m != "" {
					runsOx = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(m), "$"))
				}
			}
			continue
		}

		if m := fenceOpen.FindStringSubmatch(line); m != nil {
			marker, info := m[1], m[2]
			// CommonMark: a backtick fence's info string may not contain a
			// backtick. ```ox sync``` on one line is an inline span, not a fence.
			if marker[0] != '`' || !strings.Contains(info, "`") {
				inFence = true
				fenceMarker = marker
				if shellFence == "" && shellInfo.MatchString(info) {
					shellFence = strings.TrimSpace(line)
				}
				continue
			}
		}

		if runsOx == "" {
			if m := oxInlineSpan.FindString(line); m != "" {
				runsOx = strings.TrimPrefix(m, "`")
			}
		}
	}

	var out []RuntimeSignal
	if runsOx != "" {
		out = append(out, RuntimeSignal{Code: SignalRunsOxCommands, Evidence: clip(runsOx)})
	}
	if shellFence != "" {
		out = append(out, RuntimeSignal{Code: SignalShellFence, Evidence: clip(shellFence)})
	}
	return out
}

// closesFence reports whether line closes a fence opened with marker: the same
// character, at least as many of them, and nothing else on the line.
func closesFence(line, marker string) bool {
	trimmed := strings.TrimSpace(line)
	run := len(trimmed) - len(strings.TrimLeft(trimmed, marker[:1]))
	return run >= len(marker) && run == len(trimmed)
}

func clip(s string) string {
	const maxRunes = 120
	if r := []rune(s); len(r) > maxRunes {
		return string(r[:maxRunes]) + "…"
	}
	return s
}
