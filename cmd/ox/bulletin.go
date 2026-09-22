package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/prime"
	"github.com/spf13/cobra"
)

// bulletinCmd is feature-gated. It is registered with rootCmd only in
// syncFeatureGatedCommands, from the server-evaluated pilot flag
// (flags.Get().BulletinEnabled). Do not add it to rootCmd in init(): cobra
// renders help before any run hook, so removal is the only gate that keeps
// the command out of both help and execution.
var bulletinCmd = &cobra.Command{
	Use:   "bulletin",
	Short: "Post to the team bulletin board",
	Long: `Publishes a Markdown or HTML post to your team's bulletin board through the
SageOx server. The board is a shared space in Team Context where any human or
AI coworker on the team posts useful, time-limited information.

Available only to people the server has enrolled in the pilot. A newly
enrolled person sees the command after the daemon's next settings refresh,
which can take up to an hour.`,
}

var bulletinPostCmd = &cobra.Command{
	Use:   "post <file>",
	Short: "Publish a Markdown or HTML file as a time-limited bulletin post",
	Long: `Publish one file as a post on the team bulletin board. The server stores the
post in Team Context; this machine never writes to the checkout, and the post
appears locally after the next Team Context sync.

The file's bytes are sent exactly as they are. The format comes from the
extension (.md/.markdown or .html/.htm), the slug from the file name, and the
title from the first "# " heading (Markdown) or <title> (HTML), falling back to
the file name. Pass "-" to read stdin, in which case --format, --title, and
--slug are required.

Identical content is one post per board, whatever the slug or format, and
reposting never extends the expiry. Publishing is a person's act: a team
service token cannot post.

Examples:
  ox bulletin post release-notes.md --ttl 14d
  ox bulletin post notes.html --ttl 6h --title "Incident 42 timeline"
  cat notes.md | ox bulletin post - --ttl 1d --format markdown --title "Notes" --slug notes
  ox bulletin post release-notes.md --ttl 14d --json`,
	Args: cobra.MaximumNArgs(1),
	RunE: runBulletinPost,
}

func init() {
	f := bulletinPostCmd.Flags()
	f.String("ttl", "", "How long the post stays active, e.g. 14d or 6h (required; 1h to 90d)")
	_ = bulletinPostCmd.MarkFlagRequired("ttl")
	f.String("title", "", "Post title (default: derived from the file)")
	f.String("slug", "", "Post slug (default: derived from the file name)")
	f.String("format", "", "Post format: markdown or html (default: from the file extension)")
	f.String("board", api.BulletinBoardGeneral, "Board to post to")
	f.String("team", "", "Team id or slug (default: the team that owns this repo's Team Context)")
	f.BoolP("yes", "y", false, "Publish without asking for confirmation")
	bulletinCmd.AddCommand(bulletinPostCmd)
}

// bulletinPublisher is the seam between the command and the network.
// *api.RepoClient satisfies it; tests drive the command against a fake server
// through the real client, so the seam exists for the retry loop's sake.
type bulletinPublisher interface {
	PublishBulletinPost(ctx context.Context, teamRef string, req api.BulletinPublishRequest) (*api.BulletinPublishResult, error)
}

// Retry policy for a 503 (the team's Team Context store is unavailable). The
// identical request is resent — same slug, same bytes — because identical
// content is one post per board: if the first attempt did land after all,
// the retry answers duplicate_post instead of creating a second post.
const (
	// bulletinMaxRetries is how many times the request is resent after the
	// first attempt.
	bulletinMaxRetries = 3
	// bulletinRetryMinWait is the floor on the wait between attempts when the
	// server sends no Retry-After (or a shorter one).
	bulletinRetryMinWait = time.Second
	// bulletinRetryBudget caps the total wall time spent on retries. A wait
	// that would overrun it is not taken; the failure is reported as safe to
	// rerun instead.
	bulletinRetryBudget = 30 * time.Second
)

// bulletinSleep waits between retry attempts. It is a variable so tests can
// record the requested waits without sleeping. The context lets Ctrl-C end
// the wait early.
var bulletinSleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// The terminal seams. Tests run with no TTY, so the only way to prove that
// --yes and --json are what suppress the prompt (and that a "no" cancels
// without a request) is to stand in for both.
var (
	bulletinIsInteractive = cli.IsInteractive
	bulletinConfirm       = cli.ConfirmYesNo
)

// bulletin styles — the same label-recedes/value-stands split as invite.
var (
	bulletinLabelStyle = lipgloss.NewStyle().Foreground(cli.ColorDim)
	bulletinValueStyle = lipgloss.NewStyle()
	bulletinOKStyle    = lipgloss.NewStyle().Foreground(cli.ColorSuccess)
	bulletinErrStyle   = lipgloss.NewStyle().Foreground(cli.ColorError)
)

// Outcome codes. These strings are the --json contract (both the status and
// the error field carry them); AI coworkers branch on them, so they are
// stable.
const (
	bulletinStatusPublished          = "published"
	bulletinCodeValidation           = "validation_error"
	bulletinCodeDuplicate            = "duplicate_post"
	bulletinCodeNotEnabled           = "not_enabled"
	bulletinCodeNotAMember           = "not_a_member"
	bulletinCodeUnsupportedServer    = "unsupported_server"
	bulletinCodeUnauthenticated      = "unauthenticated"
	bulletinCodeTeamToken            = "team_token_cannot_publish"
	bulletinCodeTooLarge             = "content_too_large"
	bulletinCodeUnavailable          = "team_context_unavailable"
	bulletinCodeVersionUnsupported   = "version_unsupported"
	bulletinCodeHashMismatch         = "hash_mismatch"
	bulletinCodeNoTeam               = "no_team"
	bulletinCodeForbidden            = "forbidden"
	bulletinCodeError                = "error"
	bulletinLocalValidationGuidance  = "Nothing left this machine. Fix the named field and rerun the same command."
	bulletinServerValidationGuidance = "The server rejected the post; nothing was published. Fix the field the server named and rerun. The TTL must be between 1h and 90d."
)

// bulletinTeam is the resolved target: what goes in the URL, what a human
// sees, and where the post will appear locally after the next sync.
type bulletinTeam struct {
	Ref       string
	Label     string
	LocalPath string
}

// bulletinJSONTeam is the team object in the --json receipt.
type bulletinJSONTeam struct {
	TeamID string `json:"team_id"`
	Name   string `json:"name"`
}

// bulletinJSONReceipt is the --json success envelope.
type bulletinJSONReceipt struct {
	Status        string           `json:"status"`
	Team          bulletinJSONTeam `json:"team"`
	Board         string           `json:"board"`
	Path          string           `json:"path"`
	Slug          string           `json:"slug"`
	Title         string           `json:"title"`
	Format        string           `json:"format"`
	ContentSHA256 string           `json:"content_sha256"`
	ContentBytes  int              `json:"content_bytes"`
	CreatedAt     string           `json:"created_at"`
	ExpiresAt     string           `json:"expires_at"`
	CommitID      string           `json:"commit_id"`
	LocalPath     string           `json:"local_path"`
	Guidance      string           `json:"guidance"`
}

// bulletinJSONDuplicate names the post that already holds the content, from
// a 409. The timestamps are omitted when the server did not send them.
type bulletinJSONDuplicate struct {
	Path      string `json:"path"`
	State     string `json:"state"`
	CreatedAt string `json:"created_at,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// bulletinJSONFailure is the --json error envelope. Status and Error both
// carry the code so a consumer that keys on either field works.
type bulletinJSONFailure struct {
	Status    string                 `json:"status"`
	Error     string                 `json:"error"`
	Message   string                 `json:"message"`
	Guidance  string                 `json:"guidance"`
	Details   map[string]string      `json:"details,omitempty"`
	Duplicate *bulletinJSONDuplicate `json:"duplicate,omitempty"`
}

// bulletinFailure is one rendered refusal: the code, the human headline and
// explanation, the AI-coworker guidance, and the hints a human can act on.
type bulletinFailure struct {
	Code      string
	Headline  string
	Explain   []string
	Guidance  string
	Hints     [][2]string // command, description
	Details   map[string]string
	Duplicate *bulletinJSONDuplicate
}

func runBulletinPost(cmd *cobra.Command, args []string) error {
	// cmd.Context() is only populated by ExecuteC, so it is nil for any caller
	// that invokes RunE directly — including every test. Default it here.
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	jsonOutput, _ := cmd.Root().PersistentFlags().GetBool("json")
	ttlFlag, _ := cmd.Flags().GetString("ttl")
	titleFlag, _ := cmd.Flags().GetString("title")
	slugFlag, _ := cmd.Flags().GetString("slug")
	formatFlag, _ := cmd.Flags().GetString("format")
	boardFlag, _ := cmd.Flags().GetString("board")
	teamFlag, _ := cmd.Flags().GetString("team")
	skipConfirm, _ := cmd.Flags().GetBool("yes")

	if len(args) == 0 {
		return fmt.Errorf("give the file to publish, or - for stdin: ox bulletin post <file> --ttl 14d")
	}
	source := args[0]

	// 1. Read the bytes as they are. 2. Check them locally. Both happen before
	// the team, token, or network are touched, so a bad file costs nothing and
	// nothing invalid ever leaves the machine.
	content, err := readBulletinSource(cmd.InOrStdin(), source)
	if err != nil {
		return renderBulletinFailure(out, jsonOutput, bulletinFailureFor(err, "", 0))
	}
	plan, err := planBulletinPost(bulletinInput{
		Source:  source,
		Content: content,
		Title:   titleFlag,
		Slug:    slugFlag,
		Format:  formatFlag,
		Board:   boardFlag,
		TTL:     ttlFlag,
	})
	if err != nil {
		return renderBulletinFailure(out, jsonOutput, bulletinFailureFor(err, "", 0))
	}

	// 3. Resolve the team and the person. A team service token is refused
	// before any network call: publishing is a person's act.
	projectRoot, _ := findProjectRoot()
	teamRef, label, err := resolveTeamRef(cmd, projectRoot)
	if err != nil {
		return renderBulletinFailure(out, jsonOutput, bulletinNoTeamFailure())
	}
	team := bulletinTeam{
		Ref:       teamRef,
		Label:     label,
		LocalPath: bulletinLocalPostsDir(projectRoot, teamFlag, teamRef, plan.Request.Board),
	}

	ep := endpoint.GetForProject(projectRoot)
	token, err := auth.EnsureValidTokenForEndpoint(ep, 300)
	if err != nil || token == nil || token.AccessToken == "" {
		return renderBulletinFailure(out, jsonOutput, bulletinFailureFor(api.ErrUnauthorized, "", 0))
	}
	if strings.HasPrefix(token.AccessToken, auth.TeamTokenPrefix) {
		return renderBulletinFailure(out, jsonOutput, bulletinTeamTokenFailure())
	}

	// 4. Show exactly what will be published and ask, unless the caller
	// opted out.
	if bulletinShouldConfirm(skipConfirm, jsonOutput, bulletinIsInteractive()) {
		printBulletinPreview(out, team, plan, source)
		if !bulletinConfirm("Publish?", true) {
			fmt.Fprintln(out, "Canceled.")
			return nil
		}
		fmt.Fprintln(out)
	}

	// 5. Send. 6. The server validates again, hashes, and commits.
	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)
	result, attempts, err := publishBulletinWithRetry(ctx, errOut, client, team.Ref, plan.Request)
	if err != nil {
		return renderBulletinFailure(out, jsonOutput, bulletinFailureFor(err, team.Ref, attempts))
	}

	// 7. Compare the returned hash with a local hash of the bytes sent. A
	// mismatch means the receipt describes something other than this file.
	if !strings.EqualFold(result.ContentSHA256, plan.SHA256) || result.ContentBytes != len(plan.Request.Content) {
		return renderBulletinFailure(out, jsonOutput, bulletinHashMismatchFailure(plan, result))
	}

	// 8. The receipt.
	return renderBulletinReceipt(out, jsonOutput, team, plan, result)
}

// readBulletinSource returns the bytes of the file, or of stdin for "-",
// exactly as they are. The read is bounded at one byte past the content cap
// so an oversized input is refused by the local check with its real size
// class instead of being buffered whole (a stray multi-gigabyte path would
// otherwise be read into memory before the size rule ever ran).
//
// A file that cannot be read is a local input refusal (validation_error on
// the content field) — the fix is the path, and nothing has left the machine.
func readBulletinSource(stdin io.Reader, source string) ([]byte, error) {
	var r io.Reader
	if source == bulletinStdinSource {
		r = stdin
	} else {
		f, err := os.Open(source)
		if err != nil {
			return nil, &bulletinInputError{Field: "content", Fix: source,
				Message: fmt.Sprintf("cannot read %s: %v", source, err)}
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, bulletinMaxContentBytes+1))
	if err != nil {
		return nil, &bulletinInputError{Field: "content", Fix: source,
			Message: fmt.Sprintf("cannot read %s: %v", source, err)}
	}
	return data, nil
}

// bulletinLocalPostsDir is where the post will appear on this machine after
// the next Team Context sync: the team's checkout joined with the board's
// posts directory. When no local checkout is known for that team (a --team
// this machine never synced), the directory relative to the Team Context
// root is returned instead, so the receipt still says where to look.
func bulletinLocalPostsDir(projectRoot, teamFlag, teamRef, board string) string {
	rel := filepath.FromSlash(prime.BulletinPostsRelDir(board))
	if projectRoot == "" {
		return rel
	}
	if teamFlag == "" {
		if tc := config.FindRepoTeamContext(projectRoot); tc != nil && tc.Path != "" {
			return filepath.Join(tc.Path, rel)
		}
		return rel
	}
	for _, tc := range config.FindAllTeamContexts(projectRoot) {
		if tc.TeamID == teamRef && tc.Path != "" {
			return filepath.Join(tc.Path, rel)
		}
	}
	return rel
}

// publishBulletinWithRetry sends the request and, on a 503, resends the
// IDENTICAL request up to bulletinMaxRetries more times, waiting
// max(Retry-After, 1s) between attempts and never overrunning
// bulletinRetryBudget of wall time. One progress line per retry goes to
// errOut in both output modes (stderr, so --json stdout stays one document).
// attempts is how many requests were actually made.
func publishBulletinWithRetry(ctx context.Context, errOut io.Writer, p bulletinPublisher, teamRef string, req api.BulletinPublishRequest) (*api.BulletinPublishResult, int, error) {
	start := time.Now()
	attempts := 0
	var waited time.Duration
	for {
		attempts++
		result, err := p.PublishBulletinPost(ctx, teamRef, req)
		if err == nil {
			return result, attempts, nil
		}
		var unavailable *api.BulletinUnavailableError
		if !errors.As(err, &unavailable) || attempts > bulletinMaxRetries {
			return nil, attempts, err
		}
		wait := unavailable.RetryAfter
		if wait < bulletinRetryMinWait {
			wait = bulletinRetryMinWait
		}
		// Two clocks: the waits already taken (so the cap holds even when the
		// sleeper is replaced) and wall time since the first attempt (so slow
		// 503 responses count too). Overrunning either is a stop, not a clamp:
		// retrying sooner than the server asked is a wasted request.
		if waited+wait > bulletinRetryBudget || time.Since(start)+wait > bulletinRetryBudget {
			return nil, attempts, err
		}
		waited += wait
		fmt.Fprintf(errOut, "Team Context temporarily unavailable; retrying in %s (attempt %d of %d)\n",
			wait, attempts+1, bulletinMaxRetries+1)
		if serr := bulletinSleep(ctx, wait); serr != nil {
			return nil, attempts, err
		}
	}
}

// printBulletinPreview shows exactly what is about to be published. The slug
// becomes a file name teammates see for as long as the post lives, which is
// why it is shown before the question is asked.
func printBulletinPreview(w io.Writer, team bulletinTeam, plan *bulletinPlan, source string) {
	file := source
	if source == bulletinStdinSource {
		file = "stdin"
	}
	row := func(label, value string) {
		fmt.Fprintf(w, "  %s   %s\n", bulletinLabelStyle.Render(padBulletinLabel(label)), bulletinValueStyle.Render(value))
	}
	fmt.Fprintln(w)
	row("Team", team.Label)
	row("Board", plan.Request.Board)
	row("Slug", plan.Request.Slug)
	row("Title", cli.SanitizeTerminalText(plan.Request.Title))
	row("Format", plan.Request.Format)
	row("TTL", plan.Request.TTL)
	row("Bytes", bulletinCommaInt(len(plan.Request.Content)))
	row("File", cli.SanitizeTerminalText(file))
	fmt.Fprintln(w)
}

// bulletinPreviewLabelWidth pads preview and receipt labels to one column.
const bulletinPreviewLabelWidth = 7

func padBulletinLabel(label string) string {
	if gap := bulletinPreviewLabelWidth - len(label); gap > 0 {
		return label + strings.Repeat(" ", gap)
	}
	return label
}

// renderBulletinReceipt prints the success receipt. Every field the server
// returned is echoed as-is (it is what the server stored), plus the local
// directory the post will appear in and the trust framing shared with prime
// and the guide.
func renderBulletinReceipt(w io.Writer, jsonOutput bool, team bulletinTeam, plan *bulletinPlan, res *api.BulletinPublishResult) error {
	teamID := res.TeamID
	if teamID == "" {
		teamID = team.Ref
	}
	if jsonOutput {
		return writeJSONIndent(w, bulletinJSONReceipt{
			Status:        bulletinStatusPublished,
			Team:          bulletinJSONTeam{TeamID: teamID, Name: team.Label},
			Board:         res.Board,
			Path:          res.Path,
			Slug:          res.Slug,
			Title:         res.Title,
			Format:        res.Format,
			ContentSHA256: res.ContentSHA256,
			ContentBytes:  res.ContentBytes,
			CreatedAt:     res.CreatedAt.UTC().Format(time.RFC3339),
			ExpiresAt:     res.ExpiresAt.UTC().Format(time.RFC3339),
			CommitID:      res.CommitID,
			LocalPath:     team.LocalPath,
			Guidance:      prime.BulletinReceiptGuidance,
		})
	}

	// Server-supplied strings are echoed to a terminal, so they are sanitized
	// like any other untrusted text.
	cell := cli.SanitizeTerminalText
	fmt.Fprintf(w, "%s Published %s to %s on team %s\n\n",
		bulletinOKStyle.Render("✓"), cell(res.Slug), cell(res.Board), cell(team.Label))
	row := func(label, value string) {
		fmt.Fprintf(w, "  %s   %s\n", bulletinLabelStyle.Render(padBulletinLabel(label)), bulletinValueStyle.Render(value))
	}
	row("path", cell(res.Path))
	row("hash", fmt.Sprintf("%s · %s bytes · %s",
		bulletinShortHash(res.ContentSHA256), bulletinCommaInt(res.ContentBytes), cell(res.Format)))
	row("expires", fmt.Sprintf("%s (%s)", res.ExpiresAt.UTC().Format(time.RFC3339), plan.Request.TTL))
	row("commit", bulletinShortCommit(res.CommitID))
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s\n", bulletinValueStyle.Render(bulletinWhereItAppears(team.LocalPath)))
	fmt.Fprintf(w, "  %s\n", bulletinLabelStyle.Render(prime.BulletinTrustNote))
	return nil
}

// bulletinWhereItAppears words the receipt's local-location line. An absolute
// path is a checkout on this machine; a relative one means no checkout for
// that team is known here, so the sentence must not promise a local sync.
func bulletinWhereItAppears(localPath string) string {
	if filepath.IsAbs(localPath) {
		return fmt.Sprintf("Appears in %s after the next Team Context sync.", localPath)
	}
	return fmt.Sprintf("Appears at %s in that team's Team Context checkout after its next sync (this machine has none for that team).", localPath)
}

// bulletinShortHash renders a 64-hex digest as first 8 … last 4, which is
// enough to match against the stored file name by eye.
func bulletinShortHash(h string) string {
	if len(h) <= 12 {
		return cli.SanitizeTerminalText(h)
	}
	return cli.SanitizeTerminalText(h[:8] + "…" + h[len(h)-4:])
}

func bulletinShortCommit(id string) string {
	if len(id) > 7 {
		id = id[:7]
	}
	return cli.SanitizeTerminalText(id)
}

// bulletinCommaInt renders a byte count with thousands separators.
func bulletinCommaInt(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	first := len(s) % 3
	if first > 0 {
		b.WriteString(s[:first])
	}
	for i := first; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// renderBulletinFailure writes one refusal in the requested mode and returns
// cli.ErrSilent: the report is already on screen, so main must exit 1 without
// printing a second, redundant error line.
func renderBulletinFailure(w io.Writer, jsonOutput bool, f bulletinFailure) error {
	if jsonOutput {
		if jerr := writeJSONIndent(w, bulletinJSONFailure{
			Status:    f.Code,
			Error:     f.Code,
			Message:   f.Headline,
			Guidance:  f.Guidance,
			Details:   f.Details,
			Duplicate: f.Duplicate,
		}); jerr != nil {
			return jerr
		}
		return cli.ErrSilent
	}
	fmt.Fprintf(w, "%s %s\n", bulletinErrStyle.Render("✗"), cli.SanitizeTerminalText(f.Headline))
	if len(f.Explain) > 0 {
		fmt.Fprintln(w)
		for _, line := range f.Explain {
			fmt.Fprintf(w, "  %s\n", cli.StyleDim.Render(cli.SanitizeTerminalText(line)))
		}
	}
	if len(f.Hints) > 0 {
		fmt.Fprintln(w)
		for _, h := range f.Hints {
			cli.PrintActionHintTo(w, h[0], h[1])
		}
	}
	return cli.ErrSilent
}

// bulletinNoTeamFailure is the refusal when neither the repo nor --team names
// a team.
func bulletinNoTeamFailure() bulletinFailure {
	return bulletinFailure{
		Code:     bulletinCodeNoTeam,
		Headline: "No team to post to.",
		Explain:  []string{"This directory isn't linked to a team, and --team wasn't given."},
		Guidance: "This directory isn't linked to a team. Pass --team <slug>, or run 'ox teams' to list teams you belong to.",
		Hints: [][2]string{
			{"ox teams", "List teams you belong to"},
			{"ox bulletin post <file> --ttl 14d --team <slug>", "Name the team explicitly"},
		},
	}
}

// bulletinTeamTokenFailure refuses a team service token before any network
// call. The server would refuse it too (401), but saying why here saves a
// round-trip and names the actual fix.
func bulletinTeamTokenFailure() bulletinFailure {
	return bulletinFailure{
		Code:     bulletinCodeTeamToken,
		Headline: "A team service token cannot publish a post.",
		Explain: []string{
			"Publishing is a person's act. Sign in as yourself; if SAGEOX_TOKEN carries",
			"the team token, unset it for this command.",
		},
		Guidance: "A team service token (oxt_...) cannot publish: publishing is a person's act. Nothing was sent. Sign in as a person with 'ox login', or unset SAGEOX_TOKEN if it carries the team token, then rerun.",
		Hints:    [][2]string{{"ox login", "Sign in to SageOx as yourself"}},
	}
}

// bulletinHashMismatchFailure is the hard error for a receipt whose hash or
// size does not match the bytes sent. The receipt cannot be trusted, and the
// honest next step is a rerun: if the post did land, the server answers
// duplicate_post with its path.
func bulletinHashMismatchFailure(plan *bulletinPlan, res *api.BulletinPublishResult) bulletinFailure {
	return bulletinFailure{
		Code:     bulletinCodeHashMismatch,
		Headline: "The server's receipt does not match the bytes sent.",
		Explain: []string{
			fmt.Sprintf("sent    %s · %s bytes", bulletinShortHash(plan.SHA256), bulletinCommaInt(len(plan.Request.Content))),
			fmt.Sprintf("receipt %s · %s bytes", bulletinShortHash(res.ContentSHA256), bulletinCommaInt(res.ContentBytes)),
			"Do not trust this receipt. Rerun the same command: if the post did land,",
			"the server answers duplicate_post with its path.",
		},
		Guidance: "Do not trust this receipt: the server's content_sha256 or content_bytes does not match the bytes sent. Rerun the same command. If the post actually landed, the server answers duplicate_post with its path; otherwise it is published fresh.",
		Details: map[string]string{
			"sent_sha256":     plan.SHA256,
			"received_sha256": res.ContentSHA256,
			"sent_bytes":      strconv.Itoa(len(plan.Request.Content)),
			"received_bytes":  strconv.Itoa(res.ContentBytes),
		},
	}
}

// bulletinFailureFor maps every error the publish flow can produce — local
// refusals and each server outcome — onto its code, wording, and guidance.
// teamRef and attempts feed the wording; they may be empty/0 for errors
// raised before the request was built.
func bulletinFailureFor(err error, teamRef string, attempts int) bulletinFailure {
	var inputErr *bulletinInputError
	var validation *api.BulletinValidationError
	var duplicate *api.BulletinDuplicateError
	var unavailable *api.BulletinUnavailableError
	var forbidden *api.ForbiddenError

	switch {
	case errors.As(err, &inputErr):
		return bulletinFailure{
			Code:     bulletinCodeValidation,
			Headline: inputErr.Error(),
			Explain:  []string{"Nothing left this machine. Fix " + inputErr.Fix + " and rerun."},
			Guidance: bulletinLocalValidationGuidance,
			Details:  map[string]string{inputErr.Field: inputErr.Message},
		}

	case errors.As(err, &validation):
		explain := append([]string{"The server rejected the post; nothing was published."},
			bulletinSortedFields(validation.Fields)...)
		return bulletinFailure{
			Code:     bulletinCodeValidation,
			Headline: validation.Error(),
			Explain:  explain,
			Guidance: bulletinServerValidationGuidance,
			Details:  validation.Fields,
		}

	case errors.As(err, &duplicate):
		dup := duplicate.Duplicate
		guidance := "This board already holds a post with identical content"
		if dup.Path != "" {
			guidance += " at " + dup.Path
		}
		guidance += ". If this was a retry after a lost response, the post is already published there and nothing else is needed. Otherwise change the content: identical content is one post per board, whatever the slug or format, and reposting never extends the expiry."
		explain := []string{"Identical content is one post per board, whatever the slug or format."}
		if dup.Path != "" {
			explain = append(explain, "existing "+dup.Path)
		}
		if dup.State != "" {
			explain = append(explain, "state    "+dup.State)
		}
		if dup.ExpiresAt != "" {
			explain = append(explain, "expires  "+dup.ExpiresAt)
		}
		explain = append(explain, "If this was a retry after a lost response, it is already published.",
			"Otherwise change the content; reposting never extends the expiry.")
		return bulletinFailure{
			Code:     bulletinCodeDuplicate,
			Headline: "This board already holds a post with identical content.",
			Explain:  explain,
			Guidance: guidance,
			Duplicate: &bulletinJSONDuplicate{
				Path: dup.Path, State: dup.State, CreatedAt: dup.CreatedAt, ExpiresAt: dup.ExpiresAt,
			},
		}

	case errors.Is(err, api.ErrBulletinNotEnabled):
		return bulletinFailure{
			Code:     bulletinCodeNotEnabled,
			Headline: "The bulletin board is not enabled for your account.",
			Explain: []string{
				"The server enables this per person. A newly enrolled person sees it",
				"after the daemon's next settings refresh, which can take up to an hour.",
			},
			Guidance: "Do not retry. The server enables the bulletin board per person, and this person is not enrolled. A newly enrolled person sees the command after the daemon's next settings refresh (up to an hour).",
		}

	case errors.Is(err, api.ErrBulletinNotAMember):
		// The server hides team existence behind this answer on purpose; the
		// wording must not claim the team does or does not exist.
		return bulletinFailure{
			Code:     bulletinCodeNotAMember,
			Headline: fmt.Sprintf("You can't post to %s.", teamRef),
			Explain:  []string{"Choose a team you belong to."},
			Guidance: "You are not a member of that team (or it is not one you can reach). Choose a team you belong to: run 'ox teams' to list them, then rerun with --team <slug>.",
			Hints:    [][2]string{{"ox teams", "List teams you belong to"}},
		}

	case errors.Is(err, api.ErrBulletinUnsupported):
		return bulletinFailure{
			Code:     bulletinCodeUnsupportedServer,
			Headline: api.ErrBulletinUnsupported.Error() + ".",
			Explain:  []string{"This deployment has no bulletin board."},
			Guidance: "This SageOx server has no bulletin board. Do not retry against this endpoint.",
		}

	case errors.Is(err, api.ErrUnauthorized):
		return bulletinFailure{
			Code:     bulletinCodeUnauthenticated,
			Headline: "Not authenticated.",
			Guidance: "Run 'ox login' first, then retry 'ox bulletin post'. Publishing is a person's act: a team service token is refused.",
			Hints:    [][2]string{{"ox login", "Sign in to SageOx"}},
		}

	case errors.Is(err, api.ErrBulletinTooLarge):
		return bulletinFailure{
			Code:     bulletinCodeTooLarge,
			Headline: "The server refused the post as too large.",
			Explain: []string{
				err.Error(),
				"Content is capped at 1 MiB and the whole request at 2 MiB.",
			},
			Guidance: "The server refused the post as too large (content over 1 MiB or request over 2 MiB). Nothing was published. Shorten the post and rerun.",
		}

	case errors.As(err, &unavailable):
		tries := "after " + strconv.Itoa(attempts) + " attempts"
		if attempts == 1 {
			tries = "after 1 attempt"
		}
		return bulletinFailure{
			Code:     bulletinCodeUnavailable,
			Headline: "The team's Team Context is temporarily unavailable.",
			Explain: []string{
				"Nothing was published " + tries + ". The same command is safe to rerun:",
				"identical content is one post per board.",
			},
			Guidance: "The team's Team Context store is temporarily unavailable; nothing was published " + tries + ". Rerun the same command later — an identical retry is safe because identical content is one post per board.",
		}

	case errors.Is(err, api.ErrVersionUnsupported):
		return bulletinFailure{
			Code:     bulletinCodeVersionUnsupported,
			Headline: api.ErrVersionUnsupported.Error(),
			Guidance: "Upgrade ox, then retry 'ox bulletin post'.",
			Hints:    [][2]string{{"ox upgrade", "Upgrade ox"}},
		}

	case errors.As(err, &forbidden):
		reason := forbidden.Reason
		if reason == "" {
			reason = "the server refused this post"
		}
		return bulletinFailure{
			Code:     bulletinCodeForbidden,
			Headline: reason,
			Guidance: "The server refused this post. Ask a team owner or admin if you need it published.",
		}
	}

	// An invalid --team never becomes a request: it is a local refusal, and
	// the fix is the flag, not a rerun.
	if errors.Is(err, api.ErrInvalidTeamRef) {
		return bulletinFailure{
			Code:     bulletinCodeValidation,
			Headline: err.Error(),
			Explain:  []string{"Nothing left this machine. Pass a real team id or slug to --team."},
			Guidance: "Nothing left this machine: --team is not a team id or slug. Run 'ox teams' to list teams you belong to, then rerun with --team <slug>.",
			Hints:    [][2]string{{"ox teams", "List teams you belong to"}},
			Details:  map[string]string{"team": err.Error()},
		}
	}

	// Network errors, 415/500, a malformed receipt: nothing on this machine
	// changed, and the request is safe to repeat.
	return bulletinFailure{
		Code:     bulletinCodeError,
		Headline: err.Error(),
		Explain:  []string{"No receipt was returned. Nothing on this machine changed."},
		Guidance: "Publishing failed before a receipt was returned: " + err.Error() + ". Nothing on this machine changed. Rerun the same command; an identical retry is safe because identical content is one post per board.",
	}
}

// bulletinSortedFields renders a 400's per-field messages as "field: message"
// lines in key order, so the explanation is stable between runs.
func bulletinSortedFields(fields map[string]string) []string {
	if len(fields) == 0 {
		return nil
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	// Small map; a simple insertion sort keeps the file free of a sort import
	// for one call site.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+": "+fields[k])
	}
	return lines
}
