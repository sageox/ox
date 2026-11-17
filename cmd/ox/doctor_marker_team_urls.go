package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	entries, err := os.ReadDir(sageoxDir)
	if err != nil {
		if os.IsNotExist(err) {
			return SkippedCheck(markerTeamURLsCheckName, ".sageox/ missing", "")
		}
		return WarningCheck(markerTeamURLsCheckName, "read error", err.Error())
	}

	var found, stripped, failed []string
	markers := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), ".repo_") {
			continue
		}
		markers++

		markerPath := filepath.Join(sageoxDir, entry.Name())
		data, err := os.ReadFile(markerPath)
		if err != nil {
			continue
		}
		// Generic map so fields this ox version doesn't know about survive the rewrite.
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil {
			continue
		}
		if _, ok := fields["team_urls"]; !ok {
			continue
		}
		if !fix {
			found = append(found, entry.Name())
			continue
		}

		delete(fields, "team_urls")
		updated, err := json.MarshalIndent(fields, "", "  ")
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s (%s)", entry.Name(), err))
			continue
		}
		if err := os.WriteFile(markerPath, updated, 0644); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%s)", entry.Name(), err))
			continue
		}
		stripped = append(stripped, entry.Name())
	}

	if markers == 0 {
		return SkippedCheck(markerTeamURLsCheckName, "no .repo_* marker", "")
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		return WarningCheck(markerTeamURLsCheckName, "could not remove team_urls", strings.Join(failed, ", "))
	}
	if len(stripped) > 0 {
		sort.Strings(stripped)
		return PassedCheck(markerTeamURLsCheckName,
			fmt.Sprintf("removed team_urls from %s; commit .sageox/ to publish the cleanup", strings.Join(stripped, ", ")))
	}
	if len(found) > 0 {
		sort.Strings(found)
		return WarningCheck(markerTeamURLsCheckName,
			fmt.Sprintf("%s lists the names of the user's teams", strings.Join(found, ", ")),
			"Run `ox doctor --fix` to remove team_urls from the committed marker")
	}
	return PassedCheck(markerTeamURLsCheckName, "ok")
}
