package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// checkExternalAdapters discovers external adapters, registers them into the
// adapter registry (so they're available for session detection), and runs their
// diagnose subcommand, converting results into doctor checkResults.
func checkExternalAdapters(opts doctorOptions) []checkResult {
	// register first so external adapters are in the registry for session detection
	// after doctor runs. DiscoverExternalAdapters is called again below because
	// RegisterExternalAdapters doesn't return the list — it only mutates the registry.
	if err := adapters.RegisterExternalAdapters(); err != nil {
		slog.Warn("external adapter registration failed during doctor", "error", err)
	}

	discovered := adapters.DiscoverExternalAdapters()
	if len(discovered) == 0 {
		return nil // no adapters found, silently skip
	}

	var results []checkResult
	gitRoot := findGitRoot()

	for _, ea := range discovered {
		name := ea.Name()

		diagnoseResult, err := ea.Diagnose(gitRoot, "project", version.Version)
		if err != nil {
			// adapter crashed or timed out -- report as a warning, continue
			severity := "warning"
			if errors.Is(err, adapters.ErrAdapterTimeout) {
				slog.Warn("adapter diagnose timed out", "adapter", name, "error", err)
			} else {
				slog.Warn("adapter diagnose failed", "adapter", name, "error", err)
			}
			results = append(results, adapterErrorToCheckResult(name, err, severity))
			continue
		}

		if diagnoseResult.OK && len(diagnoseResult.Issues) == 0 {
			results = append(results, PassedCheck(
				name+" adapter",
				"all checks passed",
			))
			continue
		}

		// If the target CLI isn't installed, skip ALL issues from this adapter.
		// Most users only have one or two agents — showing "hooks not installed"
		// for absent CLIs is noise, not signal.
		cliNotInstalled := false
		for _, issue := range diagnoseResult.Issues {
			if strings.HasSuffix(issue.Slug, ":not-installed") || issue.Slug == "not-installed" {
				cliNotInstalled = true
				break
			}
		}
		if cliNotInstalled {
			continue
		}

		for _, issue := range diagnoseResult.Issues {
			slug := adapterIssueSlug(name, issue.Slug)
			cr := adapterIssueToCheckResult(name, issue)
			cr.slug = slug

			// handle fix execution for adapter-reported fixes
			if (issue.Fix != "" || len(issue.FixArgv) > 0) && opts.shouldFixAdapterSlug(slug, issue.FixSafe) {
				fixErr := runAdapterFix(issue, opts.forceYes)
				if fixErr != nil {
					cr.detail = fmt.Sprintf("fix failed: %s", fixErr)
				} else {
					// re-run diagnose to verify the fix worked
					verified := verifyAdapterFix(ea, gitRoot, issue.Slug)
					if verified {
						cr = PassedCheck(cr.name, "fixed: "+issue.Title)
						cr.slug = slug
					} else {
						cr.detail = "fix applied but issue persists"
					}
				}
			}

			results = append(results, cr)
		}
	}

	return results
}

// checkRecordingAdapters verifies that every active recording's named adapter
// is discoverable. When brew ships `ox` but fails to link `ox-adapter-<name>`
// onto the user's PATH (or the bundled dir), recording state is created under
// the adapter's name but every hook invocation silently no-ops because the
// binary can't be forked. This was the top root-cause hypothesis for issue
// #519 (header-only raw.jsonl across every session).
func checkRecordingAdapters(projectRoot string) []checkResult {
	if projectRoot == "" {
		return nil
	}
	states, err := session.LoadAllRecordingStates(projectRoot)
	if err != nil || len(states) == 0 {
		return nil
	}

	// collect unique adapter names from active recordings
	needed := make(map[string]bool)
	for _, s := range states {
		if s.AdapterName != "" && s.AdapterName != "generic" {
			needed[s.AdapterName] = true
		}
	}
	if len(needed) == 0 {
		return nil
	}

	// ensure registry is populated with any external adapters on disk
	_ = adapters.RegisterExternalAdapters()

	var results []checkResult
	for name := range needed {
		if _, gerr := adapters.GetAdapter(name); gerr == nil {
			results = append(results, PassedCheck(
				"adapter "+name+" available",
				"registered for active recordings",
			))
			continue
		}
		detail := fmt.Sprintf(
			"active recording uses adapter %q but ox-adapter-%s is not discoverable. "+
				"Session hooks will silently skip every turn (see issue #519). "+
				"Check OX_ADAPTER_PATH, ~/.local/share/ox/adapters, or the directory containing the ox binary.",
			name, name,
		)
		results = append(results, CriticalCheck(
			"adapter "+name+" missing",
			"adapter binary not found for active recording",
			detail,
		).WithFixInfo("adapter:"+name+":missing", FixLevelCheckOnly))
	}

	return results
}

// adapterIssueSlug returns the namespaced slug for an adapter issue.
func adapterIssueSlug(adapterName, issueSlug string) string {
	return adapterName + ":" + issueSlug
}

// isAdapterSlug returns true if the slug contains a colon, indicating
// it is namespaced to an adapter (e.g., "claude-code:hooks-missing").
func isAdapterSlug(slug string) bool {
	return strings.Contains(slug, ":")
}

// adapterIssueToCheckResult converts a DiagnoseIssue into a doctor checkResult.
func adapterIssueToCheckResult(adapterName string, issue adapterprotocol.DiagnoseIssue) checkResult {
	displayName := adapterName + ": " + issue.Title

	detail := issue.Detail
	if issue.Fix != "" {
		if detail != "" {
			detail += " | "
		}
		detail += "fix: " + issue.Fix
	}

	switch issue.Severity {
	case "error":
		return CriticalCheck(displayName, issue.Title, detail)
	case "warning":
		return WarningCheck(displayName, issue.Title, detail)
	case "info":
		return InfoCheck(displayName, issue.Title, detail)
	default:
		return WarningCheck(displayName, issue.Title, detail)
	}
}

// adapterErrorToCheckResult creates a check result for an adapter that failed
// to run diagnose (crash, timeout, etc.).
func adapterErrorToCheckResult(adapterName string, err error, _ string) checkResult {
	return WarningCheck(
		adapterName+" adapter",
		"diagnose failed: "+err.Error(),
		"adapter may be misconfigured or crashing",
	).WithFixInfo(adapterName+":diagnose-error", FixLevelCheckOnly)
}

// shouldFixAdapterSlug checks whether an adapter-namespaced slug should be
// fixed. Adapter slugs are not in the DoctorCheckRegistry, so we handle them
// separately from core slugs.
func (opts doctorOptions) shouldFixAdapterSlug(slug string, fixSafe bool) bool {
	// check if this specific slug was requested via --fix-slug
	for _, s := range opts.fixSlugs {
		if s == slug {
			return true
		}
	}
	// if --fix flag is set and the fix is safe, apply it
	if opts.fix && fixSafe {
		return true
	}
	return false
}

// adapterFixArgvAllowlist enumerates the binaries an adapter is allowed
// to invoke via the auto-fix path. Per ox-saoy: arbitrary argv[0] +
// hijacked PATH = code execution on `ox doctor --fix --yes`. By
// restricting argv[0] to "git" and "ox" we keep the common cases
// (config writes, git-config tweaks, ox subcommand orchestration)
// working while making the auto-run path useless as a backdoor.
var adapterFixArgvAllowlist = map[string]bool{
	"git": true,
	"ox":  true,
}

// gitScopeEscalationFlags are argv tokens that escalate a fix beyond the current
// repo into machine-wide / persistent state. `git config --global|--system|
// --worktree` and git's inline `-c` are the persistence vectors; the same flags
// are treated as escalating for `ox` argv too (ADR-022 §8).
var gitScopeEscalationFlags = map[string]bool{
	"--global":   true,
	"--system":   true,
	"--worktree": true,
	"-c":         true,
}

// argvHasScopeEscalation reports whether any argument (after argv[0]) is a
// scope-escalating flag, including the `--global=...` joined form.
func argvHasScopeEscalation(argv []string) bool {
	for _, a := range argv[1:] {
		if gitScopeEscalationFlags[a] {
			return true
		}
		for flag := range gitScopeEscalationFlags {
			if strings.HasPrefix(a, flag+"=") {
				return true
			}
		}
	}
	return false
}

// runAdapterFix executes a fix produced by an adapter.
//
// Hardening contract (ox-saoy):
//   - Refuses Fix string-only issues — they're display-only now.
//   - Requires FixArgv != nil.
//   - Allowlists FixArgv[0] to {"git", "ox"}.
//   - FixSafe=false issues are surfaced as manual instructions even
//     when --yes is passed (auto-run only follows the FixSafe=true
//     contract; --yes can't elevate an unsafe argv to auto-run).
func runAdapterFix(issue adapterprotocol.DiagnoseIssue, forceYes bool) error {
	if !issue.FixSafe && !forceYes {
		return fmt.Errorf("fix requires confirmation (run with --yes or execute manually: %s)", issue.Fix)
	}
	if len(issue.FixArgv) == 0 {
		// Legacy Fix-string-only issue. Per ox-saoy this is no longer
		// auto-executed — the strings.Fields splitter mis-parsed quoted
		// arguments and gave hijacked-PATH attacks a comfortable home.
		// Surface as a manual instruction.
		return fmt.Errorf("auto-fix not available (display-only Fix; adapter must supply FixArgv): %s", issue.Fix)
	}
	argv0 := issue.FixArgv[0]
	if !adapterFixArgvAllowlist[argv0] {
		return fmt.Errorf("auto-fix refused: argv[0]=%q not in allowlist {git, ox}; manual instruction: %s",
			argv0, issue.Fix)
	}

	// A scope-escalating flag (e.g. `git config --global core.hooksPath=/tmp/evil`)
	// grants machine-wide persistence — every future git command in every repo
	// would run attacker code. FixSafe means "idempotent and non-destructive", NOT
	// "run a persistence-granting command without showing the user". Per ADR-022 §8
	// we surface the exact command and require explicit confirmation regardless of
	// FixSafe/--yes, and refuse outright when we can't prompt. A key denylist
	// (core.hooksPath, credential.helper, init.templateDir, …) is a losing game;
	// gating on the scope flag is the durable control.
	if argvHasScopeEscalation(issue.FixArgv) {
		cli.PrintWarning(fmt.Sprintf("adapter-requested fix modifies global/system state:\n  %s",
			strings.Join(issue.FixArgv, " ")))
		if !cli.IsInteractive() {
			return fmt.Errorf("auto-fix refused: %q modifies global/system state and cannot be confirmed non-interactively; apply manually: %s",
				strings.Join(issue.FixArgv, " "), issue.Fix)
		}
		if !cli.ConfirmYesNo("Run this adapter-requested command?", false) {
			return fmt.Errorf("auto-fix declined")
		}
	}

	// Surface the exact command to the user (stdout, not only slog) so an
	// adapter can never silently mutate state under `ox doctor --fix`.
	fmt.Printf("  running: %s\n", strings.Join(issue.FixArgv, " "))
	slog.Info("running adapter fix", "argv", issue.FixArgv)
	cmd := exec.Command(issue.FixArgv[0], issue.FixArgv[1:]...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// verifyAdapterFix re-runs diagnose on the adapter and checks whether the
// issue (identified by its original slug, without adapter prefix) is gone.
func verifyAdapterFix(ea *adapters.ExternalAdapter, repoRoot, issueSlug string) bool {
	result, err := ea.Diagnose(repoRoot, "project", version.Version)
	if err != nil {
		return false
	}
	for _, issue := range result.Issues {
		if issue.Slug == issueSlug {
			return false // issue still present
		}
	}
	return true
}
