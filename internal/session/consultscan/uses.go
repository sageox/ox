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
// item. The argument after the prefix must name a surfaced ref. Listing
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
				if i >= s.Seq && readMatchesRef(path, s.Ref, roots) {
					emit(i, s, contexttrace.MechanismRetrieval, contexttrace.EvidenceRead)
				}
			}
		case shellToolNames[e.ToolName]:
			arg, ok := detectRequeryCommand(e.ToolInput)
			if !ok {
				continue
			}
			for _, s := range surfaced {
				if i >= s.Seq && arg == strings.TrimRight(s.Ref, "/") {
					emit(i, s, contexttrace.MechanismRetrieval, contexttrace.EvidenceRequery)
				}
			}
		case e.Type == "assistant" && e.Content != "":
			for _, s := range surfaced {
				if i >= s.Seq && MatchesRef(e.Content, s.Ref, s.Title) {
					emit(i, s, contexttrace.MechanismCitation, contexttrace.EvidenceCited)
				}
			}
		}
	}
	return events
}

// readMatchesRef reports whether a file read of path is a read of the surfaced
// ref: the path relative to a SageOx root (or its basename) equals the ref,
// the basename equals the ref's basename when the ref names a file, or the
// path sits in the ledger under sessions/<ref>/ or data/plans/<ref>.
func readMatchesRef(path, ref string, roots Roots) bool {
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

// detectRequeryCommand reports whether a shell tool input runs an `ox`
// re-fetch of one ref, returning the ref argument.
func detectRequeryCommand(toolInput string) (string, bool) {
	cmd := extractCommand(toolInput)
	if cmd == "" {
		return "", false
	}
	for _, prefix := range requeryCommands {
		call, ok := findOxCall(cmd, prefix)
		if !ok {
			continue
		}
		arg := firstQuotedOrToken(cmd[call.start:call.end])
		if arg == "" {
			return "", false
		}
		return strings.TrimRight(arg, "/"), true
	}
	return "", false
}

// MatchesRef reports whether free text names a surfaced ref: either the ref
// appears as a whole token (word-bounded; at least minCitedRefLen characters,
// or any length for id-shaped refs), or the surfaced title appears as an exact
// phrase of at least minCitedTitleLen characters. Shared by the assistant-output
// citation scan and the commit-message citation hook so both grade evidence
// the same way.
func MatchesRef(text, ref, title string) bool {
	if text == "" {
		return false
	}
	if ref != "" && (len(ref) >= minCitedRefLen || idShapedRef.MatchString(ref)) && containsToken(text, ref) {
		return true
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
