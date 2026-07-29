package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/kb"
	"github.com/sageox/ox/internal/paths"
	"github.com/spf13/cobra"
)

// kbIDPrefix is the canonical kb_id prefix. Any positional arg starting with
// this prefix is treated as an immutable kb_id and passed straight to the
// detail endpoint; anything else is treated as a slug and resolved within
// the requested scope (slugs are unique per scope, NOT globally — ADR-073).
const kbIDPrefix = "kb_"

// kbDescribeScopeDeferredMsg is the copy for `--scope personal`, which is
// recognized but not yet serviceable: personal bubbles live on per-user
// private teams (ADR-086) whose backfill has a known server-side issue.
// Tracked as bead ox-cag9.8; ox ADR-028 §4 records the deferral.
const kbDescribeScopeDeferredMsg = "the personal scope is not available yet — personal-team provisioning is still rolling out; use --scope team (the default) for now"

var kbDescribeCmd = &cobra.Command{
	Use:     "describe <#slug|kb_id>",
	Aliases: []string{"show"},
	Short:   "Describe a knowledge bubble",
	Long: `Describe a single knowledge bubble.

The argument may be a kb_id (starts with 'kb_', passed directly to the
detail endpoint) or a slug (with or without the display '#' prefix).
Slugs are unique within a scope, not globally, so slug resolution happens
inside one scope: the project's team by default, or the scope named by
--scope. Rename aliases are followed server-side.

Examples:
  ox kb describe kb_01HXYZ...
  ox kb describe '#engineering'
  ox kb describe engineering --scope team
  ox kb describe platform --json`,
	Args: cobra.ExactArgs(1),
	RunE: runKBDescribe,
}

func init() {
	kbCmd.AddCommand(kbDescribeCmd)
	kbDescribeCmd.Flags().String("scope", "team", "scope to resolve a slug in: team|personal (personal is not yet available)")
}

// kbDescribeOutput is the JSON envelope for `ox kb describe --json`. It
// embeds the raw api.KB struct (so server-added fields surface automatically
// without a CLI release) and appends the locally-resolved on-disk path.
type kbDescribeOutput struct {
	*api.KB
	LocalPath string `json:"local_path"`
}

func runKBDescribe(cmd *cobra.Command, args []string) error {
	// Strip the optional human-display `#` prefix before resolution. Slugs
	// are stored and looked up bare; the prefix is presentation-only.
	input := strings.TrimSpace(kb.NormalizeSlugArg(args[0]))
	if input == "" {
		return fmt.Errorf("kb identifier required\nUsage: ox kb describe <#slug|kb_id>")
	}

	jsonOutput, _ := cmd.Root().PersistentFlags().GetBool("json")
	scopeFlag, _ := cmd.Flags().GetString("scope")

	projectRoot, _ := findProjectRoot()
	ep := resolveKBEndpoint(projectRoot)

	client := api.NewKBClientWithEndpoint(ep)
	if token, err := auth.GetTokenForEndpoint(ep); err == nil && token != nil && token.AccessToken != "" {
		client = client.WithAuthToken(token.AccessToken)
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
	defer cancel()

	kbID, resolveErr := resolveKBIdentifier(ctx, client, input, scopeFlag, projectRoot)
	if resolveErr != nil {
		return handleKBDescribeError(cmd.OutOrStdout(), resolveErr, input, jsonOutput)
	}

	slog.Info("kb describe: resolving", "input", input, "kb_id", kbID)

	bubble, err := client.GetBubble(ctx, kbID)
	if err != nil {
		return handleKBDescribeError(cmd.OutOrStdout(), err, input, jsonOutput)
	}

	localPath := paths.KBDir(ep, bubble.KBID)

	if jsonOutput {
		return outputJSON(cmd.OutOrStdout(), kbDescribeOutput{KB: bubble, LocalPath: localPath})
	}

	renderKBDescribe(cmd.OutOrStdout(), bubble, localPath, ep)
	return nil
}

// errKBScopeDeferred marks a slug resolution against a scope the CLI
// recognizes but cannot serve yet (--scope personal).
var errKBScopeDeferred = errors.New(kbDescribeScopeDeferredMsg)

// resolveKBEndpoint picks the endpoint for the kb client. Project-scoped
// endpoint when we're inside an initialized project, otherwise the global
// default — same precedence the rest of the CLI uses for read-only API calls.
func resolveKBEndpoint(projectRoot string) string {
	if projectRoot != "" {
		return endpoint.GetForProject(projectRoot)
	}
	return endpoint.Get()
}

// resolveKBIdentifier maps a user-typed kb identifier (kb_id or slug) to a
// concrete kb_id suitable for GET /api/v1/kb/{id}.
//
//   - kb_id-prefixed inputs pass straight through.
//   - slugs resolve within ONE scope via GET /api/v1/kb/resolve (follows
//     server-side rename aliases): the project's team for --scope team,
//     an explicit deferred error for --scope personal.
func resolveKBIdentifier(ctx context.Context, client *api.KBClient, input, scopeFlag, projectRoot string) (string, error) {
	if strings.HasPrefix(input, kbIDPrefix) {
		return input, nil
	}

	switch scopeFlag {
	case "", "team":
		scopes := ambientKBScopes(projectRoot)
		if len(scopes) == 0 {
			return "", fmt.Errorf("no team scope available — run inside an ox-initialized repo (or pass a kb_id directly)")
		}
		return client.ResolveSlug(ctx, scopes[0], input)
	case "personal":
		return "", errKBScopeDeferred
	default:
		return "", fmt.Errorf("invalid --scope %q (want team or personal)", scopeFlag)
	}
}

// handleKBDescribeError translates an API/resolution error into a
// user-friendly CLI error. ErrKBAPIUnavailable gets dedicated copy because
// it covers both "feature flag off" and "slug not found in scope" (the
// server deliberately returns an identical 404 for both so slugs cannot be
// enumerated by non-members).
func handleKBDescribeError(w io.Writer, err error, input string, jsonOutput bool) error {
	if errors.Is(err, errKBScopeDeferred) {
		if jsonOutput {
			return outputJSON(w, map[string]any{
				"status":  "deferred",
				"message": kbDescribeScopeDeferredMsg,
			})
		}
		fmt.Fprintln(os.Stderr, kbDescribeScopeDeferredMsg)
		return cli.ErrSilent
	}
	if errors.Is(err, api.ErrKBAPIUnavailable) {
		// kb_id inputs are immutable identifiers — show them bare; slug
		// inputs get the human-display `#` prefix.
		display := input
		if !strings.HasPrefix(input, kbIDPrefix) {
			display = cli.FormatKBSlug(input)
		}
		msg := fmt.Sprintf("no knowledge bubble matching %q in this scope (or knowledge bubbles are not enabled for this account)", display)
		slog.Info("kb describe: unavailable or not found", "input", input, "error", err)
		if jsonOutput {
			return outputJSON(w, map[string]any{
				"status":  "unavailable",
				"message": msg,
				"hint":    "Run 'ox kb list' to see the bubbles in this project's scopes.",
			})
		}
		fmt.Fprintln(os.Stderr, msg)
		cli.PrintHint("Run 'ox kb list' to see the bubbles in this project's scopes.")
		return cli.ErrSilent
	}
	if errors.Is(err, api.ErrUnauthorized) {
		return fmt.Errorf("not authenticated — run 'ox login'")
	}
	// any other error passes through unwrapped.
	return err
}

// renderKBDescribe prints the human-readable view: a small key/value table
// with two-space indent and dim keys. This view shows ~10 fields; table
// styling would be over-engineered.
func renderKBDescribe(w io.Writer, bubble *api.KB, localPath, ep string) {
	keyStyle := lipgloss.NewStyle().Foreground(cli.ColorDim)
	valStyle := lipgloss.NewStyle().Foreground(cli.ColorPrimary)

	row := func(key, val string) {
		if val == "" {
			return
		}
		fmt.Fprintf(w, "  %s %s\n", keyStyle.Render(fmt.Sprintf("%-17s", key+":")), val)
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s %s\n", keyStyle.Render(fmt.Sprintf("%-17s", "kb_id:")), valStyle.Render(bubble.KBID))
	row("type", string(bubble.KBType))
	if bubble.Slug != "" {
		row("slug", cli.FormatKBSlug(bubble.Slug))
	}
	row("name", bubble.Name)
	if bubble.ScopeType != "" || bubble.ScopeID != "" {
		row("scope", strings.TrimSpace(bubble.ScopeType+" "+bubble.ScopeID))
	}
	row("description", bubble.Description)
	if len(bubble.Topics) > 0 {
		row("topics", strings.Join(bubble.Topics, ", "))
	}
	row("lifecycle_state", bubble.LifecycleState)
	row("viewer_role", bubble.ViewerRole)
	// "admin" is the bubble-admin label (ADR-073 §2) — the human in charge,
	// not an access grant. The server field is still named manager/owner.
	admin := bubble.Manager
	if admin == "" {
		admin = bubble.OwnerUserID
	}
	if admin != "" && !ownerIsCaller(admin, ep) {
		row("admin", admin)
	}
	row("last_activity", bubble.LastActivityAt)
	row("local_path", localPath)
	fmt.Fprintln(w)
}

// ownerIsCaller reports whether the bubble's admin label matches the
// authenticated caller. Best-effort — when we can't resolve the auth
// principal (offline / not logged in) we conservatively return false so
// the field is shown rather than silently elided.
func ownerIsCaller(ownerUserID, ep string) bool {
	if ownerUserID == "" || ep == "" {
		return false
	}
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil {
		return false
	}
	// display optimization, not an access decision — false negatives merely
	// show a field that would have been hidden.
	return ownerUserID == token.UserInfo.UserID || ownerUserID == token.UserInfo.Email
}

// _ keeps json package referenced if all uses go through outputJSON; remove
// when richer JSON shaping arrives in this file.
var _ = json.Marshal
