package agentwork

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/envutil"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/paths"
)

// isolationUnsupported marks a CLI whose help or feature list shows it cannot
// isolate. A probe that fails to answer proves nothing, so it is never this.
type isolationUnsupported struct{ error }

// workerBaseEnv is what every worker CLI needs beyond envutil's base: USER for
// the macOS keychain, where Claude Code reads as logged out without it, a
// corporate network's proxy and CA settings, and the DO_NOT_TRACK opt-out.
var workerBaseEnv = []string{
	"USER",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "ALL_PROXY", "https_proxy", "http_proxy", "no_proxy", "all_proxy",
	"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "SSL_CERT_DIR",
	"DO_NOT_TRACK",
}

// workerEnv is a worker CLI's whole environment: envutil's sanitized base,
// workerBaseEnv, and the variables under the CLI's own provider prefixes, so
// SAGEOX_TOKEN, forge tokens and other credentials stay with ox. Recording is
// off so the worker never becomes a recorded session of its own, even if
// something inside it runs ox agent prime.
func workerEnv(providerPrefixes ...string) []string {
	environ := os.Environ()
	env := envutil.SanitizedEnv(environ, workerBaseEnv)
	for _, entry := range environ {
		for _, prefix := range providerPrefixes {
			if strings.HasPrefix(entry, prefix) {
				env = append(env, entry)
				break
			}
		}
	}
	// Last, so they override inherited copies: exec keeps the last duplicate.
	return append(env, "OX_SESSION_RECORDING=disabled", "SAGEOX_DAEMON=false")
}

// claudeCloudRoutes maps each Claude Code cloud-provider switch to the
// credential variables that provider's SDK reads.
var claudeCloudRoutes = map[string][]string{
	"CLAUDE_CODE_USE_BEDROCK":                {"AWS_"},
	"CLAUDE_CODE_USE_ANTHROPIC_AWS":          {"AWS_"},
	"CLAUDE_CODE_USE_MANTLE":                 {"AWS_"},
	"CLAUDE_CODE_USE_VERTEX":                 {"GOOGLE_", "GCLOUD_", "CLOUDSDK_", "CLOUD_ML_", "VERTEX_"},
	"CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD": {"GOOGLE_", "GCLOUD_", "CLOUDSDK_", "CLOUD_ML_", "VERTEX_"},
	"CLAUDE_CODE_USE_FOUNDRY":                {"AZURE_"},
}

// claudeEnvPrefixes are Claude Code's own variables, including its DISABLE_*
// opt-outs (telemetry, auto-update), plus the credentials of each cloud route
// switched on in the environment. A route switched on only in Claude Code's
// settings files gets no cloud credentials from ox's environment.
func claudeEnvPrefixes() []string {
	prefixes := []string{"ANTHROPIC_", "CLAUDE_", "DISABLE_"}
	for route, credentials := range claudeCloudRoutes {
		if os.Getenv(route) != "" {
			prefixes = append(prefixes, credentials...)
		}
	}
	return prefixes
}

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

// geminiIsolationFlags are what an isolated Gemini run depends on.
var geminiIsolationFlags = []string{"--policy", "--extensions", "--skip-trust"}

// geminiDenyAllTools is passed with --policy, which replaces the user's own
// policies. A deny without an argument pattern hides the tool from the model,
// and "*" matches every built-in and MCP tool; only an administrator's policy
// that allows a tool outranks it.
const geminiDenyAllTools = "[[rule]]\ntoolName = \"*\"\ndecision = \"deny\"\npriority = 999\n"

// geminiIsolation returns the args for an isolated Gemini run and the directory
// to run it in. Gemini will not run headless in an untrusted directory and
// --skip-trust trusts the working one, so it must hold no settings, hooks or
// MCP servers: never the Ledger. The directory is stable because Gemini keeps
// state under ~/.gemini for every directory it runs in, including each run's
// chat, which its default retention deletes after 30 days.
func geminiIsolation(ctx context.Context, binary string) ([]string, string, error) {
	help, err := probeOutput(ctx, binary, "--help")
	if err != nil {
		return nil, "", fmt.Errorf("cannot verify Gemini CLI isolation: %w", err)
	}
	if err := requireFlags(help, geminiIsolationFlags, "Gemini CLI"); err != nil {
		return nil, "", err
	}
	// Gemini ignores a policy file it cannot find and then offers every tool.
	// It splits the flag's value on commas, and would resolve a relative path
	// from the worker directory rather than from ox's.
	dir := filepath.Join(paths.CacheDir(), "gemini-worker")
	if !filepath.IsAbs(dir) || strings.Contains(dir, ",") {
		return nil, "", fmt.Errorf("gemini workspace %q must be an absolute path without commas", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", fmt.Errorf("gemini workspace: %w", err)
	}
	// Rewritten atomically before every run, so it is never missing or partial.
	policy := filepath.Join(dir, "deny-all-tools.toml")
	if err := fileutil.AtomicWriteBytes(policy, []byte(geminiDenyAllTools), 0o600); err != nil {
		return nil, "", fmt.Errorf("gemini policy: %w", err)
	}
	return []string{"--policy", policy, "--extensions", "none", "--skip-trust"}, dir, nil
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
