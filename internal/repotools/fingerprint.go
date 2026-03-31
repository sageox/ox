package repotools

import (
	"bufio"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// RepoFingerprint holds repository identity fingerprint data for detecting
// identical or related repositories across different teams or installations.
//
// Purpose: When multiple teams run `ox init` on the same codebase (forks,
// clones, or parallel work), this fingerprint enables the SageOx API to:
//   - Detect that repos are the same or related
//   - Suggest team merges when appropriate
//   - Identify divergence between forks
//
// The fingerprint uses multiple signals because no single identifier is perfect:
//   - FirstCommit: Identifies the original repo, but forks share this
//   - MonthlyCheckpoints: Detect divergence over time (different commits = divergence)
//   - AncestrySamples: Consistent sampling regardless of commit frequency
type RepoFingerprint struct {
	// FirstCommit is the hash of the initial commit (same as repo_salt).
	// Forks share this value, making it useful for detecting related repos.
	FirstCommit string `json:"first_commit"`

	// MonthlyCheckpoints maps "YYYY-MM" to the first commit hash of that month.
	// Used to detect divergence: if two repos share the same first commit but
	// have different monthly checkpoints, they've diverged.
	// Only includes months with commits; sparse map.
	MonthlyCheckpoints map[string]string `json:"monthly_checkpoints"`

	// AncestrySamples contains commit hashes at power-of-2 intervals from the
	// first commit: 1st, 2nd, 4th, 8th, 16th, 32nd, 64th, 128th, 256th.
	// Provides consistent sampling regardless of commit cadence or age.
	//
	// FUTURE CONSIDERATION (not implemented): Add yearly exponential fingerprints
	// starting from 2027. Each year would have its own set of power-of-2 samples
	// from Jan 1 of that year, stored by year key (e.g., "2027": [...hashes]).
	// These would NOT cascade into following years - each year stands alone.
	//
	// This adds recency detection for repos with long histories. The downside:
	// during the first month of a new year, merge detection may fail if one
	// candidate doesn't have current year hashes yet. Server-side logic must
	// account for this when deciding whether to use current year hashes.
	AncestrySamples []string `json:"ancestry_samples"`

	// RemoteHashes contains salted SHA256 hashes of normalized remote URLs.
	// Different clones of the same repo often share remote URLs (origin).
	// Hashed with FirstCommit as salt to prevent enumeration attacks.
	RemoteHashes []string `json:"remote_hashes,omitempty"`
}

// ComputeFingerprint generates a repository fingerprint from git history.
// This performs a single O(N) scan of the commit history to compute all
// fingerprint components efficiently.
// Returns nil if the repository has no commits.
func ComputeFingerprint() (*RepoFingerprint, error) {
	if err := RequireVCS(VCSGit); err != nil {
		return nil, err
	}

	// single git log call: hash and author date, oldest first
	// format: "<hash> <ISO8601 date>"
	cmd := exec.Command("git", "log", "--format=%H %aI", "--reverse")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start git log: %w", err)
	}

	fingerprint := &RepoFingerprint{
		MonthlyCheckpoints: make(map[string]string),
	}

	// cutoff: only include months within last 12 months
	cutoff := time.Now().UTC().AddDate(0, -12, 0)
	cutoffMonth := cutoff.Format("2006-01")

	// power-of-2 sample positions (0-indexed): 0, 1, 3, 7, 15, 31, 63, 127, 255
	// corresponds to 1st, 2nd, 4th, 8th, 16th, 32nd, 64th, 128th, 256th commits
	samplePositions := make(map[int]bool)
	for interval := 1; interval <= 256; interval *= 2 {
		samplePositions[interval-1] = true
	}

	scanner := bufio.NewScanner(stdout)
	position := 0

	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			position++
			continue
		}

		hash := parts[0]
		dateStr := parts[1]

		// first commit
		if position == 0 {
			fingerprint.FirstCommit = hash
		}

		// ancestry samples at power-of-2 positions
		if samplePositions[position] {
			fingerprint.AncestrySamples = append(fingerprint.AncestrySamples, hash)
		}

		// monthly checkpoints: first commit of each month (within last 12 months)
		commitTime, err := time.Parse(time.RFC3339, dateStr)
		if err == nil {
			monthKey := commitTime.UTC().Format("2006-01")
			// only include if within last 12 months and not already recorded
			if monthKey >= cutoffMonth {
				if _, exists := fingerprint.MonthlyCheckpoints[monthKey]; !exists {
					fingerprint.MonthlyCheckpoints[monthKey] = hash
				}
			}
		}

		position++
	}

	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("git log failed: %w", err)
	}

	if fingerprint.FirstCommit == "" {
		return nil, fmt.Errorf("no commits found")
	}

	return fingerprint, nil
}

// WithRemoteHashes adds salted remote URL hashes to the fingerprint.
// Call this after ComputeFingerprint() to include remote URL identity.
// The hashes are salted with FirstCommit to prevent enumeration.
// offline-safe: remote hashes are optional; fingerprint is valid without them
func (f *RepoFingerprint) WithRemoteHashes() error {
	if f == nil || f.FirstCommit == "" {
		return fmt.Errorf("fingerprint has no first commit for salting")
	}

	urls, err := GetRemoteURLs()
	if err != nil {
		return err
	}

	if len(urls) > 0 {
		f.RemoteHashes = HashRemoteURLs(f.FirstCommit, urls)
	}

	return nil
}
