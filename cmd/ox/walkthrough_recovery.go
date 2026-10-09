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

func walkthroughRecovery(cmd *cobra.Command, idArg string, f conversationWalkthroughFlags, opts read.WalkthroughOptions) *read.Envelope {
	fail := func(e error) *read.Envelope {
		return read.ErrorEnvelope(read.NewError("extraction_unavailable", e.Error()))
	}
	id, e := read.ParseID(idArg)
	if e != nil {
		return read.ErrorEnvelope(e)
	}
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fail(err)
	}
	team := config.FindRepoTeamContext(projectRoot)
	if team == nil || team.TeamID == "" {
		return fail(fmt.Errorf("no active team context"))
	}
	ep := endpoint.GetForProject(projectRoot)
	if id.LinkHost != "" && id.LinkHost != endpoint.NormalizeSlug(ep) {
		return fail(fmt.Errorf("recording link belongs to another SageOx environment; use its matching project/login"))
	}
	stored, err := auth.EnsureValidTokenForEndpoint(ep, 300)
	if err != nil || stored == nil || stored.AccessToken == "" {
		return fail(fmt.Errorf("a valid login is required for server extraction"))
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
	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(stored.AccessToken).WithTimeout(15 * time.Second)
	var result *api.WalkthroughJob
	if f.Prepare {
		result, err = client.PrepareWalkthrough(ctx, team.TeamID, id.RecordingID, f.Retry)
	} else {
		result, err = client.WalkthroughExtraction(ctx, team.TeamID, id.RecordingID, f.Job, request)
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
		if fetched >= 8 {
			warnings = append(warnings, "image fetch budget reached (8); narrow the cue/time window for remaining images")
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
