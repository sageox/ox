package claude

import "embed"

//go:embed commands/*.md
var CommandFS embed.FS

// SkillFS embeds the per-skill directory tree (skills/<name>/SKILL.md).
// Unlike CommandFS (one .md per command), skills are directories, so the
// embed uses `all:skills` to capture the nested SKILL.md files.
//
//go:embed all:skills
var SkillFS embed.FS
