package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"sort"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/ui"
	"github.com/spf13/cobra"
)

// inviteSender is the seam between this command's decision logic and the
// network. *api.RepoClient satisfies it. Everything interesting about `ox
// invite` — validation, ordering, status mapping, exit code — is decided in
// sendInvites, which talks only to this interface, so the behavior can be
// driven end-to-end without reaching for the real endpoint.
type inviteSender interface {
	CreateTeamInvite(ctx context.Context, teamRef string, req api.CreateInviteRequest) (*api.InviteResponse, error)
}

// inviteStatus is the closed set of per-recipient outcomes. These strings are
// the --json contract; AI coworkers branch on them, so they are stable.
type inviteStatus string

const (
	statusSent           inviteStatus = "sent"
	statusAlreadyInvited inviteStatus = "already_invited"
	statusNotPermitted   inviteStatus = "not_permitted"
	statusNotAMember     inviteStatus = "not_a_member"
	statusInvalidEmail   inviteStatus = "invalid_email"
	statusPersonalTeam   inviteStatus = "personal_team"
	statusError          inviteStatus = "error"
)

// isFailure reports whether this outcome should make the command exit non-zero.
//
// statusAlreadyInvited is deliberately NOT a failure: the desired end state —
// that this person has a pending invitation — already holds. Exiting non-zero
// would make the command unusable in a re-runnable script.
func (s inviteStatus) isFailure() bool {
	switch s {
	case statusSent, statusAlreadyInvited:
		return false
	default:
		return true
	}
}

// inviteTarget is the resolved team an invitation is aimed at.
type inviteTarget struct {
	// Ref is what goes in the request URL — a team_id or a slug. The server
	// resolves either, which is why an unresolvable-locally --team value can
	// still be passed through rather than rejected.
	Ref      string
	ID       string
	Name     string
	Slug     string
	ThisRepo bool
}

// displayName is the team label shown to a human: name plus slug when both
// are known, falling back to whatever identifier we do have.
func (t inviteTarget) displayName() string {
	switch {
	case t.Name != "" && t.Slug != "":
		return fmt.Sprintf("%s (%s)", t.Name, t.Slug)
	case t.Name != "":
		return t.Name
	case t.Slug != "":
		return t.Slug
	default:
		return t.Ref
	}
}

type inviteOutcome struct {
	Email     string       `json:"email"`
	Status    inviteStatus `json:"status"`
	Message   string       `json:"message,omitempty"`
	InviteID  string       `json:"invite_id,omitempty"`
	ExpiresAt string       `json:"expires_at,omitempty"`
}

type inviteResult struct {
	Team     inviteTarget
	Role     string
	Outcomes []inviteOutcome
}

// counts returns sent / already-pending / failed for the summary line.
func (r inviteResult) counts() (sent, pending, failed int) {
	for _, o := range r.Outcomes {
		switch o.Status {
		case statusSent:
			sent++
		case statusAlreadyInvited:
			pending++
		default:
			failed++
		}
	}
	return sent, pending, failed
}

func (r inviteResult) hasFailure() bool {
	for _, o := range r.Outcomes {
		if o.Status.isFailure() {
			return true
		}
	}
	return false
}

var inviteCmd = &cobra.Command{
	Use:   "invite <email>...",
	Short: "Invite people to a team by email",
	Long: `Invite one or more people to a SageOx team.

Addresses may be comma-, semicolon-, or space-separated, and repeated as
separate arguments. SageOx then sends each person a link to join. Delivery is
best-effort and happens after the invitation is created, so a reported
invitation is not proof an email arrived. Invitations expire after 7 days.

With no --team, the invitation targets this repository's team. Whether you
may invite, list, or cancel is decided by the server and reported back.

Examples:
  ox invite alice@acme.com
  ox invite alice@acme.com,bob@acme.com
  ox invite alice@acme.com --team sageox --role admin
  ox invite --list
  ox invite --cancel 0198f3c2-6c1a-7e40-9a0b-2f8c4d5e6f70
  ox invite alice@acme.com --json`,
	Args: cobra.ArbitraryArgs,
	RunE: runInvite,
}

func init() {
	inviteCmd.Flags().String("team", "", "team slug, id, or name (defaults to the repo's active team)")
	inviteCmd.Flags().String("role", api.RoleMember, "role to grant: member, admin, or owner")
	inviteCmd.Flags().BoolP("yes", "y", false, "skip the confirmation prompt")
	inviteCmd.Flags().Bool("list", false, "list invitations that are still outstanding")
	inviteCmd.Flags().String("cancel", "", "cancel a pending invitation by its id (from --list)")
}

// invite styles — mirrors the Tufte-ish pattern in teams.go/status.go.
var (
	inviteHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(cli.ColorPrimary)
	// Label recedes, value does not. These were the same style, which put
	// column headers, body values and boilerplate on one visual tier — the
	// failure reason read no louder than the evergreen footer. Mirrors the
	// statusLabelStyle/statusValueStyle split already used by ox status.
	inviteLabelStyle = lipgloss.NewStyle().Foreground(cli.ColorDim)
	inviteValueStyle = lipgloss.NewStyle()
	inviteOKStyle    = lipgloss.NewStyle().Foreground(cli.ColorSuccess)
	inviteWarnStyle  = lipgloss.NewStyle().Foreground(cli.ColorWarning)
	inviteErrStyle   = lipgloss.NewStyle().Foreground(cli.ColorError)
)

func runInvite(cmd *cobra.Command, args []string) error {
	// cmd.Context() is only populated by ExecuteC, so it is nil for any caller
	// that invokes RunE directly — including every test. A nil context reaches
	// net/http and fails with an error that names neither the cause nor this
	// command, so default it here rather than leaving the landmine.
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	out := cmd.OutOrStdout()
	jsonOutput, _ := cmd.Root().PersistentFlags().GetBool("json")
	teamFlag, _ := cmd.Flags().GetString("team")
	roleFlag, _ := cmd.Flags().GetString("role")
	skipConfirm, _ := cmd.Flags().GetBool("yes")
	listFlag, _ := cmd.Flags().GetBool("list")
	cancelFlag, _ := cmd.Flags().GetString("cancel")

	if listFlag && cancelFlag != "" {
		return fmt.Errorf("--list and --cancel do different things; use one at a time")
	}
	if (listFlag || cancelFlag != "") && len(args) > 0 {
		return fmt.Errorf("--list and --cancel take no email addresses")
	}

	role, err := normalizeRole(roleFlag)
	if err != nil {
		return err
	}

	projectRoot, _ := findProjectRoot()
	ep := endpoint.Get()
	if projectRoot != "" {
		ep = endpoint.GetForProject(projectRoot)
	}

	token, err := auth.EnsureValidTokenForEndpoint(ep, 300)
	if err != nil || token == nil || token.AccessToken == "" {
		return errInviteNotAuthenticated(out, jsonOutput)
	}

	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)

	// --list and --cancel resolve a team but need no addresses, so they branch
	// before the address handling below.
	if listFlag || cancelFlag != "" {
		target, terr := resolveInviteTarget(ctx, client, projectRoot, teamFlag, jsonOutput, out)
		if terr != nil {
			return terr
		}
		if listFlag {
			return runInviteList(ctx, out, client, target, jsonOutput)
		}
		return runInviteCancel(ctx, out, client, target, cancelFlag, jsonOutput, skipConfirm)
	}

	emails := splitEmails(args)
	if len(emails) == 0 {
		if jsonOutput || !cli.IsInteractive() {
			return fmt.Errorf("give at least one email address, for example: ox invite alice@acme.com")
		}
		emails, err = promptForEmails()
		if err != nil {
			return err
		}
		if len(emails) == 0 {
			return fmt.Errorf("no email addresses given")
		}
	}

	target, err := resolveInviteTarget(ctx, client, projectRoot, teamFlag, jsonOutput, out)
	if err != nil {
		return err
	}

	if !skipConfirm && !jsonOutput && cli.IsInteractive() {
		printInvitePreview(out, target, role, emails)
		noun := "invitations"
		if len(emails) == 1 {
			noun = "invitation"
		}
		if !cli.ConfirmYesNo(fmt.Sprintf("Send %d %s?", len(emails), noun), true) {
			fmt.Fprintln(out, "Canceled.")
			return nil
		}
		fmt.Fprintln(out)
	}

	res, err := sendInvites(ctx, client, target, role, emails)
	if err != nil {
		return renderInvitePartialAbort(out, res, err, jsonOutput)
	}

	if err := renderInviteResult(out, res, jsonOutput); err != nil {
		return err
	}
	if res.hasFailure() {
		// Output is already rendered; ErrSilent yields exit 1 without main
		// printing a second, redundant error line.
		return cli.ErrSilent
	}
	return nil
}

// normalizeRole validates --role locally so an obvious typo costs no network
// round-trip and no partially-sent batch.
func normalizeRole(role string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case api.RoleMember:
		return api.RoleMember, nil
	case api.RoleAdmin:
		return api.RoleAdmin, nil
	case api.RoleOwner:
		return api.RoleOwner, nil
	case "":
		// The server rejects a blank role with a 400 despite its OpenAPI doc
		// claiming a "member" default, so never send one.
		return api.RoleMember, nil
	default:
		return "", fmt.Errorf("invalid role %q: must be member, admin, or owner", role)
	}
}

// splitEmails flattens positional arguments into addresses. Arguments may be
// comma-, semicolon-, or whitespace-separated in any combination, mirroring
// how the web invite composer accepts a pasted list.
//
// Duplicates are collapsed case-insensitively, keeping first-seen order and
// original spelling: inviting the same person twice in one command means one
// invitation, and sending it twice would earn a spurious "already invited".
func splitEmails(args []string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, arg := range args {
		for _, field := range strings.FieldsFunc(arg, func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
		}) {
			e := strings.TrimSpace(field)
			if e == "" {
				continue
			}
			key := strings.ToLower(e)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, e)
		}
	}
	return out
}

// isValidEmail is a deliberately permissive check: it exists to catch typos
// like "dave@acme" before they consume a network round-trip, not to enforce
// RFC 5322. net/mail accepts display-name forms ("A <a@b.com>") that would
// confuse the server, so a bare address with a dotted domain is required.
func isValidEmail(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s {
		return false
	}
	at := strings.LastIndex(s, "@")
	if at < 1 || at == len(s)-1 {
		return false
	}
	// RFC 5321 §4.5.3.1 size limits. Without these, an over-long address is
	// accepted here, stored server-side, and then silently fails at the SMTP
	// hop — the invitation looks sent and simply never arrives, which is the
	// worst outcome available: the inviter has no signal to act on.
	if len(s) > maxEmailOctets || at > maxEmailLocalOctets {
		return false
	}
	domain := s[at+1:]
	if len(domain) > maxEmailDomainOctets {
		return false
	}
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	return true
}

// RFC 5321 §4.5.3.1 address size limits, in octets.
const (
	maxEmailLocalOctets  = 64
	maxEmailDomainOctets = 255
	maxEmailOctets       = 254
)

// sendInvites is the heart of the command: validate every address first, then
// invite the valid ones ONE AT A TIME.
//
// Sequential is a correctness requirement, not a style choice. The server has
// no uniqueness constraint on (team_id, email) and no rate limit on this
// route; duplicate suppression is a read-then-write check in its service
// layer. Concurrent requests for the same address can therefore both create a
// row, producing two live invitations.
//
// The returned error is reserved for conditions that doom the whole command
// (no such route, bad credentials). Anything a single recipient can fail at
// becomes an outcome, so one bad address never hides the others' results.
func sendInvites(ctx context.Context, s inviteSender, target inviteTarget, role string, emails []string) (inviteResult, error) {
	res := inviteResult{Team: target, Role: role}

	for _, email := range emails {
		if !isValidEmail(email) {
			res.Outcomes = append(res.Outcomes, inviteOutcome{
				Email:   email,
				Status:  statusInvalidEmail,
				Message: "not sent",
			})
			continue
		}

		invite, err := s.CreateTeamInvite(ctx, target.Ref, api.CreateInviteRequest{
			Email: email,
			Role:  role,
		})
		if err != nil {
			// These say nothing about the recipient — they mean no address in
			// this batch can succeed, so stop rather than emit N identical
			// failures.
			if errors.Is(err, api.ErrInviteUnsupported) ||
				errors.Is(err, api.ErrUnauthorized) ||
				errors.Is(err, api.ErrVersionUnsupported) ||
				// A personal team is a property of the TEAM, not of any
				// recipient: it can never accept an invitation from anyone.
				// Reporting it per-address would print the same refusal once
				// per email and imply a different address might work.
				errors.Is(err, api.ErrPersonalTeam) {
				return res, err
			}
			res.Outcomes = append(res.Outcomes, outcomeForError(email, err))
			continue
		}

		out := inviteOutcome{Email: email, Status: statusSent}
		if invite != nil {
			out.InviteID = invite.ID
			out.ExpiresAt = invite.ExpiresAt
		}
		res.Outcomes = append(res.Outcomes, out)
	}

	return res, nil
}

// outcomeForError maps a per-recipient error onto its status. The two
// same-status server collisions are already resolved into distinct sentinels
// by the api package, so this is a straight mapping.
func outcomeForError(email string, err error) inviteOutcome {
	switch {
	case errors.Is(err, api.ErrInviteExists):
		return inviteOutcome{Email: email, Status: statusAlreadyInvited, Message: "still pending"}
	case errors.Is(err, api.ErrInviteForbidden):
		// Show the server's own reason. Who may invite is server policy and can
		// change; a message composed here would be wrong the day it does.
		msg := api.InviteForbiddenReason(err)
		if msg == "" {
			msg = "the server refused this invitation"
		}
		return inviteOutcome{Email: email, Status: statusNotPermitted, Message: msg}
	case errors.Is(err, api.ErrInviteNotAMember):
		return inviteOutcome{Email: email, Status: statusNotAMember, Message: api.ErrInviteNotAMember.Error()}
	default:
		return inviteOutcome{Email: email, Status: statusError, Message: err.Error()}
	}
}

// resolveInviteTarget decides which team to invite to.
//
// Order: --team resolved locally -> --team passed through verbatim (the
// server resolves slugs too, so a team this machine never synced still works)
// -> this repo's team -> interactive picker -> an error naming the fix.
func resolveInviteTarget(ctx context.Context, client *api.RepoClient, projectRoot, teamFlag string, jsonOutput bool, out io.Writer) (inviteTarget, error) {
	if teamFlag != "" {
		if t := resolveTeamByQuery(projectRoot, teamFlag); t != nil {
			return inviteTarget{Ref: t.TeamID, ID: t.TeamID, Name: t.Name, Slug: t.Slug}, nil
		}
		// Unknown locally is not an error: this machine may simply never have
		// synced that team. Let the server rule on it.
		return inviteTarget{Ref: teamFlag, Slug: teamFlag}, nil
	}

	if projectRoot != "" {
		if tc := config.FindRepoTeamContext(projectRoot); tc != nil && tc.TeamID != "" {
			return inviteTarget{
				Ref: tc.TeamID, ID: tc.TeamID, Name: tc.TeamName, Slug: tc.Slug, ThisRepo: true,
			}, nil
		}
	}

	if jsonOutput || !cli.IsInteractive() {
		return inviteTarget{}, errInviteNoTeam(out, jsonOutput)
	}

	return pickInviteTeam(ctx, out, client, projectRoot)
}

// pickInviteTeam shows the team chooser. The list comes from the API rather
// than the local disk scan behind `ox teams`, because only the API carries
// each membership's role — and the role is what tells a person, before they
// commit, which teams they can actually invite into.
func pickInviteTeam(ctx context.Context, w io.Writer, client *api.RepoClient, projectRoot string) (inviteTarget, error) {
	teams, err := fetchInviteTeams(client)
	if err != nil || len(teams) == 0 {
		// Fall back to the offline view rather than dead-ending on a network
		// hiccup; the server still gets the final say on membership.
		local := discoverAllTeams(projectRoot)
		if len(local) == 0 {
			return inviteTarget{}, fmt.Errorf("no teams found — run 'ox login' then 'ox init'")
		}
		options := make([]string, len(local))
		for i, t := range local {
			options[i] = t.Name
		}
		idx, selErr := cli.SelectOne("Team:", options, 0)
		if selErr != nil || idx < 0 || idx >= len(local) {
			return inviteTarget{}, fmt.Errorf("selection canceled")
		}
		return inviteTarget{Ref: local[idx].TeamID, ID: local[idx].TeamID, Name: local[idx].Name, Slug: local[idx].Slug}, nil
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, ui.RenderCategory("Invite to Team"))
	fmt.Fprintln(w, cli.StyleDim.Render("Choose which team to invite people to."))
	fmt.Fprintln(w)

	options := make([]string, len(teams))
	for i, t := range teams {
		options[i] = inviteTeamOptionLabel(t)
	}

	idx, err := cli.SelectOne("Team:", options, 0)
	if err != nil || idx < 0 || idx >= len(teams) {
		return inviteTarget{}, fmt.Errorf("selection canceled")
	}
	sel := teams[idx]

	// Refuse a team the server has already told us cannot be invited to,
	// rather than sending a request that is certain to be refused.
	if sel.InviteCapabilityKnown() && !sel.CanInvite {
		if sel.InviteBlockedReason == api.InviteBlockedPersonalTeam {
			return inviteTarget{}, renderPersonalTeamRefusal(w, false)
		}
		return inviteTarget{}, fmt.Errorf("you can't invite people to %s — ask a team owner or admin", sel.Name)
	}

	return inviteTarget{Ref: sel.ID, ID: sel.ID, Name: sel.Name, Slug: sel.Slug}, nil
}

// inviteTeamOptionLabel renders one picker row: name, slug, role, and — when
// the server has told us — why this team can't be invited to. Showing it in the
// list is what stops someone selecting a private team and only then learning it
// was never possible.
func inviteTeamOptionLabel(t api.TeamMembership) string {
	label := t.Name
	if label == "" {
		label = t.ID
	}
	if t.Slug != "" {
		label = fmt.Sprintf("%s  %s", label, t.Slug)
	}
	if t.Role != "" {
		label = fmt.Sprintf("%s  %s", label, t.Role)
	}
	if t.InviteCapabilityKnown() && !t.CanInvite {
		switch t.InviteBlockedReason {
		case api.InviteBlockedPersonalTeam:
			label += "  (private — no invites)"
		default:
			label += "  (can't invite)"
		}
	}
	return label
}

// fetchInviteTeams pulls the caller's memberships, sorted by name so the
// picker's order is stable between runs.
func fetchInviteTeams(client *api.RepoClient) ([]api.TeamMembership, error) {
	resp, err := client.GetRepos()
	if err != nil {
		return nil, err
	}
	teams := resp.TeamMembershipsFromRepos()
	sort.SliceStable(teams, func(i, j int) bool { return teams[i].Name < teams[j].Name })
	return teams, nil
}

// promptForEmails asks for addresses when none were given on the command line.
func promptForEmails() ([]string, error) {
	raw, err := cli.InputWithDefault("Emails (comma-separated):", "")
	if err != nil {
		return nil, err
	}
	return splitEmails([]string{raw}), nil
}

func printInvitePreview(w io.Writer, target inviteTarget, role string, emails []string) {
	label := target.displayName()
	if target.ThisRepo {
		label += " · this repo"
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s   %s\n", inviteLabelStyle.Render("Team"), inviteValueStyle.Render(label))
	fmt.Fprintf(w, "  %s   %s\n", inviteLabelStyle.Render("Role"), inviteValueStyle.Render(role))
	for i, e := range emails {
		field := "To  "
		if i > 0 {
			field = "    "
		}
		fmt.Fprintf(w, "  %s   %s\n", inviteLabelStyle.Render(field), inviteValueStyle.Render(e))
	}
	fmt.Fprintln(w)
}

// inviteJSONOutput is the machine-readable envelope. It carries no token
// field — see api.InviteResponse for why the token never leaves the server.
type inviteJSONOutput struct {
	Team     inviteJSONTeam  `json:"team"`
	Role     string          `json:"role"`
	Results  []inviteOutcome `json:"results"`
	Summary  inviteJSONSum   `json:"summary"`
	Error    string          `json:"error,omitempty"`
	Message  string          `json:"message,omitempty"`
	Guidance string          `json:"guidance"`
}

type inviteJSONTeam struct {
	TeamID string `json:"team_id,omitempty"`
	Name   string `json:"name,omitempty"`
	Slug   string `json:"slug,omitempty"`
}

type inviteJSONSum struct {
	Sent           int `json:"sent"`
	AlreadyInvited int `json:"already_invited"`
	Failed         int `json:"failed"`
}

// renderInviteResult is the unit-testable core: a pre-built result in, bytes
// out, no network and no globals.
func renderInviteResult(w io.Writer, res inviteResult, jsonOutput bool) error {
	sent, pending, failed := res.counts()

	if jsonOutput {
		return writeJSONIndent(w, inviteJSONOutput{
			Team:     inviteJSONTeam{TeamID: res.Team.ID, Name: res.Team.Name, Slug: res.Team.Slug},
			Role:     res.Role,
			Results:  res.Outcomes,
			Summary:  inviteJSONSum{Sent: sent, AlreadyInvited: pending, Failed: failed},
			Guidance: inviteGuidance(res, sent, pending, failed),
		})
	}

	fmt.Fprintln(w, inviteHeaderStyle.Render("Invitations"))
	fmt.Fprintln(w, inviteLabelStyle.Render(strings.Repeat("─", len("Invitations"))))

	// The address column is sized to the rows that will actually sit in it.
	// Addresses longer than the cap get a line of their own, so letting them
	// set the width would pad every short row out to an emptiness no row uses.
	emailW := 0
	for _, o := range res.Outcomes {
		l := lipgloss.Width(inviteCell(o.Email))
		if l > inviteEmailCap {
			continue
		}
		if l > emailW {
			emailW = l
		}
	}
	statusW := 0
	for _, o := range res.Outcomes {
		if l := lipgloss.Width(inviteStatusLabel(o.Status)); l > statusW {
			statusW = l
		}
	}

	for _, o := range res.Outcomes {
		glyph := inviteOKStyle.Render("✓")
		switch {
		case o.Status == statusAlreadyInvited:
			glyph = inviteWarnStyle.Render("⚠")
		case o.Status.isFailure():
			glyph = inviteErrStyle.Render("✗")
		}
		label := inviteStatusLabel(o.Status)
		email := inviteCell(o.Email)
		msg := ""
		if o.Message != "" && o.Status != statusSent {
			msg = inviteCell(o.Message)
		}

		// The glyph column NEVER moves — it is what the eye scans down to find
		// the failures, so it sits at a fixed offset on every row.
		//
		// Layout is decided PER ROW, never from the batch-wide maximum:
		// otherwise a single long address drags every short row into the wide
		// layout with it. Wrapping is never an option — a wrapped row
		// misaligns every column, exactly when it matters most, on a batch
		// that partly failed. So the status and reason give way, never the
		// address: which address failed is the one thing the row exists to say.
		emailWidth := lipgloss.Width(email)
		detail := label
		if msg != "" {
			detail += "  " + msg
		}

		switch {
		case emailWidth > emailW:
			// Longer than the whole column. Give it a line to itself, in full,
			// and hang the verdict beneath it.
			fmt.Fprintf(w, "  %s  %s\n", glyph, truncateInviteMessage(email, inviteMaxWidth-5))
			fmt.Fprintf(w, "%s%s\n", inviteDetailIndent,
				inviteValueStyle.Render(truncateInviteMessage(detail, inviteMaxWidth-len(inviteDetailIndent))))

		case 2+1+2+emailW+2+statusW+2+lipgloss.Width(msg) <= inviteMaxWidth:
			line := fmt.Sprintf("  %s  %s  %s", glyph,
				padInviteCell(email, emailW), padInviteCell(label, statusW))
			if msg != "" {
				line += "  " + inviteValueStyle.Render(msg)
			}
			fmt.Fprintln(w, strings.TrimRight(line, " "))

		default:
			// Address and status fit; only the reason has to drop down.
			fmt.Fprintln(w, strings.TrimRight(fmt.Sprintf("  %s  %s  %s", glyph,
				padInviteCell(email, emailW), padInviteCell(label, statusW)), " "))
			fmt.Fprintf(w, "%s%s\n", inviteDetailIndent,
				inviteValueStyle.Render(truncateInviteMessage(msg, inviteMaxWidth-len(inviteDetailIndent))))
		}
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s\n", inviteSummaryLine(sent, pending, failed))
	if sent > 0 {
		// One line, at the moment it is relevant. The same caveat is NOT
		// repeated in --help: saying it three times taught nobody anything.
		fmt.Fprintf(w, "  %s\n", inviteLabelStyle.Render(
			"Delivery is best-effort — resend from the team page if it doesn't arrive."))
	}
	return nil
}

// inviteMaxWidth is the column budget every invite screen renders within.
// Design rule 12: nothing may wrap at 80 columns.
const inviteMaxWidth = 80

// inviteEmailCap bounds the address column. Email addresses are unbounded and
// a single long one would otherwise dictate the layout of every other row.
const inviteEmailCap = 46

// inviteDetailIndent aligns a dropped-down reason under the address column,
// so a continuation line reads as belonging to the row above it.
const inviteDetailIndent = "     "

// truncateInviteMessage keeps a server-supplied reason inside the width budget.
// Server messages are unbounded, so a long one must be cut rather than allowed
// to wrap. Cuts on rune boundaries so a multi-byte character is never split.
func truncateInviteMessage(s string, max int) string {
	if max <= 1 || lipgloss.Width(s) <= max {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > max {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

// inviteCell prepares untrusted text for display.
//
// Email addresses and server messages are attacker-influenced — an invitation
// can be addressed to anything — so escape sequences must be stripped before
// they reach a terminal, or a crafted address could repaint the screen and
// misrepresent the result.
func inviteCell(s string) string {
	return cli.SanitizeTerminalText(s)
}

// padInviteCell pads to a DISPLAY width, not a byte count. Accented and
// non-Latin addresses are multi-byte; padding by len() would misalign every
// column after them.
func padInviteCell(s string, w int) string {
	if gap := w - lipgloss.Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

func inviteStatusLabel(s inviteStatus) string {
	switch s {
	case statusSent:
		return "sent"
	case statusAlreadyInvited:
		return "already invited"
	case statusNotPermitted:
		return "not permitted"
	case statusNotAMember:
		return "no access"
	case statusInvalidEmail:
		return "invalid address"
	case statusPersonalTeam:
		return "personal team"
	default:
		return "failed"
	}
}

// inviteSummaryLine is the one line that answers "did this work?". Each count
// carries the same color as the rows it summarizes, so the verdict is legible
// without reading the table — and stays legible under NO_COLOR, where the
// words alone still say it.
func inviteSummaryLine(sent, pending, failed int) string {
	parts := []string{inviteOKStyle.Render(fmt.Sprintf("%d sent", sent))}
	if pending > 0 {
		parts = append(parts, inviteWarnStyle.Render(fmt.Sprintf("%d already pending", pending)))
	}
	if failed > 0 {
		parts = append(parts, inviteErrStyle.Render(fmt.Sprintf("%d failed", failed)))
	}
	line := strings.Join(parts, inviteLabelStyle.Render(" · "))
	if sent > 0 {
		line += inviteLabelStyle.Render(" · they expire in 7 days")
	}
	return line
}

// inviteGuidance tells an AI coworker what actually happened and what to do
// next. Per CLAUDE.md this — not a skill file — is where agent-facing
// behavior belongs, so every agent gets it, not just Claude Code.
func inviteGuidance(res inviteResult, sent, pending, failed int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s sent", pluralizeInvitations(sent))
	if pending > 0 {
		fmt.Fprintf(&b, "; %d already had a pending invite (no action needed)", pending)
	}
	b.WriteString(".")

	teamRef := res.Team.Slug
	if teamRef == "" {
		teamRef = res.Team.Ref
	}
	for _, o := range res.Outcomes {
		switch o.Status {
		case statusInvalidEmail:
			fmt.Fprintf(&b, " %s was rejected as malformed — correct it and re-run 'ox invite <email> --team %s'.", o.Email, teamRef)
		case statusNotPermitted:
			// Quote the server rather than diagnosing: who may invite is server
			// policy and can change without this client changing, so a fix
			// composed here would eventually be wrong advice stated confidently.
			if o.Message != "" {
				fmt.Fprintf(&b, " %s was refused by the server: %s.", o.Email, o.Message)
			} else {
				fmt.Fprintf(&b, " %s was refused by the server; ask a team owner or admin.", o.Email)
			}
		case statusPersonalTeam:
			b.WriteString(" This is a personal team; pick a shared team with 'ox teams'.")
		case statusNotAMember:
			b.WriteString(" That team is not one you can invite to — check 'ox teams'.")
		}
	}
	if sent > 0 {
		b.WriteString(" Invitations expire in 7 days; email delivery is best-effort, so if a recipient reports nothing arrived, resend from the team page.")
	}
	if failed > 0 {
		b.WriteString(" Exit code is 1 because at least one address failed.")
	}
	return b.String()
}

// renderInviteAbort handles the whole-command failures: a server without the
// route, and bad credentials.
func renderInviteAbort(w io.Writer, err error, jsonOutput bool) error {
	switch {
	case errors.Is(err, api.ErrInviteUnsupported):
		if jsonOutput {
			if jerr := writeJSONIndent(w, map[string]string{
				"error":    "unsupported",
				"message":  api.ErrInviteUnsupported.Error(),
				"guidance": "This SageOx server has no CLI invite endpoint. Invite from the dashboard with 'ox view team'.",
			}); jerr != nil {
				return jerr
			}
			// Nobody was invited — this must not read as success.
			return cli.ErrSilent
		}
		fmt.Fprintf(w, "%s %s\n\n", inviteWarnStyle.Render("⚠"), api.ErrInviteUnsupported.Error())
		cli.PrintActionHintTo(w, "ox view team", "Invite from the dashboard")
		return cli.ErrSilent
	case errors.Is(err, api.ErrPersonalTeam):
		return renderPersonalTeamRefusal(w, jsonOutput)
	case errors.Is(err, api.ErrUnauthorized):
		return errInviteNotAuthenticated(w, jsonOutput)
	}
	return err
}

// renderInvitePartialAbort preserves the record of any invitations that were
// already attempted before a team-level/auth/version abort stopped the batch.
// Text output can print a report followed by the abort explanation; JSON output
// must be a single document, so partial results and abort metadata share one
// envelope.
func renderInvitePartialAbort(w io.Writer, res inviteResult, err error, jsonOutput bool) error {
	if len(res.Outcomes) == 0 {
		return renderInviteAbort(w, err, jsonOutput)
	}

	if !jsonOutput {
		if rerr := renderInviteResult(w, res, false); rerr != nil {
			return rerr
		}
		fmt.Fprintln(w)
		return renderInviteAbort(w, err, false)
	}

	code, message, abortGuidance, ok := inviteAbortJSONDetails(err)
	if !ok {
		return err
	}
	sent, pending, failed := res.counts()
	guidance := inviteGuidance(res, sent, pending, failed)
	if guidance != "" {
		guidance += " "
	}
	guidance += abortGuidance

	if jerr := writeJSONIndent(w, inviteJSONOutput{
		Team:     inviteJSONTeam{TeamID: res.Team.ID, Name: res.Team.Name, Slug: res.Team.Slug},
		Role:     res.Role,
		Results:  res.Outcomes,
		Summary:  inviteJSONSum{Sent: sent, AlreadyInvited: pending, Failed: failed},
		Error:    code,
		Message:  message,
		Guidance: guidance,
	}); jerr != nil {
		return jerr
	}
	return cli.ErrSilent
}

func inviteAbortJSONDetails(err error) (code, message, guidance string, ok bool) {
	switch {
	case errors.Is(err, api.ErrInviteUnsupported):
		return "unsupported", api.ErrInviteUnsupported.Error(),
			"This SageOx server has no CLI invite endpoint. Invite from the dashboard with 'ox view team'.", true
	case errors.Is(err, api.ErrPersonalTeam):
		return string(statusPersonalTeam), api.ErrPersonalTeam.Error(),
			"This is a private per-user team; it is single-member by design and cannot take invitations from anyone. Choose a shared team — run 'ox teams' to see them — and retry with --team <slug>.", true
	case errors.Is(err, api.ErrUnauthorized):
		return "unauthenticated", "not authenticated",
			"Run 'ox login' first, then retry 'ox invite'.", true
	case errors.Is(err, api.ErrVersionUnsupported):
		return "version_unsupported", api.ErrVersionUnsupported.Error(),
			"Upgrade ox, then retry 'ox invite'.", true
	default:
		return "", "", "", false
	}
}

// renderPersonalTeamRefusal explains that a private per-user team cannot take
// invitations — a property of the team, so no address and no role would help.
func renderPersonalTeamRefusal(w io.Writer, jsonOutput bool) error {
	if jsonOutput {
		if jerr := writeJSONIndent(w, map[string]string{
			"error":    string(statusPersonalTeam),
			"message":  api.ErrPersonalTeam.Error(),
			"guidance": "This is a private per-user team; it is single-member by design and cannot take invitations from anyone. Choose a shared team — run 'ox teams' to see them — and retry with --team <slug>.",
		}); jerr != nil {
			return jerr
		}
		return cli.ErrSilent
	}
	fmt.Fprintf(w, "%s %s\n\n", inviteErrStyle.Render("✗"), "That's a private team — it can't take invitations.")
	fmt.Fprintf(w, "  %s\n", cli.StyleDim.Render("Private per-user teams are single-member by design, so no"))
	fmt.Fprintf(w, "  %s\n\n", cli.StyleDim.Render("address and no role will work."))
	cli.PrintActionHintTo(w, "ox teams", "Pick a shared team instead")
	return cli.ErrSilent
}

func errInviteNotAuthenticated(w io.Writer, jsonOutput bool) error {
	if jsonOutput {
		if jerr := writeJSONIndent(w, map[string]string{
			"error":    "unauthenticated",
			"message":  "not authenticated",
			"guidance": "Run 'ox login' first, then retry 'ox invite'.",
		}); jerr != nil {
			return jerr
		}
		return cli.ErrSilent
	}
	fmt.Fprintf(w, "%s %s\n\n", inviteErrStyle.Render("✗"), "Not authenticated.")
	cli.PrintActionHintTo(w, "ox login", "Sign in to SageOx")
	return cli.ErrSilent
}

func errInviteNoTeam(w io.Writer, jsonOutput bool) error {
	if jsonOutput {
		if jerr := writeJSONIndent(w, map[string]string{
			"error":    "no_team",
			"message":  "no team to invite to",
			"guidance": "This directory isn't linked to a team. Pass --team <slug>, or run 'ox teams' to list teams you belong to.",
		}); jerr != nil {
			return jerr
		}
		return cli.ErrSilent
	}
	fmt.Fprintf(w, "%s %s\n\n", inviteErrStyle.Render("✗"), "No team to invite to.")
	fmt.Fprintf(w, "  %s\n\n", cli.StyleDim.Render("This directory isn't linked to a team, and --team wasn't given."))
	cli.PrintActionHintTo(w, "ox teams", "List teams you belong to")
	cli.PrintActionHintTo(w, "ox invite <email> --team <slug>", "Name the team explicitly")
	return cli.ErrSilent
}

// inviteLister is the seam for --list / --cancel, mirroring inviteSender.
type inviteLister interface {
	ListTeamInvites(ctx context.Context, teamRef string) ([]api.PendingInvite, error)
	RevokeTeamInvite(ctx context.Context, teamRef, inviteID string) error
}

// inviteListJSON is the --list envelope.
type inviteListJSON struct {
	Team     inviteJSONTeam    `json:"team"`
	Invites  []inviteListEntry `json:"invites"`
	Total    int               `json:"total"`
	Guidance string            `json:"guidance"`
}

type inviteListEntry struct {
	InviteID   string `json:"invite_id"`
	Email      string `json:"email"`
	Role       string `json:"role"`
	InvitedBy  string `json:"invited_by,omitempty"`
	InvitedAt  string `json:"invited_at,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	ExpiresIn  string `json:"expires_in,omitempty"`
	HasExpired bool   `json:"has_expired"`
}

func runInviteList(ctx context.Context, out io.Writer, l inviteLister, target inviteTarget, jsonOutput bool) error {
	invites, err := l.ListTeamInvites(ctx, target.Ref)
	if err != nil {
		return renderInviteOpAbort(out, err, jsonOutput, "list invitations for")
	}
	return renderInviteList(out, target, invites, time.Now(), jsonOutput)
}

// renderInviteList is the pure, testable core of --list.
//
// now is injected so the relative expiry column is deterministic in tests
// rather than depending on wall-clock at assertion time.
func renderInviteList(w io.Writer, target inviteTarget, invites []api.PendingInvite, now time.Time, jsonOutput bool) error {
	entries := make([]inviteListEntry, 0, len(invites))
	for _, inv := range invites {
		e := inviteListEntry{
			InviteID:  inv.ID,
			Email:     inv.Email,
			Role:      inv.Role,
			InvitedBy: inv.InviterName,
			InvitedAt: inv.InvitedAt,
			ExpiresAt: inv.ExpiresAt,
		}
		if exp, perr := time.Parse(time.RFC3339, inv.ExpiresAt); perr == nil {
			e.ExpiresIn = humanizeExpiry(exp, now)
			e.HasExpired = !exp.After(now)
		}
		entries = append(entries, e)
	}

	if jsonOutput {
		return writeJSONIndent(w, inviteListJSON{
			Team:     inviteJSONTeam{TeamID: target.ID, Name: target.Name, Slug: target.Slug},
			Invites:  entries,
			Total:    len(entries),
			Guidance: inviteListGuidance(target, entries),
		})
	}

	fmt.Fprintln(w, inviteHeaderStyle.Render("Pending Invitations"))
	fmt.Fprintln(w, inviteLabelStyle.Render(strings.Repeat("─", len("Pending Invitations"))))

	if len(entries) == 0 {
		fmt.Fprintf(w, "  %s\n", inviteValueStyle.Render(
			fmt.Sprintf("No outstanding invitations for %s.", target.displayName())))
		return nil
	}

	// Every column here is server-supplied and therefore attacker-influenced —
	// an invitation can be addressed to anything, and role/id are echoed back
	// from the same response. Sanitize before measuring so the widths match
	// what is actually printed, and so a crafted value cannot repaint the list
	// into misrepresenting which invitations exist.
	type listRow struct{ email, role, expires, id string }
	rows := make([]listRow, 0, len(entries))
	for _, e := range entries {
		expires := e.ExpiresIn
		if expires == "" {
			expires = "unknown"
		}
		rows = append(rows, listRow{
			email:   inviteCell(e.Email),
			role:    inviteCell(e.Role),
			expires: inviteCell(expires),
			id:      inviteCell(e.InviteID),
		})
	}

	emailW, roleW, expW, idW := len("EMAIL"), len("ROLE"), len("EXPIRES"), 0
	for i, r := range rows {
		// Same cap as the send table, and for the same reason: an address is
		// unbounded, and without this a single long one wraps the row — which
		// happens BEFORE the id-on-its-own-line fallback can help, since that
		// only sheds the id.
		rows[i].email = truncateInviteMessage(r.email, inviteEmailCap)
		if l := lipgloss.Width(rows[i].email); l > emailW {
			emailW = l
		}
		if l := lipgloss.Width(r.role); l > roleW {
			roleW = l
		}
		if l := lipgloss.Width(r.expires); l > expW {
			expW = l
		}
		if l := lipgloss.Width(r.id); l > idW {
			idW = l
		}
	}

	// An invite id is a full UUID (36 columns) and MUST be shown whole — it is
	// what --cancel takes, so a truncated one is useless. Next to a long
	// corporate address that cannot also fit in 80 columns, so the id moves to
	// its own indented line rather than wrapping the table (design rule 12).
	inline := 2 + emailW + 2 + roleW + 2 + expW + 2 + idW
	idInline := inline <= inviteMaxWidth

	header := fmt.Sprintf("  %s  %s  %s",
		inviteLabelStyle.Render(padInviteCell("EMAIL", emailW)),
		inviteLabelStyle.Render(padInviteCell("ROLE", roleW)),
		inviteLabelStyle.Render("EXPIRES"))
	if idInline {
		header += "  " + inviteLabelStyle.Render("INVITE ID")
	}
	fmt.Fprintln(w, strings.TrimRight(header, " "))

	for i, r := range rows {
		expiryStyle := inviteValueStyle
		if entries[i].HasExpired {
			expiryStyle = inviteWarnStyle
		}
		row := fmt.Sprintf("  %s  %s  %s",
			padInviteCell(r.email, emailW),
			inviteValueStyle.Render(padInviteCell(r.role, roleW)),
			expiryStyle.Render(padInviteCell(r.expires, expW)))
		if idInline {
			row += "  " + inviteValueStyle.Render(r.id)
		}
		fmt.Fprintln(w, strings.TrimRight(row, " "))
		if !idInline {
			fmt.Fprintf(w, "    %s\n", inviteValueStyle.Render(r.id))
		}
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s\n", inviteValueStyle.Render(
		fmt.Sprintf("%d outstanding · %s", len(entries), target.displayName())))
	cli.PrintActionHintTo(w, "ox invite --cancel <invite id>", "Cancel one of these")
	return nil
}

// humanizeExpiry renders the time left on an invitation, or how long ago it
// lapsed. Invitations live 7 days, so day/hour granularity is the useful scale.
//
// Deliberately does NOT use formatAge: that helper formats an AGE and appends
// its own "ago", which yields "in 5d ago" and "expired 2d ago ago" when the
// value is a remaining duration rather than an elapsed one.
func humanizeExpiry(expires, now time.Time) string {
	if d := expires.Sub(now); d > 0 {
		return "in " + formatInviteDuration(d)
	} else {
		return "expired " + formatInviteDuration(-d) + " ago"
	}
}

// formatInviteDuration renders a bare duration — no "in", no "ago" — at the
// coarsest unit that still says something useful.
func formatInviteDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return "under a minute"
	}
}

func inviteListGuidance(target inviteTarget, entries []inviteListEntry) string {
	ref := target.Slug
	if ref == "" {
		ref = target.Ref
	}
	if len(entries) == 0 {
		return fmt.Sprintf("No outstanding invitations for %s. Send one with 'ox invite <email> --team %s'.", target.displayName(), ref)
	}
	var expired int
	for _, e := range entries {
		if e.HasExpired {
			expired++
		}
	}
	msg := fmt.Sprintf("%d outstanding invitation(s) for %s. Cancel one with 'ox invite --cancel <invite_id> --team %s' using the invite_id field, not the invite link.", len(entries), target.displayName(), ref)
	if expired > 0 {
		msg += fmt.Sprintf(" %d has already lapsed and can be canceled to tidy the list.", expired)
	}
	return msg
}

func runInviteCancel(ctx context.Context, out io.Writer, l inviteLister, target inviteTarget, inviteID string, jsonOutput, skipConfirm bool) error {
	inviteID = strings.TrimSpace(inviteID)
	if inviteID == "" {
		return fmt.Errorf("--cancel needs an invite id (see 'ox invite --list')")
	}
	// An email address here is the most likely mistake, and the server would
	// answer with an opaque 400. Name the actual problem instead.
	if strings.Contains(inviteID, "@") {
		return fmt.Errorf("--cancel takes an invite id, not an email address — run 'ox invite --list' to find it")
	}

	if !skipConfirm && !jsonOutput && cli.IsInteractive() {
		if !cli.ConfirmYesNo(fmt.Sprintf("Cancel invitation %s?", inviteID), true) {
			fmt.Fprintln(out, "Canceled.")
			return nil
		}
	}

	if err := l.RevokeTeamInvite(ctx, target.Ref, inviteID); err != nil {
		return renderInviteOpAbort(out, err, jsonOutput, "cancel invitations for")
	}

	if jsonOutput {
		return writeJSONIndent(out, map[string]any{
			"team":      inviteJSONTeam{TeamID: target.ID, Name: target.Name, Slug: target.Slug},
			"invite_id": inviteID,
			"status":    "canceled",
			"guidance":  "The invitation was canceled. Its link no longer works. Run 'ox invite --list' to see what remains.",
		})
	}
	fmt.Fprintf(out, "%s Invitation canceled.\n\n", inviteOKStyle.Render("✓"))
	fmt.Fprintf(out, "  %s\n", inviteValueStyle.Render("Its link no longer works."))
	return nil
}

// renderInviteOpAbort reports a whole-operation failure for --list/--cancel.
// verb completes the sentence "you can't <verb> this team".
func renderInviteOpAbort(w io.Writer, err error, jsonOutput bool, verb string) error {
	switch {
	case errors.Is(err, api.ErrUnauthorized):
		return errInviteNotAuthenticated(w, jsonOutput)
	case errors.Is(err, api.ErrInviteUnsupported), errors.Is(err, api.ErrInviteNotAMember):
		if jsonOutput {
			if jerr := writeJSONIndent(w, map[string]string{
				"error":    "no_access",
				"message":  err.Error(),
				"guidance": "Either the team doesn't exist or you can't reach it. Run 'ox teams' to list teams you belong to.",
			}); jerr != nil {
				return jerr
			}
			return cli.ErrSilent
		}
		fmt.Fprintf(w, "%s %s\n\n", inviteErrStyle.Render("✗"), err.Error())
		cli.PrintActionHintTo(w, "ox teams", "List teams you belong to")
		return cli.ErrSilent
	case errors.Is(err, api.ErrInviteForbidden):
		reason := api.InviteForbiddenReason(err)
		if reason == "" {
			reason = fmt.Sprintf("the server won't let you %s this team", verb)
		}
		if jsonOutput {
			if jerr := writeJSONIndent(w, map[string]string{
				"error":   "forbidden",
				"message": reason,
				// No policy claim here: the server decides, and it may change.
				"guidance": "The server refused this operation. Ask a team owner or admin if you need it done.",
			}); jerr != nil {
				return jerr
			}
			return cli.ErrSilent
		}
		fmt.Fprintf(w, "%s %s\n\n", inviteErrStyle.Render("✗"), reason)
		fmt.Fprintf(w, "  %s\n", cli.StyleDim.Render("Ask a team owner or admin if you need this done."))
		return cli.ErrSilent
	}
	return err
}

// pluralizeInvitations renders a count with the right noun. The "(s)" form was
// leaking into agent-facing guidance, which AI coworkers quote back to humans
// verbatim.
func pluralizeInvitations(n int) string {
	if n == 1 {
		return "1 invitation"
	}
	return fmt.Sprintf("%d invitations", n)
}
