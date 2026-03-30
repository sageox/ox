package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/status"
	"github.com/sageox/ox/internal/session"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
	"github.com/spf13/cobra"
)

// contextBytesProduced accumulates the number of context bytes written by the
// current ox agent subcommand. Reset at the start of runWithAgentID and read
// in a deferred heartbeat after the command completes. Best-effort tracking.
// Bytes are converted to estimated tokens at the heartbeat boundary (sendContextHeartbeat).
var contextBytesProduced atomic.Int64

// trackContextBytes adds n bytes to the current command's context byte counter.
// Called by agent subcommands after producing output (e.g., JSON responses).
// Conversion to tokens happens later in sendContextHeartbeat, not here,
// so callers pass the natural measurement (bytes written).
func trackContextBytes(n int64) {
	contextBytesProduced.Add(n)
}

// SageOx is multiplayer - offline API mode is not supported.
// See internal/auth/feature.go for the multiplayer philosophy.
// Git repos (ledger, team context) work fine offline - only API calls require connectivity.

// Design decision: ox agent <agent_id> <cmd> pattern
//
// Why agent_id is required:
//   1. Session state management: tracks context across multiple commands in a session
//   2. Analytics: enables understanding of agent usage patterns and command sequences
//   3. Metrics: allows measuring session duration, command counts, and performance
//   4. Progressive disclosure: supports advanced model fine-tuning by tracking what
//      guidance was shown when, enabling smarter context-aware recommendations
//
// The short 6-char agent_id (e.g., "Oxa7b3") reduces context pollution vs the full
// 45-char OxSID (oxsid_01KCJECKEGETGX6HC80NRYVZ3P) while maintaining traceability.
// See: docs/plan/drifting-exploring-quill.md for full analysis

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "UX exposed to AI coding agents (Not for Human Use)",
	Long: `Agent commands for AI coding assistants.

Initialize a session:
  ox agent prime                              # Returns agent_id (e.g., "Oxa7b3")

Use the session:
  ox agent <agent_id> doctor                  # Check session health, find incomplete sessions
  ox agent <agent_id> session start        # Start recording
  ox agent <agent_id> session stop         # Stop and save
  ox agent <agent_id> session summarize    # Generate summary
  ox agent <agent_id> session import       # Import prior session (stdin or --file)
  ox agent <agent_id> session capture-prior # Capture prior history (schema-validated)
  ox agent <agent_id> session subagent-complete # Report subagent completion to parent
  ox agent <agent_id> session subagent-list # List subagent sessions
  ox agent <agent_id> session log            # Append a single conversation entry
  ox agent <agent_id> session recover       # Recover stale/crashed session
  ox agent <agent_id> session abort         # Discard active session (destructive)
  ox agent <agent_id> session delete <name> # Delete a completed session (destructive)

Check for team whispers:
  ox agent <agent_id> whisper              # Check for pending whispers from coworkers

Query team knowledge:
  ox agent <agent_id> query "search text"   # Semantic search (--limit, --team, --repo)

Redaction policy:
  ox agent redact                           # View full redaction policy (all sources)
  ox agent redact test "sample text"        # Test redaction against sample text

Example:
  $ ox agent prime
  Agent: Oxa7b3
  ...

  $ ox agent Oxa7b3 session start
  [starts recording the session]

  $ ox agent Oxa7b3 session stop
  [stops recording and saves session]

  $ ox agent Oxa7b3 doctor
  [check session health and find incomplete sessions]`,
	// allow arbitrary args for dispatcher pattern
	Args:                  cobra.ArbitraryArgs,
	DisableFlagParsing:    false,
	DisableFlagsInUseLine: true,
	RunE:                  runAgentDispatcher,
}

// agentListCmd lists active AI coworker instances.
var agentListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active AI coworkers and their session state",
	RunE:  runAgentList,
}

func init() {
	// register subcommands under agent
	agentCmd.AddCommand(agentPrimeCmd)
	agentCmd.AddCommand(agentListCmd)
	agentCmd.AddCommand(agentTeamCtxCmd)
	agentCmd.AddCommand(agentRedactCmd)

	// review flag - security audit mode for inspecting what agents receive
	// shows both human-readable summary and machine JSON output
	agentCmd.PersistentFlags().Bool("review", false,
		"security audit mode: human summary + machine output")

	// text flag - human-readable output for debugging or manual inspection
	agentCmd.PersistentFlags().Bool("text", false,
		"human-readable text output (overrides JSON default)")

	// force flag - skip confirmation for destructive operations (e.g., session abort, delete)
	agentCmd.PersistentFlags().Bool("force", false,
		"skip confirmation for destructive operations")
	_ = agentCmd.PersistentFlags().MarkHidden("force")

	// initialize prime command flags
	initAgentPrimeCmd()

	// register agent command with root
	rootCmd.AddCommand(agentCmd)
}

// runAgentDispatcher handles the ox agent <agent_id> <cmd> pattern
func runAgentDispatcher(cmd *cobra.Command, args []string) error {
	// no args = show help
	if len(args) == 0 {
		return cmd.Help()
	}

	firstArg := args[0]

	// check if first arg is a known subcommand
	for _, subcmd := range cmd.Commands() {
		if subcmd.Name() == firstArg {
			// let cobra handle it
			return nil
		}
	}

	// "hook" dispatched directly — no agent_id required.
	// hooks fire before prime, so no agent ID exists yet.
	if firstArg == "hook" {
		return runAgentHook(args[1:])
	}

	// check if first arg looks like an agent_id (Ox<4-char>)
	if agentinstance.IsValidAgentID(firstArg) {
		return runWithAgentID(cmd, firstArg, args[1:])
	}

	// first arg isn't a cobra subcommand or agent ID — check if it's
	// a known agent subcommand (e.g. "session", "doctor") being called
	// without an explicit agent ID: `ox agent session start`
	if isAgentSubcommand(firstArg) {
		envID := os.Getenv("SAGEOX_AGENT_ID")
		if agentinstance.IsValidAgentID(envID) {
			return runWithAgentID(cmd, envID, args)
		}
		return fmt.Errorf("no agent ID: %q requires an agent ID (run 'ox agent prime' first)", firstArg)
	}

	// unknown argument — check for common wrong-format patterns
	if msg := agentinstance.ClassifyBadID(firstArg); msg != "" {
		return fmt.Errorf("%s", msg)
	}
	return fmt.Errorf("unknown command or invalid agent_id: %s\nRun 'ox agent --help' for usage", firstArg)
}

// agentSubcommands are commands valid inside `runWithAgentID`.
// Used to distinguish `ox agent session start` (missing agent ID)
// from `ox agent typo` (genuinely unknown command).
var agentSubcommands = map[string]bool{
	"doctor":  true,
	"query":   true,
	"session": true,
	"whisper": true,
}

func isAgentSubcommand(name string) bool {
	if name == "distill" {
		return auth.IsMemoryEnabled()
	}
	return agentSubcommands[name]
}

// runWithAgentID executes a command using the specified agent instance
func runWithAgentID(cmd *cobra.Command, agentID string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing command after agent_id\nUsage: ox agent %s <command>", agentID)
	}

	// quick health check - non-blocking if daemon unavailable
	emitDaemonIssueWarnings()

	// resolve instance from store
	inst, err := resolveInstance(agentID)
	if err != nil {
		return err
	}

	// fire-and-forget heartbeat with agent ID (non-blocking)
	if gitRoot := findGitRoot(); gitRoot != "" {
		Heartbeat(gitRoot, nil, agentID)
	}

	// Third whisper delivery path: emits whispers to stdout on every
	// `ox agent <id> <cmd>` invocation. When the agent runs any ox command
	// (session log, query, whisper, etc.), pending whispers piggyback on the
	// command output. This is opportunistic — the primary channel is
	// handlePrompt (UserPromptSubmit hook) and the active pull is
	// `ox agent <id> whisper`.
	emitWhispers(agentID)

	subcommand := args[0]

	// reset context byte counter; deferred heartbeat sends accumulated bytes
	contextBytesProduced.Store(0)
	defer func() {
		if bytes := contextBytesProduced.Load(); bytes > 0 {
			sendContextHeartbeat(agentID, bytes, subcommand)
		}
	}()
	subargs := args[1:]

	switch subcommand {
	case "doctor":
		return runAgentDoctor(inst)
	case "session":
		if len(subargs) == 0 {
			return fmt.Errorf("session requires a subcommand\nUsage: ox agent %s session <start|stop|abort|delete|log|remind|summarize|html|record|plan|import|capture-prior|subagent-complete|subagent-list|recover>", inst.AgentID)
		}
		sessionCmd := subargs[0]
		sessionArgs := subargs[1:]
		switch sessionCmd {
		case "start":
			return runAgentSessionStart(inst, sessionArgs)
		case "stop":
			return runAgentSessionStop(inst)
		case "remind":
			return runAgentSessionRemind(inst)
		case "summarize":
			return runAgentSessionSummarize(inst, sessionArgs)
		case "html":
			return fmt.Errorf("session html command has been removed; use the web viewer at sageox.ai")
		case "record":
			return runAgentSessionRecord(inst, sessionArgs)
		case "log":
			return runAgentSessionLog(inst, sessionArgs)
		case "plan":
			return runAgentSessionPlan(inst)
		case "import":
			return runAgentSessionPlanHistory(inst, sessionArgs)
		case "capture-prior":
			return runAgentSessionCapturePrior(inst, sessionArgs)
		case "subagent-complete":
			return runAgentSessionSubagentComplete(inst, sessionArgs)
		case "subagent-list":
			return runAgentSessionSubagentList(inst)
		case "recover":
			return runAgentSessionRecover(inst)
		case "abort":
			return runAgentSessionAbort(inst, cmd, sessionArgs)
		case "delete":
			return runAgentSessionDelete(inst, cmd, sessionArgs)
		default:
			return fmt.Errorf("unknown session command: %s\nAvailable: start, stop, abort, delete, log, remind, summarize, html, record, plan, import, capture-prior, subagent-complete, subagent-list, recover", sessionCmd)
		}
	case "query":
		return runAgentQuery(inst, subargs)
	case "distill":
		if !auth.IsMemoryEnabled() {
			return fmt.Errorf("memory features are not enabled\nSet FEATURE_MEMORY=true to enable")
		}
		return runAgentDistill(inst, cmd)
	case "whisper":
		// `ox agent <id> whisper history` — show all whispers without advancing cursor
		if len(subargs) > 0 && subargs[0] == "history" {
			return runAgentWhisperHistory(inst)
		}
		return runAgentWhisper(inst)
	case "hook":
		return runAgentHook(subargs)
	default:
		available := "doctor, hook, query, session, whisper"
		if auth.IsMemoryEnabled() {
			available = "distill, " + available
		}
		return fmt.Errorf("unknown command: %s\nAvailable: %s", subcommand, available)
	}
}

// resolveInstance looks up an agent instance by agent_id
func resolveInstance(agentID string) (*agentinstance.Instance, error) {
	// find project root (look for .sageox directory)
	projectRoot, err := findProjectRoot()
	if err != nil {
		return nil, fmt.Errorf("could not find project root: %w\nRun 'ox agent prime' to initialize an instance", err)
	}

	store, err := getInstanceStore(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to access instance store: %w", err)
	}

	inst, err := store.Get(agentID)
	if err != nil {
		return nil, fmt.Errorf("instance not found: %s\nRun 'ox agent prime' to create a new instance", agentID)
	}

	return inst, nil
}

// findProjectRoot walks up from cwd looking for .sageox directory.
// OX_PROJECT_ROOT env var overrides discovery when set to a valid initialized project.
func findProjectRoot() (string, error) {
	if resolved := config.ResolveProjectRootOverride(); resolved != "" {
		return resolved, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	dir := cwd
	for {
		sageoxDir := filepath.Join(dir, ".sageox")
		if info, err := os.Stat(sageoxDir); err == nil && info.IsDir() {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// reached filesystem root, use cwd
			return cwd, nil
		}
		dir = parent
	}
}

// getUserSlug returns the current git user's slug for per-user session isolation
func getUserSlug() string {
	identity, err := repotools.DetectGitIdentity()
	if err != nil || identity == nil {
		return "anonymous"
	}
	return identity.Slug()
}

// getInstanceStore returns an instance store for the current user
func getInstanceStore(projectRoot string) (*agentinstance.Store, error) {
	return agentinstance.NewStoreForUser(projectRoot, getUserSlug())
}

// runAgentList lists active AI coworkers with context consumption and session info.
// Uses daemon as primary source (heartbeat-based), merges with disk instances for metadata.
func runAgentList(cmd *cobra.Command, args []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	// primary source: daemon heartbeat-tracked instances
	var daemonInstances []daemon.InstanceInfo
	if client := daemon.TryConnect(); client != nil {
		daemonInstances, _ = client.Instances()
	}

	// secondary source: disk-based instance store (has agent type, PID, parent info)
	store, err := getInstanceStore(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to access instance store: %w", err)
	}
	diskInstances, _ := store.List()
	diskByID := make(map[string]*agentinstance.Instance)
	for _, inst := range diskInstances {
		diskByID[inst.AgentID] = inst
	}

	if len(daemonInstances) == 0 && len(diskInstances) == 0 {
		fmt.Println("No active AI coworkers.")
		fmt.Println("\nRun 'ox agent prime' to start one.")
		return nil
	}

	// merge: daemon instances are authoritative for liveness, disk provides metadata
	type mergedInstance struct {
		AgentID       string
		AgentType     string
		Status        string
		Tokens        int64
		CommandCount  int
		LastHeartbeat time.Time
		LastWhisper   time.Time
		CreatedAt     time.Time
		WorkspacePath string
		ParentAgentID string
		Recording     bool
	}

	var merged []mergedInstance
	seen := make(map[string]bool)

	// daemon-known instances first (authoritative)
	for _, di := range daemonInstances {
		m := mergedInstance{
			AgentID:       di.AgentID,
			Status:        di.Status,
			Tokens:        di.CumulativeContextTokens,
			CommandCount:  di.CommandCount,
			LastHeartbeat: di.LastHeartbeat,
			LastWhisper:   di.LastWhisper,
			WorkspacePath: di.WorkspacePath,
			Recording:     session.IsRecordingForAgent(projectRoot, di.AgentID),
		}
		if disk, ok := diskByID[di.AgentID]; ok {
			m.AgentType = disk.AgentType
			m.CreatedAt = disk.CreatedAt
			m.ParentAgentID = disk.ParentAgentID
		} else {
			m.CreatedAt = di.LastHeartbeat
		}
		// daemon heartbeat info works cross-worktree; use as fallback
		// when disk instance store is not available (different worktree)
		if m.AgentType == "" && di.AgentType != "" {
			m.AgentType = di.AgentType
		}
		if m.ParentAgentID == "" && di.ParentAgentID != "" {
			m.ParentAgentID = di.ParentAgentID
		}
		merged = append(merged, m)
		seen[di.AgentID] = true
	}

	// disk instances not known to daemon (only if process is alive)
	for _, inst := range diskInstances {
		if seen[inst.AgentID] {
			continue
		}
		if !inst.IsProcessAlive() {
			continue
		}
		merged = append(merged, mergedInstance{
			AgentID:       inst.AgentID,
			AgentType:     inst.AgentType,
			Status:        "active",
			CreatedAt:     inst.CreatedAt,
			ParentAgentID: inst.ParentAgentID,
			WorkspacePath: projectRoot,
			Recording:     session.IsRecordingForAgent(projectRoot, inst.AgentID),
		})
	}

	if len(merged) == 0 {
		fmt.Println("No active AI coworkers.")
		fmt.Println("\nRun 'ox agent prime' to start one.")
		return nil
	}

	dim := cli.StyleDim
	green := cli.StyleSuccess

	// check if workspaces are heterogeneous
	workspaces := make(map[string]bool)
	for _, m := range merged {
		ws := filepath.Base(projectRoot)
		if m.WorkspacePath != "" {
			ws = filepath.Base(m.WorkspacePath)
		}
		workspaces[ws] = true
	}
	showWorkspace := len(workspaces) > 1

	// build agent hierarchy
	mergedByID := make(map[string]mergedInstance)
	children := make(map[string][]string)
	var roots []string
	for _, m := range merged {
		mergedByID[m.AgentID] = m
	}
	for _, m := range merged {
		if m.ParentAgentID != "" {
			if _, parentExists := mergedByID[m.ParentAgentID]; parentExists {
				children[m.ParentAgentID] = append(children[m.ParentAgentID], m.AgentID)
				continue
			}
		}
		roots = append(roots, m.AgentID)
	}

	fmt.Printf("Active AI coworkers (%d):\n\n", len(merged))
	header := fmt.Sprintf("  %s  %s  %s  %s  %s  %s  %s",
		dim.Render(fmt.Sprintf("%-8s", "ID")),
		dim.Render(fmt.Sprintf("%-10s", "Type")),
		dim.Render(fmt.Sprintf("%8s", "Tokens")),
		dim.Render(fmt.Sprintf("%5s", "Cmds")),
		dim.Render(fmt.Sprintf("%7s", "Uptime")),
		dim.Render(fmt.Sprintf("%-3s", "Rec")),
		dim.Render(fmt.Sprintf("%-9s", "Whisper")),
	)
	if showWorkspace {
		header += "  " + dim.Render(fmt.Sprintf("%-20s", "Workspace"))
	}
	fmt.Println(header)

	renderRow := func(m mergedInstance, prefix string) {
		agentType := m.AgentType
		if agentType == "" {
			agentType = "-"
		}

		tokens := fmt.Sprintf("%8s", "-")
		cmds := fmt.Sprintf("%5s", "-")
		if m.Tokens > 0 {
			tokens = fmt.Sprintf("%8s", "~"+status.FormatTokenCount(int(m.Tokens)))
			cmds = fmt.Sprintf("%5d", m.CommandCount)
		}

		uptime := fmt.Sprintf("%7s", status.FormatDurationShort(time.Since(m.CreatedAt)))

		rec := dim.Render(fmt.Sprintf("%-3s", "-"))
		if m.Recording {
			rec = green.Render(fmt.Sprintf("%-3s", "rec"))
		}

		idCol := dim.Render(prefix) + cli.StyleSecondary.Render(m.AgentID)
		idWidth := len(prefix) + len(m.AgentID)
		idPad := ""
		if idWidth < 8 {
			idPad = fmt.Sprintf("%*s", 8-idWidth, "")
		}

		// status indicator for non-active
		statusStr := ""
		switch m.Status {
		case "idle":
			statusStr = " " + dim.Render("idle")
		case "exited":
			statusStr = " " + dim.Render("exited")
		}

		whisper := fmt.Sprintf("%-9s", "-")
		if !m.LastWhisper.IsZero() {
			whisper = fmt.Sprintf("%-9s", formatTimeAgoShort(m.LastWhisper))
		}

		row := fmt.Sprintf("  %s%s  %-10s  %s  %s  %s  %s  %s%s",
			idCol, idPad,
			agentType,
			dim.Render(tokens),
			dim.Render(cmds),
			dim.Render(uptime),
			rec,
			dim.Render(whisper),
			statusStr,
		)
		if showWorkspace {
			workspace := filepath.Base(projectRoot)
			if m.WorkspacePath != "" {
				workspace = filepath.Base(m.WorkspacePath)
			}
			row += "  " + dim.Render(fmt.Sprintf("%-20s", workspace))
		}
		fmt.Println(row)
	}

	for _, rootID := range roots {
		renderRow(mergedByID[rootID], "")
		for _, childID := range children[rootID] {
			renderRow(mergedByID[childID], " \u2514")
		}
	}

	return nil
}

// murmur whisper budget: keep context lean so murmurs don't crowd out real work.
const (
	maxMurmurWhisperTokens   = 1024 // ~4096 bytes — hard cap on total murmur whisper content
	maxMurmurWhispersPerAgent = 1   // keep only the most recent murmur per authoring agent
	estimatedBytesPerToken    = 4   // rough byte-to-token ratio for English text
)

// runAgentWhisperHistory is a human debugging tool for inspecting what whispers
// ox has sent (or is about to send) to a given agent session.
//
// Agents receive whispers passively via the UserPromptSubmit hook — they never
// call this. Use it when debugging: "why hasn't my agent seen this whisper yet?"
//
// Unlike `ox agent <id> whisper`, this does NOT advance the delivery cursor,
// so it's safe to run repeatedly without side effects.
func runAgentWhisperHistory(inst *agentinstance.Instance) error {
	client := daemon.NewClientForCurrentRepoWithTimeout(500 * time.Millisecond)

	// collect all pages; break if no more entries or no pagination
	var allEntries []whisperstore.WhisperEntry
	var hasCursor bool
	var cursor time.Time
	before := time.Time{}
	for {
		resp, err := client.WhisperHistory(inst.AgentID, before, 0)
		if err != nil || resp == nil {
			if len(allEntries) == 0 {
				fmt.Fprintln(os.Stderr, "daemon unavailable — cannot retrieve whisper history")
				return nil
			}
			// mid-pagination IPC failure — history is incomplete
			fmt.Fprintln(os.Stderr, "warning: whisper history truncated (IPC error mid-pagination; showing partial results)")
			break
		}
		allEntries = append(allEntries, resp.Entries...)
		hasCursor = resp.HasCursor
		cursor = resp.Cursor
		if !resp.HasMore {
			break
		}
		before = resp.NextCursor
	}

	if len(allEntries) == 0 {
		fmt.Fprintln(os.Stdout, "No whispers in store.")
		return nil
	}

	// The cursor marks the last whisper the agent has consumed via the hook.
	// Entries created after the cursor are still pending; at or before it are delivered.
	var pending, delivered []whisperstore.WhisperEntry
	for _, e := range allEntries {
		if hasCursor && !e.CreatedAt.After(cursor) {
			delivered = append(delivered, e)
		} else {
			pending = append(pending, e)
		}
	}

	fmt.Fprintf(os.Stdout, "Whisper history for %s (%d total)\n\n", inst.AgentID, len(allEntries))

	if len(pending) > 0 {
		fmt.Fprintf(os.Stdout, "Pending (%d, not yet delivered):\n", len(pending))
		for _, e := range pending {
			fmt.Fprintf(os.Stdout, "  [%s] %s (%s) — %s\n",
				e.Importance, e.Topic, e.Source, e.CreatedAt.Local().Format("Jan 2 15:04:05"))
			if e.Content != "" {
				fmt.Fprintf(os.Stdout, "    %s\n", truncateString(e.Content, 120))
			}
		}
		fmt.Fprintln(os.Stdout)
	}

	if len(delivered) > 0 {
		fmt.Fprintf(os.Stdout, "Delivered (%d, already sent to agent):\n", len(delivered))
		for _, e := range delivered {
			fmt.Fprintf(os.Stdout, "  [%s] %s (%s) — %s\n",
				e.Importance, e.Topic, e.Source, e.CreatedAt.Local().Format("Jan 2 15:04:05"))
			if e.Content != "" {
				fmt.Fprintf(os.Stdout, "    %s\n", truncateString(e.Content, 120))
			}
		}
	}

	if !hasCursor {
		fmt.Fprintln(os.Stdout, "(cursor not set — all entries are pending)")
	}

	return nil
}


// runAgentWhisper handles `ox agent <id> whisper` — active pull for pending whispers.
//
// Whisper delivery uses belt-and-suspenders:
//   Belt:       UserPromptSubmit hook stdout (passive push, fires before each prompt)
//   Suspenders: `ox agent <id> whisper` via Bash tool (active pull, agent-initiated)
//
// The active pull exists because hook-based delivery has limitations:
//   - Hooks only fire at specific lifecycle events, not between prompts
//   - In long sessions, whispers may arrive after the last UserPromptSubmit fired
//   - The agent can choose when to check (e.g., "every few tool calls")
//   - Prime guidance table instructs the agent to run this periodically
//
// Uses the same WhisperStore cursor as hook delivery — if the hook already
// delivered pending whispers, this returns nothing. No double delivery.
func runAgentWhisper(inst *agentinstance.Instance) error {
	client := daemon.NewClientForCurrentRepoWithTimeout(500 * time.Millisecond)
	resp, err := client.Whispers(inst.AgentID, "normal", nil)
	if err != nil {
		// daemon unavailable — no whispers to report
		return nil
	}
	if resp == nil || len(resp.Entries) == 0 {
		// no pending whispers — emit nothing (clean stdout)
		return nil
	}

	entries := capMurmurWhispers(resp.Entries)
	projectRoot, _ := findProjectRoot()
	// filterMurmurReceive drops murmur entries when murmur_receive=off.
	// cursor was already advanced by daemon — these are discarded, not deferred
	entries = filterMurmurReceive(entries, projectRoot)
	if len(entries) == 0 {
		return nil
	}

	formatWhispers(os.Stdout, entries)
	return nil
}

// emitWhispers checks daemon for pending whisper entries and writes them to stdout.
// Non-blocking: if daemon is unavailable, silently returns.
//
// Called from two hook handlers:
//   1. handlePrompt (UserPromptSubmit) — PRIMARY: stdout reaches Claude's context
//   2. handleAfterTool (PostToolUse)   — FALLBACK: stdout discarded by Claude Code,
//      but may work for other agents (Cursor, Windsurf, etc.)
//
// Also called from runWithAgentID (line ~225) on every `ox agent <id> <cmd>`
// invocation, providing a third delivery path via command output.
//
// Uses AttentionNormal — agents receive critical + normal whispers,
// but not ambient ones, to avoid flooding agent context.
// Timeout is 100ms (vs 500ms for runAgentWhisper) because this runs in the
// hot path of every hook invocation and must not add perceptible latency.
func emitWhispers(agentID string) {
	// best-effort delivery — 100ms allows for daemon startup/load
	client := daemon.NewClientForCurrentRepoWithTimeout(100 * time.Millisecond)
	resp, err := client.Whispers(agentID, "normal", nil)
	if err != nil {
		return
	}
	if resp == nil || len(resp.Entries) == 0 {
		return
	}

	entries := capMurmurWhispers(resp.Entries)
	projectRoot, _ := findProjectRoot()
	// filterMurmurReceive drops murmur entries when murmur_receive=off.
	// cursor was already advanced by daemon — these are discarded, not deferred
	entries = filterMurmurReceive(entries, projectRoot)
	if len(entries) == 0 {
		return
	}

	formatWhispers(os.Stdout, entries)
}

// capMurmurWhispers limits murmur-sourced whispers to avoid blowing out agent context.
//
// Algorithm:
//  1. Deduplicate non-murmur whispers by (source, topic) — keep only the most
//     recent entry per key. Prevents nudge flooding when multiple identical
//     nudges accumulate between deliveries (e.g., after daemon restart).
//  2. Sort all murmurs by time (newest first) so recent signals win.
//  3. Cap at maxMurmurWhispersPerAgent per authoring agent — no single agent
//     dominates the whisper stream.
//  4. Enforce a total token budget. If the full set fits, include all.
//     If not, randomly sample from the candidates so that over many tool calls
//     in a long session, every agent's murmurs eventually get heard.
func capMurmurWhispers(entries []whisperstore.WhisperEntry) []whisperstore.WhisperEntry {
	var rawNonMurmur []whisperstore.WhisperEntry
	var allMurmurs []whisperstore.WhisperEntry

	murmurMaxAge := 24 * time.Hour
	now := time.Now()
	for _, e := range entries {
		if e.Source != "murmur" {
			rawNonMurmur = append(rawNonMurmur, e)
		} else if now.Sub(e.CreatedAt) <= murmurMaxAge {
			allMurmurs = append(allMurmurs, e)
		}
		// silently drop murmurs older than 24h
	}

	// deduplicate non-murmur entries by (source, topic) — keep newest per key
	nonMurmur := deduplicateBySourceTopic(rawNonMurmur)

	if len(allMurmurs) == 0 {
		return nonMurmur
	}

	// sort newest first
	sort.Slice(allMurmurs, func(i, j int) bool {
		return allMurmurs[i].CreatedAt.After(allMurmurs[j].CreatedAt)
	})

	// cap at N per agent (iterate in time order so newest are kept)
	agentCount := make(map[string]int)
	var capped []whisperstore.WhisperEntry
	for _, e := range allMurmurs {
		key := e.AgentID
		if key == "" {
			key = "_anonymous"
		}
		if agentCount[key] >= maxMurmurWhispersPerAgent {
			continue
		}
		agentCount[key]++
		capped = append(capped, e)
	}

	// check if everything fits in budget
	budgetBytes := maxMurmurWhisperTokens * estimatedBytesPerToken
	totalBytes := 0
	for _, e := range capped {
		totalBytes += len(e.Content)
	}

	if totalBytes <= budgetBytes {
		return append(nonMurmur, capped...)
	}

	// budget exceeded — randomly sample to be fair across agents over time.
	// shuffle then greedily fill budget, always keeping at least one.
	shuffled := make([]whisperstore.WhisperEntry, len(capped))
	copy(shuffled, capped)
	rand.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})

	var kept []whisperstore.WhisperEntry
	usedBytes := 0
	for _, e := range shuffled {
		contentBytes := len(e.Content)
		if usedBytes+contentBytes > budgetBytes && len(kept) > 0 {
			continue // skip this one, try smaller ones
		}
		kept = append(kept, e)
		usedBytes += contentBytes
	}

	// re-sort kept by time (newest first) for coherent output
	sort.Slice(kept, func(i, j int) bool {
		return kept[i].CreatedAt.After(kept[j].CreatedAt)
	})

	return append(nonMurmur, kept...)
}

// deduplicateBySourceTopic keeps only the most recent entry per (source, topic) key.
// Prevents flooding when identical whispers (e.g., murmur nudges) accumulate
// between deliveries — only the latest instance of each signal type is delivered.
func deduplicateBySourceTopic(entries []whisperstore.WhisperEntry) []whisperstore.WhisperEntry {
	if len(entries) <= 1 {
		return entries
	}
	best := make(map[string]whisperstore.WhisperEntry)
	for _, e := range entries {
		key := e.Source + "\x00" + e.Topic
		if existing, ok := best[key]; !ok || e.CreatedAt.After(existing.CreatedAt) {
			best[key] = e
		}
	}
	result := make([]whisperstore.WhisperEntry, 0, len(best))
	for _, e := range best {
		result = append(result, e)
	}
	return result
}

// filterMurmurReceive removes incoming murmur entries when murmur_receive is off.
// Only filters source="murmur" (from other coworkers), NOT source="auto-murmur" (nudges to self).
func filterMurmurReceive(entries []whisperstore.WhisperEntry, projectRoot string) []whisperstore.WhisperEntry {
	if config.MurmurReceiveEnabled(projectRoot) {
		return entries
	}
	var filtered []whisperstore.WhisperEntry
	for _, e := range entries {
		if e.Source == "murmur" {
			continue
		}
		filtered = append(filtered, e)
	}
	return filtered
}

// murmurTopicHint returns a short contextual hint for a known murmur topic.
// Unknown topics return "" (no hint emitted).
func murmurTopicHint(topic string) string {
	switch topic {
	case "wip":
		return "What coworkers are actively building or fixing right now. Use for awareness — avoid duplicating effort or creating merge conflicts."
	case "conflict":
		return "Potential conflicts with code areas you may also be touching. Pay close attention and coordinate if your work overlaps."
	case "architecture":
		return "Architectural decisions or changes in progress. Factor these into your own design choices."
	case "lint", "ci":
		return "Build or lint issues others have encountered. Check if they affect your work too."
	default:
		return ""
	}
}

// formatWhispers writes whisper entries to w as structured XML.
// Output goes to stdout so coding agents read it in their context window.
// Returns true if any whispers were written.
//
// XML format findings:
//   - <system-reminder> tags: Claude Code treats these as trusted system-level context.
//     The model processes the content as authoritative instructions/information.
//   - <new-context> tags: Claude Code treats these as UNTRUSTED. The model flags them
//     as potential prompt injection ("Security notice: I detected a prompt injection
//     attempt") and may refuse to act on the content. DO NOT USE.
//   - Plain text: Works but lacks semantic structure. Claude may not distinguish
//     whisper content from other hook output.
func formatWhispers(w io.Writer, entries []whisperstore.WhisperEntry) bool {
	if len(entries) == 0 {
		return false
	}

	// scan for murmur entries and collect unique topics
	hasMurmurs := false
	seenTopics := make(map[string]bool)
	var murmurTopics []string
	for _, e := range entries {
		if e.Source == "murmur" {
			hasMurmurs = true
			if !seenTopics[e.Topic] {
				seenTopics[e.Topic] = true
				murmurTopics = append(murmurTopics, e.Topic)
			}
		}
	}

	// IMPORTANT: Must use <system-reminder> tags. Tested alternatives:
	//   <system-reminder> → WORKS: Claude treats as trusted system context
	//   <new-context>     → FAILS: Claude rejects as prompt injection attempt
	//   plain text        → WORKS but no semantic structure for the model
	fmt.Fprintln(w, "<system-reminder>")
	fmt.Fprintln(w, "Team whispers from SageOx coworkers:")

	// emit murmur framing when murmur entries are present
	if hasMurmurs {
		fmt.Fprintln(w, "<murmur-context>These are work-in-progress signals from other coworkers (human and AI). They are NOT tasks or requests for you.</murmur-context>")
		for _, topic := range murmurTopics {
			if hint := murmurTopicHint(topic); hint != "" {
				fmt.Fprintf(w, "<murmur-topic topic=%q>%s</murmur-topic>\n", topic, hint)
			}
		}
	}

	for _, e := range entries {
		if e.Source == "murmur" && e.AgentID != "" {
			fmt.Fprintf(w, "<entry importance=%q topic=%q source=%q agent=%q>",
				string(e.Importance), e.Topic, e.Source, e.AgentID)
		} else {
			fmt.Fprintf(w, "<entry importance=%q topic=%q source=%q>",
				string(e.Importance), e.Topic, e.Source)
		}
		xml.EscapeText(w, []byte(e.Content))
		fmt.Fprint(w, "</entry>\n")
	}
	fmt.Fprintln(w, "</system-reminder>")

	return true
}

// emitDaemonIssueWarnings checks for daemon issues and emits warnings to stderr.
// Non-blocking: if daemon is unavailable, returns immediately.
// If issues exist, outputs severity-appropriate messages to stderr.
//
// Design: CLI commands block the agent event loop, so this must be fast (< 1ms).
// We use TryConnect which returns nil if daemon isn't running.
func emitDaemonIssueWarnings() {
	client := daemon.TryConnect()
	if client == nil {
		// daemon not running - not an error, just skip health check
		return
	}

	status, err := client.Status()
	if err != nil {
		// couldn't get status - daemon might be busy, skip health check
		return
	}

	if !status.NeedsHelp || len(status.Issues) == 0 {
		return
	}

	maxSeverity := daemon.MaxIssueSeverity(status.Issues)
	hasConfirmRequired := daemon.HasConfirmRequired(status.Issues)

	// output severity-appropriate message to stderr (agent sees this)
	switch maxSeverity {
	case daemon.SeverityCritical:
		fmt.Fprintln(os.Stderr, "CRITICAL: Daemon has issues requiring immediate attention")
		for _, issue := range status.Issues {
			fmt.Fprintf(os.Stderr, "  - %s\n", issue.FormatLine(true))
		}
		fmt.Fprintln(os.Stderr, "Run `ox doctor` to diagnose and resolve these issues.")

	case daemon.SeverityError:
		fmt.Fprintln(os.Stderr, "WARNING: Daemon has issues blocking normal operation")
		for _, issue := range status.Issues {
			fmt.Fprintf(os.Stderr, "  - %s\n", issue.FormatLine(true))
		}
		if hasConfirmRequired {
			fmt.Fprintln(os.Stderr, "Issues marked [CONFIRM REQUIRED] need human approval before resolution.")
		}
		fmt.Fprintln(os.Stderr, "The agent should investigate and resolve these issues.")

	case daemon.SeverityWarning:
		fmt.Fprintln(os.Stderr, "INFO: Daemon has issues that should be addressed soon")
		for _, issue := range status.Issues {
			fmt.Fprintf(os.Stderr, "  - %s\n", issue.FormatLine(false))
		}
		fmt.Fprintln(os.Stderr, "Run `ox doctor` when convenient to resolve.")
	}
}
