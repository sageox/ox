package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/errkind"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
)

// maxContextWhyRunes caps the one-line --why an AI coworker records with a use.
const maxContextWhyRunes = 200

// Error codes for `ox agent <id> context used`.
const (
	contextErrUnknownRef   = "unknown_ref"
	contextErrAmbiguousRef = "ambiguous_ref"
)

// contextRefJSON is one surfaced ref as `context list` and the error payload
// show it. Evidence and Grade are set once the ref has been used.
type contextRefJSON struct {
	Ref      string `json:"ref"`
	RefType  string `json:"ref_type"`
	Via      string `json:"via"`
	Title    string `json:"title,omitempty"`
	URL      string `json:"url,omitempty"`
	FromName string `json:"from_name,omitempty"`
	Seq      int    `json:"seq,omitempty"`
	Evidence string `json:"evidence,omitempty"`
	Grade    string `json:"grade,omitempty"`
}

// contextUsedOutput is the JSON `ox agent <id> context used` prints on success.
type contextUsedOutput struct {
	Success     bool   `json:"success"`
	Type        string `json:"type"` // always "context_used"
	AgentID     string `json:"agent_id"`
	Recording   bool   `json:"recording"`
	Ref         string `json:"ref,omitempty"`
	RefType     string `json:"ref_type,omitempty"`
	Title       string `json:"title,omitempty"`
	URL         string `json:"url,omitempty"`
	Evidence    string `json:"evidence"` // reported | none
	Why         string `json:"why,omitempty"`
	Seq         int    `json:"seq"`
	SurfacedSeq int    `json:"surfaced_seq,omitempty"`
	TracePath   string `json:"trace_path"`
	Guidance    string `json:"guidance"`
}

// contextErrorDetail names a context command failure an AI coworker can act on.
type contextErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// contextErrorOutput is the JSON printed when a ref cannot be resolved: it lists
// what was surfaced so the AI coworker corrects the ref instead of inventing one.
type contextErrorOutput struct {
	Success  bool               `json:"success"`
	Type     string             `json:"type"` // always "context_used"
	AgentID  string             `json:"agent_id"`
	Error    contextErrorDetail `json:"error"`
	Surfaced []contextRefJSON   `json:"surfaced"`
	Guidance string             `json:"guidance"`
}

// contextListOutput is the JSON `ox agent <id> context list` prints.
type contextListOutput struct {
	Success      bool             `json:"success"`
	Type         string           `json:"type"` // always "context_list"
	AgentID      string           `json:"agent_id"`
	Recording    bool             `json:"recording"`
	Surfaced     []contextRefJSON `json:"surfaced"`
	UsedCount    int              `json:"used_count"`
	ReportedNone bool             `json:"reported_none,omitempty"`
	Guidance     string           `json:"guidance"`
}

// contextUsedArgs is the parsed form of `context used` arguments.
type contextUsedArgs struct {
	ref     string
	why     string
	refType string
	none    bool
}

// runAgentContext dispatches `ox agent <id> context <used|list>`.
func runAgentContext(w io.Writer, inst *agentinstance.Instance, args []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}
	return runAgentContextAt(w, projectRoot, inst.AgentID, args)
}

func runAgentContextAt(w io.Writer, projectRoot, agentID string, args []string) error {
	if len(args) == 0 {
		return errkind.Errorf(errkind.Usage, "context requires a subcommand\nUsage: ox agent %s context <used|list>", agentID)
	}
	switch args[0] {
	case "used":
		parsed, err := parseContextUsedArgs(args[1:])
		if err != nil {
			return errkind.Errorf(errkind.Usage, "%v\nUsage: ox agent %s context used <ref> [--why \"<one line>\"] [--ref-type <type>] | --none", err, agentID)
		}
		return runAgentContextUsed(w, projectRoot, agentID, parsed)
	case "list":
		return runAgentContextList(w, projectRoot, agentID)
	default:
		return errkind.Errorf(errkind.Usage, "unknown context command: %s\nAvailable: used, list", args[0])
	}
}

// parseContextUsedArgs reads the ref and the reinjected --why, --ref-type and
// --none flags. Exactly one of a ref or --none is required.
func parseContextUsedArgs(args []string) (contextUsedArgs, error) {
	var out contextUsedArgs
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--none":
			out.none = true
		case a == "--why" || a == "--ref-type":
			if i+1 >= len(args) {
				return out, fmt.Errorf("%s requires a value", a)
			}
			if a == "--why" {
				out.why = args[i+1]
			} else {
				out.refType = args[i+1]
			}
			i++
		case strings.HasPrefix(a, "--why="):
			out.why = strings.TrimPrefix(a, "--why=")
		case strings.HasPrefix(a, "--ref-type="):
			out.refType = strings.TrimPrefix(a, "--ref-type=")
		case strings.HasPrefix(a, "--"):
			return out, fmt.Errorf("unknown flag %s", a)
		case out.ref == "":
			out.ref = strings.TrimSpace(a)
		default:
			return out, fmt.Errorf("unexpected argument %q: pass one ref", a)
		}
	}
	if out.none && out.ref != "" {
		return out, errors.New("pass a ref or --none, not both")
	}
	if !out.none && out.ref == "" {
		return out, errors.New("missing ref")
	}
	return out, nil
}

// agentContextTrace is where an AI coworker's context events live: the recording's
// context-trace.jsonl, or the per-agent cache file when not recording.
type agentContextTrace struct {
	writer    *contexttrace.Writer
	state     *session.RecordingState // nil when not recording
	readPaths []string
}

// resolveAgentContextTrace finds the trace an AI coworker's use reports read from
// and write to. Not recording, it reads prime's shared cache trace plus the
// per-agent usage cache file, and writes to the per-agent file.
func resolveAgentContextTrace(projectRoot, agentID string) (agentContextTrace, error) {
	if session.IsRecordingForAgent(projectRoot, agentID) {
		state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
		if err == nil && state != nil && state.SessionPath != "" {
			w := contexttrace.NewWriter(state.SessionPath)
			return agentContextTrace{writer: w, state: state, readPaths: []string{w.Path()}}, nil
		}
	}
	w, state := usageTraceWriter(projectRoot, agentID)
	if w == nil {
		return agentContextTrace{}, fmt.Errorf("no context trace available for agent %s", agentID)
	}
	if state != nil {
		return agentContextTrace{writer: w, state: state, readPaths: []string{w.Path()}}, nil
	}
	shared := filepath.Join(agentTraceCacheDir(projectRoot), contexttrace.FileName)
	return agentContextTrace{writer: w, readPaths: []string{shared, w.Path()}}, nil
}

func (t agentContextTrace) events() []contexttrace.Event {
	var all []contexttrace.Event
	for _, p := range t.readPaths {
		evs, err := contexttrace.ReadEvents(p)
		if err != nil {
			slog.Debug("context used: read trace", "path", p, "err", err)
		}
		all = append(all, evs...)
	}
	return all
}

func (t agentContextTrace) seq() int {
	if t.state == nil {
		return 0
	}
	return t.state.EntryCount
}

func runAgentContextUsed(w io.Writer, projectRoot, agentID string, args contextUsedArgs) error {
	trace, err := resolveAgentContextTrace(projectRoot, agentID)
	if err != nil {
		return err
	}

	ev := contexttrace.Event{
		Type:      contexttrace.EventUsed,
		Timestamp: contexttrace.Now(),
		Source:    contexttrace.SourceOnDemand,
		Mechanism: contexttrace.MechanismSelfReport,
		Seq:       trace.seq(),
	}
	out := contextUsedOutput{
		Success:   true,
		Type:      "context_used",
		AgentID:   agentID,
		Recording: trace.state != nil,
		Seq:       ev.Seq,
		TracePath: trace.writer.Path(),
	}

	if args.none {
		ev.Evidence = contexttrace.EvidenceNone
		out.Evidence = contexttrace.EvidenceNone
		out.Guidance = contextNoneGuidance(agentID)
	} else {
		surfaced := contextuse.Surfaced(trace.events())
		match, code, msg := resolveSurfacedRef(args.ref, args.refType, surfaced)
		if code != "" {
			return writeContextRefError(w, agentID, code, msg, surfaced)
		}
		refType := match.RefType
		if args.refType != "" {
			refType = args.refType
		}
		why := capContextWhy(args.why)
		ev.Evidence = contexttrace.EvidenceReported
		// The use names the surfaced item canonically and carries every other
		// name it went by, so it attaches whichever one a reader keys on.
		ev.Ref = contextuse.CanonicalRef(match.RefType, match.Ref)
		ev.Aliases = hitAliases(ev.Ref, append([]string{match.Ref}, match.Aliases...)...)
		ev.RefType = refType
		ev.Title = match.Title
		ev.URL = match.URL
		ev.Decision = why
		ev.SurfacedSeq = match.Seq
		out.Evidence = contexttrace.EvidenceReported
		out.Ref, out.RefType, out.Title, out.URL = ev.Ref, refType, match.Title, match.URL
		out.Why = why
		out.SurfacedSeq = match.Seq
		out.Guidance = contextUsedGuidance(agentID)
	}

	if err := trace.writer.Append(ev); err != nil {
		return fmt.Errorf("record context use: %w", err)
	}
	if ev.Evidence == contexttrace.EvidenceReported {
		recordStatuslineUse(projectRoot, trace.state, ev.RefType, ev.Ref)
	}
	return printContextJSON(w, out)
}

func runAgentContextList(w io.Writer, projectRoot, agentID string) error {
	trace, err := resolveAgentContextTrace(projectRoot, agentID)
	if err != nil {
		return err
	}
	events := trace.events()
	surfaced := contextuse.Surfaced(events)
	rollup := contextuse.Build(events, surfaced, 0, false)

	out := contextListOutput{
		Success:      true,
		Type:         "context_list",
		AgentID:      agentID,
		Recording:    trace.state != nil,
		Surfaced:     surfacedJSON(surfaced, rollup.Refs),
		UsedCount:    len(rollup.Refs),
		ReportedNone: rollup.ReportedNone,
		Guidance:     contextListGuidance(agentID),
	}
	return printContextJSON(w, out)
}

// resolveSurfacedRef matches a ref an AI coworker typed against what was
// surfaced, through every name an item went by — its ref and its aliases, raw
// and canonical — so a rec_ id, a /c/ link or a citation finds the cnv_ it
// names and a slug or dated directory finds its plan: exact ref, then
// canonical ref, then alias, then case-insensitive basename, then title, then
// a unique case-insensitive prefix of any name (one item counts once however
// many of its names match). A non-empty refType narrows the candidates. On
// failure it returns an error code and message instead of a match.
func resolveSurfacedRef(ref, refType string, surfaced []contextuse.SurfacedRef) (contextuse.SurfacedRef, string, string) {
	candidates := surfaced
	if refType != "" {
		candidates = nil
		for _, s := range surfaced {
			if strings.EqualFold(s.RefType, refType) {
				candidates = append(candidates, s)
			}
		}
	}
	for _, s := range candidates {
		if s.Ref == ref {
			return s, "", ""
		}
	}
	if canon := contextuse.CanonicalRef(refType, ref); canon != "" {
		for _, s := range candidates {
			if strings.EqualFold(contextuse.CanonicalRef(s.RefType, s.Ref), canon) {
				return s, "", ""
			}
		}
	}
	for _, s := range candidates {
		if s.Matches(ref) {
			return s, "", ""
		}
	}
	base := refBase(ref)
	for _, s := range candidates {
		for _, n := range surfacedNames(s) {
			if strings.EqualFold(refBase(n), base) {
				return s, "", ""
			}
		}
	}
	for _, s := range candidates {
		if s.Title != "" && strings.EqualFold(s.Title, ref) {
			return s, "", ""
		}
	}
	var prefixed []contextuse.SurfacedRef
	lower := strings.ToLower(ref)
	for _, s := range candidates {
		for _, n := range surfacedNames(s) {
			if strings.HasPrefix(strings.ToLower(n), lower) {
				prefixed = append(prefixed, s)
				break
			}
		}
	}
	switch len(prefixed) {
	case 1:
		return prefixed[0], "", ""
	case 0:
		return contextuse.SurfacedRef{}, contextErrUnknownRef, fmt.Sprintf("%q was not surfaced to this AI coworker", ref)
	default:
		names := make([]string, 0, len(prefixed))
		for _, s := range prefixed {
			names = append(names, s.Ref)
		}
		return contextuse.SurfacedRef{}, contextErrAmbiguousRef, fmt.Sprintf("%q matches %d surfaced refs: %s", ref, len(prefixed), strings.Join(names, ", "))
	}
}

// surfacedNames is every name a surfaced item went by: its ref and aliases as
// surfaced, then their canonical forms.
func surfacedNames(s contextuse.SurfacedRef) []string {
	return append(append([]string{s.Ref}, s.Aliases...), s.Names()...)
}

func refBase(ref string) string {
	return path.Base(strings.ReplaceAll(ref, "\\", "/"))
}

// capContextWhy keeps --why to one line of at most maxContextWhyRunes runes.
func capContextWhy(why string) string {
	why = strings.Join(strings.Fields(why), " ")
	if utf8.RuneCountInString(why) <= maxContextWhyRunes {
		return why
	}
	return string([]rune(why)[:maxContextWhyRunes])
}

// surfacedJSON joins surfaced refs with their strongest use evidence.
func surfacedJSON(surfaced []contextuse.SurfacedRef, used []contextuse.UseRef) []contextRefJSON {
	out := make([]contextRefJSON, 0, len(surfaced))
	for _, s := range surfaced {
		item := contextRefJSON{
			Ref: s.Ref, RefType: s.RefType, Via: s.Via, Title: s.Title,
			URL: s.URL, FromName: s.FromName, Seq: s.Seq,
		}
		for _, u := range used {
			if s.Matches(u.Ref) || strings.EqualFold(refBase(u.Ref), refBase(s.Ref)) {
				item.Evidence, item.Grade = u.Evidence, u.Grade
				break
			}
		}
		out = append(out, item)
	}
	return out
}

func writeContextRefError(w io.Writer, agentID, code, msg string, surfaced []contextuse.SurfacedRef) error {
	out := contextErrorOutput{
		Success:  false,
		Type:     "context_used",
		AgentID:  agentID,
		Error:    contextErrorDetail{Code: code, Message: msg},
		Surfaced: surfacedJSON(surfaced, nil),
		Guidance: fmt.Sprintf("Nothing was recorded. Pick one of surfaced[] (by ref, link, slug, file name, title, or a unique prefix) or run `ox agent %s context list`. Never invent a ref; if nothing surfaced changed your work, record nothing.", agentID),
	}
	if err := printContextJSON(w, out); err != nil {
		return err
	}
	return cli.Silent(errkind.Errorf(errkind.Usage, "context used: %s: %s", code, msg))
}

func printContextJSON(w io.Writer, v any) error {
	b, err := cli.MarshalJSONIndent(v)
	if err != nil {
		return fmt.Errorf("marshal context output: %w", err)
	}
	trackContextBytes(int64(len(b)))
	return cli.WriteJSONBytes(w, b)
}

func contextUsedGuidance(agentID string) string {
	return fmt.Sprintf("Recorded. Keep working. Whenever another surfaced item (a whisper, a prime doc, a query hit, a recalled session) changes what you do, record it at that moment: `ox agent %s context used <ref> --why \"<one line>\"`. Say nothing about items that did not change your work.", agentID)
}

func contextNoneGuidance(agentID string) string {
	return fmt.Sprintf("Recorded that no surfaced SageOx context changed your work. If one later does, record it at that moment: `ox agent %s context used <ref> --why \"<one line>\"`.", agentID)
}

func contextListGuidance(agentID string) string {
	return fmt.Sprintf("These are the SageOx items surfaced to you. When one changes what you do, record it at that moment: `ox agent %s context used <ref> --why \"<one line>\"`. Do not report an item just because it was surfaced; if none changed your work, do nothing (or run `ox agent %s context used --none` at the end).", agentID, agentID)
}
