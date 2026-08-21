package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestArtifactURL(t *testing.T) {
	const ep = "https://sageox.ai"
	tests := []struct {
		name, raw, prefix, idPrefix, want string
	}{
		{"full https passthrough", "https://test.sageox.ai/c/ses_x", "/c/", "ses_", "https://test.sageox.ai/c/ses_x"},
		{"full http passthrough", "http://localhost:3000/plan/pln_x", "/plan/", "pln_", "http://localhost:3000/plan/pln_x"},
		{"bare session id", "ses_9f2a3c", "/c/", "ses_", "https://sageox.ai/c/ses_9f2a3c"},
		{"bare plan id", "pln_4d8e2f", "/plan/", "pln_", "https://sageox.ai/plan/pln_4d8e2f"},
		{"prefixless id still resolves", "abc", "/plan/", "pln_", "https://sageox.ai/plan/abc"},
		{"mismatched type prefix is rejected", "ses_9f2a3c", "/plan/", "pln_", ""},
		{"empty is dropped", "  ", "/c/", "ses_", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := artifactURL(ep, tc.raw, tc.prefix, tc.idPrefix); got != tc.want {
				t.Errorf("artifactURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestArtifactURL_noEndpointDropsBareID(t *testing.T) {
	// A bare id with no endpoint has no way to become a real URL — drop it
	// rather than emit a relative link.
	if got := artifactURL("", "ses_x", "/c/", "ses_"); got != "" {
		t.Errorf("want empty for bare id + no endpoint, got %q", got)
	}
}

// styleCmd builds a command carrying just the --style flag so resolvePRStyle can
// be tested without the whole root command tree.
func styleCmd(value string) *cobra.Command {
	c := &cobra.Command{Use: "header"}
	c.Flags().String("style", "", "")
	if value != "" {
		_ = c.Flags().Set("style", value)
	}
	return c
}

func TestResolvePRStyle(t *testing.T) {
	// A valid flag value overrides config.
	for _, v := range []string{config.PRVisualsStyleText, config.PRVisualsStyleImage, config.PRVisualsStyleAuto} {
		if got := resolvePRStyle(styleCmd(v), ""); got != v {
			t.Errorf("flag %q => %q, want %q", v, got, v)
		}
	}
	// An unset or invalid flag falls back to the config default (auto here, since
	// no config is set in the test environment).
	if got := resolvePRStyle(styleCmd(""), ""); got != config.DefaultPRVisualsStyle {
		t.Errorf("unset flag => %q, want default %q", got, config.DefaultPRVisualsStyle)
	}
	if got := resolvePRStyle(styleCmd("chartreuse"), ""); got != config.DefaultPRVisualsStyle {
		t.Errorf("invalid flag => %q, want default %q", got, config.DefaultPRVisualsStyle)
	}
}

// --- Command-level proofs: drive the real runPRHeader against a real project ---
//
// These exercise the JOIN the unit tests above cannot: a coworker standing in
// their repo runs `ox pr header`, and the command resolves config + endpoint,
// links the work, and either emits the credit line or honestly no-ops. The
// resolver/render unit tests prove the pieces; these prove the whole flow.

// prHeaderProject builds a real git+.sageox project (team optional) and isolates
// user config so the host's real ~/.sageox cannot bleed into the resolved value.
// cwd is set to the project so findGitRoot resolves it the way it does for a
// coworker in their repo.
func prHeaderProject(t *testing.T, withTeam bool) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short: real git + config file writes")
	}
	root := t.TempDir()
	runGit(t, root, "init")
	sageox := filepath.Join(root, ".sageox")
	require.NoError(t, os.MkdirAll(sageox, 0o755))
	cfgJSON := `{"config_version":"2","repo_id":"repo_prheader","endpoint":"https://sageox.ai"}`
	if withTeam {
		cfgJSON = `{"config_version":"2","repo_id":"repo_prheader","endpoint":"https://sageox.ai","team":"acme","team_name":"Acme Rockets"}`
	}
	require.NoError(t, os.WriteFile(filepath.Join(sageox, "config.json"), []byte(cfgJSON), 0o644))

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("OX_USER_CONFIG", filepath.Join(home, "user-config.yaml"))
	t.Setenv("SAGEOX_ENDPOINT", "")
	t.Chdir(root)
	return root
}

// buildPRHeaderCmd carries exactly the flags runPRHeader reads, so the test can
// drive the real command function without the persistent-flag merge of the full
// root tree. Output is captured on the returned buffers.
func buildPRHeaderCmd() (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	c := &cobra.Command{Use: "header"}
	f := c.Flags()
	f.StringArray("session", nil, "")
	f.StringArray("plan", nil, "")
	f.Int("prior-art", 0, "")
	f.Int("collisions", 0, "")
	f.Int("expert-routes", 0, "")
	f.Bool("no-stat", false, "")
	f.String("style", "", "")
	f.Bool("allow-unconfirmed", false, "")
	f.Bool("json", false, "")
	var out, errb bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errb)
	return c, &out, &errb
}

// TestPRHeaderCommand_JSONContract proves the --json shape an agent consumes: the
// paste-ready markdown, the resolved plan link, the whisper signals, and the
// text tier (the image uploader is stubbed, so every style resolves to text).
// Failure prevented: an agent pastes markup that does not link the work it
// claims, or misreads the tier and expects a hosted image that never renders.
func TestPRHeaderCommand_JSONContract(t *testing.T) {
	prHeaderProject(t, true)

	c, out, _ := buildPRHeaderCmd()
	require.NoError(t, c.Flags().Set("json", "true"))
	require.NoError(t, c.Flags().Set("plan", "pln_4d8e2f"))
	require.NoError(t, c.Flags().Set("prior-art", "1"))
	require.NoError(t, runPRHeader(c, nil))

	var resp prHeaderResponse
	require.NoError(t, json.Unmarshal(out.Bytes(), &resp))

	require.NotEmpty(t, resp.Markdown, "markdown must be present")
	require.Contains(t, resp.Markdown, "<!-- sageox:pr-header v1 -->", "carries the idempotency marker")
	require.Contains(t, resp.Markdown, "Acme&nbsp;Rockets", "names the team (non-breaking)")
	require.Contains(t, resp.Markdown, "https://sageox.ai/plan/pln_4d8e2f", "links the plan")
	require.Equal(t, []string{"https://sageox.ai/plan/pln_4d8e2f"}, resp.Plans)
	require.True(t, resp.Signals.Material, "a fired signal marks the whisper material")
	require.Equal(t, 1, resp.Signals.PriorArt)
	require.Equal(t, "text", resp.Tier, "no uploader is wired, so the tier is text")
}

// TestPRHeaderCommand_OptOutNoOp proves the team/user opt-out: with
// pr_visuals.header off, the real command emits nothing and tells the coworker
// how to turn it back on — it does not stamp an empty block into a PR body.
// Failure prevented: a team that disabled the header still gets it. Red-first:
// make runPRHeader ignore config.PRVisualsHeader and this stdout stays non-empty.
func TestPRHeaderCommand_OptOutNoOp(t *testing.T) {
	prHeaderProject(t, true)
	oxConfigSetRepo(t, "pr_visuals.header", "off")

	c, out, errb := buildPRHeaderCmd()
	require.NoError(t, runPRHeader(c, nil))

	require.Empty(t, strings.TrimSpace(out.String()), "off => no line on stdout")
	require.Contains(t, errb.String(), "pr_visuals.header is off", "explains it is off")
	require.Contains(t, errb.String(), "ox config set pr_visuals.header on", "tells how to re-enable")
}

// TestPRHeaderCommand_NoEnrichmentNoWhisper proves honest enrichment: with no
// signals passed, the line renders the team but makes NO "Guided by SageOx"
// claim. Failure prevented: the credit line brags about enrichment that never
// fired.
func TestPRHeaderCommand_NoEnrichmentNoWhisper(t *testing.T) {
	prHeaderProject(t, true)

	c, out, _ := buildPRHeaderCmd()
	require.NoError(t, runPRHeader(c, nil))

	require.Contains(t, out.String(), "Acme&nbsp;Rockets", "still renders the team")
	require.NotContains(t, out.String(), "Guided by SageOx", "no whisper when nothing fired")
}

// TestPRHeaderCommand_LoneWordmarkEmitsNothing proves the degenerate state: with
// no team, no session, no plan, and no enrichment, the command emits nothing on
// stdout rather than a bare wordmark stamped onto a PR body.
// Failure prevented: a payload-free credit line puts a lone SageOx logo at the
// top of someone's PR.
func TestPRHeaderCommand_LoneWordmarkEmitsNothing(t *testing.T) {
	prHeaderProject(t, false) // no team configured

	c, out, errb := buildPRHeaderCmd()
	require.NoError(t, runPRHeader(c, nil))

	require.Empty(t, strings.TrimSpace(out.String()), "nothing to credit => no line on stdout")
	require.Contains(t, errb.String(), "nothing to paste", "explains there was nothing to credit")
}
