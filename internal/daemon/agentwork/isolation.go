package agentwork

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// isolationUnsupported marks a CLI whose help or feature list shows it cannot
// isolate. A probe that fails to answer proves nothing, so it is never this.
type isolationUnsupported struct{ error }

// isolatedEnv keeps an isolated summarizer from ever becoming a recorded
// session of its own, even if something inside it runs ox agent prime.
var isolatedEnv = []string{"OX_SESSION_RECORDING=disabled", "SAGEOX_DAEMON=false"}

// claudeIsolationFlags are what an isolated Claude run depends on. A Claude
// without them fails the run; it is never retried with broader permissions.
var claudeIsolationFlags = []string{"--safe-mode", "--tools", "--no-session-persistence"}

// claudeIsolatedArgs replaces the daemon's permission bypass. --safe-mode drops
// CLAUDE.md, skills, plugins, hooks and MCP servers; --tools "" leaves no
// built-in tool, so there is nothing a permission mode would govern.
func claudeIsolatedArgs() []string {
	return []string{"--safe-mode", "--tools", "", "--no-session-persistence"}
}

// codexIsolationFlags are required on `codex exec` for an isolated run.
var codexIsolationFlags = []string{"--ignore-user-config", "--ignore-rules", "--skip-git-repo-check", "--disable"}

// codexToolFeatures are the Codex features that give a run tools or reach
// beyond the prompt. Each one this Codex lists is disabled for an isolated run;
// a name it does not list is skipped, so releases that dropped one still work.
var codexToolFeatures = []string{
	"apps", "browser_use", "browser_use_external", "browser_use_full_cdp_access",
	"code_mode_host", "computer_use", "image_generation", "in_app_browser",
	"in_app_local_automation", "memories", "multi_agent", "plugins", "remote_plugin",
	"shell_tool", "skill_mcp_dependency_install", "skill_search", "sleep_tool",
	"standalone_web_search", "tool_suggest", "unified_exec", "view_image",
	"web_search_cached", "web_search_request", "workspace_dependencies",
}

// codexShellFeatures must be listed, and so disabled, for a run to count as
// isolated: a Codex that renamed them could still run commands and read files,
// so the run is refused rather than trusted.
var codexShellFeatures = []string{"shell_tool", "unified_exec"}

// probeOutput runs a help-style command under the probe deadline and returns
// its combined output. It never sends session content.
func probeOutput(ctx context.Context, binary string, args ...string) (string, error) {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, codexProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	out := &boundedCodexOutput{limit: 256 * 1024}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = time.Second
	setProcAttr(cmd)
	if err := cmd.Run(); err != nil {
		if parent.Err() == nil && ctx.Err() != nil {
			return "", fmt.Errorf("%s %s did not answer within %s: %w", binary, strings.Join(args, " "), codexProbeTimeout, ctx.Err())
		}
		return "", err
	}
	if out.overflow {
		return "", fmt.Errorf("%s %s output exceeds limit", binary, strings.Join(args, " "))
	}
	return out.buf.String(), nil
}

func requireFlags(help string, flags []string, cli string) error {
	for _, flag := range flags {
		if !strings.Contains(help, flag) {
			return isolationUnsupported{fmt.Errorf("isolated summarization requires %s %s; upgrade and retry", cli, flag)}
		}
	}
	return nil
}

func checkClaudeIsolation(ctx context.Context, binary string) error {
	help, err := probeOutput(ctx, binary, "--help")
	if err != nil {
		return fmt.Errorf("cannot verify Claude Code isolation: %w", err)
	}
	return requireFlags(help, claudeIsolationFlags, "Claude Code")
}

// codexIsolatedArgs verifies the isolation flags in the exec help the runner
// already probed and returns them, with one --disable per tool-bearing feature
// this Codex lists.
func codexIsolatedArgs(ctx context.Context, binary, help string) ([]string, error) {
	if err := requireFlags(help, codexIsolationFlags, "Codex"); err != nil {
		return nil, err
	}
	list, err := probeOutput(ctx, binary, "features", "list")
	if err != nil {
		return nil, fmt.Errorf("cannot list Codex features: %w", err)
	}
	known := map[string]bool{}
	for _, line := range strings.Split(list, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			known[fields[0]] = true
		}
	}
	for _, feature := range codexShellFeatures {
		if !known[feature] {
			return nil, isolationUnsupported{fmt.Errorf("isolated summarization cannot disable the shell: this Codex does not list the %s feature; update Codex or use another summarizer", feature)}
		}
	}
	args := []string{"--ignore-user-config", "--ignore-rules", "--skip-git-repo-check"}
	for _, feature := range codexToolFeatures {
		if known[feature] {
			args = append(args, "--disable", feature)
		}
	}
	return args, nil
}
