package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/prime"
	"github.com/sageox/ox/internal/testguard"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// JSON selection must survive the real root command's environment and flag resolution.
func TestGuideCLIJSONPrecedence(t *testing.T) {
	if testing.Short() {
		t.Skip("short: builds and runs the ox binary")
	}
	oxBin := testguard.BuildOxBinary(t, repoPath("..", ".."))

	for _, tt := range []struct {
		name     string
		envJSON  string
		args     []string
		wantJSON bool
	}{
		{name: "environment enables JSON", envJSON: "1", args: []string{"guide"}, wantJSON: true},
		{name: "flag enables JSON", envJSON: "0", args: []string{"guide", "--json"}, wantJSON: true},
		{name: "false flag overrides environment", envJSON: "1", args: []string{"guide", "--json=false", "--raw"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			env := []string{
				"OX_JSON=" + tt.envJSON,
				"OX_XDG_ENABLE=1",
				"SAGEOX_ENDPOINT=http://127.0.0.1:1",
				"HTTP_PROXY=http://127.0.0.1:1",
				"HTTPS_PROXY=http://127.0.0.1:1",
				"NO_PROXY=localhost,127.0.0.1",
			}
			for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
				env = append(env, key+"="+filepath.Join(dir, key))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := testguard.OxCmdContext(t, ctx, oxBin, dir, env, tt.args...)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			output, err := cmd.Output()
			require.NoError(t, err, "stderr: %s", stderr.String())
			if tt.wantJSON {
				var guides []map[string]string
				require.NoError(t, json.Unmarshal(output, &guides), "output: %s", output)
				require.NotEmpty(t, guides)
				for _, guide := range guides {
					require.NotEmpty(t, guide["topic"])
					require.NotEmpty(t, guide["title"])
				}
				return
			}
			require.Contains(t, string(output), "team-rules\tTeam Rules\t")
			for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
				require.Len(t, strings.Split(line, "\t"), 3)
			}
		})
	}
}

// JSON consumers must receive one complete document for both the catalog and a topic.
func TestGuideJSONOutput(t *testing.T) {
	previousConfig := cfg
	cfg = &config.Config{JSON: true}
	t.Cleanup(func() { cfg = previousConfig })

	for _, topic := range []string{"", "team-rules", "team-rules.md"} {
		name := topic
		if name == "" {
			name = "catalog"
		}
		t.Run(name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.Flags().Bool("raw", false, "")
			var args []string
			if topic != "" {
				args = []string{topic}
			}
			var runErr error
			output := captureRealStdout(t, func() { runErr = guideCmd.RunE(cmd, args) })
			require.NoError(t, runErr)

			if topic == "" {
				var guides []map[string]string
				require.NoError(t, json.Unmarshal(output, &guides), "output: %s", output)
				require.NotEmpty(t, guides)
				var topics []string
				for _, guide := range guides {
					require.NotEmpty(t, guide["topic"])
					require.NotEmpty(t, guide["title"])
					require.NotEmpty(t, guide["description"])
					require.NotContains(t, guide, "content")
					topics = append(topics, guide["topic"])
				}
				require.IsIncreasing(t, topics)
				require.Contains(t, topics, "team-rules")
				return
			}

			var guide map[string]string
			require.NoError(t, json.Unmarshal(output, &guide), "output: %s", output)
			require.Equal(t, "team-rules", guide["topic"])
			require.Equal(t, "Team Rules", guide["title"])
			require.NotEmpty(t, guide["description"])
			require.True(t, strings.HasPrefix(guide["content"], "# Team Rules\n"))
			require.NotContains(t, guide["content"], "\x1b[")
		})
	}
}

// Raw mode must retain its TSV catalog and full Markdown (including frontmatter).
func TestGuideRawOutput(t *testing.T) {
	previousConfig := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = previousConfig })

	for _, topic := range []string{"", "team-rules"} {
		name := topic
		if name == "" {
			name = "catalog"
		}
		t.Run(name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.Flags().Bool("raw", true, "")
			var output bytes.Buffer
			cmd.SetOut(&output)
			if topic == "" {
				require.NoError(t, guideCmd.RunE(cmd, nil))
				require.Contains(t, output.String(), "team-rules\tTeam Rules\t")
				for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
					require.Len(t, strings.Split(line, "\t"), 3)
				}
				return
			}

			require.NoError(t, guideCmd.RunE(cmd, []string{topic}))
			original, err := guidesFS.ReadFile("guides/team-rules.md")
			require.NoError(t, err)
			require.Equal(t, string(original), output.String())
		})
	}
}

// The default format must remain readable terminal output when JSON mode is off.
func TestGuideTerminalOutput(t *testing.T) {
	previousConfig := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = previousConfig })

	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{name: "catalog", want: "ox guides"},
		{name: "topic", args: []string{"team-rules"}, want: "Team Rules"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.Flags().Bool("raw", false, "")
			var output bytes.Buffer
			cmd.SetOut(&output)
			require.NoError(t, guideCmd.RunE(cmd, tt.args))
			visible := stripANSI(output.String())
			require.Contains(t, visible, tt.want)
			require.NotContains(t, visible, "audience: both")
			require.False(t, json.Valid(output.Bytes()), "default output should be terminal text")
		})
	}
}

// Invalid format combinations, missing topics, and failed writes must not report success.
func TestGuideOutputErrors(t *testing.T) {
	previousConfig := cfg
	t.Cleanup(func() { cfg = previousConfig })

	for _, tt := range []struct {
		name       string
		args       []string
		jsonOutput bool
		raw        bool
		failWrite  bool
		wantError  string
	}{
		{name: "catalog format conflict", jsonOutput: true, raw: true, wantError: "--raw and --json cannot be combined"},
		{name: "topic format conflict", args: []string{"team-rules"}, jsonOutput: true, raw: true, wantError: "--raw and --json cannot be combined"},
		{name: "missing topic", args: []string{"not-a-guide"}, jsonOutput: true, wantError: `no bundled guide named "not-a-guide". Available:`},
		{name: "JSON catalog write failure", jsonOutput: true, failWrite: true, wantError: "write failed"},
		{name: "JSON topic write failure", args: []string{"team-rules"}, jsonOutput: true, failWrite: true, wantError: "write failed"},
		{name: "raw catalog write failure", raw: true, failWrite: true, wantError: "write failed"},
		{name: "raw topic write failure", args: []string{"team-rules"}, raw: true, failWrite: true, wantError: "write failed"},
		{name: "terminal catalog write failure", failWrite: true, wantError: "write failed"},
		{name: "terminal topic write failure", args: []string{"team-rules"}, failWrite: true, wantError: "write failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg = &config.Config{JSON: tt.jsonOutput}
			cmd := &cobra.Command{}
			cmd.Flags().Bool("raw", tt.raw, "")
			var output bytes.Buffer
			cmd.SetOut(&output)
			if tt.failWrite {
				cmd.SetOut(failingWriter{})
			}
			require.ErrorContains(t, guideCmd.RunE(cmd, tt.args), tt.wantError)
			require.Empty(t, output.String())
		})
	}
}

func TestDiscoverGuides_BundledTopicsPresent(t *testing.T) {
	guides, err := discoverGuides()
	if err != nil {
		t.Fatalf("discoverGuides: %v", err)
	}

	// the plan calls for these five bundled topics
	wantTopics := map[string]bool{
		"team-rules":      false,
		"agents-md":       false,
		"team-context":    false,
		"murmur-vs-rule":  false,
		"getting-started": false,
	}

	for _, g := range guides {
		if _, ok := wantTopics[g.Topic]; ok {
			wantTopics[g.Topic] = true
		}
		if g.Title == "" {
			t.Errorf("guide %q has empty title", g.Topic)
		}
		if g.Description == "" {
			t.Errorf("guide %q has empty description", g.Topic)
		}
	}

	for topic, found := range wantTopics {
		if !found {
			t.Errorf("expected bundled guide %q to be present", topic)
		}
	}
}

func TestDiscoverGuides_SortedByTopic(t *testing.T) {
	guides, err := discoverGuides()
	if err != nil {
		t.Fatalf("discoverGuides: %v", err)
	}
	for i := 1; i < len(guides); i++ {
		if guides[i-1].Topic > guides[i].Topic {
			t.Errorf("guides not sorted: %s before %s", guides[i-1].Topic, guides[i].Topic)
		}
	}
}

func TestReadGuideFrontmatter_ExtractsTitleAndDescription(t *testing.T) {
	title, desc := readGuideFrontmatter("team-rules.md")
	if title == "" {
		t.Errorf("team-rules.md should have a title")
	}
	if desc == "" {
		t.Errorf("team-rules.md should have a description")
	}
	if !strings.Contains(strings.ToLower(title), "team rules") {
		t.Errorf("expected team-rules title to mention 'team rules', got %q", title)
	}
}

// TestGuideTopicsReferencedByPrime_Exist verifies every `ox guide <topic>`
// pointer embedded in prime's steering text resolves to a bundled guide.
//
// Prime's knowledge-bubble block is deliberately compressed and defers its
// long form to `ox guide knowledge-bubbles`; if that guide is renamed or
// dropped, the compression turns into a dead end — the agent is told where
// to read the detail and finds "unknown topic". This test is the only
// cross-check between the two packages (internal/prime cannot import
// cmd/ox), so it must live here.
func TestGuideTopicsReferencedByPrime_Exist(t *testing.T) {
	guides, err := discoverGuides()
	if err != nil {
		t.Fatalf("discoverGuides: %v", err)
	}

	bundled := make(map[string]bool, len(guides))
	for _, g := range guides {
		bundled[g.Topic] = true
	}

	// topic -> the prime text that points at it
	referenced := map[string]string{
		"knowledge-bubbles": "prime.KBGuidanceText (the <knowledge-bubbles> block)",
		"conversations":     "prime.KBGuidanceText (the citation-walking pointer)",
	}

	for topic, source := range referenced {
		if !bundled[topic] {
			t.Errorf("guide %q is referenced by %s but not bundled in cmd/ox/guides/", topic, source)
		}
	}
}

// TestKBGuidancePointsAtBundledGuide pins the pointer itself: prime's KB
// block must keep telling agents where the long form lives. Dropping the
// line would strand every detail migrated out of prime into the guide.
func TestKBGuidancePointsAtBundledGuide(t *testing.T) {
	if !strings.Contains(prime.KBGuidanceText, "ox guide knowledge-bubbles") {
		t.Errorf("prime KB guidance must point at `ox guide knowledge-bubbles`, got:\n%s", prime.KBGuidanceText)
	}
}

// TestKBGuidanceNamesConversationVerb pins the citation-walking verb in the
// Layer-1 floor: prime's KB block is the only surface reaching non-Claude
// agents, so it must name `ox conversation` (and the conversations guide) or
// those agents are left with citations they have no tool for.
func TestKBGuidanceNamesConversationVerb(t *testing.T) {
	for _, want := range []string{"ox conversation", "ox guide conversations"} {
		if !strings.Contains(prime.KBGuidanceText, want) {
			t.Errorf("prime KB guidance must name %q, got:\n%s", want, prime.KBGuidanceText)
		}
	}
}
