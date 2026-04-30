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

	"github.com/sageox/ox/internal/gitutil"
)

// systemPrompt is the instruction header prepended to every LLM merge
// request. It is intentionally terse: every token competes with the
// conflict content for the model's attention budget.
const systemPrompt = `You are a careful code-merge assistant. The user will give you a single file containing git merge conflict markers (<<<<<<<, =======, >>>>>>>). Produce ONLY the merged file content with no commentary, no markdown fences, no explanation. Preserve user intent on both sides; when in doubt prefer including more content over deleting. Remove all conflict markers from your output.`

// tryLLMTier attempts a semantic merge for each remaining conflicted path.
// On any per-file failure it returns immediately so the caller can abort
// the rebase rather than partially-resolve.
func (r *Resolver) tryLLMTier(ctx context.Context, repoPath string, paths []string) error {
	r.logger.Info("automerge.tier", "tier", "llm", "paths", len(paths), "binary", r.opts.LLMBinary)

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
	if hasConflictMarkers([]byte(merged)) {
		return fmt.Errorf("llm output still contains conflict markers")
	}

	// preserve original file mode where possible
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(full); statErr == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(full, []byte(merged), mode); err != nil {
		return fmt.Errorf("write merged: %w", err)
	}
	if _, err := gitutil.RunGit(ctx, repoPath, "add", "--", path); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	r.logger.Info("automerge.llm.merged", "path", path, "bytes_in", len(data), "bytes_out", len(merged))
	return nil
}

// buildPrompt assembles the per-file prompt. Both 'ours' and 'theirs' are
// included verbatim via the conflict-marked content itself; we don't try
// to pre-parse the markers — the LLM is better at that than we are.
func buildPrompt(path string, content []byte) string {
	var b strings.Builder
	b.Grow(len(systemPrompt) + len(content) + 256)
	b.WriteString(systemPrompt)
	b.WriteString("\n\nFile path: ")
	b.WriteString(path)
	b.WriteString("\n\nFile content with conflict markers (everything between BEGIN_FILE and END_FILE):\n\nBEGIN_FILE\n")
	b.Write(content)
	if len(content) > 0 && content[len(content)-1] != '\n' {
		b.WriteByte('\n')
	}
	b.WriteString("END_FILE\n\nReturn the merged file content only.")
	return b.String()
}

// stripFences removes common LLM artifacts (leading/trailing markdown
// code fences) defensively. The system prompt forbids them, but models
// occasionally ignore that.
func stripFences(s string) string {
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "```") {
		// drop first fence line
		if nl := strings.IndexByte(t, '\n'); nl >= 0 {
			t = t[nl+1:]
		} else {
			t = ""
		}
	}
	if strings.HasSuffix(strings.TrimRight(t, "\n"), "```") {
		t = strings.TrimRight(t, "\n")
		t = t[:len(t)-3]
	}
	// preserve trailing newline if the original input had one — we don't
	// know either way, so default to ensuring exactly one trailing newline.
	t = strings.TrimRight(t, "\n") + "\n"
	return t
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
// --permission-mode bypassPermissions -p <prompt>` and returns the result
// message's text. This is the production llmRunner; tests inject a fake.
func runClaudeBinary(ctx context.Context, binary, prompt string) (string, error) {
	args := []string{"--output-format", "stream-json", "--verbose", "--permission-mode", "bypassPermissions", "-p", prompt}
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

