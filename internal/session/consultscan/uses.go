package consultscan

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
)

// This file is the deterministic "was it used?" half of the scanner. Scan
// answers "what did the agent pull?"; ScanUses answers "which of the context
// that had ALREADY been surfaced did the agent demonstrably go back to?" —
// by re-reading it, re-fetching it by id, or naming it in its own output. Each
// hit is a provable `used` event (ADR-027: refs only, never content).

// requeryCommands are the `ox` invocations that re-open one specific surfaced
// item. Any argument after the prefix (up to the next shell separator) that
// names the item under any of its names counts. Listing
// commands (`ox conversation list`) are not re-opens and stay out.
var requeryCommands = []string{
	"ox agent team-ctx",
	"ox session view",
	"ox session show",
	"ox conversation show",
	"ox conversation transcript",
	"ox conversation topics",
	"ox conversation topic",
	"ox conversation walkthrough",
	"ox walkthrough",
	"ox plan view",
	"ox plan status",
}

// idShapedRef matches refs that are unambiguous identifiers regardless of
// length: short murmur/agent ids, prefixed session/conversation/recording/plan
// ids, and conversation links.
var idShapedRef = regexp.MustCompile(`^(Ox[0-9A-Za-z]{4,}|ses_\S+|cnv_\S+|rec_\S+|pln_\S+|/c/\S+)$`)

// minCitedRefLen is the shortest non-id ref a citation match accepts. Shorter
// refs ("auth", "README") are too common as ordinary words to be evidence.
const minCitedRefLen = 6

// minCitedTitleLen is the shortest title a phrase match accepts.
const minCitedTitleLen = 12

// ScanUses returns one `used` event per (surfaced ref, evidence) for which
// the transcript holds provable evidence that the agent went back to context
// it had already been given. Only entries at or after the surfacing turn
// count (entry index and SurfacedRef.Seq share the EntryCount frame). Within
// one scan a (ref, evidence) pair is reported once, at its earliest turn.
// Deterministic and pure: no I/O, no writes.
//
//   - read (retrieval): a file-read tool opened a surfaced doc (by relative
//     path or basename), or a ledger path under sessions/<ref>/ or
//     data/plans/<ref>.
//   - requery (retrieval): a shell command re-fetched the ref by id
//     (`ox session view <ref>`, `ox conversation transcript <ref>`, …).
//   - cited (citation): an ASSISTANT entry names the ref as a whole token, or
//     quotes the surfaced title. User/system/tool entries never count — they
//     carry the injected context itself.
func ScanUses(entries []Entry, surfaced []contextuse.SurfacedRef, roots Roots) []contexttrace.Event {
	if len(entries) == 0 || len(surfaced) == 0 {
		return nil
	}
	var events []contexttrace.Event
	seen := map[string]bool{}
	emit := func(i int, s contextuse.SurfacedRef, mech contexttrace.Mechanism, evidence string) {
		key := s.Ref + "\x00" + evidence
		if seen[key] {
			return
		}
		seen[key] = true
		events = append(events, contexttrace.Event{
			Type:        contexttrace.EventUsed,
			Source:      contexttrace.SourceOnDemand,
			Mechanism:   mech,
			Evidence:    evidence,
			Seq:         i,
			SurfacedSeq: s.Seq,
			Ref:         s.Ref,
			RefType:     s.RefType,
			Title:       s.Title,
			URL:         s.URL,
		})
	}

	for i, e := range entries {
		switch {
		case readToolNames[e.ToolName]:
			path := extractPath(e.ToolInput)
			if path == "" {
				continue
			}
			for _, s := range surfaced {
				if i >= s.Seq && readMatchesRef(path, s, roots) {
					emit(i, s, contexttrace.MechanismRetrieval, contexttrace.EvidenceRead)
				}
			}
		case shellToolNames[e.ToolName]:
			args := requeryArgs(e.ToolInput)
			if len(args) == 0 {
				continue
			}
			for _, s := range surfaced {
				if i >= s.Seq && anyNames(args, s) {
					emit(i, s, contexttrace.MechanismRetrieval, contexttrace.EvidenceRequery)
				}
			}
		case e.Type == "assistant" && e.Content != "":
			for _, s := range surfaced {
				if i >= s.Seq && MatchesRef(e.Content, s.Ref, s.Title, s.Aliases...) {
					emit(i, s, contexttrace.MechanismCitation, contexttrace.EvidenceCited)
				}
			}
		}
	}
	return events
}

// readMatchesRef reports whether a file read of path is a read of the surfaced
// item under its ref or any alias (see readMatchesName). Aliases that are links
// or URIs name no file and are skipped.
func readMatchesRef(path string, s contextuse.SurfacedRef, roots Roots) bool {
	for _, name := range append([]string{s.Ref}, s.Aliases...) {
		if strings.Contains(name, "://") {
			continue
		}
		if readMatchesName(path, name, roots) {
			return true
		}
	}
	return false
}

// readMatchesName reports whether a file read of path is a read of the named
// item: the path relative to a SageOx root (or its basename) equals the name,
// the basename equals the name's basename when the name is a file, or the path
// sits in the ledger under sessions/<name>/ or data/plans/<name>.
func readMatchesName(path, ref string, roots Roots) bool {
	ref = strings.TrimRight(ref, "/")
	if ref == "" {
		return false
	}
	clean := filepath.Clean(path)
	if root, _ := classify(clean, roots); root != "" {
		rel := relOrBase(root, clean)
		if rel == ref {
			return true
		}
		if root == roots.Ledger {
			for _, prefix := range []string{"sessions/", "data/plans/"} {
				if strings.HasPrefix(rel, prefix+ref+"/") || rel == prefix+ref {
					return true
				}
			}
		}
	}
	// A surfaced doc name ("MEMORY.md", "agents/rules/postgres.md") read from
	// any location still counts when the file names agree. Extensionless refs
	// (slugs, ids) are too easy to collide with by basename alone.
	base := filepath.Base(ref)
	return strings.Contains(base, ".") && base == filepath.Base(clean)
}

// requeryArgs returns the arguments of the first `ox` re-open command in a
// shell tool input, up to the next shell separator, or nil when there is none.
// A --flag=value argument contributes its value; bare flags are dropped.
func requeryArgs(toolInput string) []string {
	cmd := extractCommand(toolInput)
	if cmd == "" {
		return nil
	}
	for _, prefix := range requeryCommands {
		call, ok := findOxCall(cmd, prefix)
		if !ok {
			continue
		}
		var args []string
		for _, a := range call.args {
			arg := a.text
			if strings.HasPrefix(arg, "-") {
				_, v, ok := strings.Cut(arg, "=")
				if !ok {
					continue
				}
				arg = v
			}
			if arg != "" {
				args = append(args, arg)
			}
		}
		return args
	}
	return nil
}

// anyNames reports whether any argument names the surfaced item under one of
// its names (canonical ref, rec_/cnv_ twin, sageox:// URI, /c/ link, alias).
func anyNames(args []string, s contextuse.SurfacedRef) bool {
	for _, a := range args {
		if s.Matches(a) {
			return true
		}
	}
	return false
}

// MatchesRef reports whether free text names a surfaced ref: the ref or one
// of its aliases appears as a whole token (word-bounded; at least
// minCitedRefLen characters, or any length for id-shaped names), a
// conversation id in the text is another name of the ref (rec_ for cnv_, in a
// link or a sageox:// URI), or the surfaced title appears as an exact phrase
// of at least minCitedTitleLen characters. Shared by the assistant-output
// citation scan and the commit-message citation hook so both grade evidence
// the same way.
func MatchesRef(text, ref, title string, aliases ...string) bool {
	if text == "" {
		return false
	}
	for _, name := range append([]string{ref}, aliases...) {
		if name != "" && (len(name) >= minCitedRefLen || idShapedRef.MatchString(name)) && containsToken(text, name) {
			return true
		}
	}
	for _, id := range contextuse.ConversationIDPattern.FindAllString(text, -1) {
		if contextuse.SameItem("", id, ref, aliases...) {
			return true
		}
	}
	if len(title) >= minCitedTitleLen && strings.Contains(text, title) {
		return true
	}
	return false
}

// containsToken reports whether tok occurs in text with no identifier
// character (letter, digit, underscore) touching either end — so "OxAbcd"
// does not match inside "OxAbcdef", but does match in "(see OxAbcd)".
func containsToken(text, tok string) bool {
	for start := 0; ; {
		i := strings.Index(text[start:], tok)
		if i < 0 {
			return false
		}
		i += start
		end := i + len(tok)
		// A boundary is only required on a side where the token itself is
		// identifier-like: "/c/cnv_1" may follow "sageox.ai" directly.
		before := i == 0 || !isIdentByte(tok[0]) || !isIdentByte(text[i-1])
		after := end == len(text) || !isIdentByte(tok[len(tok)-1]) || !isIdentByte(text[end])
		if before && after {
			return true
		}
		start = i + 1
	}
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
