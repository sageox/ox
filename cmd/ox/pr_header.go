package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/prheader"
	"github.com/spf13/cobra"
)

// prCmd groups pull-request authoring helpers. It is distinct from `ox code prs`
// (which LISTS indexed PRs for triage): `ox pr` AUTHORS content that goes INTO a
// PR — today, the SageOx credit line.
var prCmd = &cobra.Command{
	Use:   "pr",
	Short: "Author SageOx content for pull requests",
	Long: `Author SageOx content that goes into a pull request.

Distinct from 'ox code prs', which lists indexed PRs for triage.`,
}

var prHeaderCmd = &cobra.Command{
	Use:   "header",
	Short: "Emit the SageOx credit line for the top of a PR description",
	Long: `Emit a thin, on-brand credit line to paste at the very TOP of a pull-request
description body — the human-facing counterpart to the 'SageOx-Session:' trailer.

It links the session(s), plan(s), and discussion(s) that produced the change and
names the team they belong to. Paste the output above your description; keep the
'SageOx-Session:' trailer at the bottom.

The line renders ONLY when it has at least one artifact to link. A team name
alone is not a credit — a wordmark with nothing behind it is a logo stamp, not
provenance — so with no session, plan, or discussion the command prints nothing
and explains why on stderr.

ox verifies only the CURRENT session, from local recording state, and withholds
its link until the server has confirmed it. Ids passed explicitly are the
caller's assertion: ox includes them as given rather than adding a network
round-trip to a render command that must never fail on an unreachable remote, so
it warns on stderr instead. Pass --allow-unconfirmed to accept a possible 404 and
silence the warning.

The markup is built from the primitives that survive GitHub's PR-body sanitizer
(a theme-adaptive ` + "`<picture>`" + ` wordmark and real ` + "`<a>`" + ` links) and fits on one line.

Examples:
  # Auto-link the current session
  ox pr header

  # Link two plans the session produced
  ox pr header --plan pln_4d8e2f --plan pln_1a6b9c

  # Credit a recorded discussion the PR came directly out of
  ox pr header --discussion cnv_019ff2f5-2079-7be1-b05e-8caad2772e61

  # Write straight into a PR body file (never a heredoc — it mangles the markup)
  ox pr header > body.md && cat description.md >> body.md
  gh pr create --body-file body.md`,
	RunE: runPRHeader,
}

func init() {
	prCmd.GroupID = "dev" // sits with `ox plan`, `ox session`, `ox code`
	prCmd.AddCommand(prHeaderCmd)
	rootCmd.AddCommand(prCmd)

	f := prHeaderCmd.Flags()
	f.StringArray("session", nil, "session URL or ses_ id to link (repeatable; defaults to the current session)")
	f.StringArray("plan", nil, "plan URL or pln_ id to link (repeatable)")
	f.StringArray("discussion", nil, "recorded-discussion URL or cnv_ id to link (repeatable; only when the PR came directly out of it)")
	f.Bool("allow-unconfirmed", false, "accept links that may not be server-visible yet — the current session before upload, and explicit --session/--plan/--discussion refs — without a warning (may 404)")
}

// prHeaderGuidance is the behavioral contract for the AI coworker pasting this
// line. It ships in the --json payload rather than only in a SKILL body: skills
// are Claude-only, and Codex/Droid install no commands, so guidance that lives in
// a skill never reaches them. One source of truth, delivered by the live binary,
// which cannot drift from the behavior it describes.
const prHeaderGuidance = "Paste this markdown VERBATIM as the first lines of the PR body, above your " +
	"summary — never hand-author or edit it; the markup is tuned to GitHub's PR-body sanitizer and " +
	"editing it is how it renders as a bordered table or a vanished wordmark. Write the body via a " +
	"file, never a heredoc (a heredoc mangles the markup). Add it only when SageOx-delivered team " +
	"context measurably shaped the work; if it did not, emit neither this header nor the " +
	"SageOx-Session: trailer. When it did, the header is the FIRST line of the body and the " +
	"SageOx-Session: trailer stays the LAST. Do not retitle the links or add artifact titles — the " +
	"/c/ and /plan/ URLs are deliberately opaque so nothing about the work leaks into a public PR. " +
	"An empty markdown field means there was nothing to credit: paste nothing and move on."

// prHeaderResponse is the --json shape for agent consumption: the paste-ready
// markdown plus the resolved inputs, so an agent can verify what it will paste.
// Markdown is empty when there was nothing to credit.
type prHeaderResponse struct {
	Markdown    string   `json:"markdown"`
	Team        string   `json:"team,omitempty"`
	Sessions    []string `json:"sessions,omitempty"`
	Plans       []string `json:"plans,omitempty"`
	Discussions []string `json:"discussions,omitempty"`
	Guidance    string   `json:"guidance"`
}

func runPRHeader(cmd *cobra.Command, _ []string) error {
	gitRoot := findGitRoot()

	// Respect a team/user opt-out. A disabled header no-ops with a hint rather
	// than emitting an empty block into a PR body.
	if !config.PRVisualsHeader(gitRoot) {
		fmt.Fprintln(cmd.ErrOrStderr(), "pr_visuals.header is off — enable with: ox config set pr_visuals.header on")
		return nil
	}

	cfg, _ := config.LoadProjectConfig(gitRoot)
	ep := prResolveEndpoint(cfg)

	var in prheader.Input
	if cfg != nil {
		in.TeamName = cfg.TeamName
		if slug := strings.TrimSpace(cfg.Team); slug != "" {
			in.TeamURL = ep + "/t/" + url.PathEscape(slug)
		}
	}

	// Sessions: explicit flags win; otherwise auto-link the current live session
	// (only when server-visible, unless --allow-unconfirmed).
	allowUnconfirmed := flagBool(cmd, "allow-unconfirmed")
	sessionFlags := flagStringArray(cmd, "session")
	planFlags := flagStringArray(cmd, "plan")
	discussionFlags := flagStringArray(cmd, "discussion")
	sessionArgs := sessionFlags
	if len(sessionArgs) == 0 {
		if u, unconfirmed := autoSessionURL(gitRoot, allowUnconfirmed); u != "" {
			sessionArgs = []string{u}
		} else if unconfirmed {
			// A real local session exists but the server has not confirmed it —
			// withhold the link (it would 404) and tell the coworker how to
			// proceed, honoring the "no link a reviewer cannot open" promise.
			fmt.Fprintln(cmd.ErrOrStderr(), "current session is not yet server-visible — link withheld; re-run once it uploads, or pass --allow-unconfirmed to link it now (may 404)")
		}
	}
	sessionURLs := make([]string, 0, len(sessionArgs))
	for _, s := range sessionArgs {
		if u := artifactURL(ep, s, "/c/", "ses_"); u != "" {
			in.Sessions = append(in.Sessions, prheader.Session{URL: u})
			sessionURLs = append(sessionURLs, u)
		}
	}

	planURLs := make([]string, 0)
	for _, p := range planFlags {
		if u := artifactURL(ep, p, "/plan/", "pln_"); u != "" {
			in.Plans = append(in.Plans, prheader.Plan{URL: u})
			planURLs = append(planURLs, u)
		}
	}

	// Discussions resolve through the SAME universal /c/ route as sessions: a
	// conversation id is the cnv_ twin of a ses_ id, and /c/ resolves either.
	// Never auto-discovered — only the agent can judge that a recorded discussion
	// is DIRECTLY about this PR, which is rare.
	discussionURLs := make([]string, 0)
	for _, d := range discussionFlags {
		if u := artifactURL(ep, d, "/c/", "cnv_"); u != "" {
			in.Discussions = append(in.Discussions, prheader.Discussion{URL: u})
			discussionURLs = append(discussionURLs, u)
		}
	}

	// Explicit refs are the caller's assertion. Unlike the auto current session
	// (whose local recording state ox can check), an arbitrary id carries no local
	// signal — and ox will not add a per-id network round-trip to a render command
	// that must never fail on an unreachable remote. So explicit refs are included
	// AS GIVEN, but never SILENTLY: a typo'd or not-yet-uploaded ref would 404 for
	// a reviewer, so warn (stderr only, never in the PR markdown).
	// --allow-unconfirmed means "I accept a possible 404" and silences it.
	if !allowUnconfirmed && (len(sessionFlags) > 0 || len(planFlags) > 0 || len(discussionFlags) > 0) {
		fmt.Fprintln(cmd.ErrOrStderr(), "note: explicit --session/--plan/--discussion links are included as given and not verified against the server — confirm they resolve or a reviewer may hit a 404 (pass --allow-unconfirmed to accept and silence)")
	}

	markup := prheader.Render(in)

	if flagBool(cmd, "json") {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(prHeaderResponse{
			Markdown:    markup,
			Team:        in.TeamName,
			Sessions:    sessionURLs,
			Plans:       planURLs,
			Discussions: discussionURLs,
			Guidance:    prHeaderGuidance,
		})
	}
	// Render returns "" when the header would link nothing. Emit nothing to
	// stdout rather than a bare wordmark stamped onto a PR body.
	if markup == "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "no session, plan, or discussion to link — nothing to paste (a team name alone is not a credit)")
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), markup)
	return nil
}

// prResolveEndpoint returns the normalized web endpoint, tolerating a nil config
// (falls back to the environment/default) so the command still works with
// fully-explicit flags outside a SageOx repo.
func prResolveEndpoint(cfg *config.ProjectConfig) string {
	if cfg != nil {
		if ep := endpoint.NormalizeEndpoint(cfg.GetEndpoint()); ep != "" {
			return ep
		}
	}
	return endpoint.NormalizeEndpoint(endpoint.Get())
}

// autoSessionURL resolves the current live session's /c/ link for auto-linking
// into a PR header. It returns the URL to link (or "") and whether a real local
// session was WITHHELD because the server has not confirmed it yet.
//
// A locally minted id is not evidence the remote resolver knows it, so a pending
// session is withheld by default (unconfirmed == true) and the caller explains
// the link will appear once upload completes; allowUnconfirmed links it anyway —
// the coworker has accepted a possible 404. No live session at all, session
// attribution turned off, or a recording predating start-minted ids yields
// ("", false): nothing to link and nothing to withhold.
//
// This deliberately does NOT route through liveSessionConversationURL, which
// omits the pending check (it stamps plan artifacts, a different contract) and
// would link an unconfirmed session into a public PR body.
func autoSessionURL(gitRoot string, allowUnconfirmed bool) (url string, unconfirmed bool) {
	if attr := loadResolvedAttribution(); attr.Session == "" {
		return "", false // session attribution disabled — no link expected
	}
	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil || cfg == nil {
		return "", false
	}
	agentID, _ := detectAgentContext()
	state := loadPlanRecordingState(gitRoot, agentID)
	if state == nil || state.SessionID == "" {
		return "", false
	}
	u := buildConversationURL(cfg, state.SessionID)
	if u == "" {
		return "", false // no valid ses_ id (older binary) — nothing to link
	}
	if effectiveSessionPublishing() == config.SessionPublishingManual {
		// Checked BEFORE the pending branch on purpose. A session can be both
		// pending and manual, and reporting unconfirmed=true there makes the
		// caller suggest --allow-unconfirmed — a flag that then yields no URL,
		// because manual withholds unconditionally. Two states, opposite
		// remedies: "pending" resolves itself on the next upload and may be
		// forced; "manual" never resolves until someone explicitly uploads, so
		// forcing it could only ever emit a permanently dead link into a public
		// PR body.
		return "", false
	}
	if state.LifecycleRegistrationState == "pending" && !allowUnconfirmed {
		return "", true // server has not observed it — withhold, signal the caller
	}
	return u, false
}

// artifactURL maps a flag value — either a full http(s) URL or a bare id — to a
// web URL. A full URL passes through; a bare id becomes {ep}{pathPrefix}{id},
// tolerating a missing type prefix so both "pln_abc" and "abc" resolve. A bare
// id that names a DIFFERENT type (e.g. a "ses_" id passed to a --plan slot) is
// rejected rather than resolved into a plausible-but-wrong 404 URL.
func artifactURL(ep, raw, pathPrefix, idPrefix string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	if p := idTypePrefix(raw); p != "" && p != idPrefix {
		return ""
	}
	if ep == "" {
		return ""
	}
	return ep + pathPrefix + url.PathEscape(raw)
}

// idTypePrefix returns the leading "<type>_" token of a bare id (e.g. "pln_" for
// "pln_4d8e2f"), or "" when the id carries no such prefix ("4d8e2f"). Only a
// lowercase-letter run followed by an underscore counts, so an id whose body
// merely contains underscores is treated as prefix-less.
func idTypePrefix(raw string) string {
	i := strings.IndexByte(raw, '_')
	if i <= 0 {
		return ""
	}
	for _, r := range raw[:i] {
		if r < 'a' || r > 'z' {
			return ""
		}
	}
	return raw[:i+1]
}

// Small flag accessors keep runPRHeader readable; cobra's error returns are safe
// to drop here because every flag is registered with a default above.
func flagBool(cmd *cobra.Command, name string) bool {
	v, _ := cmd.Flags().GetBool(name)
	return v
}

func flagStringArray(cmd *cobra.Command, name string) []string {
	v, _ := cmd.Flags().GetStringArray(name)
	return v
}
