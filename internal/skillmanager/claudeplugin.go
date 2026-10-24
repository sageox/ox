package skillmanager

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/sageox/ox/internal/teamskills"
)

// claudeplugin.go answers one question before ox stops copying a team skill
// into .claude/skills: does Claude Code on THIS machine have the team's
// Claude organization plugin, and does that plugin carry the skill?
//
// The server's claude_org_synced_skills is team-wide. It says the
// organization pulled the export, not that this coworker's Claude Code loads
// it. A coworker signed in to another Claude account or organization, or
// using an API key, Bedrock or Vertex, never receives org plugins; deferring
// for them deletes ox's copy and nothing replaces it. So ox reads Claude
// Code's own files and defers only the skills the local plugin carries.
//
// Everything short of "found, loaded, carries the skill" keeps ox copying: a
// duplicate is a nuisance, a missing skill is a broken workflow.
//
// What ox reads. Synced plugins live under plugins/synced/ and load only in
// sessions signed in with a claude.ai account
// (https://code.claude.com/docs/en/plugins/loading#plugins-synced-from-claude-ai).
// The layout INSIDE synced/ is not documented; this is what Claude Code
// 2.1.310 writes, and anything else reads as "not found":
//
//	~/.claude.json                                oauthAccount.organizationUuid, .accountUuid
//	~/.claude/plugins/synced/<org>_<account>/     one bucket per signed-in claude.ai account
//	    <plugin>/skills/<skill>/SKILL.md          the synced plugin, in plugin layout
//	    <plugin>.meta.json                        {"marketplace_name": "sageox-<team-slug>", ...}
//
// The bucket matters: a bucket from an earlier sign-in stays on disk, so a
// plugin anywhere under synced/ is not proof that the current session loads
// it.
//
// ox reads files only, never the auth or provider variables of its own
// process. The daemon reconciles the same .claude/skills without the
// session's environment; an answer that depended on it would flip the copy
// in and out between the daemon and `ox agent prime`. Sessions that sign in
// some other way only in the shell are what skills.claude_org_dedupe=off is
// for.

const (
	// claudeOrgMarketplacePrefix is the export's marketplace namespace. Mirrors
	// marketplacePrefix in the monorepo's packages/teamskills/export.go, which
	// names each team's marketplace "sageox-" + team slug.
	claudeOrgMarketplacePrefix = "sageox-"

	claudeSyncedMetaSuffix = ".meta.json"

	// Claude Code caps the sidecar at 4 KiB itself; a bigger one is not its.
	maxClaudeSyncedMetaBytes = 4 << 10
	// ~/.claude.json grows with project history (hundreds of KiB is normal).
	// The cap bounds a pathological file on the prime hot path; over it, ox
	// does not guess and keeps copying.
	maxClaudeAccountFileBytes = 32 << 20
	maxClaudeSettingsBytes    = 1 << 20
	// A bucket holds a directory and a sidecar per synced plugin, and a plugin
	// one directory per skill. Far above any real organization, low enough
	// that a directory that is something else cannot stall a session start.
	maxClaudeDirEntries = 1024
)

// claudeUUID matches the ids Claude Code builds a bucket name from. It
// lowercases both before joining them, so ox does the same.
var claudeUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$`)

// claudeAiPluginBlockers are settings `env` keys that make a Claude Code
// session sign in without a claude.ai account, or skip the flag fetch synced
// plugins need. Such a session does not load org plugins (see the sign-in
// requirements in the loading doc above).
var claudeAiPluginBlockers = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CODE_OAUTH_TOKEN",
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_VERTEX",
	"CLAUDE_CODE_USE_FOUNDRY",
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
}

// ClaudeCodeOrgPlugin is what this machine's Claude Code holds of a team's
// Claude organization plugin.
type ClaudeCodeOrgPlugin struct {
	// Found is true when Claude Code here loads the plugin and it carries at
	// least one skill.
	Found bool
	// Skills are the skills the local plugin carries, sorted. ox stops copying
	// a team skill into .claude/skills only when it is listed here.
	Skills []string
	// Why is one line for a coworker: that Claude Code has the plugin, or why
	// ox keeps copying every team skill for Claude Code here.
	Why string
}

func claudePluginNotHere(why string) ClaudeCodeOrgPlugin { return ClaudeCodeOrgPlugin{Why: why} }

// ClaudeOrgPluginHere reports what this machine's Claude Code holds of the
// org plugin orgSync names. repoRoot locates the repository's Claude Code
// settings, which can turn the plugin off for this repository.
func ClaudeOrgPluginHere(repoRoot string, orgSync teamskills.ClaudeOrgSync) ClaudeCodeOrgPlugin {
	cfg, err := config.LoadUserConfig()
	if err != nil {
		return claudePluginNotHere("ox could not read your ox config, so it cannot tell whether you turned off skills.claude_org_dedupe (" + err.Error() + ")")
	}
	if !cfg.ClaudeOrgDedupeEnabled() {
		return claudePluginNotHere("you turned off skills.claude_org_dedupe")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return claudePluginNotHere("ox could not find your home directory, where Claude Code keeps its plugins")
	}
	return claudeOrgProbe{home: home, getenv: os.Getenv, repoRoot: repoRoot, teamSlug: orgSync.TeamSlug}.run()
}

// claudeOrgProbe is one lookup, with everything machine-specific passed in
// so a test can point it at a fixture home.
type claudeOrgProbe struct {
	home     string
	getenv   func(string) string
	repoRoot string
	teamSlug string
}

func (p claudeOrgProbe) run() ClaudeCodeOrgPlugin {
	if !teamdocs.ValidTeamSkillName(p.teamSlug) {
		return claudePluginNotHere("the server did not say what your organization's plugin is called")
	}
	configDir, accountFile, why := p.locate()
	if why != "" {
		return claudePluginNotHere(why)
	}
	bucket, why := claudeSignedInBucket(accountFile)
	if why != "" {
		return claudePluginNotHere(why)
	}
	marketplace := claudeOrgMarketplacePrefix + p.teamSlug
	pluginDir, why := findClaudeSyncedPlugin(filepath.Join(configDir, "plugins", "synced", bucket), marketplace)
	if why != "" {
		return claudePluginNotHere(why)
	}

	settings := []string{filepath.Join(configDir, "settings.json")}
	if p.repoRoot != "" {
		settings = append(settings,
			filepath.Join(p.repoRoot, ".claude", "settings.json"),
			filepath.Join(p.repoRoot, ".claude", "settings.local.json"))
	}
	for _, file := range settings {
		if why := claudeSettingsStopPlugin(file, p.teamSlug); why != "" {
			return claudePluginNotHere(why)
		}
	}

	carried, why := claudeSyncedPluginSkills(pluginDir)
	if why != "" {
		return claudePluginNotHere(why)
	}
	if len(carried) == 0 {
		return claudePluginNotHere("Claude Code here has the " + p.teamSlug + " plugin, but it carries no skills yet")
	}
	return ClaudeCodeOrgPlugin{
		Found:  true,
		Skills: carried,
		Why:    "Claude Code here has the " + p.teamSlug + " plugin from your Claude organization",
	}
}

// locate returns Claude Code's config directory and account file. It follows
// CLAUDE_CONFIG_DIR, as Claude Code does and as ox's other readers of these
// files do (internal/trace, internal/daemon/agentwork). Whether synced/
// follows CLAUDE_CODE_PLUGIN_CACHE_DIR is not documented, so ox does not
// guess where the plugins went.
func (p claudeOrgProbe) locate() (configDir, accountFile, why string) {
	if p.getenv("CLAUDE_CODE_PLUGIN_CACHE_DIR") != "" {
		return "", "", "Claude Code keeps its plugins elsewhere here (CLAUDE_CODE_PLUGIN_CACHE_DIR), where ox does not look"
	}
	if dir := p.getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir, filepath.Join(dir, ".claude.json"), ""
	}
	if p.home == "" {
		return "", "", "ox could not find your home directory, where Claude Code keeps its plugins"
	}
	return filepath.Join(p.home, ".claude"), filepath.Join(p.home, ".claude.json"), ""
}

// claudeSignedInBucket names the synced/ bucket of the claude.ai account
// Claude Code is signed in to.
func claudeSignedInBucket(accountFile string) (bucket, why string) {
	data, err := readFileCapped(accountFile, maxClaudeAccountFileBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return "", "Claude Code is not signed in to a claude.ai account here"
	}
	if err != nil {
		return "", "ox could not read Claude Code's sign-in (" + err.Error() + ")"
	}
	var doc struct {
		OAuthAccount *struct {
			OrganizationUUID string `json:"organizationUuid"`
			AccountUUID      string `json:"accountUuid"`
		} `json:"oauthAccount"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", "ox could not parse Claude Code's sign-in in " + accountFile
	}
	account := doc.OAuthAccount
	if account == nil || !claudeUUID.MatchString(account.OrganizationUUID) || !claudeUUID.MatchString(account.AccountUUID) {
		return "", "Claude Code is not signed in to a claude.ai account here"
	}
	return strings.ToLower(account.OrganizationUUID) + "_" + strings.ToLower(account.AccountUUID), ""
}

// findClaudeSyncedPlugin finds the one plugin directory in bucket whose
// sidecar names marketplace. Zero or several is "not found": with two, ox
// cannot tell which one Claude Code loads.
func findClaudeSyncedPlugin(bucket, marketplace string) (pluginDir, why string) {
	entries, err := readDirCapped(bucket)
	if errors.Is(err, fs.ErrNotExist) {
		return "", "Claude Code has no plugins from claude.ai for the account signed in here"
	}
	if err != nil {
		return "", "ox could not read Claude Code's synced plugins (" + err.Error() + ")"
	}
	var found []string
	unreadable := 0
	for _, e := range entries {
		name := e.Name()
		dir, isMeta := strings.CutSuffix(name, claudeSyncedMetaSuffix)
		if !isMeta || dir == "" || strings.HasPrefix(name, ".") || !e.Type().IsRegular() {
			continue
		}
		data, err := readFileCapped(filepath.Join(bucket, name), maxClaudeSyncedMetaBytes)
		var meta struct {
			MarketplaceName string `json:"marketplace_name"`
		}
		if err != nil || json.Unmarshal(data, &meta) != nil {
			unreadable++
			continue
		}
		if meta.MarketplaceName != marketplace {
			continue
		}
		// Lstat: a link in place of the plugin is not something Claude Code wrote.
		if info, err := os.Lstat(filepath.Join(bucket, dir)); err == nil && info.IsDir() {
			found = append(found, filepath.Join(bucket, dir))
		}
	}
	switch len(found) {
	case 1:
		return found[0], ""
	case 0:
		why := "the claude.ai account signed in here has no " + marketplace + " plugin"
		if unreadable > 0 {
			why += fmt.Sprintf(" (ox could not read %d of its plugin records)", unreadable)
		}
		return "", why
	default:
		return "", fmt.Sprintf("Claude Code has %d copies of the %s plugin here, and ox cannot tell which one it loads", len(found), marketplace)
	}
}

// claudeSettingsStopPlugin reports why the settings in file keep a synced
// plugin from loading, or "" when they do not. A missing file says nothing; a
// file ox cannot read or parse could say anything, so it stops the dedupe.
func claudeSettingsStopPlugin(file, pluginName string) string {
	data, err := readFileCapped(file, maxClaudeSettingsBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		return "ox could not read Claude Code settings " + file + " (" + err.Error() + ")"
	}
	var settings struct {
		SyncClaudeAiPlugins *bool                      `json:"syncClaudeAiPlugins"`
		EnabledPlugins      map[string]json.RawMessage `json:"enabledPlugins"`
		APIKeyHelper        string                     `json:"apiKeyHelper"`
		Env                 map[string]any             `json:"env"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return "ox could not parse Claude Code settings " + file
	}
	if settings.SyncClaudeAiPlugins != nil && !*settings.SyncClaudeAiPlugins {
		return "syncClaudeAiPlugins is off in " + file
	}
	if id := pluginName + "@synced"; strings.TrimSpace(string(settings.EnabledPlugins[id])) == "false" {
		return id + " is turned off in " + file
	}
	if settings.APIKeyHelper != "" {
		return "Claude Code signs in through apiKeyHelper (" + file + "), and such sessions do not load claude.ai plugins"
	}
	for _, key := range claudeAiPluginBlockers {
		v, ok := settings.Env[key]
		if !ok {
			continue
		}
		// "0"/"false" read as unset; any other value is treated as set, which
		// at worst keeps a copy Claude Code did not need.
		if s, isString := v.(string); isString && (s == "" || s == "0" || strings.EqualFold(s, "false")) {
			continue
		}
		return "Claude Code's settings set " + key + " (" + file + "), and such sessions do not load claude.ai plugins"
	}
	return ""
}

// claudeSyncedPluginSkills lists the skills a synced plugin carries: each
// skills/<name>/ directory holding a regular SKILL.md, which is how the
// export lays them out.
func claudeSyncedPluginSkills(pluginDir string) ([]string, string) {
	root := filepath.Join(pluginDir, "skills")
	entries, err := readDirCapped(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ""
	}
	if err != nil {
		return nil, "ox could not read the skills in Claude Code's copy of the plugin (" + err.Error() + ")"
	}
	var carried []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if info, err := os.Lstat(filepath.Join(root, e.Name(), skills.SkillFileName)); err == nil && info.Mode().IsRegular() {
			carried = append(carried, e.Name())
		}
	}
	sort.Strings(carried)
	return carried, ""
}

// DeferOnlyWhatClaudeCodeHolds narrows orgSync to the skills this machine's
// copy of the plugin carries. Claude Code syncs at start-up, so the org can
// have pulled a skill this machine has not received yet; until it arrives ox
// keeps its own copy, and there is never a moment with neither.
func DeferOnlyWhatClaudeCodeHolds(orgSync teamskills.ClaudeOrgSync, plugin ClaudeCodeOrgPlugin) teamskills.ClaudeOrgSync {
	if !plugin.Found {
		orgSync.Skills = nil
		return orgSync
	}
	held := make(map[string]bool, len(plugin.Skills))
	for _, name := range plugin.Skills {
		held[name] = true
	}
	// orgSync.Skills is sorted and Has binary-searches it; filtering in order
	// keeps it sorted.
	kept := make([]string, 0, len(orgSync.Skills))
	for _, name := range orgSync.Skills {
		if held[name] {
			kept = append(kept, name)
		}
	}
	orgSync.Skills = kept
	return orgSync
}

// readFileCapped reads at most limit bytes of file and fails rather than
// truncate: half a JSON document parsed as a whole one is a guess.
func readFileCapped(file string, limit int64) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", file, limit)
	}
	return data, nil
}

// readDirCapped lists dir, failing when it holds more than
// maxClaudeDirEntries: a partial listing could miss the one entry that
// matters and report it absent as if ox had looked.
func readDirCapped(dir string) ([]os.DirEntry, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(maxClaudeDirEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maxClaudeDirEntries {
		return nil, fmt.Errorf("%s holds more than %d entries", dir, maxClaudeDirEntries)
	}
	return entries, nil
}
