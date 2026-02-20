package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/lfs"
	"github.com/spf13/cobra"
)

var sessionUploadCmd = &cobra.Command{
	Use:   "upload <session-name>",
	Short: "Upload session content to the ledger",
	Long: `Upload session content to the ledger.

When a session stop fails during the network phase (content upload or git push),
the content files are saved locally but never uploaded. This command retries
the upload: content upload, meta.json write, git commit, and push.

The session-name can be the full directory name or just the agent ID suffix.

Example:
  ox session upload 2026-01-06T14-32-ryan-Ox7f3a
  ox session upload Ox7f3a`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		projectRoot, err := requireProjectRoot()
		if err != nil {
			return err
		}

		ledgerPath, err := resolveLedgerPath()
		if err != nil {
			return err
		}

		sessionsDir := filepath.Join(ledgerPath, "sessions")

		sessionName, err := resolveSessionInDir(sessionsDir, args[0])
		if err != nil {
			return err
		}

		sessionPath := filepath.Join(sessionsDir, sessionName)

		// verify session directory exists
		if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
			return fmt.Errorf("session not found: %s", sessionName)
		}

		// verify at least one content file exists
		if !hasContentFiles(sessionPath) {
			return fmt.Errorf("no content files found in session %s\nExpected at least one of: raw.jsonl, events.jsonl, summary.md, session.md, session.html", sessionName)
		}

		// build or create meta.json first (before LFS upload) to preserve metadata even if LFS fails
		uploadEndpoint := endpoint.GetForProject(projectRoot)
		meta, err := buildSessionMeta(sessionPath, sessionName, nil, uploadEndpoint)
		if err != nil {
			return fmt.Errorf("build meta.json: %w", err)
		}

		if err := lfs.WriteSessionMeta(sessionPath, meta); err != nil {
			return fmt.Errorf("write meta.json: %w", err)
		}

		// upload content files to LFS
		fmt.Printf("Uploading session %s...\n", sessionName)
		fileRefs, err := uploadSessionLFS(projectRoot, sessionPath)
		if err != nil {
			if errors.Is(err, api.ErrReadOnly) {
				fmt.Println("\nUpload skipped — you have read-only access to this public repo.")
				fmt.Println("To upload sessions, request team membership from an admin.")
				return nil
			}
			return fmt.Errorf("upload: %w", err)
		}

		// update meta.json with LFS file references
		meta.Files = fileRefs
		if err := lfs.WriteSessionMeta(sessionPath, meta); err != nil {
			return fmt.Errorf("update meta.json with LFS refs: %w", err)
		}

		// ensure sessions/.gitignore exists
		if err := ensureSessionsGitignore(sessionsDir); err != nil {
			return fmt.Errorf("ensure .gitignore: %w", err)
		}

		// commit and push
		if err := commitAndPushLedger(ledgerPath, sessionName); err != nil {
			return fmt.Errorf("commit and push: %w", err)
		}

		fmt.Printf("Session %s uploaded successfully\n", sessionName)
		return nil
	},
}

// hasContentFiles checks if a session directory has at least one content file.
func hasContentFiles(sessionPath string) bool {
	contentFiles := []string{
		"raw.jsonl",
		"events.jsonl",
		"summary.md",
		"session.md",
		"session.html",
	}
	for _, name := range contentFiles {
		if _, err := os.Stat(filepath.Join(sessionPath, name)); err == nil {
			return true
		}
	}
	return false
}

// buildSessionMeta reads existing meta.json or constructs one from the directory name.
// If fileRefs is nil, Files field is initialized as empty map.
// If fileRefs is non-nil, Files field is updated with the provided references.
// ep is the normalized endpoint used for auth token lookup.
func buildSessionMeta(sessionPath, sessionName string, fileRefs map[string]lfs.FileRef, ep string) (*lfs.SessionMeta, error) {
	// try reading existing meta.json first
	meta, err := lfs.ReadSessionMeta(sessionPath)
	if err == nil {
		// update the file manifest
		if fileRefs != nil {
			meta.Files = fileRefs
		} else if meta.Files == nil {
			meta.Files = make(map[string]lfs.FileRef)
		}
		return meta, nil
	}

	// no existing meta.json — construct from directory name
	ts, username, agentID := parseSessionDirName(sessionName)

	// count entries in raw.jsonl if present
	entryCount := countJSONLLines(filepath.Join(sessionPath, "raw.jsonl"))

	// read summary if present
	summary := readFileString(filepath.Join(sessionPath, "summary.md"))

	// initialize Files with empty map if nil
	if fileRefs == nil {
		fileRefs = make(map[string]lfs.FileRef)
	}

	return lfs.NewSessionMeta(sessionName, firstNonEmpty(username, getAuthenticatedUsername(ep), "unknown"), agentID, "unknown", ts).
		EntryCount(entryCount).
		Summary(summary).
		UserID(auth.GetUserID(ep)).
		WithFiles(fileRefs).
		Build(), nil
}

// parseSessionDirName extracts timestamp, username, and agent ID from a session
// directory name in the format: YYYY-MM-DDTHH-MM-<username>-<agentID>
func parseSessionDirName(name string) (createdAt time.Time, username, agentID string) {
	// try parsing the timestamp prefix (16 chars: "2006-01-02T15-04")
	const tsLayout = "2006-01-02T15-04"
	const tsLen = 16

	if len(name) > tsLen+1 && name[tsLen] == '-' {
		if t, err := time.Parse(tsLayout, name[:tsLen]); err == nil {
			createdAt = t
			rest := name[tsLen+1:] // everything after timestamp-

			// the last segment after '-' is the agent ID
			if lastDash := strings.LastIndex(rest, "-"); lastDash >= 0 {
				username = rest[:lastDash]
				agentID = rest[lastDash+1:]
			} else {
				agentID = rest
			}
			return
		}
	}

	// fallback: just use the last segment as agent ID
	if lastDash := strings.LastIndex(name, "-"); lastDash >= 0 {
		agentID = name[lastDash+1:]
	} else {
		agentID = name
	}
	return
}

// countJSONLLines counts lines in a JSONL file. Returns 0 if file doesn't exist.
func countJSONLLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		count++
	}
	return count
}

// readFileString reads a file and returns its content as a string.
// Returns empty string if the file doesn't exist or can't be read.
func readFileString(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// firstNonEmpty returns the first non-empty string from the arguments.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
