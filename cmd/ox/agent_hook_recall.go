package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/promptintent"
	"github.com/sageox/ox/internal/selfexec"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/tokens"
)

// LocalQueryRunner runs a recall query against the *local* ledger / cache
// and never reaches the network. Implementations must respect the caller's
// context deadline — the hook handler enforces a tight 100ms budget and
// will discard any results that arrive late.
//
// This interface is the seam between the UserPromptSubmit hook (built in
// ox-tshf) and the local query implementation (built in ox-m01h). The
// orchestrator wires the real implementation; tests substitute fakes.
type LocalQueryRunner interface {
	Query(ctx context.Context, prompt string, limit int) ([]LocalQueryResult, error)
}

// LocalQueryResult is one hit from the local recall index. Keep this
// schema deliberately minimal — the preamble we render has a 5-line
// cap and only needs a label + a path-or-pointer to follow up on.
type LocalQueryResult struct {
	Title   string `json:"title,omitempty"`
	Source  string `json:"source,omitempty"`     // e.g. "session", "ledger", "team-context"
	Snippet string `json:"snippet,omitempty"`    // short context blurb
	Path    string `json:"path,omitempty"`       // on-disk path when applicable
	Session string `json:"session_id,omitempty"` // session ID when applicable
	// SourceID is the ledger's own id for a non-session hit: the murmur ID or
	// the plan's dated slug. It is the ref the context trace records, because
	// it is what `ox murmur show` / `ox plan view` take back.
	SourceID string `json:"source_id,omitempty"`
}

// recallQueryTimeout caps the entire recall round-trip. The UserPromptSubmit
// hook runs on the user's input latency budget — anything slower than a
// human-perceptible blink (~100ms) makes the agent feel laggy, which
// would push users to disable hooks entirely.
const recallQueryTimeout = 100 * time.Millisecond

// recallMaxResults is the number of hits we ask the runner for. Anything
// more inflates the preamble past its 5-line budget.
const recallMaxResults = 5

// defaultRunner is the package-level runner used by the hook. Tests
// override it via SetLocalQueryRunner. Default is the shell-out runner
// that invokes `ox query --local`.
var defaultRunner LocalQueryRunner = &shellLocalQueryRunner{}

// SetLocalQueryRunner installs a runner for tests / orchestration. The
// previous runner is returned so callers can restore it on cleanup.
func SetLocalQueryRunner(r LocalQueryRunner) LocalQueryRunner {
	prev := defaultRunner
	defaultRunner = r
	return prev
}

// emitLocalRecallPreamble inspects the user prompt, runs a local recall
// query when intent + length gates pass, and writes a <system-reminder>
// preamble to w. It is purely additive — callers continue to invoke
// emitWhispers (or any other delivery path) regardless of what this
// function does.
//
// Fail-open contract: any error, timeout, panic in the runner, or empty
// result MUST result in no output and no returned error. The user's
// prompt must reach the model unchanged in all failure modes.
//
// When projectRoot and agentID name a live recording, each emitted hit is
// also recorded as a "provided" event in that session's context trace (refs
// only — the prompt never reaches the trace).
func emitLocalRecallPreamble(w io.Writer, projectRoot, agentID, prompt string) {
	// crash isolation: a panicking runner (network adapter dereference,
	// nil map, etc.) must not bring down the hook process — which would
	// take the agent's whole turn with it.
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("hook: local recall panic recovered", "panic", fmt.Sprint(r))
		}
	}()

	if !promptintent.LooksLikeRecall(prompt) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), recallQueryTimeout)
	defer cancel()

	// Trim only the matching surface — never the user's actual prompt (the
	// model still sees the full original text). Long planning preambles
	// would otherwise tokenize into too many AND terms (zero matches) or
	// flood OR ranking with noise (per ox-3ti2). The cap keeps the
	// intent sentence(s) intact while bounding the keyword set.
	matchText := promptintent.TrimForMatch(prompt)
	results, err := defaultRunner.Query(ctx, matchText, recallMaxResults)
	if err != nil {
		slog.Debug("hook: local recall query error", "error", err)
		return
	}
	if len(results) == 0 {
		return
	}

	emitted := writeRecallPreamble(w, results)
	traceRecallDelivery(projectRoot, agentID, emitted)
}

// recallHit is one hit that made it into the rendered preamble, with the
// token cost of its line.
type recallHit struct {
	Result LocalQueryResult
	Tokens int
}

// recallPreambleHeader names the self-report command so a hit that shapes
// the work can be credited at the moment of use.
const recallPreambleHeader = "[ox-recall] Local matches from this ledger (if one shapes your work: ox agent <id> context used <ref>):"

// writeRecallPreamble renders the recall preamble with a hard 5-line
// budget (one header + up to four hits). Truncation is silent — a
// noisier preamble would erode trust in the channel. Returns the hits that
// were rendered.
func writeRecallPreamble(w io.Writer, results []LocalQueryResult) []recallHit {
	const maxLines = 5
	lines := make([]string, 0, maxLines)
	lines = append(lines, recallPreambleHeader)
	var emitted []recallHit
	for _, r := range results {
		if len(lines) >= maxLines {
			break
		}
		label := r.Title
		if label == "" {
			label = r.Snippet
		}
		label = collapseWhitespace(label)
		if len(label) > 120 {
			label = label[:117] + "..."
		}
		ref := r.Path
		if ref == "" {
			ref = r.Session
		}
		src := r.Source
		if src == "" {
			src = "local"
		}
		line := fmt.Sprintf("- (%s) %s", src, label)
		if ref != "" {
			line += fmt.Sprintf(" — %s", ref)
		}
		lines = append(lines, line)
		emitted = append(emitted, recallHit{Result: r, Tokens: tokens.EstimateTokens(line)})
	}

	fmt.Fprintln(w, "<system-reminder>")
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	fmt.Fprintln(w, "</system-reminder>")
	return emitted
}

// traceRecallDelivery appends one "provided" event per rendered recall hit
// to the live recording's context trace, so the hit counts as surfaced.
//
// Runs inside the prompt hook's latency budget: local file I/O only, no
// network, no URL building. Silent no-op when not recording. Refs only —
// never the prompt, the match query, or a snippet (ADR-027 rule 4).
func traceRecallDelivery(projectRoot, agentID string, hits []recallHit) {
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("hook: recall trace panic recovered", "panic", fmt.Sprint(r))
		}
	}()
	if projectRoot == "" || agentID == "" || len(hits) == 0 {
		return
	}
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil || state == nil || state.SessionPath == "" {
		return
	}
	ts := contexttrace.Now()
	events := make([]contexttrace.Event, 0, len(hits))
	for _, h := range hits {
		ref, refType := recallTraceRef(h.Result)
		if ref == "" {
			continue
		}
		events = append(events, contexttrace.Event{
			Type:         contexttrace.EventProvided,
			Timestamp:    ts,
			Source:       contexttrace.SourceLocalRecall,
			Mechanism:    contexttrace.MechanismInjection,
			Ref:          ref,
			RefType:      refType,
			Title:        recallTraceTitle(h.Result.Title),
			Seq:          state.EntryCount,
			InlineTokens: h.Tokens,
		})
	}
	if err := contexttrace.NewWriter(state.SessionPath).AppendAll(events); err != nil {
		slog.Debug("hook: recall trace append failed", "agent_id", agentID, "error", err)
	}
}

// recallTraceRef picks the ref a recall hit is traced under: the session
// name for a session hit, the murmur ID or plan slug for those, and the
// path (relative when it already is) otherwise.
func recallTraceRef(r LocalQueryResult) (ref, refType string) {
	refType = recallRefType(r)
	switch {
	case refType == "session" && r.Session != "":
		return r.Session, refType
	case r.SourceID != "":
		return r.SourceID, refType
	case r.Path != "":
		return filepath.ToSlash(r.Path), refType
	case r.Session != "":
		return r.Session, "session"
	}
	return "", refType
}

// recallRefType maps a hit's source to a trace ref_type.
func recallRefType(r LocalQueryResult) string {
	switch r.Source {
	case "session", "murmur", "plan":
		return r.Source
	case "plan-feedback":
		return "plan"
	}
	if r.Session != "" && r.Path == "" {
		return "session"
	}
	return "doc"
}

// recallTraceTitle bounds a hit title for the trace. A title is a label, not
// content, but it is still capped so a malformed index cannot bloat the trace.
func recallTraceTitle(title string) string {
	title = collapseWhitespace(title)
	if len(title) > 120 {
		title = title[:117] + "..."
	}
	return title
}

func collapseWhitespace(s string) string {
	s = strings.TrimSpace(s)
	return strings.Join(strings.Fields(s), " ")
}

// extractPromptText pulls the user's prompt out of the raw hook stdin
// payload. Claude Code's UserPromptSubmit hook sends a JSON envelope with
// a top-level "prompt" string; other agents may use different field names
// in the future. Returns "" when no prompt field is present.
func extractPromptText(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var env struct {
		Prompt     string `json:"prompt"`
		UserPrompt string `json:"user_prompt"`
		Message    string `json:"message"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return ""
	}
	switch {
	case env.Prompt != "":
		return env.Prompt
	case env.UserPrompt != "":
		return env.UserPrompt
	case env.Message != "":
		return env.Message
	}
	return ""
}

// shellLocalQueryRunner is the default LocalQueryRunner. It shells out to
// the ox binary as `ox query --local <prompt> --json --limit N`.
//
// LOAD-BEARING COMMENT — DO NOT REPLACE `--local` WITH A REMOTE/CLOUD MODE.
// (Lint guard: cmd/ox/agent_hook_recall_lint_test.go pins this comment to
// the call site; deleting or weakening it fails the build.)
//
// Why this runs against the LOCAL ledger and not the SageOx cloud API:
//
//   - Privacy: this hook fires on EVERY user prompt. A remote query would
//     transit every keystroke the user typed into their agent — including
//     pasted secrets, proprietary code snippets, customer data, and
//     credentials the user did not consent to share for *recall*. The user
//     consented to ledger upload (explicit `ox` actions), not to a silent
//     egress on every turn.
//
//   - Latency: a network round-trip would add 200-2000ms per turn to a
//     hook that has a 100ms hard budget. Cloud-default would force the
//     budget up and make the agent perceptibly laggy on every keystroke
//     submission — the worst possible place to spend latency.
//
//   - Relevance: most prompts will not match remote content the local
//     ledger doesn't already mirror. Default-cloud is cost-for-nothing on
//     the majority of invocations: bandwidth, server CPU, and user trust
//     spent for a near-zero hit rate over local-only recall.
//
//   - Cloud escape hatch exists: opt-in cloud-mode lives behind
//     `hooks.userpromptsubmit.cloud_query` (sibling task ox-8wmo), which
//     also adds the redaction pipeline. There is NO scenario in which
//     this default path should reach the network. If you came here to
//     "fix" `--local` to remote because cloud results would be richer —
//     please re-read the privacy bullet, then send your richer-results
//     idea through ox-8wmo's redaction-and-consent gate instead.
type shellLocalQueryRunner struct{}

func (s *shellLocalQueryRunner) Query(ctx context.Context, prompt string, limit int) ([]LocalQueryResult, error) {
	oxPath, err := selfexec.Path()
	if err != nil {
		return nil, fmt.Errorf("locate ox executable: %w", err)
	}
	// SEE COMMENT ABOVE shellLocalQueryRunner: `--local` is a privacy and
	// latency contract, not an optimization. Do not change it to a remote
	// mode without going through the ox-8wmo cloud-opt-in pipeline.
	cmd := exec.CommandContext(ctx, oxPath, "query", "--local", prompt, "--json", "--limit", fmt.Sprintf("%d", limit))
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseLocalQueryJSON(out)
}

// parseLocalQueryJSON tolerates three shapes from `ox query --local --json`:
//
//	{"local_results":[ {doc_type, text, file_path, source_id}, ... ]}  (what ox query emits)
//	{"results":[ {...}, ... ]}
//	[ {...}, ... ]
//
// Any shape collapses to a flat slice here.
func parseLocalQueryJSON(b []byte) ([]LocalQueryResult, error) {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) == 0 {
		return nil, nil
	}
	if b[0] == '{' {
		var env struct {
			Results      []LocalQueryResult `json:"results"`
			LocalResults []struct {
				Text     string `json:"text"`
				DocType  string `json:"doc_type"`
				FilePath string `json:"file_path"`
				SourceID string `json:"source_id"`
			} `json:"local_results"`
		}
		if err := json.Unmarshal(b, &env); err != nil {
			return nil, err
		}
		if len(env.Results) > 0 {
			return env.Results, nil
		}
		out := make([]LocalQueryResult, 0, len(env.LocalResults))
		for _, lr := range env.LocalResults {
			r := LocalQueryResult{Source: lr.DocType, Snippet: lr.Text, Path: lr.FilePath, SourceID: lr.SourceID}
			if lr.DocType == "session" {
				r.Session = lr.SourceID
			}
			out = append(out, r)
		}
		return out, nil
	}
	var flat []LocalQueryResult
	if err := json.Unmarshal(b, &flat); err != nil {
		return nil, err
	}
	return flat, nil
}
