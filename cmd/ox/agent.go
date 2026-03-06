package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/session"
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
  ox agent <agent_id> session html         # Generate HTML viewer
  ox agent <agent_id> session import       # Import prior session (stdin or --file)
  ox agent <agent_id> session capture-prior # Capture prior history (schema-validated)
  ox agent <agent_id> session subagent-complete # Report subagent completion to parent
  ox agent <agent_id> session subagent-list # List subagent sessions
  ox agent <agent_id> session log            # Append a single conversation entry
  ox agent <agent_id> session recover       # Recover stale/crashed session
  ox agent <agent_id> session abort         # Discard active session (destructive)
  ox agent <agent_id> session delete <name> # Delete a completed session (destructive)

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

// agentListCmd lists active agent instances (hidden from help)
var agentListCmd = &cobra.Command{
	Use:    "list",
	Short:  "List active agent instances",
	Hidden: true, // debug only, not in help
	RunE:   runAgentList,
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
			return runAgentSessionHTML(inst, sessionArgs)
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
			return runAgentSessionAbort(inst, cmd)
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
	case "hook":
		return runAgentHook(subargs)
	default:
		available := "doctor, hook, query, session"
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
func runAgentList(cmd *cobra.Command, args []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	store, err := getInstanceStore(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to access instance store: %w", err)
	}

	instances, err := store.List()
	if err != nil {
		return fmt.Errorf("failed to list instances: %w", err)
	}

	if len(instances) == 0 {
		fmt.Println("No active AI coworkers.")
		fmt.Println("\nRun 'ox agent prime' to start one.")
		return nil
	}

	// build lookup of daemon context stats by agent ID
	daemonStats := make(map[string]daemon.InstanceInfo)
	if client := daemon.TryConnect(); client != nil {
		if daemonInstances, err := client.Instances(); err == nil {
			for _, di := range daemonInstances {
				daemonStats[di.AgentID] = di
			}
		}
	}

	dim := cli.StyleDim
	green := cli.StyleSuccess

	// filter to agents whose parent process is still running.
	// PID check is definitive (kill -0); falls back to daemon/recording heuristics
	// for instances created before PID tracking was added.
	var alive []*agentinstance.Instance
	for _, inst := range instances {
		if inst.IsProcessAlive() {
			alive = append(alive, inst)
			continue
		}
		// fallback for pre-PID instances: daemon knows about them or actively recording
		if inst.ParentPID == 0 {
			_, knownToDaemon := daemonStats[inst.AgentID]
			if knownToDaemon || session.IsRecordingForAgent(projectRoot, inst.AgentID) {
				alive = append(alive, inst)
			}
		}
	}

	if len(alive) == 0 {
		fmt.Println("No active AI coworkers.")
		fmt.Println("\nRun 'ox agent prime' to start one.")
		return nil
	}

	// check if workspaces are heterogeneous — only show column when useful
	workspaces := make(map[string]bool)
	for _, inst := range alive {
		ws := filepath.Base(projectRoot)
		if ds, ok := daemonStats[inst.AgentID]; ok && ds.WorkspacePath != "" {
			ws = filepath.Base(ds.WorkspacePath)
		}
		workspaces[ws] = true
	}
	showWorkspace := len(workspaces) > 1

	// build agent hierarchy — group subagents under their parent
	aliveSet := make(map[string]*agentinstance.Instance)
	children := make(map[string][]string) // parentID -> child IDs
	var roots []string                    // agents with no parent (or parent not alive)
	for _, inst := range alive {
		aliveSet[inst.AgentID] = inst
	}
	for _, inst := range alive {
		if inst.ParentAgentID != "" {
			if _, parentAlive := aliveSet[inst.ParentAgentID]; parentAlive {
				children[inst.ParentAgentID] = append(children[inst.ParentAgentID], inst.AgentID)
				continue
			}
		}
		roots = append(roots, inst.AgentID)
	}

	fmt.Printf("Active AI coworkers (%d):\n\n", len(alive))
	// dim headers — minimize non-data ink (Tufte)
	header := fmt.Sprintf("  %s  %s  %s  %s  %s  %s",
		dim.Render(fmt.Sprintf("%-8s", "ID")),
		dim.Render(fmt.Sprintf("%-10s", "Type")),
		dim.Render(fmt.Sprintf("%8s", "Tokens")),
		dim.Render(fmt.Sprintf("%5s", "Cmds")),
		dim.Render(fmt.Sprintf("%7s", "Uptime")),
		dim.Render(fmt.Sprintf("%-3s", "Rec")),
	)
	if showWorkspace {
		header += "  " + dim.Render(fmt.Sprintf("%-20s", "Workspace"))
	}
	fmt.Println(header)

	// renderRow prints one agent row with optional tree prefix
	renderRow := func(inst *agentinstance.Instance, prefix string) {
		agentType := inst.AgentType
		if agentType == "" {
			agentType = "-"
		}

		// token count from daemon (already estimated at source)
		tokens := fmt.Sprintf("%8s", "-")
		cmds := fmt.Sprintf("%5s", "-")
		if ds, ok := daemonStats[inst.AgentID]; ok && ds.CumulativeContextTokens > 0 {
			tokens = fmt.Sprintf("%8s", "~"+formatTokenCount(int(ds.CumulativeContextTokens)))
			cmds = fmt.Sprintf("%5d", ds.CommandCount)
		}

		// session uptime
		uptime := fmt.Sprintf("%7s", formatDurationShort(time.Since(inst.CreatedAt)))

		// recording status
		rec := dim.Render(fmt.Sprintf("%-3s", "-"))
		if session.IsRecordingForAgent(projectRoot, inst.AgentID) {
			rec = green.Render(fmt.Sprintf("%-3s", "rec"))
		}

		// ID column: tree prefix (dim) + agent ID (gold)
		idCol := dim.Render(prefix) + cli.StyleSecondary.Render(inst.AgentID)
		// pad to account for prefix width: 2 (indent) + 8 (id field)
		padded := fmt.Sprintf("%-8s", prefix+inst.AgentID)
		_ = padded // use visual width from styled rendering
		idWidth := len(prefix) + len(inst.AgentID)
		idPad := ""
		if idWidth < 8 {
			idPad = fmt.Sprintf("%*s", 8-idWidth, "")
		}

		row := fmt.Sprintf("  %s%s  %-10s  %s  %s  %s  %s",
			idCol, idPad,
			agentType,
			dim.Render(tokens),
			dim.Render(cmds),
			dim.Render(uptime),
			rec,
		)
		if showWorkspace {
			workspace := filepath.Base(projectRoot)
			if ds, ok := daemonStats[inst.AgentID]; ok && ds.WorkspacePath != "" {
				workspace = filepath.Base(ds.WorkspacePath)
			}
			row += "  " + dim.Render(fmt.Sprintf("%-20s", workspace))
		}
		fmt.Println(row)
	}

	// render tree: roots first, then their children indented
	for _, rootID := range roots {
		renderRow(aliveSet[rootID], "")
		for _, childID := range children[rootID] {
			renderRow(aliveSet[childID], " \u2514")
		}
	}

	return nil
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
