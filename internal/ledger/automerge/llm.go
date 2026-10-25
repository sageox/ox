package automerge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/llmprompt"
)

// systemPrompt is the instruction header prepended to every LLM merge
// request. It is intentionally terse: every token competes with the
// conflict content for the model's attention budget.
const systemPrompt = `You are a careful code-merge assistant. The user will give you a single file containing git merge conflict markers (<<<<<<<, =======, >>>>>>>). Produce ONLY the merged file content with no commentary, no markdown fences, no explanation. Preserve user intent on both sides; when in doubt prefer including more content over deleting. Remove all conflict markers from your output.`

// llmBinaryAllowlist enumerates the binary names automerge is permitted
// to invoke. Per ox-7esb: the LLMBinary is exec'd against attacker-
// influenced prompt content (merge-conflict bodies). If LLMBinary ever
// got sourced from team-context-synced config (e.g. .sageox/config.toml
// pulled from the cloud), an attacker who controls the config could
// redirect to /tmp/evil. Restricting argv[0] to a small allowlist makes
// that redirect harmless even if such a config path is ever introduced.
//
// Absolute paths are accepted regardless — the operator running ox
// already chose where to point this binary. The allowlist applies only
// to bare-name resolution (which goes through $PATH).
var llmBinaryAllowlist = map[string]bool{
	"claude": true,
	"gemini": true,
	"codex":  true,
}

// isAllowedLLMBinary returns true if binary names a permitted LLM tool.
// Absolute paths pass through (operator-chosen); bare names must match
// the allowlist; relative paths with a slash are refused outright.
func isAllowedLLMBinary(binary string) bool {
	if binary == "" {
		return false
	}
	if filepath.IsAbs(binary) {
		return true
	}
	// Reject relative paths containing a separator — those are neither
	// allowlisted bare names nor explicit absolute paths.
	if strings.ContainsRune(binary, filepath.Separator) {
		return false
	}
	return llmBinaryAllowlist[strings.ToLower(binary)]
}

// tryLLMTier attempts a semantic merge for each remaining conflicted path.
// On any per-file failure it returns immediately so the caller can abort
// the rebase rather than partially-resolve.
func (r *Resolver) tryLLMTier(ctx context.Context, repoPath string, paths []string) error {
	r.logger.Info("automerge.tier", "tier", "llm", "paths", len(paths), "binary", r.opts.LLMBinary)

	if !isAllowedLLMBinary(r.opts.LLMBinary) {
		return fmt.Errorf("%w: refusing non-allowlisted LLM binary %q (allowlist: claude, gemini, codex; or absolute path)",
			ErrLLMUnavailable, r.opts.LLMBinary)
	}
	if _, err := exec.LookPath(r.opts.LLMBinary); err != nil {
		return fmt.Errorf("%w: %s", ErrLLMUnavailable, r.opts.LLMBinary)
	}

	for _, p := range paths {
		if err := r.mergeOneWithLLM(ctx, repoPath, p); err != nil {
			return fmt.Errorf("merge %q: %w", p, err)
		}
	}
	return nil
}

// mergeOneWithLLM reads a single conflicted file, sends it to the LLM,
// writes back the merged result, and stages it.
func (r *Resolver) mergeOneWithLLM(ctx context.Context, repoPath, path string) error {
	full := filepath.Join(repoPath, path)

	// Refuse to follow symlinks. A malicious commit can stage a symlink
	// at a conflicted path that points outside repoPath; reading and
	// (worse) writing through it during merge would leak/clobber files
	// outside the ledger. Lstat + Mode().IsRegular() blocks this before
	// any os.ReadFile / os.WriteFile (which both follow symlinks).
	info, err := os.Lstat(full)
	if err != nil {
		return fmt.Errorf("lstat: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refuse to merge non-regular file: %s (mode=%v)", path, info.Mode())
	}

	data, err := os.ReadFile(full)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if len(data) > r.opts.MaxLLMFileBytes {
		return fmt.Errorf("file too large for LLM merge: %d > %d", len(data), r.opts.MaxLLMFileBytes)
	}

	prompt := buildPrompt(path, data)

	cctx, cancel := context.WithTimeout(ctx, r.opts.LLMTimeout)
	defer cancel()

	merged, err := r.runLLM(cctx, r.opts.LLMBinary, prompt)
	if err != nil {
		return fmt.Errorf("llm: %w", err)
	}
	merged = stripFences(merged)
	if gitutil.HasConflictMarkersBytes([]byte(merged)) {
		return fmt.Errorf("llm output still contains conflict markers")
	}
	// Marker-free output is only syntactically clean. The prompt asks the
	// model to keep both sides; this enforces it, so a merge that silently
	// drops or paraphrases one side's lines is refused instead of committed.
	if dropped := droppedLines(requiredLines(data), merged); dropped > 0 {
		r.logger.Warn("automerge.llm.dropped_content", "path", path, "lines_dropped", dropped)
		return fmt.Errorf("llm output dropped %d line(s) from the conflicted file", dropped)
	}
	if err := validateStructured(path, merged); err != nil {
		r.logger.Warn("automerge.llm.invalid_structure", "path", path, "error", err)
		return fmt.Errorf("llm output is not valid %s: %w", filepath.Ext(path), err)
	}

	// preserve original file mode (info from the Lstat above; we already
	// confirmed it's a regular file)
	mode := info.Mode().Perm()
	if err := os.WriteFile(full, []byte(merged), mode); err != nil {
		return fmt.Errorf("write merged: %w", err)
	}
	if _, err := gitutil.RunGit(ctx, repoPath, "add", "--", path); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	r.logger.Info("automerge.llm.merged", "path", path, "bytes_in", len(data), "bytes_out", len(merged))
	return nil
}

// requiredLines counts, per normalized non-blank line, how many times a
// lossless merge must contain it: once per occurrence outside the conflict
// hunks, plus, for each hunk, the larger of its "ours" and "theirs" counts.
// Taking the larger count lets a line both sides added appear once, while a
// line one side repeats must keep every copy. A diff3 base section
// ("|||||||" up to "=======") is skipped: it is the common ancestor, which a
// correct merge may legitimately discard.
func requiredLines(data []byte) map[string]int {
	const (
		outside = iota
		ours
		base
		theirs
	)
	required := make(map[string]int)
	var oursCount, theirsCount map[string]int
	state := outside
	for _, line := range strings.Split(string(data), "\n") {
		bare := strings.TrimSuffix(line, "\r")
		switch {
		case strings.HasPrefix(bare, gitutil.ConflictMarkerStart):
			state = ours
			oursCount, theirsCount = make(map[string]int), make(map[string]int)
		case state != outside && strings.HasPrefix(bare, "|||||||"):
			state = base
		case state != outside && bare == "=======":
			state = theirs
		case state != outside && strings.HasPrefix(bare, ">>>>>>>"):
			for l, n := range oursCount {
				required[l] += max(n, theirsCount[l])
			}
			for l, n := range theirsCount {
				if _, seen := oursCount[l]; !seen {
					required[l] += n
				}
			}
			state = outside
		default:
			n := normalizeMergeLine(bare)
			if n == "" {
				continue
			}
			switch state {
			case outside:
				required[n]++
			case ours:
				oursCount[n]++
			case theirs:
				theirsCount[n]++
			}
		}
	}
	return required
}

// droppedLines returns how many required occurrences merged is missing.
// Lines are compared after normalizeMergeLine, so re-indenting or adding the
// comma a union of two list entries needs is not a drop.
//
// A conflict where both sides rewrote the same line cannot pass: keeping
// both is the only merge this accepts. That is deliberate — the tier then
// fails exactly as it does with no LLM configured, and the conflict is left
// for a person rather than resolved by discarding someone's record.
func droppedLines(required map[string]int, merged string) int {
	have := make(map[string]int)
	for _, line := range strings.Split(merged, "\n") {
		have[normalizeMergeLine(line)]++
	}
	dropped := 0
	for line, want := range required {
		if have[line] < want {
			dropped += want - have[line]
		}
	}
	return dropped
}

// validateStructured parses merged as the format its extension names, so a
// union that keeps both sides of the same key is refused rather than staged.
// go-toml and yaml.v3 both reject duplicate keys. Other extensions pass.
func validateStructured(path, merged string) error {
	var v any
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		if !json.Valid([]byte(merged)) {
			return errors.New("invalid JSON")
		}
		// json.Valid accepts duplicate keys, and a decoder keeps only the
		// last one, so a union holding both sides' value would drop one.
		return rejectDuplicateJSONKeys(merged)
	case ".toml":
		return toml.Unmarshal([]byte(merged), &v)
	case ".yaml", ".yml":
		// Unmarshal reads only the first document; walk every one after "---".
		dec := yaml.NewDecoder(strings.NewReader(merged))
		for {
			var doc any
			if err := dec.Decode(&doc); err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
		}
	}
	return nil
}

// normalizeMergeLine strips whitespace and one trailing comma.
func normalizeMergeLine(line string) string {
	line = strings.TrimSpace(line)
	return strings.TrimSpace(strings.TrimSuffix(line, ","))
}

// rejectDuplicateJSONKeys walks data token by token and errors on the first
// object that defines a key twice. data must already be valid JSON.
func rejectDuplicateJSONKeys(data string) error {
	// keys is nil for an array; inValue means the object's next token is the
	// value for a key just read, not another key.
	type frame struct {
		keys    map[string]bool
		inValue bool
	}
	var stack []*frame
	dec := json.NewDecoder(strings.NewReader(data))
	dec.UseNumber() // no float64 conversion: valid numbers like 1e400 must not error
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if d, ok := tok.(json.Delim); ok && (d == '}' || d == ']') {
			stack = stack[:len(stack)-1]
			continue
		}
		if top != nil && top.keys != nil && !top.inValue {
			key := tok.(string) // an object key is always a string token
			if top.keys[key] {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			top.keys[key] = true
			top.inValue = true
			continue
		}
		if top != nil {
			top.inValue = false
		}
		switch tok {
		case json.Delim('{'):
			stack = append(stack, &frame{keys: map[string]bool{}})
		case json.Delim('['):
			stack = append(stack, &frame{})
		}
	}
}

// buildPrompt assembles the per-file prompt. Both 'ours' and 'theirs' are
// included verbatim via the conflict-marked content itself; we don't try
// to pre-parse the markers — the LLM is better at that than we are.
//
// Trust-boundary note: a ledger commit file whose content includes the
// literal string `END_FILE` on its own line would otherwise terminate the
// data section early, allowing subsequent attacker-controlled text to land
// in the prompt's instruction plane. Fixed delimiters are not safe for
// arbitrary file content. Use a per-call nonce so the delimiter cannot be
// pre-embedded by whoever authored the conflicting commit. See SECREVIEW
// llm-trust LOW.
func buildPrompt(path string, content []byte) string {
	nonce := llmprompt.Nonce()
	beginTag := "BEGIN_FILE_" + nonce
	endTag := "END_FILE_" + nonce
	var b strings.Builder
	b.Grow(len(systemPrompt) + len(content) + 256)
	b.WriteString(systemPrompt)
	b.WriteString("\n\nFile path: ")
	b.WriteString(path)
	fmt.Fprintf(&b, "\n\nFile content with conflict markers (everything between %s and %s):\n\n%s\n", beginTag, endTag, beginTag)
	b.Write(content)
	if len(content) > 0 && content[len(content)-1] != '\n' {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "%s\n\nReturn the merged file content only.", endTag)
	return b.String()
}

// stripFences removes common LLM artifacts (leading/trailing markdown
// code fences) defensively. The system prompt forbids them, but models
// occasionally ignore that.
//
// IMPORTANT: only strip fences when they are present. Do NOT trim
// arbitrary whitespace from the body — many file types (Python,
// Makefiles, YAML, multi-line strings) carry semantically meaningful
// leading indentation and trailing blank lines that must be preserved.
func stripFences(s string) string {
	// detect a leading fence on its own line, ignoring at most one optional
	// leading blank line that some models prepend.
	t := strings.TrimPrefix(s, "\n")
	if strings.HasPrefix(t, "```") {
		if nl := strings.IndexByte(t, '\n'); nl >= 0 {
			t = t[nl+1:]
		} else {
			t = ""
		}
		// also drop a single trailing fence (with optional trailing newline)
		// if present.
		trimTrailing := strings.TrimRight(t, "\n")
		if strings.HasSuffix(trimTrailing, "```") {
			t = trimTrailing[:len(trimTrailing)-3]
			// if the model emitted a final newline before the fence, keep it
			if strings.HasSuffix(t, "\n") {
				return t
			}
			return t + "\n"
		}
	}
	// no fence detected — return untouched (preserves whitespace)
	return s
}

// claudeStreamMessage models the subset of `claude --output-format
// stream-json` JSONL messages we care about. Mirrors the shape used by
// internal/daemon/agentwork/claude_runner.go but is duplicated here to
// keep this package free of daemon imports.
type claudeStreamMessage struct {
	Type   string `json:"type"`
	Result string `json:"result,omitempty"`
}

// runClaudeBinary spawns `claude --output-format stream-json
// --permission-mode plan -p <prompt>` and returns the result message's
// text. This is the production llmRunner; tests inject a fake.
//
// Permission mode rationale: the merge prompt feeds untrusted ledger
// content (potentially including hostile prompt-injection in conflict
// markers) to the model. We want pure text output — no tool execution
// at all. `plan` mode disables all tool use including Bash/Edit/Write.
// Even if the model is tricked into trying to "fix" something, it
// can't write to disk or execute commands.
func runClaudeBinary(ctx context.Context, binary, prompt string) (string, error) {
	args := []string{"--output-format", "stream-json", "--verbose", "--permission-mode", "plan", "-p", prompt}
	cmd := exec.CommandContext(ctx, binary, args...)
	setProcAttr(cmd) // process-group isolation so hung descendants get killed on cancel

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start: %w", err)
	}

	stderrDone := make(chan struct{})
	var stderrBuf []byte
	go func() {
		defer close(stderrDone)
		stderrBuf, _ = io.ReadAll(io.LimitReader(stderrPipe, 64*1024))
	}()

	type parseRes struct {
		result string
		err    error
	}
	parseCh := make(chan parseRes, 1)
	go func() {
		result, err := parseClaudeStream(stdoutPipe)
		parseCh <- parseRes{result: result, err: err}
	}()

	waitErr := cmd.Wait()
	<-stderrDone
	pr := <-parseCh

	if ctx.Err() != nil {
		return "", fmt.Errorf("timeout: %w", ctx.Err())
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return "", fmt.Errorf("claude exited %d: %s", exitErr.ExitCode(), strings.TrimSpace(string(stderrBuf)))
		}
		return "", fmt.Errorf("wait: %w", waitErr)
	}
	if pr.err != nil && pr.result == "" {
		return "", fmt.Errorf("parse: %w", pr.err)
	}
	if pr.result == "" {
		return "", fmt.Errorf("claude produced no result message")
	}
	return pr.result, nil
}

// parseClaudeStream scans JSONL output and returns the `result` message body.
func parseClaudeStream(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var result string
	var lastErr error
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg claudeStreamMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			lastErr = err
			continue
		}
		if msg.Type == "result" && msg.Result != "" {
			result = msg.Result
		}
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	if result != "" {
		return result, nil
	}
	return "", lastErr
}
