package session

import "strings"

// SessionFilterMode represents the level of session recording.
// Values: "none", "all"
type SessionFilterMode string

const (
	SessionFilterModeNone SessionFilterMode = "none" // no automatic sessions
	SessionFilterModeAll  SessionFilterMode = "all"  // all coding agent sessions
)

// IsValid returns true if the mode is a recognized value.
func (m SessionFilterMode) IsValid() bool {
	switch m {
	case SessionFilterModeNone, SessionFilterModeAll, "":
		return true
	}
	return false
}

// ShouldRecord returns true if this mode enables any recording.
func (m SessionFilterMode) ShouldRecord() bool {
	return m != SessionFilterModeNone && m != ""
}

// noiseCommands are bash commands that are always filtered out (unless they
// fail). Used by cmd/ox/session_resummary.go when trimming low-value tool
// calls before a re-summarization. Formerly lived in pkg/sessionsummary
// alongside a broader FilterForSummarization — that function was dead code
// (the summary prompt tells the calling agent to re-read raw.jsonl directly,
// bypassing any pre-filter), so the filter file was deleted and the one
// piece that had a real caller (IsNoiseCommand) was inlined here next to
// its only consumer.
var noiseCommands = []string{
	"ls", "pwd", "cd", "clear",
	"cat", "head", "tail", "less", "more",
	"echo", "printf",
	"which", "whereis", "type",
	"env", "export",
}

// IsNoiseCommand returns true when a bash command is low-value noise whose
// output rarely carries signal for summarization. Matches prefix followed
// by a space or exact command with no args.
func IsNoiseCommand(cmd string) bool {
	cmdLower := strings.ToLower(strings.TrimSpace(cmd))
	for _, pattern := range noiseCommands {
		if strings.HasPrefix(cmdLower, pattern+" ") || cmdLower == pattern {
			return true
		}
	}
	return false
}
