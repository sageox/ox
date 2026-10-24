package skillmanager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/teamskills"
	"github.com/stretchr/testify/require"
)

// claudeplugin_test.go — ox stops copying a team skill into .claude/skills
// only when Claude Code on this machine has the team's org plugin and that
// plugin carries the skill. Every other state must keep ox's copy: the
// coworker in bead 13.3 is signed in somewhere the org plugin never reaches.

// Claude Code ids, in the mixed case Claude's own JSON may carry; the bucket
// on disk is always lowercase.
const (
	fixtureClaudeOrg     = "0F8B7A6E-1111-4222-8333-944455556666"
	fixtureClaudeAccount = "A1B2C3D4-5555-4666-8777-988899990000"
	otherClaudeAccount   = "b1b2c3d4-5555-4666-8777-988899990000"
)

func claudeBucketName(org, account string) string {
	return strings.ToLower(org) + "_" + strings.ToLower(account)
}

// writeClaudeSignIn records a claude.ai sign-in the way Claude Code does.
func writeClaudeSignIn(t *testing.T, accountFile, org, account string) {
	t.Helper()
	doc := map[string]any{
		"numStartups": 3,
		"oauthAccount": map[string]any{
			"organizationUuid": org, "accountUuid": account, "displayName": "Person A",
		},
	}
	data, err := json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(accountFile, data, 0o600))
}

// writeSyncedPlugin writes a synced plugin into bucket: the plugin directory
// in plugin layout, and the sidecar naming the marketplace it came from.
func writeSyncedPlugin(t *testing.T, bucket, dir, marketplace string, skillNames ...string) {
	t.Helper()
	for _, name := range skillNames {
		skillDir := filepath.Join(bucket, dir, "skills", name)
		require.NoError(t, os.MkdirAll(skillDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, skills.SkillFileName), []byte("---\nname: "+name+"\n---\nbody\n"), 0o644))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(bucket, dir), 0o755))
	meta := fmt.Sprintf(`{"server_plugin_id":"plugin_01Fixture","marketplace_name":%q,"installation_preference":"available"}`, marketplace)
	require.NoError(t, os.WriteFile(filepath.Join(bucket, dir+claudeSyncedMetaSuffix), []byte(meta), 0o644))
}

// renameSyncedPlugin moves a staged plugin and its sidecar to another
// directory name, the way Claude Code may name a plugin apart from its
// marketplace.
func renameSyncedPlugin(t *testing.T, bucket, from, to string) {
	t.Helper()
	require.NoError(t, os.Rename(filepath.Join(bucket, from), filepath.Join(bucket, to)))
	require.NoError(t, os.Rename(filepath.Join(bucket, from+claudeSyncedMetaSuffix), filepath.Join(bucket, to+claudeSyncedMetaSuffix)))
}

// stageClaudeHome writes a Claude Code config dir signed in to the fixture
// account, whose bucket holds the slug's org plugin carrying skillNames.
func stageClaudeHome(t *testing.T, configDir, accountFile, slug string, skillNames ...string) (bucket string) {
	t.Helper()
	bucket = filepath.Join(configDir, "plugins", "synced", claudeBucketName(fixtureClaudeOrg, fixtureClaudeAccount))
	require.NoError(t, os.MkdirAll(bucket, 0o755))
	writeClaudeSignIn(t, accountFile, fixtureClaudeOrg, fixtureClaudeAccount)
	writeSyncedPlugin(t, bucket, slug, claudeOrgMarketplacePrefix+slug, skillNames...)
	// What every real bucket also holds: Anthropic's directory, and the marker
	// of a finished sync round. Neither is the team's plugin.
	writeSyncedPlugin(t, bucket, "anthropic-tools", "anthropic-plugin-directory", "pdf")
	require.NoError(t, os.WriteFile(filepath.Join(bucket, ".last-complete-round"), []byte("round"), 0o644))
	return bucket
}

func writeJSONFile(t *testing.T, file, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	require.NoError(t, os.WriteFile(file, []byte(content), 0o644))
}

// TestClaudeOrgPluginProbe decides, for one machine state each, whether
// Claude Code here really has the team's org plugin.
// Failure prevented: ox deletes its -team copy for a coworker whose Claude
// Code never loads the org plugin, and the coworker has the skill nowhere.
func TestClaudeOrgPluginProbe(t *testing.T) {
	t.Parallel()
	const slug = "acme"
	type machine struct {
		home, configDir, accountFile, bucket, repo string
		env                                        map[string]string
		slug                                       string
	}
	for _, tt := range []struct {
		name   string
		mutate func(t *testing.T, m *machine)
		// skills is what ox may defer; nil means the probe must say not found.
		skills []string
		why    string // substring of the one line a coworker reads
	}{
		// --- present: the only states that defer ---
		{name: "plugin synced for the signed-in account",
			skills: []string{"grill-me", "notes"}, why: "has the acme plugin from your Claude organization"},
		{name: "a skill directory without SKILL.md is not carried",
			mutate: func(t *testing.T, m *machine) {
				require.NoError(t, os.Remove(filepath.Join(m.bucket, slug, "skills", "notes", skills.SkillFileName)))
			},
			skills: []string{"grill-me"}},
		{name: "enabledPlugins true does not stop it",
			mutate: func(t *testing.T, m *machine) {
				writeJSONFile(t, filepath.Join(m.configDir, "settings.json"), `{"enabledPlugins":{"acme@synced":true}}`)
			},
			skills: []string{"grill-me", "notes"}},
		{name: "a provider variable set to 0 is unset",
			mutate: func(t *testing.T, m *machine) {
				writeJSONFile(t, filepath.Join(m.configDir, "settings.json"), `{"env":{"CLAUDE_CODE_USE_BEDROCK":"0"}}`)
			},
			skills: []string{"grill-me", "notes"}},
		{name: "CLAUDE_CONFIG_DIR is where Claude Code keeps it",
			mutate: func(t *testing.T, m *machine) {
				moved := filepath.Join(t.TempDir(), "claude-config")
				stageClaudeHome(t, moved, filepath.Join(moved, ".claude.json"), slug, "grill-me")
				m.env["CLAUDE_CONFIG_DIR"] = moved
				m.home = ""
			},
			skills: []string{"grill-me"}},

		// --- absent: the coworker in bead 13.3 ---
		{name: "not signed in to Claude Code",
			mutate: func(t *testing.T, m *machine) { require.NoError(t, os.Remove(m.accountFile)) },
			why:    "not signed in to a claude.ai account"},
		{name: "signed in without a claude.ai account",
			mutate: func(t *testing.T, m *machine) { writeJSONFile(t, m.accountFile, `{"numStartups":3}`) },
			why:    "not signed in to a claude.ai account"},
		{name: "plugin only in an earlier sign-in's bucket",
			mutate: func(t *testing.T, m *machine) {
				writeClaudeSignIn(t, m.accountFile, fixtureClaudeOrg, otherClaudeAccount)
			},
			why: "no plugins from claude.ai for the account signed in here"},
		{name: "signed-in account has only other plugins",
			mutate: func(t *testing.T, m *machine) {
				require.NoError(t, os.RemoveAll(filepath.Join(m.bucket, slug)))
				require.NoError(t, os.Remove(filepath.Join(m.bucket, slug+claudeSyncedMetaSuffix)))
			},
			why: "has no sageox-acme plugin"},
		{name: "another team's SageOx plugin",
			mutate: func(t *testing.T, m *machine) { m.slug = "other-team" },
			why:    "has no sageox-other-team plugin"},
		{name: "plugin carries no skills yet",
			mutate: func(t *testing.T, m *machine) {
				require.NoError(t, os.RemoveAll(filepath.Join(m.bucket, slug, "skills")))
			},
			why: "carries no skills yet"},
		{name: "sidecar names it but the plugin directory is gone",
			mutate: func(t *testing.T, m *machine) { require.NoError(t, os.RemoveAll(filepath.Join(m.bucket, slug))) },
			why:    "has no sageox-acme plugin"},
		{name: "two copies of the plugin",
			mutate: func(t *testing.T, m *machine) {
				writeSyncedPlugin(t, m.bucket, slug+"~g2", claudeOrgMarketplacePrefix+slug, "grill-me")
			},
			why: "2 copies of the sageox-acme plugin"},

		// --- turned off in Claude Code's settings ---
		{name: "syncClaudeAiPlugins off",
			mutate: func(t *testing.T, m *machine) {
				writeJSONFile(t, filepath.Join(m.configDir, "settings.json"), `{"syncClaudeAiPlugins":false}`)
			},
			why: "syncClaudeAiPlugins is off"},
		{name: "plugin disabled in user settings",
			mutate: func(t *testing.T, m *machine) {
				writeJSONFile(t, filepath.Join(m.configDir, "settings.json"), `{"enabledPlugins":{"acme@synced":false}}`)
			},
			why: "acme@synced is turned off"},
		{name: "plugin directory named apart from the slug, disabled by its own name",
			mutate: func(t *testing.T, m *machine) {
				renameSyncedPlugin(t, m.bucket, slug, "acme-plugin")
				writeJSONFile(t, filepath.Join(m.configDir, "settings.json"), `{"enabledPlugins":{"acme-plugin@synced":false}}`)
			},
			why: "acme-plugin@synced is turned off"},
		{name: "plugin directory named apart from the slug; the slug's switch is another plugin's",
			mutate: func(t *testing.T, m *machine) {
				renameSyncedPlugin(t, m.bucket, slug, "acme-plugin")
				writeJSONFile(t, filepath.Join(m.configDir, "settings.json"), `{"enabledPlugins":{"acme@synced":false}}`)
			},
			skills: []string{"grill-me", "notes"}},
		{name: "plugin disabled in the repository's local settings",
			mutate: func(t *testing.T, m *machine) {
				writeJSONFile(t, filepath.Join(m.repo, ".claude", "settings.local.json"), `{"enabledPlugins":{"acme@synced":false}}`)
			},
			why: "acme@synced is turned off"},
		{name: "signs in through apiKeyHelper",
			mutate: func(t *testing.T, m *machine) {
				writeJSONFile(t, filepath.Join(m.repo, ".claude", "settings.json"), `{"apiKeyHelper":"~/bin/key.sh"}`)
			},
			why: "apiKeyHelper"},
		{name: "Bedrock set in settings env",
			mutate: func(t *testing.T, m *machine) {
				writeJSONFile(t, filepath.Join(m.configDir, "settings.json"), `{"env":{"CLAUDE_CODE_USE_BEDROCK":"1"}}`)
			},
			why: "CLAUDE_CODE_USE_BEDROCK"},

		// --- unreadable or unknown: ox does not guess ---
		{name: "account file is a directory",
			mutate: func(t *testing.T, m *machine) {
				require.NoError(t, os.Remove(m.accountFile))
				require.NoError(t, os.Mkdir(m.accountFile, 0o755))
			},
			why: "could not read Claude Code's sign-in"},
		{name: "account file is not JSON",
			mutate: func(t *testing.T, m *machine) { writeJSONFile(t, m.accountFile, `{"oauthAccount":`) },
			why:    "could not parse Claude Code's sign-in"},
		{name: "account ids are not Claude's",
			mutate: func(t *testing.T, m *machine) { writeClaudeSignIn(t, m.accountFile, "../../etc", fixtureClaudeAccount) },
			why:    "not signed in to a claude.ai account"},
		{name: "sidecar without marketplace_name",
			mutate: func(t *testing.T, m *machine) {
				writeJSONFile(t, filepath.Join(m.bucket, slug+claudeSyncedMetaSuffix), `{"server_plugin_id":"plugin_01Fixture"}`)
			},
			why: "has no sageox-acme plugin"},
		{name: "sidecar is not JSON",
			mutate: func(t *testing.T, m *machine) {
				writeJSONFile(t, filepath.Join(m.bucket, slug+claudeSyncedMetaSuffix), `{"marketplace_name":`)
			},
			why: "could not read 1 of its plugin records"},
		{name: "bucket is a file",
			mutate: func(t *testing.T, m *machine) {
				require.NoError(t, os.RemoveAll(m.bucket))
				require.NoError(t, os.WriteFile(m.bucket, []byte("x"), 0o644))
			},
			why: "could not read Claude Code's synced plugins"},
		{name: "bucket too large to list",
			mutate: func(t *testing.T, m *machine) {
				for i := 0; i <= maxClaudeDirEntries; i++ {
					require.NoError(t, os.WriteFile(filepath.Join(m.bucket, fmt.Sprintf("filler-%04d", i)), nil, 0o644))
				}
			},
			why: "more than 1024 entries"},
		{name: "user settings are not JSON",
			mutate: func(t *testing.T, m *machine) { writeJSONFile(t, filepath.Join(m.configDir, "settings.json"), `{`) },
			why:    "could not parse Claude Code settings"},
		{name: "plugins relocated with CLAUDE_CODE_PLUGIN_CACHE_DIR",
			mutate: func(t *testing.T, m *machine) { m.env["CLAUDE_CODE_PLUGIN_CACHE_DIR"] = t.TempDir() },
			why:    "CLAUDE_CODE_PLUGIN_CACHE_DIR"},
		{name: "server sent no team slug",
			mutate: func(t *testing.T, m *machine) { m.slug = "" },
			why:    "did not say what your organization's plugin is called"},
		{name: "team slug the export could never write",
			mutate: func(t *testing.T, m *machine) { m.slug = "../acme" },
			why:    "did not say what your organization's plugin is called"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			m := &machine{
				home: home, configDir: filepath.Join(home, ".claude"), accountFile: filepath.Join(home, ".claude.json"),
				repo: t.TempDir(), env: map[string]string{}, slug: slug,
			}
			m.bucket = stageClaudeHome(t, m.configDir, m.accountFile, slug, "grill-me", "notes")
			if tt.mutate != nil {
				tt.mutate(t, m)
			}

			got := claudeOrgProbe{
				home: m.home, getenv: func(k string) string { return m.env[k] }, repoRoot: m.repo, teamSlug: m.slug,
			}.run()

			require.NotEmpty(t, got.Why, "a decision with no reason leaves the coworker guessing")
			require.Contains(t, got.Why, tt.why)
			if tt.skills == nil {
				require.False(t, got.Found, "ox would defer to a plugin this Claude Code does not load: %s", got.Why)
				require.Empty(t, got.Skills)
				return
			}
			require.True(t, got.Found, got.Why)
			require.Equal(t, tt.skills, got.Skills)
		})
	}
}

// TestDeferOnlyWhatClaudeCodeHolds: the org can list a skill this machine's
// Claude Code has not synced yet (it syncs at start-up). Only skills the
// local plugin carries are deferred, so there is no window with neither copy.
func TestDeferOnlyWhatClaudeCodeHolds(t *testing.T) {
	t.Parallel()
	orgSync := teamskills.ClaudeOrgSync{TeamSlug: "acme", Skills: []string{"a", "b", "c"}, FetchedAt: time.Now(), Fresh: true}

	got := DeferOnlyWhatClaudeCodeHolds(orgSync, ClaudeCodeOrgPlugin{Found: true, Skills: []string{"c", "a", "z"}})
	require.Equal(t, []string{"a", "c"}, got.Skills, "deferred must be the sorted intersection")
	require.True(t, got.Has("a"))
	require.False(t, got.Has("b"), "a skill the local plugin lacks was deferred")

	none := DeferOnlyWhatClaudeCodeHolds(orgSync, ClaudeCodeOrgPlugin{Skills: []string{"a"}})
	require.False(t, none.Active(), "a plugin that is not loaded deferred something")
	require.Empty(t, none.Revision(), "the revision must read exactly as a repo with no org sync")
}

// TestClaudeOrgPluginHere_DedupeSetting: skills.claude_org_dedupe=off forces
// ox to copy even with the plugin present, and a config ox cannot read is
// not taken as permission to stop copying.
// Not parallel: it points OX_USER_CONFIG and CLAUDE_CONFIG_DIR at fixtures.
func TestClaudeOrgPluginHere_DedupeSetting(t *testing.T) {
	for _, tt := range []struct {
		name   string
		config string // "" means no config file at all
		found  bool
		why    string
	}{
		{name: "no config: on by default", found: true},
		{name: "explicitly on", config: "skills:\n  claude_org_dedupe: true\n", found: true},
		{name: "opted out", config: "skills:\n  claude_org_dedupe: false\n", why: "you turned off skills.claude_org_dedupe"},
		{name: "config unreadable", config: "skills: [\n", why: "could not read your ox config"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			claudeDir := t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
			t.Setenv("CLAUDE_CODE_PLUGIN_CACHE_DIR", "")
			stageClaudeHome(t, claudeDir, filepath.Join(claudeDir, ".claude.json"), "acme", "grill-me")
			cfgFile := filepath.Join(t.TempDir(), "config.yaml")
			if tt.config != "" {
				require.NoError(t, os.WriteFile(cfgFile, []byte(tt.config), 0o600))
			}
			t.Setenv("OX_USER_CONFIG", cfgFile)

			got := ClaudeOrgPluginHere(t.TempDir(), teamskills.ClaudeOrgSync{TeamSlug: "acme"})

			require.Equal(t, tt.found, got.Found, got.Why)
			if tt.why != "" {
				require.Contains(t, got.Why, tt.why)
				require.Empty(t, got.Skills)
			}
		})
	}
}
