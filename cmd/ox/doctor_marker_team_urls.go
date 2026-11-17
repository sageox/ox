package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/fileutil"
)

const markerTeamURLsCheckName = "Marker team URLs"

// checkMarkerTeamURLs finds team_urls left in committed .repo_* markers by
// ox init before #1336. The map named every team the initializing user
// belonged to, so anyone with read access to the repo could see them.
func checkMarkerTeamURLs(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(markerTeamURLsCheckName, "not in git repo", "")
	}
	if !isSageoxInitialized(gitRoot) {
		return SkippedCheck(markerTeamURLsCheckName, ".sageox/ not initialized", "")
	}
	return checkMarkerTeamURLsIn(filepath.Join(gitRoot, ".sageox"), fix)
}

func checkMarkerTeamURLsIn(sageoxDir string, fix bool) checkResult {
	info, err := os.Lstat(sageoxDir)
	if err != nil {
		if os.IsNotExist(err) {
			return SkippedCheck(markerTeamURLsCheckName, ".sageox/ missing", "")
		}
		return WarningCheck(markerTeamURLsCheckName, "read error", err.Error())
	}
	// A symlinked .sageox/ could point anywhere; never rewrite through it.
	if !info.IsDir() {
		return WarningCheck(markerTeamURLsCheckName, ".sageox/ is not a directory",
			"Markers were not checked; replace the .sageox symlink with a directory")
	}

	// Every read and write stays inside .sageox/, even if an entry is swapped for a symlink mid-run.
	root, err := os.OpenRoot(sageoxDir)
	if err != nil {
		return WarningCheck(markerTeamURLsCheckName, "read error", err.Error())
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return WarningCheck(markerTeamURLsCheckName, "read error", err.Error())
	}

	var found, stripped, failed, unreadable, irregular []string
	markers := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, ".repo_") {
			continue
		}
		markers++
		if !entry.Type().IsRegular() {
			irregular = append(irregular, name)
			continue
		}

		data, err := root.ReadFile(name)
		if err != nil {
			unreadable = append(unreadable, name)
			continue
		}
		// Generic map so fields this ox version doesn't know about survive the rewrite.
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil {
			unreadable = append(unreadable, name)
			continue
		}
		if _, ok := fields["team_urls"]; !ok {
			continue
		}
		if !fix {
			found = append(found, name)
			continue
		}

		delete(fields, "team_urls")
		updated, err := json.MarshalIndent(fields, "", "  ")
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s (%s)", name, err))
			continue
		}
		// Atomic replace: a failed write leaves the original marker intact.
		if err := fileutil.AtomicWriteBytesInRoot(root, name, updated, 0644); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%s)", name, err))
			continue
		}
		stripped = append(stripped, name)
	}

	if markers == 0 {
		return SkippedCheck(markerTeamURLsCheckName, "no .repo_* marker", "")
	}

	var parts []string
	list := func(prefix string, names []string) {
		if len(names) > 0 {
			sort.Strings(names)
			parts = append(parts, prefix+strings.Join(names, ", "))
		}
	}
	list("removed team_urls from ", stripped)
	if len(stripped) > 0 {
		parts[0] += "; commit .sageox/ to publish the cleanup"
	}
	cleanup := len(parts)
	list("could not remove team_urls from ", failed)
	list("could not read ", unreadable)
	list("not a regular file, skipped: ", irregular)
	problems := len(parts) > cleanup

	switch {
	case len(found) > 0:
		sort.Strings(found)
		detail := "Run `ox doctor --fix` to remove team_urls from the committed marker"
		if problems {
			detail += "; " + strings.Join(parts, "; ")
		}
		return WarningCheck(markerTeamURLsCheckName,
			fmt.Sprintf("%s lists the names of the user's teams", strings.Join(found, ", ")), detail)
	case problems:
		return WarningCheck(markerTeamURLsCheckName, strings.Join(parts, "; "), "")
	case len(stripped) > 0:
		return PassedCheck(markerTeamURLsCheckName, parts[0])
	}
	return PassedCheck(markerTeamURLsCheckName, "ok")
}
