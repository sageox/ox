package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/prime"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Edge paths of `ox bulletin post` that the end-to-end harness cannot reach
// (no TTY, no real clock) or does not need to: the real sleeper, the nil
// context default, the receipt's local-path fallbacks, and the failure
// wording for the rarer server answers.

// TestBulletinSleep_RealTimerAndCancellation pins the production sleeper the
// tests otherwise replace.
//
// Failure prevented: a sleeper that ignores the context would keep a
// Ctrl-C'd retry loop alive for the full Retry-After; one that never fires
// would hang every 503 retry.
func TestBulletinSleep_RealTimerAndCancellation(t *testing.T) {
	require.NoError(t, bulletinSleep(context.Background(), time.Millisecond), "a short wait must return nil once it elapses")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := bulletinSleep(ctx, time.Hour)
	require.ErrorIs(t, err, context.Canceled, "a canceled context must end the wait immediately")
}

// TestPublishBulletinWithRetry_CanceledWaitReturnsTheServerError proves a
// wait cut short by the context reports the 503, not the cancellation, and
// makes no further request.
//
// Failure prevented: Ctrl-C during a retry wait being rendered as an
// unexplained "context canceled" instead of the actionable unavailable
// outcome — or worse, a request being sent after the user asked to stop.
func TestPublishBulletinWithRetry_CanceledWaitReturnsTheServerError(t *testing.T) {
	prev := bulletinSleep
	bulletinSleep = func(ctx context.Context, _ time.Duration) error { return context.Canceled }
	t.Cleanup(func() { bulletinSleep = prev })

	calls := 0
	p := bulletinPublisherFunc(func(context.Context, string, api.BulletinPublishRequest) (*api.BulletinPublishResult, error) {
		calls++
		return nil, &api.BulletinUnavailableError{RetryAfter: time.Second}
	})
	var errOut bytes.Buffer
	res, attempts, err := publishBulletinWithRetry(context.Background(), &errOut, p, "team_abc", api.BulletinPublishRequest{})
	assert.Nil(t, res)
	assert.Equal(t, 1, attempts)
	assert.Equal(t, 1, calls, "no request may follow a canceled wait")
	assert.ErrorIs(t, err, api.ErrBulletinUnavailable, "the server outcome is what the user is told")
	assert.Contains(t, errOut.String(), "retrying in 1s (attempt 2 of 4)")
}

type bulletinPublisherFunc func(context.Context, string, api.BulletinPublishRequest) (*api.BulletinPublishResult, error)

func (f bulletinPublisherFunc) PublishBulletinPost(ctx context.Context, ref string, req api.BulletinPublishRequest) (*api.BulletinPublishResult, error) {
	return f(ctx, ref, req)
}

// TestRunBulletinPost_NilContextAndNoArgument covers the direct-RunE entry
// every test uses: a command with no context set must still refuse cleanly
// when no file is given.
//
// Failure prevented: a nil context reaching net/http and failing with an
// error that names neither the cause nor the command.
func TestRunBulletinPost_NilContextAndNoArgument(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("ttl", "", "")
	cmd.Flags().String("title", "", "")
	cmd.Flags().String("slug", "", "")
	cmd.Flags().String("format", "", "")
	cmd.Flags().String("board", "", "")
	cmd.Flags().String("team", "", "")
	cmd.Flags().Bool("yes", false, "")
	root := &cobra.Command{}
	root.PersistentFlags().Bool("json", false, "")
	root.AddCommand(cmd)

	err := runBulletinPost(cmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ox bulletin post <file> --ttl 14d")
}

// TestPrintBulletinPreview_ShowsEverythingThatWillBePublished pins the
// confirmation preview: the slug becomes a file name teammates see for as
// long as the post lives, so it must be on screen before the question.
//
// Failure prevented: a preview that omits the slug or the byte count, so a
// person confirms a post they would have renamed or trimmed.
func TestPrintBulletinPreview_ShowsEverythingThatWillBePublished(t *testing.T) {
	plan := &bulletinPlan{Request: api.BulletinPublishRequest{
		Board: "general", Slug: "release-notes", Title: "Release notes", Format: "markdown", TTL: "14d",
		Content: strings.Repeat("x", 1234),
	}}
	var buf bytes.Buffer
	printBulletinPreview(&buf, bulletinTeam{Label: "Acme"}, plan, "release-notes.md")
	got := stripANSI(buf.String())
	for _, want := range []string{"Acme", "general", "release-notes", "Release notes", "markdown", "14d", "1,234", "release-notes.md"} {
		assert.Contains(t, got, want)
	}

	buf.Reset()
	printBulletinPreview(&buf, bulletinTeam{Label: "Acme"}, plan, bulletinStdinSource)
	assert.Contains(t, stripANSI(buf.String()), "stdin", "stdin must be named as such, not as \"-\"")
}

// TestBulletinLocalPostsDir_FallsBackToTheRelativeDir covers every branch of
// the receipt's local path: no project, a project with no team, a --team this
// machine never synced, and a --team it has.
//
// Failure prevented: a receipt that names a directory on a machine that has
// no checkout for that team, or that names nothing at all.
func TestBulletinLocalPostsDir_FallsBackToTheRelativeDir(t *testing.T) {
	rel := filepath.FromSlash(prime.BulletinPostsRelDir("general"))

	assert.Equal(t, rel, bulletinLocalPostsDir("", "", "", "general"), "no project root: relative dir")

	bare := createInitializedProject(t)
	assert.Equal(t, rel, bulletinLocalPostsDir(bare, "", "", "general"), "project without a team: relative dir")
	assert.Equal(t, rel, bulletinLocalPostsDir(bare, "other", "team_other", "general"), "--team never synced here: relative dir")

	teamDir := t.TempDir()
	root := createInitializedProjectWithConfig(t, &config.ProjectConfig{RepoID: "r", TeamID: "team_abc"})
	require.NoError(t, config.SaveLocalConfig(root, &config.LocalConfig{
		TeamContexts: []config.TeamContext{{TeamID: "team_abc", TeamName: "Acme", Path: teamDir}},
	}))
	assert.Equal(t, filepath.Join(teamDir, rel), bulletinLocalPostsDir(root, "", "team_abc", "general"), "repo team: its checkout")
	assert.Equal(t, filepath.Join(teamDir, rel), bulletinLocalPostsDir(root, "acme", "team_abc", "general"), "--team naming a synced team: its checkout")
}

// TestRenderBulletinReceipt_EmptyTeamIDFallsBackToTheRef guards the JSON
// receipt against a server that omits team_id.
//
// Failure prevented: an AI coworker reading "team_id": "" and concluding
// the post landed nowhere.
func TestRenderBulletinReceipt_EmptyTeamIDFallsBackToTheRef(t *testing.T) {
	var buf bytes.Buffer
	res := &api.BulletinPublishResult{Board: "general", Path: "bulletin/general/posts/x-abc.md", Slug: "x", ContentSHA256: "abc", ContentBytes: 1}
	plan := &bulletinPlan{Request: api.BulletinPublishRequest{TTL: "1d", Content: "x"}, SHA256: "abc"}
	require.NoError(t, renderBulletinReceipt(&buf, true, bulletinTeam{Ref: "team_abc", Label: "Acme", LocalPath: "/x"}, plan, res))
	var doc map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))
	team, _ := doc["team"].(map[string]any)
	assert.Equal(t, "team_abc", team["team_id"])
	assert.Equal(t, prime.BulletinReceiptGuidance, doc["guidance"])
}

func TestBulletinShortHash_ShortInputIsReturnedWhole(t *testing.T) {
	assert.Equal(t, "abc", bulletinShortHash("abc"))
	assert.Equal(t, "7c4a8d09…f2e1", bulletinShortHash("7c4a8d09a1b2c3d4e5f60718293a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f2e1"))
}

// TestBulletinFailureFor_RarerServerAnswers pins the code and wording for
// the answers the fake-server suite does not script: version gate, 403 with
// and without a reason, and the attempt count in the unavailable message.
//
// Failure prevented: a 403 with no server wording rendering an empty
// headline; the unavailable message saying "after 1 attempts".
func TestBulletinFailureFor_RarerServerAnswers(t *testing.T) {
	f := bulletinFailureFor(api.ErrVersionUnsupported, "team_abc", 1)
	assert.Equal(t, bulletinCodeVersionUnsupported, f.Code)
	assert.Contains(t, f.Guidance, "Upgrade ox")

	f = bulletinFailureFor(&api.ForbiddenError{}, "team_abc", 1)
	assert.Equal(t, bulletinCodeForbidden, f.Code)
	assert.Equal(t, "the server refused this post", f.Headline)

	f = bulletinFailureFor(&api.ForbiddenError{Reason: "owners only"}, "team_abc", 1)
	assert.Equal(t, "owners only", f.Headline)

	f = bulletinFailureFor(&api.BulletinUnavailableError{}, "team_abc", 1)
	assert.Equal(t, bulletinCodeUnavailable, f.Code)
	assert.Contains(t, strings.Join(f.Explain, " "), "after 1 attempt.")
	f = bulletinFailureFor(&api.BulletinUnavailableError{}, "team_abc", 4)
	assert.Contains(t, strings.Join(f.Explain, " "), "after 4 attempts")

	f = bulletinFailureFor(errors.New("network error: dial tcp: refused"), "team_abc", 1)
	assert.Equal(t, bulletinCodeError, f.Code)
	assert.Contains(t, f.Guidance, "identical retry is safe")
}

func TestBulletinSortedFields_IsStableInKeyOrder(t *testing.T) {
	assert.Nil(t, bulletinSortedFields(nil))
	assert.Equal(t,
		[]string{"board: b", "slug: c", "ttl: a"},
		bulletinSortedFields(map[string]string{"ttl": "a", "board": "b", "slug": "c"}))
}

// TestBulletinTitle_FallsBackToTheFileStemThenRefuses covers the last two
// rungs of title derivation.
//
// Failure prevented: an HTML file with no <title> being refused instead of
// taking its file name; a file whose name is only whitespace producing a
// blank title the server would reject one round-trip later.
func TestBulletinTitle_FallsBackToTheFileStemThenRefuses(t *testing.T) {
	title, err := bulletinTitle("", "/tmp/launch-deck.html", api.BulletinFormatHTML, []byte("<p>no title element</p>"))
	require.NoError(t, err)
	assert.Equal(t, "launch-deck", title)

	_, err = bulletinTitle("", "/tmp/ .html", api.BulletinFormatHTML, []byte("<p>x</p>"))
	var inputErr *bulletinInputError
	require.ErrorAs(t, err, &inputErr)
	assert.Equal(t, "title", inputErr.Field)
	assert.Contains(t, inputErr.Message, "blank")
}

// TestBulletinPost_InvalidTeamFlagIsALocalRefusal covers a --team that can
// never be a team (a dot segment): it is refused before any request, as a
// validation error naming the flag — not as a generic failure whose guidance
// says to rerun the identical command.
//
// Failure prevented: an AI coworker told "rerun the same command; an
// identical retry is safe" for a deterministic local refusal, looping on a
// command that can never succeed.
func TestBulletinPost_InvalidTeamFlagIsALocalRefusal(t *testing.T) {
	for _, team := range []string{"..", "."} {
		t.Run(team, func(t *testing.T) {
			h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusCreated, body: bulletinCreatedBody}})
			file := writeBulletinFile(t, "notes.md", []byte("# Notes\n\nbody\n"))

			err := runBulletin(t, file, true, map[string]string{"team": team})

			requireSilentFailure(t, err)
			assert.Equal(t, 0, h.log.count(), "an invalid team reference must never reach the server")
			doc := decodeBulletinJSON(t, h.out)
			assert.Equal(t, "validation_error", doc["error"])
			assert.NotContains(t, doc["guidance"], "Rerun the same command;")
			assert.Contains(t, doc["guidance"], "--team")
			details, _ := doc["details"].(map[string]any)
			assert.NotEmpty(t, details["team"])
		})
	}
}

// TestBulletinPost_PromptIsDecidedByYesAndJSON drives the confirmation with
// the terminal seams forced interactive, which the harness cannot do for
// real (no TTY). This is the test that goes red if --yes or --json stop
// suppressing the prompt, or if "no" stops canceling.
//
// Failure prevented: an AI coworker's --json run blocking on "Publish?" from
// a real TTY; a human's "n" being ignored and the post published anyway.
func TestBulletinPost_PromptIsDecidedByYesAndJSON(t *testing.T) {
	tests := []struct {
		name        string
		flags       map[string]string
		jsonOutput  bool
		answer      bool
		wantAsked   bool
		wantRequest int
		wantErr     bool
	}{
		{name: "no flags, answer no: canceled, nothing sent", answer: false, wantAsked: true, wantRequest: 0},
		{name: "no flags, answer yes: published", answer: true, wantAsked: true, wantRequest: 1},
		{name: "--yes: never asked", flags: map[string]string{"yes": "true"}, wantAsked: false, wantRequest: 1},
		{name: "--json: never asked", jsonOutput: true, wantAsked: false, wantRequest: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusCreated, body: bulletinCreatedBody}})
			file := writeBulletinFile(t, "release-notes.md", []byte("# Release notes\n\nbody\n"))

			prevInteractive, prevConfirm := bulletinIsInteractive, bulletinConfirm
			t.Cleanup(func() { bulletinIsInteractive, bulletinConfirm = prevInteractive, prevConfirm })
			asked := false
			bulletinIsInteractive = func() bool { return true }
			bulletinConfirm = func(prompt string, _ bool) bool {
				asked = true
				assert.Equal(t, "Publish?", prompt)
				return tt.answer
			}

			err := runBulletin(t, file, tt.jsonOutput, tt.flags)

			require.NoError(t, err, "stdout:\n%s", h.out.String())
			assert.Equal(t, tt.wantAsked, asked, "prompt asked")
			assert.Equal(t, tt.wantRequest, h.log.count(), "requests made")
			out := stripANSI(h.out.String())
			if tt.wantAsked {
				assert.Contains(t, out, "release-notes", "the preview must show the slug before asking")
			}
			if tt.wantAsked && !tt.answer {
				assert.Contains(t, out, "Canceled.")
			}
		})
	}
}

// TestBulletinPost_OversizedFileIsNotReportedAsOneByteOver pins the size
// message: the read is capped one byte past the limit, so the message must
// not print that capped length as the file's size.
//
// Failure prevented: a 3 MB file being reported as "1048577 bytes", so the
// user trims one line and is told the same thing again.
func TestBulletinPost_OversizedFileIsNotReportedAsOneByteOver(t *testing.T) {
	h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusCreated, body: bulletinCreatedBody}})
	file := writeBulletinFile(t, "big.md", []byte("# t\n"+strings.Repeat("a", 3*bulletinMaxContentBytes)))

	err := runBulletin(t, file, true, nil)

	requireSilentFailure(t, err)
	assert.Equal(t, 0, h.log.count())
	doc := decodeBulletinJSON(t, h.out)
	details, _ := doc["details"].(map[string]any)
	msg, _ := details["content"].(string)
	assert.Contains(t, msg, "larger than")
	assert.NotContains(t, msg, "1048577")
}

// TestBulletinWhereItAppears_RelativePathDoesNotPromiseALocalSync pins the
// receipt wording for a --team this machine has no checkout for.
//
// Failure prevented: "Appears in bulletin/general/posts after the next sync"
// pointing an AI coworker at a directory relative to its cwd that no sync on
// this machine will ever create.
func TestBulletinWhereItAppears_RelativePathDoesNotPromiseALocalSync(t *testing.T) {
	assert.Contains(t, bulletinWhereItAppears("/home/x/teams/t/bulletin/general/posts"), "Appears in /home/x/teams/t/bulletin/general/posts after the next Team Context sync.")
	rel := bulletinWhereItAppears(filepath.FromSlash("bulletin/general/posts"))
	assert.Contains(t, rel, "this machine has none for that team")
	assert.NotContains(t, rel, "after the next Team Context sync.")
}
