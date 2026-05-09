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
	"github.com/sageox/ox/internal/paths"
	"github.com/spf13/cobra"
)

// kbIDPrefix is the canonical kb_id prefix per ADR-036. Any positional arg
// starting with this prefix is treated as an immutable kb_id and passed
// straight to the detail endpoint; anything else is treated as a slug and
// resolved client-side via ListBubbles.
const kbIDPrefix = "kb_"

// kbSlugTypePriority encodes the tie-break order used when a slug matches
// bubbles of multiple types. Personal wins because it's the caller's own
// scratchpad — the most likely target of an unqualified 'ox kb show <slug>'.
// profile/team/repo follow the kb-plan ordering; custom is last.
//
// When real data shows a different ergonomic preference (e.g. team
// dominates daily use), revisit — but the current order is what the bead
// asked for and is documented in the help text below.
var kbSlugTypePriority = []api.KBType{
	api.KBTypePersonal,
	api.KBTypeProfile,
	api.KBTypeTeam,
	api.KBTypeRepo,
	api.KBTypeCustom,
}

var kbShowCmd = &cobra.Command{
	Use:   "show <slug|id>",
	Short: "Show details for a knowledge bubble",
	Long: `Show details for a single knowledge bubble.

The argument may be a kb_id (starts with 'kb_', passed directly to the
detail endpoint) or a slug (resolved client-side against the list of
bubbles you can access). When a slug matches multiple bubbles, the tie
is broken in this order: personal, profile, team, repo, custom.

Output fields are limited to what the API reliably returns: kb_id, type,
slug, name, lifecycle_state, viewer_role, owner_user_id (if not yours),
and the local on-disk path. Members and file counts are intentionally
omitted — those endpoints are not guaranteed.

Examples:
  ox kb show kb_01HXYZ...
  ox kb show personal
  ox kb show platform --json`,
	Args: cobra.ExactArgs(1),
	RunE: runKBShow,
}

func init() {
	kbCmd.AddCommand(kbShowCmd)
}

// kbShowOutput is the JSON envelope for `ox kb show --json`. It embeds the
// raw api.KB struct (so server-added fields surface automatically without
// a CLI release) and appends the locally-resolved on-disk path. Keeping
// LocalPath as a sibling rather than mutating the embedded struct means
// the JSON contract stays close to the server response — easy to grep
// and easy to diff against the API schema.
type kbShowOutput struct {
	*api.KB
	LocalPath string `json:"local_path"`
}

func runKBShow(cmd *cobra.Command, args []string) error {
	input := strings.TrimSpace(args[0])
	if input == "" {
		return fmt.Errorf("kb identifier required\nUsage: ox kb show <slug|id>")
	}

	jsonOutput, _ := cmd.Root().PersistentFlags().GetBool("json")

	projectRoot, _ := findProjectRoot()
	ep := resolveKBEndpoint(projectRoot)

	client := api.NewKBClientWithEndpoint(ep)
	if token, err := auth.GetTokenForEndpoint(ep); err == nil && token != nil && token.AccessToken != "" {
		client = client.WithAuthToken(token.AccessToken)
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
	defer cancel()

	kbID, resolveErr := resolveKBIdentifier(ctx, client, input)
	if resolveErr != nil {
		return handleKBShowError(resolveErr, input, jsonOutput)
	}

	slog.Info("kb show: resolving", "input", input, "kb_id", kbID)

	bubble, err := client.GetBubble(ctx, kbID)
	if err != nil {
		return handleKBShowError(err, input, jsonOutput)
	}

	localPath := paths.KBDir(bubble.KBID)

	if jsonOutput {
		return outputJSON(kbShowOutput{KB: bubble, LocalPath: localPath})
	}

	renderKBShow(cmd.OutOrStdout(), bubble, localPath, ep)
	return nil
}

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
// concrete kb_id suitable for GET /api/v1/kb/{id}. kb_id-prefixed inputs
// pass straight through; slugs trigger a list call so the client can
// disambiguate by type per kbSlugTypePriority.
func resolveKBIdentifier(ctx context.Context, client *api.KBClient, input string) (string, error) {
	if strings.HasPrefix(input, kbIDPrefix) {
		return input, nil
	}

	bubbles, err := client.ListBubbles(ctx)
	if err != nil {
		return "", err
	}

	matches := filterKBsBySlug(bubbles, input)
	if len(matches) == 0 {
		return "", fmt.Errorf("no knowledge bubble found matching %q\nRun 'ox kb list' to see available bubbles", input)
	}

	chosen := pickKBByPriority(matches)
	if chosen == nil {
		// shouldn't happen given non-empty matches, but guard anyway
		return "", fmt.Errorf("no knowledge bubble found matching %q", input)
	}
	return chosen.KBID, nil
}

// filterKBsBySlug returns bubbles whose slug equals the input. Match is
// case-insensitive: slugs are normalized server-side per ADR-036, but a
// user typing `MyTeam` should resolve the same bubble as `myteam`. The
// sibling commands `ox kb path` and `ox kb hydrate` do the same. Kept
// separate from pickKBByPriority so the priority logic is independently
// testable.
func filterKBsBySlug(bubbles []api.KB, slug string) []api.KB {
	needle := strings.ToLower(strings.TrimSpace(slug))
	if needle == "" {
		return nil
	}
	var out []api.KB
	for _, b := range bubbles {
		if strings.ToLower(b.Slug) == needle {
			out = append(out, b)
		}
	}
	return out
}

// pickKBByPriority returns the highest-priority bubble from the candidate
// list per kbSlugTypePriority. Returns nil when matches is empty.
func pickKBByPriority(matches []api.KB) *api.KB {
	if len(matches) == 0 {
		return nil
	}
	for _, want := range kbSlugTypePriority {
		for i := range matches {
			if matches[i].KBType == want {
				return &matches[i]
			}
		}
	}
	// no entry matched a known priority bucket (e.g., all KBTypeUnknown).
	// fall back to the first match so the caller still gets a result
	// rather than a misleading "not found" error.
	return &matches[0]
}

// handleKBShowError translates an API/resolution error into a user-friendly
// CLI error. ErrKBAPIUnavailable gets dedicated copy because the most
// common cause is the knowledge-bubbles flag being off for the caller's
// account — we don't want users hunting through logs for a 403.
func handleKBShowError(err error, input string, jsonOutput bool) error {
	if errors.Is(err, api.ErrKBAPIUnavailable) {
		msg := "Knowledge bubbles not enabled for this account"
		slog.Info("kb show: api unavailable", "input", input, "error", err)
		if jsonOutput {
			return outputJSON(map[string]any{
				"status":  "unavailable",
				"message": msg,
				"hint":    "Set OX_KB_DISABLE=1 to skip the kb API entirely. 'ox kb list' falls back to legacy team contexts and ledgers.",
			})
		}
		// stderr for the headline, stdout hint via PrintHint.
		fmt.Fprintln(os.Stderr, msg)
		cli.PrintHint("Set OX_KB_DISABLE=1 to skip the kb API entirely.")
		cli.PrintHint("'ox kb list' will fall back to legacy team contexts and ledgers.")
		return cli.ErrSilent
	}
	if errors.Is(err, api.ErrUnauthorized) {
		return fmt.Errorf("not authenticated — run 'ox login'")
	}
	// 404 from GetBubble surfaces as a generic non-2xx wrapped error
	// (the sentinel covers 403/404 from the listing/feature-flag path,
	// not "this specific id doesn't exist"). Detect by content so the
	// user gets actionable copy.
	if strings.Contains(err.Error(), "HTTP 404") {
		return fmt.Errorf("no knowledge bubble found matching %q\nRun 'ox kb list' to see available bubbles", input)
	}
	return err
}

// renderKBShow prints the human-readable view: a small key/value table
// with two-space indent and dim keys. Mirrors the look of `ox murmur status`
// without pulling in the bigger column machinery — this view shows ~7
// fields, table styling would be over-engineered.
func renderKBShow(w io.Writer, bubble *api.KB, localPath, ep string) {
	keyStyle := lipgloss.NewStyle().Foreground(cli.ColorDim)
	valStyle := lipgloss.NewStyle().Foreground(cli.ColorPrimary)

	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s %s\n", keyStyle.Render("kb_id:           "), valStyle.Render(bubble.KBID))
	if bubble.KBType != "" {
		fmt.Fprintf(w, "  %s %s\n", keyStyle.Render("type:            "), string(bubble.KBType))
	}
	if bubble.Slug != "" {
		fmt.Fprintf(w, "  %s %s\n", keyStyle.Render("slug:            "), bubble.Slug)
	}
	if bubble.Name != "" {
		fmt.Fprintf(w, "  %s %s\n", keyStyle.Render("name:            "), bubble.Name)
	}
	if bubble.LifecycleState != "" {
		fmt.Fprintf(w, "  %s %s\n", keyStyle.Render("lifecycle_state: "), bubble.LifecycleState)
	}
	if bubble.ViewerRole != "" {
		fmt.Fprintf(w, "  %s %s\n", keyStyle.Render("viewer_role:     "), bubble.ViewerRole)
	}
	// only show owner_user_id when it's NOT the caller — saves a line for the
	// common case of "this is mine".
	if bubble.OwnerUserID != "" && !ownerIsCaller(bubble.OwnerUserID, ep) {
		fmt.Fprintf(w, "  %s %s\n", keyStyle.Render("owner_user_id:   "), bubble.OwnerUserID)
	}
	fmt.Fprintf(w, "  %s %s\n", keyStyle.Render("local_path:      "), localPath)
	fmt.Fprintln(w)
}

// ownerIsCaller reports whether the bubble's owner_user_id matches the
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
	// the server returns owner_user_id; UserInfo carries both UserID
	// and Email so accept either match. This is a display optimization,
	// not an access decision — false negatives merely show a field that
	// would have been hidden.
	return ownerUserID == token.UserInfo.UserID || ownerUserID == token.UserInfo.Email
}

// _ keeps json package referenced if all uses go through outputJSON; remove
// when richer JSON shaping arrives in this file.
var _ = json.Marshal
