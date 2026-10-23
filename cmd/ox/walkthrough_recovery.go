package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/conversation/read"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/tokens"
	"github.com/spf13/cobra"
)

// walkthroughFetchBudget caps the images one invocation downloads, whatever
// their kind. Failed attempts count too: missing media must not become an
// unbounded loop.
const walkthroughFetchBudget = 8

// walkthroughScope is what a team-scoped recording call needs: the team the
// project belongs to, the endpoint, and a valid login for it.
type walkthroughScope struct {
	teamID   string
	endpoint string
	token    string
}

// resolveWalkthroughScope resolves the project's team, endpoint and login for
// a recording id, refusing a link that belongs to another environment so a
// credential is never presented to the wrong server.
func resolveWalkthroughScope(id *read.ID) (*walkthroughScope, error) {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return nil, err
	}
	team := config.FindRepoTeamContext(projectRoot)
	if team == nil || team.TeamID == "" {
		return nil, fmt.Errorf("no active team context")
	}
	ep := endpoint.GetForProject(projectRoot)
	if id.LinkHost != "" && id.LinkHost != endpoint.NormalizeSlug(ep) {
		return nil, fmt.Errorf("recording link belongs to another SageOx environment; use its matching project/login")
	}
	stored, err := auth.EnsureValidTokenForEndpoint(ep, 300)
	if err != nil || stored == nil || stored.AccessToken == "" {
		return nil, fmt.Errorf("a valid login is required for server access")
	}
	return &walkthroughScope{teamID: team.TeamID, endpoint: ep, token: stored.AccessToken}, nil
}

func (s *walkthroughScope) client(timeout time.Duration) *api.RepoClient {
	return api.NewRepoClientWithEndpoint(s.endpoint).WithAuthToken(s.token).WithTimeout(timeout)
}

func walkthroughRecovery(cmd *cobra.Command, idArg string, f conversationWalkthroughFlags, opts read.WalkthroughOptions) *read.Envelope {
	fail := func(e error) *read.Envelope {
		return read.ErrorEnvelope(read.NewError("extraction_unavailable", e.Error()))
	}
	id, e := read.ParseID(idArg)
	if e != nil {
		return read.ErrorEnvelope(e)
	}
	scope, err := resolveWalkthroughScope(id)
	if err != nil {
		return fail(err)
	}
	request := &api.WalkthroughExtractionRequest{Revision: f.Revision, CueFirst: opts.CueFirst, CueLast: opts.CueLast, MaxFrames: f.MaxFrames, MaxWidth: f.MaxWidth, Retry: f.Retry}
	if opts.HasWindow {
		from, to := opts.FromOffset.Seconds(), opts.ToOffset.Seconds()
		request.FromSeconds = &from
		request.ToSeconds = &to
	}
	ctx, cancel := context.WithTimeout(conversationContext(cmd), 15*time.Second)
	defer cancel()
	// One request per explicit invocation. Repeated model tool calls must not
	// silently become a paid preprocessing loop; the server coalesces/caps work.
	client := scope.client(15 * time.Second)
	var result *api.WalkthroughJob
	if f.Prepare {
		result, err = client.PrepareWalkthrough(ctx, scope.teamID, id.RecordingID, f.Retry)
	} else {
		result, err = client.WalkthroughExtraction(ctx, scope.teamID, id.RecordingID, f.Job, request)
	}
	if err != nil {
		return fail(err)
	}
	guidance := fmt.Sprintf("Check this job explicitly: ox walkthrough %s --job %s --json. Do not poll in an unbounded loop.", id.ConversationID, result.JobID)
	if result.Status == "failed" {
		guidance += " Retry only after addressing the reported failure, using the original --prepare or --extract arguments with --retry. The server limits and charges retries; ordinary replay does not restart failed work."
	}
	if result.Status == "completed" {
		guidance = fmt.Sprintf("Sync Team Context, then read the exact result: ox walkthrough %s --revision %s --cues N-M --fetch --json. Inspect the returned images; server extraction makes no semantic claim.", id.ConversationID, result.Revision)
	}
	payload, _ := json.Marshal(result)
	return &read.Envelope{Success: true, Data: result, Guidance: guidance, TokenEstimate: tokens.EstimateTokens(string(payload))}
}

func fetchWalkthroughImages(ctx context.Context, data *read.WalkthroughData) []string {
	var warnings []string
	fetched := 0
	for _, m := range data.Moments {
		f := m.Frame
		if f == nil || f.LocalImage != "" || f.Image == "" || f.FetchCommand == "" {
			continue
		}
		if fetched >= walkthroughFetchBudget {
			warnings = append(warnings, fmt.Sprintf("image fetch budget reached (%d); narrow the cue/time window for remaining images", walkthroughFetchBudget))
			break
		}
		fetched++
		// Invoke the existing pure-Go LFS transport directly, never execute a
		// screen-derived command string. It writes the cache, not tracked files.
		var output bytes.Buffer
		fetch := &cobra.Command{}
		fetch.SetContext(ctx)
		fetch.SetOut(&output)
		if err := runFetch(fetch, []string{f.Image}); err != nil {
			warnings = append(warnings, "image unavailable: "+err.Error())
			continue
		}
		f.LocalImage = strings.TrimSpace(output.String())
		f.FetchCommand = ""
		f.Availability = "local"
	}
	return warnings
}

func renderWalkthroughJob(w io.Writer, env *read.Envelope) {
	job, ok := env.Data.(*api.WalkthroughJob)
	if !ok {
		return
	}
	fmt.Fprintf(w, "%s %s\n", job.JobID, job.Status)
	if job.Revision != "" {
		fmt.Fprintln(w, "revision:", job.Revision)
	}
	if job.Error != "" {
		fmt.Fprintln(w, "error:", cli.SanitizeTerminalText(job.Error))
	}
}
