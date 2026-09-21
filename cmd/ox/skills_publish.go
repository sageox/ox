package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/adapterstamp"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/spf13/cobra"
)

// skills_publish.go — `ox skills publish`.
//
// This is the authoring bridge between a skill a coworker has proved in one
// repository and the team's shared source. It intentionally does not create a
// second kind of repository-scoped install: it copies into the Team Context,
// and normal `ox sync` convergence distributes that team-owned copy.

var skillsPublishCmd = &cobra.Command{
	Use:   "publish <name>...",
	Short: "Publish repository skills to your Team Context",
	Long: `Publish hand-authored skills from this repository to your Team Context.

Each name is resolved from the skills directories selected by ` + "`ox init`" + `. If
the same name exists in more than one selected directory, every copy must be
identical. ox refuses skills it manages, symlinks, unsafe or mismatched names,
and any destination the team already owns.

Publishing copies the complete skill directory and records it in the Team
Context. It does not remove the repository copy and never overwrites a published
skill. The skill's existing ` + "`repos:`" + ` metadata controls which repositories
receive it; without ` + "`repos:`" + ` it applies to every repository on the team.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runSkillsPublish,
}

func init() {
	skillsPublishCmd.Flags().Bool("json", false, "Emit machine-readable JSON")
	skillsCmd.AddCommand(skillsPublishCmd)
}

func runSkillsPublish(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return fmt.Errorf("not inside a git repository")
	}
	out, err := publishRepoSkillsToTeam(gitRoot, args)
	if err != nil {
		return err
	}
	return emitSkillsChange(cmd.OutOrStdout(), out, asJSON)
}

// publishRepoSkillsToTeam resolves every source before the shared Team Context
// transaction starts. That keeps a multi-name publish all-or-nothing even when
// the final name is missing, malformed, or differs between selected roots.
func publishRepoSkillsToTeam(repoRoot string, names []string) (skillsChangeOutput, error) {
	out := newSkillsChangeOutput()
	names, err := validatePublishNames(names)
	if err != nil {
		return out, err
	}
	roots, err := skillTargetRoots(repoRoot)
	if err != nil {
		return out, err
	}
	if len(roots) == 0 {
		return out, fmt.Errorf("this repository has not selected an AI coworker, so there is no skills directory to publish from — run `ox init`")
	}
	desired, _, err := skillmanager.LoadDesired(repoRoot)
	if err != nil {
		return out, err
	}
	selected, err := selectedCatalogNames(desired)
	if err != nil {
		return out, err
	}
	for _, name := range names {
		if selected[name] {
			return out, fmt.Errorf("%q is managed by ox in this repository and cannot be published as a hand-authored team skill%s",
				name, nothingChangedSuffix(names))
		}
	}

	repo, err := os.OpenRoot(repoRoot)
	if err != nil {
		return out, fmt.Errorf("open repository: %w", err)
	}
	defer func() { _ = repo.Close() }()

	seeds := make([]teamSkillSeed, 0, len(names))
	for _, name := range names {
		files, foundRoots, readErr := findPublishSkill(repo, roots, name)
		if readErr != nil {
			return out, readErr
		}
		if len(foundRoots) == 0 {
			return out, fmt.Errorf("%q is not installed in this repository's selected skills directories (%s)%s",
				name, strings.Join(roots, ", "), nothingChangedSuffix(names))
		}
		if err := validatePublishManifest(name, files); err != nil {
			return out, fmt.Errorf("cannot publish %q from %s: %w%s",
				name, strings.Join(foundRoots, ", "), err, nothingChangedSuffix(names))
		}
		seeds = append(seeds, teamSkillSeed{
			name: name, relDir: path.Join(teamSkillsPublishRoot, name), files: files,
		})
	}
	published, err := publishTeamSkillSeeds(repoRoot, names, seeds)
	if err != nil {
		return published, err
	}
	published.Guidance = fmt.Sprintf("%s published to your Team Context without changing the repository copies. Their `repos:` metadata controls where they arrive; without `repos:` they reach every team repository. Distribution is automatic; run `ox sync` only if you need it immediately, verify with `ox skills status`, then remove any redundant repository copy.",
		strings.Join(names, ", "))
	return published, nil
}

func validatePublishNames(names []string) ([]string, error) {
	seen := make(map[string]bool, len(names))
	unique := make([]string, 0, len(names))
	for _, name := range names {
		if !teamdocs.ValidTeamSkillName(name) {
			return nil, fmt.Errorf("%q is not a safe team skill name; use lowercase letters, digits, dots, underscores, and hyphens, with no `..`%s",
				name, nothingChangedSuffix(names))
		}
		if skillmanager.IsReservedName(name) || name == skillmanager.CommittedOnRamp {
			return nil, fmt.Errorf("%q is managed by ox and cannot be published as a hand-authored team skill%s",
				name, nothingChangedSuffix(names))
		}
		if !seen[name] {
			seen[name] = true
			unique = append(unique, name)
		}
	}
	return unique, nil
}

// findPublishSkill reads every selected copy of name. Multiple agent targets
// often share one skill semantically; accepting divergent bytes would make the
// selected root order decide what the whole team receives.
func findPublishSkill(repo *os.Root, roots []string, name string) ([]skills.File, []string, error) {
	var canonical []skills.File
	var found []string
	for _, root := range dedupeStrings(roots) {
		relDir := path.Join(filepath.ToSlash(root), name)
		dir, err := openPublishSourceDir(repo, relDir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("inspect %s: %w", relDir, err)
		}
		files, err := readPublishSourceFiles(dir)
		_ = dir.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", relDir, err)
		}
		if len(found) > 0 && !sameSkillFiles(canonical, files) {
			return nil, nil, fmt.Errorf("%q has different copies in %s and %s; make them identical before publishing",
				name, found[0], root)
		}
		canonical = files
		found = append(found, root)
	}
	return canonical, found, nil
}

// openPublishSourceDir pins each path component to a real directory. A plain
// os.OpenRoot(repo).OpenRoot(relative) prevents an escape outside the repository
// but still follows a link to some other directory *inside* it; publishing that
// would copy bytes outside the skill the coworker named.
func openPublishSourceDir(repo *os.Root, relative string) (*os.Root, error) {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("path escapes repository")
	}
	current, err := repo.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		info, err := current.Lstat(part)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		if !info.IsDir() {
			_ = current.Close()
			return nil, fmt.Errorf("refusing symlink or non-directory path component %s", part)
		}
		child, err := current.OpenRoot(part)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		actual, err := child.Stat(".")
		if err != nil || !os.SameFile(info, actual) {
			_ = child.Close()
			_ = current.Close()
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("repository directory changed while opening %s", part)
		}
		_ = current.Close()
		current = child
	}
	return current, nil
}

func readPublishSourceFiles(dir *os.Root) ([]skills.File, error) {
	var files []skills.File
	err := fs.WalkDir(dir.FS(), ".", func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filePath == "." || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("refusing symlink or non-regular file %s", filepath.ToSlash(filePath))
		}
		info, err := dir.Lstat(filePath)
		if err != nil {
			return err
		}
		file, err := dir.Open(filePath)
		if err != nil {
			return err
		}
		actual, statErr := file.Stat()
		if statErr != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) {
			_ = file.Close()
			if statErr != nil {
				return statErr
			}
			return fmt.Errorf("refusing file that changed while reading %s", filepath.ToSlash(filePath))
		}
		content, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		files = append(files, skills.File{
			Path: strings.TrimPrefix(filepath.ToSlash(filePath), "./"), Content: content,
		})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, err
}

func sameSkillFiles(a, b []skills.File) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Path != b[i].Path || !bytes.Equal(a[i].Content, b[i].Content) {
			return false
		}
	}
	return true
}

func validatePublishManifest(name string, files []skills.File) error {
	var manifest []byte
	for _, file := range files {
		if file.Path == skills.SkillFileName {
			manifest = file.Content
			break
		}
	}
	if manifest == nil {
		return fmt.Errorf("the skill has no %s", skills.SkillFileName)
	}
	if adapterstamp.StampVerifies(manifest, "ox") {
		return fmt.Errorf("the skill is managed by ox, not hand-authored here")
	}
	gotName, _ := publishManifestField(manifest, "name")
	if gotName != name {
		return fmt.Errorf("frontmatter name is %q, but the skill directory is %q", gotName, name)
	}
	description, folded := manifestDescription(manifest)
	switch {
	case folded:
		return fmt.Errorf("description is a multi-line YAML block; rewrite it as one line so Team Context readers preserve it")
	case description == "":
		return fmt.Errorf("frontmatter has no single-line description in its first 30 lines")
	}
	if rawRepos, present := publishManifestField(manifest, "repos"); present {
		// Team Context intentionally accepts only an inline list or one bare slug.
		// A block list (`repos:` followed by `- owner/repo`) parses as absent and
		// therefore means ALL repositories. Refuse that silent scope widening at
		// the publishing boundary.
		if rawRepos == "" {
			return fmt.Errorf("repos: must use an inline list such as [\"owner/repo\"] (use [] explicitly for every team repository)")
		}
		if strings.HasPrefix(rawRepos, "[") != strings.HasSuffix(rawRepos, "]") {
			return fmt.Errorf("repos: has an incomplete inline list; use [\"owner/repo\"]")
		}
	}
	if audience, _ := publishManifestField(manifest, "audience"); audience == teamdocs.RuleAudienceHuman {
		return fmt.Errorf("audience: human prevents AI coworkers from receiving this skill")
	}
	if visibility, _ := publishManifestField(manifest, "visibility"); visibility == teamdocs.VisibilityHidden {
		return fmt.Errorf("visibility: hidden prevents this skill from being published")
	}
	if status, _ := publishManifestField(manifest, "status"); status == teamdocs.RuleStatusDraft ||
		strings.HasPrefix(status, teamdocs.RuleStatusSupersededPrefix) {
		return fmt.Errorf("status: %s prevents this skill from being published", status)
	}
	return nil
}

// publishManifestField mirrors the Team Context's deliberately-small
// frontmatter reader: unindented single-line fields in the first 30 lines.
func publishManifestField(content []byte, key string) (string, bool) {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	prefix := key + ":"
	for line := 0; scanner.Scan() && line < 30; line++ {
		text := strings.TrimRight(scanner.Text(), "\r")
		if line == 0 {
			if text != "---" {
				return "", false
			}
			continue
		}
		if text == "---" {
			return "", false
		}
		if value, ok := strings.CutPrefix(text, prefix); ok {
			value = strings.TrimSpace(value)
			if comment := strings.Index(value, " #"); comment >= 0 {
				value = value[:comment]
			}
			return strings.Trim(strings.TrimSpace(value), `"'`), true
		}
	}
	return "", false
}
