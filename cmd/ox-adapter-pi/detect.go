// detect.go handles adapter detection and diagnostics.
package main

import (
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
		// accept either the current or legacy Pi marker — a pre-#527
		// install is still considered "installed" for diagnosis purposes
		if err != nil || !piBlockAlreadyPresent(string(data)) {
			detail := "AGENTS.md does not contain ox prime marker."
			if err != nil {
				detail = "AGENTS.md not found at " + agentsPath + "."
			}
			issues = append(issues, adapterprotocol.DiagnoseIssue{
				Slug:     "pi:hooks-missing",
				Severity: "warning",
				Title:    "Pi hooks not installed",
				Detail:   detail,
				Fix:      "ox-adapter-pi install-hooks --repo-root " + p.RepoRoot + " --scope project",
				FixSafe:  true,
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
