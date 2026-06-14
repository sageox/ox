package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/carts"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	ident "github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/repotools"
	"github.com/spf13/cobra"
)

// cartStartGuidance is the Layer-1 floor instruction returned with every
// `ox carts start --json`. It travels in the CLI JSON so all coding agents
// (Codex, Droid, Claude Code) receive the naming intent — not just Claude
// Code, which is the only adapter that installs the ox-* command bodies.
// The portable intent is "name this work unit after the cart title so
// teammates can correlate it"; the host-specific mechanism for applying that
// name (e.g. Claude Code's /rename) stays a Layer-2 note in the command body.
const cartStartGuidance = "Name this work unit (session/branch) after the cart title in kebab-case so teammates can correlate it with the cart. Then confirm the cart ID, title, and that it is now in_progress assigned to you."

// cartStartOutput wraps the started issue with Layer-1 guidance for the
// agent. The issue is embedded so all of its existing JSON fields are emitted
// unchanged alongside the added guidance.
type cartStartOutput struct {
	*carts.Issue
	Guidance string `json:"guidance,omitempty"`
}

var cartsCmd = &cobra.Command{
	Use:   "carts",
	Short: "Manage carts — trackable work items backed by DoltDB",
	Long: `Carts are work items with a dependency graph, backed by DoltDB.
Like Claude Code's task system: create, list, update, close, and wire up dependencies.`,
}

// --- ox carts create ---

var cartsCreateCmd = &cobra.Command{
	Use:   "create TITLE",
	Short: "Create a new cart",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runCartsCreate,
}

func init() {
	cartsCreateCmd.Flags().StringP("type", "t", "task", "Cart type: bug, task, feature, epic, chore")
	cartsCreateCmd.Flags().IntP("priority", "p", 2, "Priority: 0 (critical) to 4 (backlog)")
	cartsCreateCmd.Flags().StringP("assignee", "a", "", "Assign to a team member")
	cartsCreateCmd.Flags().StringP("description", "d", "", "Description")
}

func runCartsCreate(cmd *cobra.Command, args []string) error {
	store, identity, err := openCartsStore(cmd)
	if err != nil {
		return err
	}
	defer store.Close()

	issueType, _ := cmd.Flags().GetString("type")
	priority, _ := cmd.Flags().GetInt("priority")
	assignee, _ := cmd.Flags().GetString("assignee")
	description, _ := cmd.Flags().GetString("description")

	issue, err := store.Create(cmd.Context(), carts.CreateOpts{
		Title:       strings.Join(args, " "),
		Description: description,
		IssueType:   carts.IssueType(issueType),
		Priority:    priority,
		Assignee:    assignee,
		Creator:     identity.Name,
	})
	if err != nil {
		return fmt.Errorf("create cart: %w", err)
	}

	if isJSON(cmd) {
		data, _ := carts.FormatIssueJSON(issue)
		fmt.Println(string(data))
	} else {
		fmt.Printf("Created %s: %s\n", issue.ID, issue.Title)
	}
	return nil
}

// --- ox carts list ---

var cartsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List carts",
	RunE:  runCartsList,
}

func init() {
	cartsListCmd.Flags().String("status", "", "Filter by status (comma-separated)")
	cartsListCmd.Flags().StringP("assignee", "a", "", "Filter by assignee")
	cartsListCmd.Flags().IntP("priority", "p", -1, "Filter by priority")
	cartsListCmd.Flags().StringP("type", "t", "", "Filter by type")
	cartsListCmd.Flags().IntP("limit", "n", 50, "Max results")
}

func runCartsList(cmd *cobra.Command, args []string) error {
	store, _, err := openCartsStore(cmd)
	if err != nil {
		return err
	}
	defer store.Close()

	status, _ := cmd.Flags().GetString("status")
	assignee, _ := cmd.Flags().GetString("assignee")
	priority, _ := cmd.Flags().GetInt("priority")
	issueType, _ := cmd.Flags().GetString("type")
	limit, _ := cmd.Flags().GetInt("limit")

	filter := carts.IssueFilter{
		Status:    status,
		Assignee:  assignee,
		IssueType: issueType,
		Limit:     limit,
	}
	if priority >= 0 {
		filter.Priority = &priority
	}

	issues, err := store.List(cmd.Context(), filter)
	if err != nil {
		return err
	}

	if isJSON(cmd) {
		data, _ := carts.FormatIssueListJSON(issues)
		fmt.Println(string(data))
	} else {
		if len(issues) == 0 {
			fmt.Println("No carts found.")
			return nil
		}
		for _, issue := range issues {
			fmt.Println(carts.FormatIssueRow(issue))
		}
		fmt.Printf("\n%d cart(s)\n", len(issues))
	}
	return nil
}

// --- ox carts ready ---

var cartsReadyCmd = &cobra.Command{
	Use:   "ready",
	Short: "Show open, unblocked carts",
	RunE:  runCartsReady,
}

func runCartsReady(cmd *cobra.Command, args []string) error {
	store, _, err := openCartsStore(cmd)
	if err != nil {
		return err
	}
	defer store.Close()

	issues, err := store.Ready(cmd.Context())
	if err != nil {
		return err
	}

	if isJSON(cmd) {
		data, _ := carts.FormatIssueListJSON(issues)
		fmt.Println(string(data))
	} else {
		if len(issues) == 0 {
			fmt.Println("No carts ready.")
			return nil
		}
		for _, issue := range issues {
			fmt.Println(carts.FormatIssueRow(issue))
		}
		fmt.Printf("\n%d cart(s) ready\n", len(issues))
	}
	return nil
}

// --- ox carts show ---

var cartsShowCmd = &cobra.Command{
	Use:   "show ID",
	Short: "Show cart details",
	Args:  cobra.ExactArgs(1),
	RunE:  runCartsShow,
}

func runCartsShow(cmd *cobra.Command, args []string) error {
	store, _, err := openCartsStore(cmd)
	if err != nil {
		return err
	}
	defer store.Close()

	issue, err := store.Get(cmd.Context(), args[0])
	if err != nil {
		return fmt.Errorf("get cart: %w", err)
	}

	issue.Dependencies, _ = store.GetDependencies(cmd.Context(), issue.ID)

	if isJSON(cmd) {
		data, _ := carts.FormatIssueJSON(issue)
		fmt.Println(string(data))
	} else {
		fmt.Print(carts.FormatIssueDetail(issue))
		if len(issue.Dependencies) > 0 {
			fmt.Println("\nDependencies:")
			for _, d := range issue.Dependencies {
				fmt.Printf("  %s %s %s\n", d.IssueID, d.Type, d.DependsOnID)
			}
		}
	}
	return nil
}

// --- ox carts update ---

var cartsUpdateCmd = &cobra.Command{
	Use:   "update ID",
	Short: "Update a cart",
	Args:  cobra.ExactArgs(1),
	RunE:  runCartsUpdate,
}

func init() {
	cartsUpdateCmd.Flags().String("title", "", "New title")
	cartsUpdateCmd.Flags().StringP("description", "d", "", "New description")
	cartsUpdateCmd.Flags().StringP("type", "t", "", "New type")
	cartsUpdateCmd.Flags().IntP("priority", "p", -1, "New priority")
	cartsUpdateCmd.Flags().String("status", "", "New status")
	cartsUpdateCmd.Flags().StringP("assignee", "a", "", "New assignee")
}

func runCartsUpdate(cmd *cobra.Command, args []string) error {
	store, _, err := openCartsStore(cmd)
	if err != nil {
		return err
	}
	defer store.Close()

	opts := carts.UpdateOpts{}

	if cmd.Flags().Changed("title") {
		v, _ := cmd.Flags().GetString("title")
		opts.Title = &v
	}
	if cmd.Flags().Changed("description") {
		v, _ := cmd.Flags().GetString("description")
		opts.Description = &v
	}
	if cmd.Flags().Changed("type") {
		v, _ := cmd.Flags().GetString("type")
		t := carts.IssueType(v)
		opts.IssueType = &t
	}
	if cmd.Flags().Changed("priority") {
		v, _ := cmd.Flags().GetInt("priority")
		opts.Priority = &v
	}
	if cmd.Flags().Changed("status") {
		v, _ := cmd.Flags().GetString("status")
		s := carts.Status(v)
		opts.Status = &s
	}
	if cmd.Flags().Changed("assignee") {
		v, _ := cmd.Flags().GetString("assignee")
		opts.Assignee = &v
	}

	issue, err := store.Update(cmd.Context(), args[0], opts)
	if err != nil {
		return err
	}

	if isJSON(cmd) {
		data, _ := carts.FormatIssueJSON(issue)
		fmt.Println(string(data))
	} else {
		fmt.Printf("Updated %s\n", issue.ID)
	}
	return nil
}

// --- ox carts start ---

var cartsStartCmd = &cobra.Command{
	Use:   "start ID",
	Short: "Claim a cart and start working on it",
	Args:  cobra.ExactArgs(1),
	RunE:  runCartsStart,
}

func runCartsStart(cmd *cobra.Command, args []string) error {
	store, identity, err := openCartsStore(cmd)
	if err != nil {
		return err
	}
	defer store.Close()

	if err := store.StartIssue(cmd.Context(), args[0], identity.Name); err != nil {
		return err
	}

	issue, err := store.Get(cmd.Context(), args[0])
	if err != nil {
		return err
	}

	if isJSON(cmd) {
		data, _ := json.MarshalIndent(cartStartOutput{Issue: issue, Guidance: cartStartGuidance}, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("Started %s: %s (assigned to %s)\n", issue.ID, issue.Title, identity.Name)
	}
	return nil
}

// --- ox carts done ---

var cartsDoneCmd = &cobra.Command{
	Use:   "done ID [ID...]",
	Short: "Mark cart(s) as completed",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runCartsDone,
}

func runCartsDone(cmd *cobra.Command, args []string) error {
	store, _, err := openCartsStore(cmd)
	if err != nil {
		return err
	}
	defer store.Close()

	for _, id := range args {
		if err := store.CloseIssue(cmd.Context(), id); err != nil {
			return fmt.Errorf("done %s: %w", id, err)
		}
		fmt.Printf("Done %s\n", id)
	}
	return nil
}

// --- ox carts drop ---

var cartsDropCmd = &cobra.Command{
	Use:   "drop ID",
	Short: "Abandon a cart (back to open, unassigns you)",
	Args:  cobra.ExactArgs(1),
	RunE:  runCartsDrop,
}

func runCartsDrop(cmd *cobra.Command, args []string) error {
	store, _, err := openCartsStore(cmd)
	if err != nil {
		return err
	}
	defer store.Close()

	if err := store.DropIssue(cmd.Context(), args[0]); err != nil {
		return err
	}
	fmt.Printf("Dropped %s (back to open)\n", args[0])
	return nil
}

// --- ox carts close ---

var cartsCloseCmd = &cobra.Command{
	Use:   "close ID [ID...]",
	Short: "Close cart(s)",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runCartsClose,
}

func runCartsClose(cmd *cobra.Command, args []string) error {
	store, _, err := openCartsStore(cmd)
	if err != nil {
		return err
	}
	defer store.Close()

	for _, id := range args {
		if err := store.CloseIssue(cmd.Context(), id); err != nil {
			return fmt.Errorf("close %s: %w", id, err)
		}
		fmt.Printf("Closed %s\n", id)
	}
	return nil
}

// --- ox carts reopen ---

var cartsReopenCmd = &cobra.Command{
	Use:   "reopen ID",
	Short: "Reopen a closed cart",
	Args:  cobra.ExactArgs(1),
	RunE:  runCartsReopen,
}

func runCartsReopen(cmd *cobra.Command, args []string) error {
	store, _, err := openCartsStore(cmd)
	if err != nil {
		return err
	}
	defer store.Close()

	if err := store.Reopen(cmd.Context(), args[0]); err != nil {
		return err
	}
	fmt.Printf("Reopened %s\n", args[0])
	return nil
}

// --- ox carts dep ---

var cartsDepCmd = &cobra.Command{
	Use:   "dep",
	Short: "Manage cart dependencies",
}

var cartsDepAddCmd = &cobra.Command{
	Use:   "add FROM TO",
	Short: "Add a dependency (FROM depends on TO)",
	Args:  cobra.ExactArgs(2),
	RunE:  runCartsDepAdd,
}

func init() {
	cartsDepAddCmd.Flags().String("type", "blocks", "Dependency type: blocks, related, discovered-from")
}

func runCartsDepAdd(cmd *cobra.Command, args []string) error {
	store, _, err := openCartsStore(cmd)
	if err != nil {
		return err
	}
	defer store.Close()

	depType, _ := cmd.Flags().GetString("type")
	if err := store.AddDep(cmd.Context(), args[0], args[1], carts.DependencyType(depType)); err != nil {
		return err
	}
	fmt.Printf("Added dependency: %s %s %s\n", args[0], depType, args[1])
	return nil
}

var cartsDepRemoveCmd = &cobra.Command{
	Use:   "remove FROM TO",
	Short: "Remove a dependency",
	Args:  cobra.ExactArgs(2),
	RunE:  runCartsDepRemove,
}

func runCartsDepRemove(cmd *cobra.Command, args []string) error {
	store, _, err := openCartsStore(cmd)
	if err != nil {
		return err
	}
	defer store.Close()

	if err := store.RemoveDep(cmd.Context(), args[0], args[1]); err != nil {
		return err
	}
	fmt.Printf("Removed dependency: %s -> %s\n", args[0], args[1])
	return nil
}

// --- registration ---

func init() {
	// GroupID is owned by root.go's init() so all command-grouping decisions
	// live in one file (cartsCmd is in the "teams" group).

	cartsDepCmd.AddCommand(cartsDepAddCmd)
	cartsDepCmd.AddCommand(cartsDepRemoveCmd)

	cartsCmd.AddCommand(cartsCreateCmd)
	cartsCmd.AddCommand(cartsListCmd)
	cartsCmd.AddCommand(cartsReadyCmd)
	cartsCmd.AddCommand(cartsShowCmd)
	cartsCmd.AddCommand(cartsUpdateCmd)
	cartsCmd.AddCommand(cartsStartCmd)
	cartsCmd.AddCommand(cartsDoneCmd)
	cartsCmd.AddCommand(cartsDropCmd)
	cartsCmd.AddCommand(cartsCloseCmd)
	cartsCmd.AddCommand(cartsReopenCmd)
	cartsCmd.AddCommand(cartsDepCmd)

	rootCmd.AddCommand(cartsCmd)
}

// --- helpers ---

func openCartsStore(cmd *cobra.Command) (*carts.Store, *repotools.GitIdentity, error) {
	root, err := repotools.FindRepoRoot(repotools.VCSGit)
	if err != nil {
		return nil, nil, fmt.Errorf("not in a git repository")
	}

	projCtx, err := config.LoadProjectContext(root)
	if err != nil {
		return nil, nil, fmt.Errorf("project not initialized: run 'ox init' first: %w", err)
	}

	teamID := projCtx.Config().TeamID
	if teamID == "" {
		return nil, nil, fmt.Errorf("no team configured: run 'ox init' first")
	}

	teamDir := projCtx.TeamContextDir(teamID)

	// use identity.ResolveAttribution for git commit author (Name + Email).
	// Name is LOCAL ONLY (not shared in ledger), safe for git author field.
	attr := ident.ResolveAttribution(endpoint.GetForProject(root), config.GetDisplayName())
	gitIdent := &repotools.GitIdentity{Name: attr.Name, Email: attr.Email}

	store, err := carts.OpenFromTeamContext(
		context.Background(),
		teamDir,
		attr.Name,
		attr.Email,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("open carts store: %w", err)
	}

	return store, gitIdent, nil
}

func isJSON(cmd *cobra.Command) bool {
	if cmd.Root().PersistentFlags().Lookup("json") != nil {
		v, _ := cmd.Root().PersistentFlags().GetBool("json")
		return v
	}
	return false
}
