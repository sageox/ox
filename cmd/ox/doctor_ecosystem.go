package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
	"github.com/sageox/ox/internal/version"
)

// checkForUpdates checks GitHub for newer releases
func checkForUpdates() checkResult {
	latestVersion, err := getLatestGitHubRelease()
	if err != nil {
		// fail gracefully - network/API issues shouldn't break doctor
		return checkResult{
			name:    "ox version",
			skipped: true,
			message: "check unavailable",
		}
	}

	// warm version cache for prime (side effect)
	writeVersionCacheFromDoctor(latestVersion)

	currentVersion := strings.TrimPrefix(version.Version, "v")
	latestVersion = strings.TrimPrefix(latestVersion, "v")

	if currentVersion == latestVersion {
		return checkResult{
			name:    "ox version",
			passed:  true,
			message: fmt.Sprintf("v%s (latest)", currentVersion),
		}
	}

	// compare versions - if latest is newer, suggest update
	if isNewerVersion(latestVersion, currentVersion) {
		return checkResult{
			name:    "ox version",
			passed:  true,
			warning: true,
			message: fmt.Sprintf("v%s → v%s available", currentVersion, latestVersion),
			detail:  "Run 'ox upgrade' to update",
		}
	}

	// current is same or newer (dev build)
	return checkResult{
		name:    "ox version",
		passed:  true,
		message: fmt.Sprintf("v%s", currentVersion),
	}
}

// getLatestGitHubRelease fetches the latest release version from GitHub
func getLatestGitHubRelease() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	url := "https://api.github.com/repos/sageox/ox/releases/latest"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	// only User-Agent for external API; no X-Orchestrator
	req.Header.Set("User-Agent", useragent.String())

	logger.LogHTTPRequest("GET", url)
	start := time.Now()

	resp, err := http.DefaultClient.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("GET", url, err, duration)
		return "", err
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("GET", url, resp.StatusCode, duration)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api returned %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}

	return release.TagName, nil
}

// isNewerVersion returns true if a is newer than b (simple semver comparison)
func isNewerVersion(a, b string) bool {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")

	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		var aNum, bNum int
		_, _ = fmt.Sscanf(aParts[i], "%d", &aNum)
		_, _ = fmt.Sscanf(bParts[i], "%d", &bNum)
		if aNum > bNum {
			return true
		}
		if aNum < bNum {
			return false
		}
	}
	return len(aParts) > len(bParts)
}

// containsString checks if a string slice contains a value
func containsString(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}
