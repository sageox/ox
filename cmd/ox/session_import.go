package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/flags"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/codexhistory"
	"github.com/sageox/ox/pkg/sessionhistory"
	"github.com/sageox/ox/pkg/sessionprovenance"
	"github.com/spf13/cobra"
)

type importOptions struct {
	Agent, Since           string
	IDs                    []string
	All, DryRun, JSON, Yes bool
}
type importDestination struct {
	Endpoint string `json:"endpoint"`
	RepoID   string `json:"repo_id"`
	TeamID   string `json:"team_id"`
	TeamName string `json:"team_name"`
	UserID   string `json:"user_id"`
}
type importCandidate struct {
	sessionhistory.Snapshot
	Status          string `json:"status"`
	Reason          string `json:"reason,omitempty"`
	Title           string `json:"title,omitempty"`
	RedactedEntries int    `json:"redacted_entries"`
	SessionName     string `json:"session_name"`
}
type importReport struct {
	Destination       importDestination `json:"destination"`
	Disclosure        string            `json:"disclosure"`
	Candidates        []importCandidate `json:"candidates"`
	NeedsConfirmation bool              `json:"needs_confirmation,omitempty"`
}

var codexImportAdapter sessionhistory.Adapter = codexhistory.Adapter{}

var sessionImportCmd = newSessionImportCommand()

func newSessionImportCommand() *cobra.Command {
	o := &importOptions{Agent: "codex"}
	c := &cobra.Command{Use: "import", Short: "Review and import local Codex history into this repository's Ledger", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return runSessionImport(c, o) }}
	f := c.Flags()
	f.StringVar(&o.Agent, "agent", "codex", "Local history source (codex)")
	f.StringVar(&o.Since, "since", "", "Last activity on or after this RFC3339 time or YYYY-MM-DD date")
	f.StringSliceVar(&o.IDs, "session", nil, "Native session IDs to review (repeatable)")
	f.BoolVar(&o.All, "all-history", false, "Review all history rather than the last 30 days")
	f.BoolVar(&o.DryRun, "dry-run", false, "Read-only preview; never upload or repair")
	f.BoolVar(&o.JSON, "json", false, "Structured preview and results")
	f.BoolVar(&o.Yes, "yes", false, "Upload explicitly selected, proven eligible sessions; never uncertain history")
	return c
}
func importDest(root string) (importDestination, error) {
	p, e := config.LoadProjectConfig(root)
	if e != nil {
		return importDestination{}, e
	}
	if p == nil || p.RepoID == "" || p.TeamID == "" {
		return importDestination{}, fmt.Errorf("repository and team must be initialized before import")
	}
	ep := endpoint.GetForProject(root)
	token, err := auth.PeekTokenForEndpoint(ep)
	if err != nil {
		return importDestination{}, err
	}
	u := ""
	if token != nil {
		u = token.UserInfo.UserID
	}
	if u == "" && token != nil && token.AccessToken != "" {
		// Older device logins saved an email/name but omitted the user ID.
		// Read the authenticated principal without refreshing or rewriting auth.
		principal, err := auth.Introspect(ep, token.AccessToken)
		if err != nil {
			return importDestination{}, fmt.Errorf("verify import identity: %w", err)
		}
		if principal.PrincipalKind == auth.PrincipalKindUser && principal.User != nil {
			u = principal.User.ID
		}
	}
	if u == "" {
		return importDestination{}, fmt.Errorf("a verified human sign-in is required before import")
	}
	return importDestination{ep, p.RepoID, p.TeamID, p.TeamName, u}, nil
}

// Preview reads access metadata using the existing credential without refreshing
// it; expired authentication must not make an observational command write files.
func importAudience(d importDestination) (string, error) {
	token, err := auth.PeekTokenForEndpoint(d.Endpoint)
	if err != nil || token == nil {
		return "", fmt.Errorf("destination audience unavailable; sign in before preview")
	}
	detail, err := api.NewRepoClientWithEndpoint(d.Endpoint).WithAuthToken(token.AccessToken).GetRepoDetail(d.RepoID)
	if err != nil || detail == nil {
		return "", fmt.Errorf("cannot verify destination audience")
	}
	if err := checkImportTeam(detail, d.TeamID); err != nil {
		return "", err
	}
	var audience string
	switch detail.Visibility {
	case "public":
		audience = "This repository is public; its Ledger can be read by anyone."
	case "private":
		audience = "This repository is private; coworkers with repository access can read its Ledger."
	default:
		return "", fmt.Errorf("destination visibility is unknown")
	}
	return fmt.Sprintf("SageOx retains a copy in repository %s for %s (%s). %s Local originals are retained.", d.RepoID, d.TeamName, d.TeamID, audience), nil
}
func checkImportTeam(detail *api.RepoDetailResponse, teamID string) error {
	for _, team := range detail.TeamContexts {
		if team.TeamID == teamID && team.AccessLevel == "member" {
			return nil
		}
	}
	return fmt.Errorf("selected team's access to the destination could not be verified")
}
func importSince(o *importOptions, now time.Time) (time.Time, error) {
	if o.All && o.Since != "" {
		return time.Time{}, fmt.Errorf("--all-history and --since are mutually exclusive")
	}
	if o.All || len(o.IDs) > 0 {
		return time.Time{}, nil
	}
	if o.Since == "" {
		return now.AddDate(0, 0, -30), nil
	}
	for _, format := range []string{time.RFC3339, "2006-01-02"} {
		if t, e := time.Parse(format, o.Since); e == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("--since requires an RFC3339 time or YYYY-MM-DD date")
}
func importGit(ctx context.Context, dir string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	b, e := c.Output()
	if e != nil {
		return "", fmt.Errorf("git %s failed: %w", args[0], e)
	}
	return strings.TrimSpace(string(b)), nil
}
func importCommonDir(ctx context.Context, dir string) (string, error) {
	s, e := importGit(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil {
		return "", e
	}
	return filepath.EvalSymlinks(s)
}
func importEntry(e adapterprotocol.RawEntry) session.SessionEntry {
	stamp, _ := time.Parse(time.RFC3339Nano, e.Timestamp)
	return session.SessionEntry{Timestamp: stamp, Type: session.SessionEntryType(e.Role), Content: e.Content, CallID: e.CallID, ToolName: e.ToolName, ToolInput: e.ToolInput, ToolOutput: e.ToolOutput, IsError: e.IsError}
}
func importedName(repo string, s sessionhistory.Snapshot) string {
	sum := sha256.Sum256([]byte(repo + "\x00" + s.NativeID + "\x00" + s.Generation + "\x00" + s.Digest))
	return s.StartedAt.UTC().Format("2006-01-02T15-04-05") + "-codex-import-" + hex.EncodeToString(sum[:16])
}
func legacyCodexHistory(ledger string) (bool, error) {
	dirs, e := os.ReadDir(filepath.Join(ledger, "sessions"))
	if errors.Is(e, os.ErrNotExist) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		m, e := lfs.ReadSessionMeta(filepath.Join(ledger, "sessions", d.Name()))
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return false, e
		}
		if m.Source == nil && (m.AgentType == "codex" || m.AgentType == "codex-cli") {
			return true, nil
		}
	}
	return false, nil
}

func scanImport(ctx context.Context, root, ledger string, dest importDestination, o *importOptions, now time.Time) (importReport, error) {
	r := importReport{Destination: dest, Disclosure: fmt.Sprintf("SageOx retains a copy in repository %s for %s (%s). Everyone with repository access can read it; public repositories may be visible publicly. Local originals are retained.", dest.RepoID, dest.TeamName, dest.TeamID)}
	since, e := importSince(o, now)
	if e != nil {
		return r, e
	}
	home, e := codexImportAdapter.Home()
	if e != nil {
		return r, e
	}
	paths, e := codexImportAdapter.Discover(home)
	if e != nil {
		return r, e
	}
	common, e := importCommonDir(ctx, root)
	if e != nil {
		return r, e
	}
	legacy, e := legacyCodexHistory(ledger)
	if e != nil {
		return r, e
	}
	ids := map[string]bool{}
	for _, id := range o.IDs {
		if _, e = sessionprovenance.Path(id); e != nil {
			return r, e
		}
		ids[id] = false
	}
	seen := map[string]string{}
	for _, p := range paths {
		header, headerErr := codexImportAdapter.Inspect(p)
		if headerErr != nil {
			continue
		}
		if len(ids) > 0 {
			if _, selected := ids[header.NativeID]; !selected {
				continue
			}
			ids[header.NativeID] = true
		}
		headerCommon, headerErr := importCommonDir(ctx, header.CWD)
		if headerErr != nil || headerCommon != common || header.Internal {
			continue
		}
		writer, e := session.NewRawStreamWriter(io.Discard, root)
		if e != nil {
			return r, e
		}
		title := ""
		redacted := 0
		s, err := codexImportAdapter.Stream(ctx, p, func(raw adapterprotocol.RawEntry) error {
			entry := importEntry(raw)
			before := entry
			if e := writer.WriteEntry(&entry); e != nil {
				return e
			}
			if entry != before {
				redacted++
			}
			if title == "" && entry.Type == session.SessionEntryTypeUser {
				title = strings.TrimSpace(entry.Content)
				if len(title) > 120 {
					title = title[:120]
				}
			}
			return nil
		})
		if err != nil {
			r.Candidates = append(r.Candidates, importCandidate{Snapshot: header, Status: "failed", Reason: err.Error()})
			continue
		}
		if s.NativeID == "" {
			continue
		}
		if len(ids) > 0 {
			if _, ok := ids[s.NativeID]; !ok {
				continue
			}
			ids[s.NativeID] = true
		}
		if !since.IsZero() && s.LastActivity.Before(since) {
			continue
		}
		sourceCommon, commonErr := importCommonDir(ctx, s.CWD)
		if commonErr != nil || sourceCommon != common {
			continue
		}
		if s.Internal {
			continue
		}
		c := importCandidate{Snapshot: s, Status: "ready", Title: title, RedactedEntries: redacted, SessionName: importedName(dest.RepoID, s)}
		if err != nil {
			c.Status = "failed"
			c.Reason = err.Error()
		} else if s.Entries == 0 {
			c.Status = "excluded"
			c.Reason = "no conversation entries"
		} else if s.NativeID == os.Getenv("CODEX_THREAD_ID") || s.InFlight || now.Sub(s.ModifiedAt) < 2*time.Minute {
			c.Status = "active"
			c.Reason = "source may still be recording; finish the active turn before importing"
		} else if s.ParentID != "" || legacy {
			c.Status = "uncertain"
			c.Reason = "legacy coverage, fork ancestry, or stopped-state cannot be proven; review possible overlap and recording exclusions individually"
		}
		record, e := session.ReadSourceRecord(ledger, s.NativeID)
		if e != nil {
			c.Status = "failed"
			c.Reason = "cannot safely read source record"
		} else if record == nil && c.Status == "ready" {
			// No receipt also means no durable evidence of earlier recording intent.
			// Legacy history must not become eligible merely because titles differ.
			c.Status = "uncertain"
			c.Reason = "no source provenance; individually review prior uploads and recording exclusions"
		} else if record != nil {
			if record.Excludes(0, s.Size) {
				c.Status = "excluded"
				c.Reason = "recording intent excludes this native history"
			} else if record.Generation != s.Generation && len(record.Coverage) > 0 {
				c.Status = "uncertain"
				c.Reason = "a different source generation was recorded; automatic extension is disabled"
			} else {
				if len(record.Coverage) > 0 {
					c.Status = "uncertain"
					c.Reason = "partial source coverage requires individual reconciliation"
				}
				if len(record.Coverage) == 1 && record.Coverage[0].End < s.Size && err == nil && s.Entries > 0 && !s.InFlight && s.NativeID != os.Getenv("CODEX_THREAD_ID") && now.Sub(s.ModifiedAt) >= 2*time.Minute {
					coverage := record.Coverage[0]
					meta, metaErr := lfs.ReadSessionMeta(filepath.Join(ledger, "sessions", coverage.SessionName))
					extension := c
					extension.SessionName = coverage.SessionName
					if metaErr == nil && validateImportExtension(&extension, record, meta, dest.RepoID) == nil {
						c.SessionName = coverage.SessionName
						c.Status = "ready"
						c.Reason = "append-only continuation; replaces the same conversation after fresh remote verification"
					}
				}
				for _, coverage := range record.Coverage {
					if coverage.Start == 0 && coverage.End == s.Size {
						meta, metaErr := lfs.ReadSessionMeta(filepath.Join(ledger, "sessions", coverage.SessionName))
						if metaErr != nil || meta == nil || meta.Source == nil || meta.Source.SnapshotDigest != s.Digest {
							c.Status = "uncertain"
							c.Reason = "coverage exists but matching source content cannot be proven"
							break
						}
						c.Status = "already_uploaded"
						c.SessionName = coverage.SessionName
						c.Reason = "source coverage exists; remote receipt will be verified before any upload"
						break
					}
				}
			}
		}
		if c.Status != "failed" && c.Status != "excluded" && (s.NativeID == os.Getenv("CODEX_THREAD_ID") || s.InFlight || now.Sub(s.ModifiedAt) < 2*time.Minute) {
			c.Status = "active"
			c.Reason = "source may still be recording; finish the active turn before importing"
		}
		if generation, ok := seen[s.NativeID]; ok {
			if generation == s.Digest {
				continue
			}
			c.Status = "uncertain"
			c.Reason = "multiple different local snapshots share this native identity"
			for i := range r.Candidates {
				if r.Candidates[i].NativeID == s.NativeID {
					r.Candidates[i].Status = "uncertain"
					r.Candidates[i].Reason = c.Reason
				}
			}
		}
		pending, pendingErr := pendingLocalDeletion(ledger, dest.RepoID, s.NativeID)
		if pendingErr != nil {
			c.Status = "failed"
			c.Reason = pendingErr.Error()
		} else if pending {
			c.Status = "excluded"
			c.Reason = "local deletion is pending publication"
		}
		seen[s.NativeID] = s.Digest
		r.Candidates = append(r.Candidates, c)
	}
	for id, found := range ids {
		if !found {
			return r, fmt.Errorf("selected native session %s was not found", id)
		}
	}
	return r, nil
}
func runSessionImport(cmd *cobra.Command, o *importOptions) error {
	if !flags.Get().SessionImportEnabled {
		return fmt.Errorf("session import is not enabled")
	}
	if o.Agent != "codex" {
		return fmt.Errorf("only Codex history is supported")
	}
	root, e := requireProjectRoot()
	if e != nil {
		return e
	}
	ledger, e := resolveLedgerPath()
	if e != nil {
		return e
	}
	dest, e := importDest(root)
	if e != nil {
		return e
	}
	disclosure, e := importAudience(dest)
	if e != nil {
		return e
	}
	report, e := scanImport(cmd.Context(), root, ledger, dest, o, time.Now())
	if e != nil {
		return e
	}
	report.Disclosure = disclosure
	writeReport := func() error {
		if o.JSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
		}
		fmt.Fprintln(cmd.OutOrStdout(), report.Disclosure)
		for _, c := range report.Candidates {
			fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s — %s  %d bytes  %d redacted entries\n  %s\n  %s\n", c.NativeID, c.Status, c.StartedAt.Format(time.RFC3339), c.LastActivity.Format(time.RFC3339), c.Size, c.RedactedEntries, c.Title, c.Reason)
		}
		return nil
	}
	if o.DryRun {
		return writeReport()
	}
	interactive := cli.IsInteractive() && !o.JSON
	if !interactive && (!o.Yes || (len(o.IDs) == 0 && !o.All && o.Since == "")) {
		report.NeedsConfirmation = true
		if e = writeReport(); e != nil {
			return e
		}
		return fmt.Errorf("needs_confirmation: supply --yes with --session, --since, or --all-history after reviewing --dry-run")
	}
	if !o.JSON {
		if e = writeReport(); e != nil {
			return e
		}
	}
	in := bufio.NewReader(cmd.InOrStdin())
	var failures []string
	for i := range report.Candidates {
		c := &report.Candidates[i]
		if c.Status == "failed" || (c.Status == "active" && len(o.IDs) > 0) {
			failures = append(failures, c.NativeID)
			continue
		}
		if c.Status != "ready" && c.Status != "uncertain" && c.Status != "already_uploaded" {
			continue
		}
		if c.Status == "uncertain" && !interactive {
			report.NeedsConfirmation = true
			continue
		}
		if interactive && c.Status != "already_uploaded" {
			fmt.Fprintf(cmd.OutOrStdout(), "Upload %s? %s Type its complete native ID to confirm, or Enter to skip: ", c.Title, c.Reason)
			answer, e := in.ReadString('\n')
			if e != nil && e != io.EOF {
				return e
			}
			if strings.TrimSpace(answer) != c.NativeID {
				continue
			}
		}
		fresh, e := importDest(root)
		if e != nil || fresh != dest {
			return fmt.Errorf("destination changed; preview again before importing")
		}
		if e = publishImportedSession(cmd.Context(), root, ledger, dest, c); e != nil {
			c.Status = "failed"
			c.Reason = e.Error()
			failures = append(failures, c.NativeID)
		} else {
			c.Status = "already_uploaded"
			c.Reason = "remote content verified; indexing and summary may still be processing"
		}
	}
	if e = writeReport(); e != nil {
		return e
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d imports remain pending; rerun the same selection to retry", len(failures))
	}
	if report.NeedsConfirmation {
		return fmt.Errorf("needs_confirmation: uncertain sessions require individual interactive review; --yes never selects them")
	}
	return nil
}
