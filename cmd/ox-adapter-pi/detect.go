// detect.go handles adapter detection and diagnostics.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func handleDetect() (*adapterprotocol.DetectResponse, error) {
	if os.Getenv("AGENT_ENV") == "pi" {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "AGENT_ENV=pi"}, nil
	}

	// pi-mono's CLI entry sets process.env.PI_CODING_AGENT = "true"
	// (packages/coding-agent/src/cli.ts) and Node child_process inherits
	// process.env, so this propagates to every subprocess pi spawns —
	// including the bash tool. Strongest available signal that we're
	// running inside pi right now.
	if v := os.Getenv("PI_CODING_AGENT"); v == "true" || v == "1" {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "PI_CODING_AGENT=" + v}, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return &adapterprotocol.DetectResponse{Detected: false, Reason: "cannot determine home directory"}, nil
	}

	piDir := filepath.Join(home, ".pi")
	if info, err := os.Stat(piDir); err == nil && info.IsDir() {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "found ~/.pi/"}, nil
	}

	if _, err := exec.LookPath("pi"); err == nil {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "pi binary found in PATH"}, nil
	}

	return &adapterprotocol.DetectResponse{Detected: false, Reason: "~/.pi/ not found and pi not in PATH"}, nil
}

func handleDiagnose(p adapterprotocol.DiagnoseParams) (*adapterprotocol.DiagnoseResult, error) {
	var issues []adapterprotocol.DiagnoseIssue

	home, _ := os.UserHomeDir()
	piDir := filepath.Join(home, ".pi")
	if _, err := os.Stat(piDir); os.IsNotExist(err) {
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "pi:not-installed",
			Severity: "warning",
			Title:    "Pi coding agent not detected",
			Detail:   "~/.pi directory not found.",
		})
	}

	if p.RepoRoot != "" {
		agentsPath := filepath.Join(p.RepoRoot, "AGENTS.md")
		// a missing AGENTS.md is a missing hook, not a reason to stay quiet —
		// install-hooks creates the file
		data, err := os.ReadFile(agentsPath)
		switch {
		case err == nil && piBlockAlreadyPresent(string(data)):
			// accept either the current or legacy Pi marker — a pre-#527
			// install is still considered "installed" for diagnosis purposes
		case err == nil, errors.Is(err, os.ErrNotExist):
			// the file is readable and just lacks the marker, or doesn't
			// exist yet — both are repairable by writing/appending the
			// block, which install-hooks does safely and idempotently.
			detail := "AGENTS.md does not contain ox prime marker."
			if err != nil {
				detail = "AGENTS.md not found at " + agentsPath + "."
			}
			issues = append(issues, adapterprotocol.DiagnoseIssue{
				Slug:     "pi:hooks-missing",
				Severity: "warning",
				Title:    "Pi hooks not installed",
				Detail:   detail,
				Fix:      "ox integrate install --pi",
				// "ox" is the only allowlisted argv[0] for the doctor
				// auto-fix path (ox-adapter-pi itself is rejected by
				// adapterFixArgvAllowlist), so route through the in-process
				// `ox integrate install --pi` command rather than the
				// external adapter binary.
				FixArgv: []string{"ox", "integrate", "install", "--pi"},
				FixSafe: true,
			})
		default:
			// anything else (permission denied, a symlink loop, an I/O
			// error) means we can't even tell whether the marker is
			// present, let alone safely write over it — surface this as
			// unreadable rather than silently offering an auto-fix that
			// might clobber content we never verified.
			issues = append(issues, adapterprotocol.DiagnoseIssue{
				Slug:     "pi:agents-md-unreadable",
				Severity: "error",
				Title:    "AGENTS.md could not be read",
				Detail:   fmt.Sprintf("%s: %v — check file permissions and ownership.", agentsPath, err),
				FixSafe:  false,
			})
		}
	}

	if detail := checkTranscriptFormat(p.RepoRoot); detail != "" {
		// a reader that silently yields zero entries looks identical to an
		// idle session; say so instead
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "pi:format-unsupported",
			Severity: "error",
			Title:    "Pi transcript format is not supported",
			Detail:   detail,
		})
	}

	return &adapterprotocol.DiagnoseResult{OK: len(issues) == 0, Issues: issues}, nil
}

// checkTranscriptFormat reports a mismatch between the newest transcript's
// session-header version and what parsePiLine understands.
func checkTranscriptFormat(repoRoot string) string {
	path, err := findPiSession(repoRoot, "", "", "")
	if err != nil {
		return "" // no transcripts yet — nothing to judge
	}

	meta := extractPiMetadata(path)
	if meta == nil || meta.AgentVersion == "" {
		return ""
	}

	var version int
	if _, err := fmt.Sscanf(meta.AgentVersion, "pi-v%d", &version); err != nil {
		return ""
	}
	if piSupportedVersions[version] {
		return ""
	}

	return fmt.Sprintf(
		"%s is session format version %d; this adapter reads version 3. Sessions will record as empty until the reader is updated.",
		path, version)
}
