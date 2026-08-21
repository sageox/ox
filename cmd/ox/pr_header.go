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

It names the team, links the session(s) and plan(s) that produced the change,
and whispers a subtle enrichment stat. Paste the output above your description;
keep the 'SageOx-Session:' trailer at the bottom.

The line is built from the primitives that survive GitHub's PR-body sanitizer
(a theme-adaptive <picture> wordmark, real <a> links, a <sub> caption) and
degrades cleanly: no session, no plan, and no enrichment each still render.

Examples:
  # Auto-link the current session; no enrichment
  ox pr header

  # Link two plans the session produced, with enrichment counts
  ox pr header --plan pln_4d8e2f --plan pln_1a6b9c --prior-art 2 --collisions 1

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
	f.Int("prior-art", 0, "enrichment: related sessions surfaced")
	f.Int("collisions", 0, "enrichment: concurrent edits flagged")
	f.Bool("no-stat", false, "suppress the enrichment whisper")
	f.String("style", "", "whisper render: text | image | auto (default: pr_visuals.style)")
	f.Bool("allow-unconfirmed", false, "accept links that may not be server-visible yet — the current session before upload, and explicit --session/--plan refs — without a warning (may 404)")
}

// prHeaderResponse is the --json shape for agent consumption: the paste-ready
// markdown plus the resolved inputs, so an agent can verify what it will paste.
type prHeaderResponse struct {
	Markdown string           `json:"markdown"`
	Tier     string           `json:"tier"` // "text" | "image"
	Team     string           `json:"team,omitempty"`
	Sessions []string         `json:"sessions,omitempty"`
	Plans    []string         `json:"plans,omitempty"`
	Signals  prheader.Signals `json:"signals"`
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

	in := prheader.Input{
		ShowStat: !flagBool(cmd, "no-stat"),
	}
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

	// Explicit --session/--plan refs are the caller's assertion. Unlike the auto
	// current session (whose local recording state ox can check), an arbitrary id
	// carries no local signal — and ox will not add a per-id network round-trip to
	// a render command that must never fail on an unreachable remote. So explicit
	// refs are included AS GIVEN, but never SILENTLY: a typo'd or not-yet-uploaded
	// ref would 404 for a reviewer, so warn (stderr only, never in the PR markdown).
	// --allow-unconfirmed means "I accept a possible 404" and silences it.
	if !allowUnconfirmed && (len(sessionFlags) > 0 || len(planFlags) > 0) {
		fmt.Fprintln(cmd.ErrOrStderr(), "note: explicit --session/--plan links are included as given and not verified against the server — confirm they resolve or a reviewer may hit a 404 (pass --allow-unconfirmed to accept and silence)")
	}

	// Enrichment signals come from the agent (it holds the ox plan enrich
	// counts). Material is derived — we only whisper when a signal actually
	// fired, never on an empty set.
	in.Signals = prheader.Signals{
		PriorArt:   flagInt(cmd, "prior-art"),
		Collisions: flagInt(cmd, "collisions"),
	}
	in.Signals.Material = in.Signals.PriorArt > 0 || in.Signals.Collisions > 0

	// Tier B (baked floated strip) only when a whisper is actually warranted (real
	// stats + a Plan link to verify them) AND the style allows AND an uploader can
	// host it. Any failure falls back to Tier A text — the header must never fail
	// because the image path is unreachable.
	tier := "text"
	if in.WantsWhisper() && resolvePRStyle(cmd, gitRoot) != config.PRVisualsStyleText {
		if strip, err := prheader.UploadStrip(resolveStripUploader(gitRoot), in.Signals); err == nil {
			in.Strip = strip
			tier = "image"
		}
	}

	markup := prheader.Render(in)

	if flagBool(cmd, "json") {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(prHeaderResponse{
			Markdown: markup,
			Tier:     tier,
			Team:     in.TeamName,
			Sessions: sessionURLs,
			Plans:    planURLs,
			Signals:  in.Signals,
		})
	}
	// Render returns "" when the line would carry no payload (no team, session,
	// plan, or whisper). Emit nothing to stdout rather than a bare wordmark.
	if markup == "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "no team, session, or plan to credit — nothing to paste")
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

// resolvePRStyle resolves the whisper style: the --style flag overrides the
// config; an unknown flag value is ignored in favor of the config default.
func resolvePRStyle(cmd *cobra.Command, gitRoot string) string {
	if v := strings.TrimSpace(flagString(cmd, "style")); v != "" {
		switch v {
		case config.PRVisualsStyleText, config.PRVisualsStyleImage, config.PRVisualsStyleAuto:
			return v
		}
	}
	return config.PRVisualsStyle(gitRoot)
}

// resolveStripUploader returns the Uploader for the Tier-B baked strip. It is a
// stub today: ox has no public-asset upload path yet (see the plan / bd
// follow-up), so this returns nil and every style resolves to Tier A text. When
// a SageOx cloud-image endpoint (or a credentialed CDN uploader) lands, wire it
// here — the rest of the flow (render, fallback, tests) already handles Tier B.
func resolveStripUploader(_ string) prheader.Uploader {
	return nil
}

// Small flag accessors keep runPRHeader readable; cobra's error returns are safe
// to drop here because every flag is registered with a default above.
func flagBool(cmd *cobra.Command, name string) bool {
	v, _ := cmd.Flags().GetBool(name)
	return v
}

func flagInt(cmd *cobra.Command, name string) int {
	v, _ := cmd.Flags().GetInt(name)
	return v
}

func flagString(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}

func flagStringArray(cmd *cobra.Command, name string) []string {
	v, _ := cmd.Flags().GetStringArray(name)
	return v
}
