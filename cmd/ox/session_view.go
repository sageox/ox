package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/spf13/cobra"
)

var sessionViewCmd = &cobra.Command{
	Use:   "view [session-name]",
	Short: "View a session",
	Long: `View a session in your preferred format.

Without arguments, shows the most recent session in the web viewer.
Default format is configurable via 'ox config set view_format web|text|json'.

Examples:
  ox session view                         # open in web viewer (default)
  ox session view 2026-01-06T14-32-ryan   # specific session in web viewer
  ox session view --text                  # view local session in terminal (markdown)
  ox session view --json                  # view local session as structured JSON
  ox session view --context                  # show what context the AI coworker had
  ox session view --context --json            # context trace as structured JSON
  ox session view --input /path/to/raw.jsonl`,
	RunE: runSessionView,
}

func init() {
	sessionCmd.AddCommand(sessionViewCmd)
	sessionViewCmd.Flags().Bool("text", false, "view local session in terminal (markdown)")
	sessionViewCmd.Flags().Bool("json", false, "view local session as structured JSON")
	sessionViewCmd.Flags().Bool("latest", false, "show most recent session")
	sessionViewCmd.Flags().StringP("input", "i", "", "input JSONL file path")
	sessionViewCmd.Flags().Bool("context", false, "show context trace (what context was available and influenced decisions)")
	sessionViewCmd.Flags().Bool("metadata", false, "show only metadata (no entries)")
	sessionViewCmd.Flags().Int("limit", 0, "limit number of entries shown (0 = all)")
}

func runSessionView(cmd *cobra.Command, args []string) error {
	textFlag, _ := cmd.Flags().GetBool("text")
	jsonFlag, _ := cmd.Flags().GetBool("json")
	contextFlag, _ := cmd.Flags().GetBool("context")
	inputPath, _ := cmd.Flags().GetString("input")
	metadataOnly, _ := cmd.Flags().GetBool("metadata")
	entryLimit, _ := cmd.Flags().GetInt("limit")

	// determine format
	format := ""
	flagCount := 0
	if textFlag {
		format = "text"
		flagCount++
	}
	if jsonFlag {
		format = "json"
		flagCount++
	}
	if flagCount > 1 {
		return fmt.Errorf("specify only one of --text or --json")
	}
	if format == "" {
		cfg, err := config.LoadUserConfig()
		if err != nil {
			format = "web"
		} else {
			format = cfg.GetViewFormat()
		}
	}

	// resolve session
	var storedSession *session.StoredSession
	var store *session.Store
	var projectRoot string

	if inputPath != "" {
		// read from arbitrary file path
		st, err := session.ReadSessionFromPath(inputPath)
		if err != nil {
			if errors.Is(err, session.ErrSessionNotFound) {
				return fmt.Errorf("file not found: %s", inputPath)
			}
			return fmt.Errorf("read session: %w", err)
		}
		storedSession = st
	} else {
		// use managed store
		var storeErr error
		store, projectRoot, storeErr = newSessionStore()
		if storeErr != nil {
			return storeErr
		}

		if len(args) > 0 {
			name := args[0]
			// ensure .jsonl extension for lookup
			if !strings.HasSuffix(name, ".jsonl") {
				name += ".jsonl"
			}
			// session name without .jsonl (used for folder-based lookups)
			sessionName := strings.TrimSuffix(name, ".jsonl")

			if format == "web" && !contextFlag {
				// web view only needs the session name to build the URL.
				// viewAsWeb validates meta.json exists in the ledger.
				return viewAsWeb(sessionName, projectRoot)
			}

			// context trace only needs the session name, not full session data
			if contextFlag {
				return runContextTrace(store, sessionName, projectRoot, jsonFlag, entryLimit)
			}

			// local formats: try local cache first, then fall back to ledger
			st, err := store.ReadSession(name)
			if err != nil {
				if errors.Is(err, session.ErrSessionNotFound) {
					st = tryReadFromLedger(name)
					if st == nil {
						// check if stub needs download; auto-download if user confirms
						if dl := store.CheckNeedsDownload(name); dl != "" {
							if !cli.ConfirmYesNo(fmt.Sprintf("Session %q is not downloaded locally. Download now?", name), true) {
								return nil
							}
							ledgerPath, ledgerErr := resolveLedgerPath()
							if ledgerErr != nil {
								return fmt.Errorf("session needs download but ledger not available: %w", ledgerErr)
							}
							sessionsDir := filepath.Join(ledgerPath, "sessions")
							if err := hydrateFromLedger(projectRoot, sessionsDir, dl, false); err != nil {
								return fmt.Errorf("download failed: %w\n\nTry manually: ox session download %s", err, dl)
							}
							// re-read the now-downloaded session
							st, err = store.ReadSession(name)
							if err != nil {
								return fmt.Errorf("read session after download: %w", err)
							}
						} else {
							return fmt.Errorf("session %q not found\nRun 'ox session list' to see available sessions", args[0])
						}
					}
				} else {
					return fmt.Errorf("read session %q: %w", args[0], err)
				}
			}
			storedSession = st
		} else {
			// no args: latest session
			latest, err := store.GetLatest()
			if err != nil {
				if errors.Is(err, session.ErrNoSessions) {
					fmt.Println()
					fmt.Println(sessionEmptyStyle.Render("  No sessions found."))
					fmt.Println()
					cli.PrintHint("Start a recording with 'ox agent <id> session start' to capture your development session.")
					return nil
				}
				return fmt.Errorf("get latest session: %w", err)
			}

			if format == "web" && !contextFlag {
				return viewAsWeb(latest.SessionName, projectRoot)
			}

			// context trace only needs the session name, not full session data
			if contextFlag {
				// find latest session that actually has a context trace
				latestName := latestSessionWithContextTrace()
				if latestName == "" {
					latestName = latest.SessionName // fallback
				}
				return runContextTrace(store, latestName, projectRoot, jsonFlag, entryLimit)
			}

			st, err := store.ReadSession(latest.Filename)
			if err != nil {
				return fmt.Errorf("read session %q: %w", latest.Filename, err)
			}
			storedSession = st
		}
	}

	// dispatch to renderer
	switch format {
	case "web":
		return viewAsWeb(storedSession.Info.SessionName, projectRoot)
	case "text":
		return viewAsText(store, storedSession, projectRoot)
	case "json":
		return viewAsJSON(storedSession, metadataOnly, entryLimit)
	default:
		return fmt.Errorf("unknown view format: %s", format)
	}
}

// runContextTrace resolves and renders context-trace events for a session.
func runContextTrace(store *session.Store, sessionName, projectRoot string, jsonOutput bool, limit int) error {
	events, err := resolveContextTrace(store, sessionName, projectRoot)
	if err != nil {
		return fmt.Errorf("resolve context trace for %s: %w", sessionName, err)
	}
	if len(events) == 0 {
		fmt.Println()
		printShowField("Session", sessionName)
		fmt.Println()
		fmt.Println(cli.StyleDim.Render("  No context trace available for this session."))
		cli.PrintHint("Context tracing records what team knowledge was provided during ox agent prime.")
		cli.PrintHint("Sessions recorded before context tracing was added won't have this data.")
		fmt.Println()
		return nil
	}
	if jsonOutput {
		return viewContextTraceJSON(events, sessionName, limit)
	}
	viewContextTraceText(events, sessionName)
	return nil
}

// latestSessionWithContextTrace finds the most recent session that has a
// context-trace.jsonl, searching across the XDG cache, ledger, and ledger cache.
// Returns empty string if none found.
func latestSessionWithContextTrace() string {
	var best string

	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return best
	}

	// scan ledger cache (where recordings land before upload)
	cacheSessionsDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions")
	best = findLatestWithContextTrace(cacheSessionsDir, best)

	// scan ledger sessions dir (uploaded sessions)
	ledgerSessionsDir := filepath.Join(ledgerPath, "sessions")
	best = findLatestWithContextTrace(ledgerSessionsDir, best)

	return best
}

// findLatestWithContextTrace scans a sessions directory for the most recent
// session that has a context-trace.jsonl file. Returns the best candidate
// (compared by name, which is timestamp-sortable).
func findLatestWithContextTrace(sessionsDir, currentBest string) string {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return currentBest
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name <= currentBest {
			continue // not newer
		}
		tracePath := filepath.Join(sessionsDir, name, contexttrace.FileName)
		if info, statErr := os.Stat(tracePath); statErr == nil && info.Size() > 0 {
			if !lfs.IsPointerFile(tracePath) {
				currentBest = name
			}
		}
	}
	return currentBest
}

// tryReadFromLedger attempts to read a full session from the ledger.
// Returns nil if the ledger is unavailable, the session isn't found,
// or the JSONL content isn't on disk (e.g., only in LFS).
func tryReadFromLedger(name string) *session.StoredSession {
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return nil
	}
	ledgerStore, err := session.NewStore(ledgerPath)
	if err != nil {
		return nil
	}
	st, err := ledgerStore.ReadSession(name)
	if err != nil {
		return nil
	}
	return st
}
