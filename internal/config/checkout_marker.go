package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// checkoutMarkerDir is the local-only directory for checkout metadata.
// This directory should be gitignored in ledger/team context repos.
const checkoutMarkerDir = ".sageox"

// checkoutMarkerFile is the marker file name.
const checkoutMarkerFile = "checkout.json"

// CheckoutMarker tracks metadata about a ledger or team context checkout.
// This file lives in <checkout-path>/.sageox/checkout.json and is gitignored.
type CheckoutMarker struct {
	// Type is "ledger" or "team-context"
	Type string `json:"type"`

	// Endpoint is the SageOx API endpoint this checkout is associated with
	Endpoint string `json:"endpoint"`

	// RepoID is the main repo's ID (from .repo_<id>) that this ledger belongs to.
	// Only set for ledger type, empty for team-context.
	RepoID string `json:"repo_id,omitempty"`

	// TeamID is the team ID for team-context checkouts.
	// Only set for team-context type, empty for ledger.
	TeamID string `json:"team_id,omitempty"`

	// CheckedOutAt is when the checkout was created
	CheckedOutAt time.Time `json:"checked_out_at"`

	// CheckedOutBy is the user who created the checkout (optional)
	CheckedOutBy string `json:"checked_out_by,omitempty"`
}

// checkoutMarkerPath returns the path to the checkout marker file.
func checkoutMarkerPath(checkoutPath string) string {
	return filepath.Join(checkoutPath, checkoutMarkerDir, checkoutMarkerFile)
}

// LoadCheckoutMarker reads the checkout marker from a ledger/team context directory.
// Returns nil, nil if the marker doesn't exist.
func LoadCheckoutMarker(checkoutPath string) (*CheckoutMarker, error) {
	if checkoutPath == "" {
		return nil, errors.New("checkout path cannot be empty")
	}

	markerPath := checkoutMarkerPath(checkoutPath)

	data, err := os.ReadFile(markerPath)
	if os.IsNotExist(err) {
		return nil, nil // marker doesn't exist yet
	}
	if err != nil {
		return nil, fmt.Errorf("read checkout marker: %w", err)
	}

	var marker CheckoutMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return nil, fmt.Errorf("parse checkout marker: %w", err)
	}

	return &marker, nil
}

// SaveCheckoutMarker writes the checkout marker to a ledger/team context directory.
// Creates the .sageox directory if it doesn't exist.
func SaveCheckoutMarker(checkoutPath string, marker *CheckoutMarker) error {
	if checkoutPath == "" {
		return errors.New("checkout path cannot be empty")
	}
	if marker == nil {
		return errors.New("marker cannot be nil")
	}

	// ensure .sageox directory exists
	sageoxDir := filepath.Join(checkoutPath, checkoutMarkerDir)
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		return fmt.Errorf("create .sageox directory: %w", err)
	}

	// ensure .gitignore exists in .sageox to protect local files
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
		gitignoreContent := "# Local-only files - do not commit\ncheckout.json\nworkspaces.jsonl\nledger\nteams/\n"
		// non-fatal error - best effort only
		_ = os.WriteFile(gitignorePath, []byte(gitignoreContent), 0644)
	}

	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal checkout marker: %w", err)
	}

	markerPath := checkoutMarkerPath(checkoutPath)
	if err := os.WriteFile(markerPath, data, 0600); err != nil {
		return fmt.Errorf("write checkout marker: %w", err)
	}

	return nil
}

// CreateLedgerMarker creates a checkout marker for a ledger repository.
func CreateLedgerMarker(checkoutPath, endpoint, repoID string) error {
	marker := &CheckoutMarker{
		Type:         "ledger",
		Endpoint:     endpoint,
		RepoID:       repoID,
		CheckedOutAt: time.Now().UTC(),
	}
	return SaveCheckoutMarker(checkoutPath, marker)
}

// CreateTeamContextMarker creates a checkout marker for a team context repository.
func CreateTeamContextMarker(checkoutPath, endpoint, teamID string) error {
	marker := &CheckoutMarker{
		Type:         "team-context",
		Endpoint:     endpoint,
		TeamID:       teamID,
		CheckedOutAt: time.Now().UTC(),
	}
	return SaveCheckoutMarker(checkoutPath, marker)
}

// CheckoutMarkerMismatch indicates a checkout is for a different endpoint or repo.
type CheckoutMarkerMismatch struct {
	CheckoutPath    string
	MarkerType      string
	MarkerEndpoint  string
	MarkerRepoID    string // for ledger
	CurrentEndpoint string
	CurrentRepoID   string // for ledger
}

func (e CheckoutMarkerMismatch) Error() string {
	if e.MarkerRepoID != "" && e.CurrentRepoID != "" && e.MarkerRepoID != e.CurrentRepoID {
		return fmt.Sprintf("ledger at %s is for repo %s but current repo is %s",
			e.CheckoutPath, e.MarkerRepoID, e.CurrentRepoID)
	}
	return fmt.Sprintf("%s at %s is for endpoint %s but current endpoint is %s",
		e.MarkerType, e.CheckoutPath, e.MarkerEndpoint, e.CurrentEndpoint)
}

// ValidateLedgerMarker checks if the ledger checkout matches the current endpoint and repo.
// Returns nil if OK, CheckoutMarkerMismatch if mismatched.
func ValidateLedgerMarker(checkoutPath, currentEndpoint, currentRepoID string) error {
	marker, err := LoadCheckoutMarker(checkoutPath)
	if err != nil {
		return fmt.Errorf("load checkout marker: %w", err)
	}
	if marker == nil {
		return nil // no marker, can't validate
	}

	// check endpoint first
	if marker.Endpoint != "" && currentEndpoint != "" && marker.Endpoint != currentEndpoint {
		return CheckoutMarkerMismatch{
			CheckoutPath:    checkoutPath,
			MarkerType:      "ledger",
			MarkerEndpoint:  marker.Endpoint,
			CurrentEndpoint: currentEndpoint,
		}
	}

	// check repo ID for ledger
	if marker.RepoID != "" && currentRepoID != "" && marker.RepoID != currentRepoID {
		return CheckoutMarkerMismatch{
			CheckoutPath:    checkoutPath,
			MarkerType:      "ledger",
			MarkerEndpoint:  marker.Endpoint,
			MarkerRepoID:    marker.RepoID,
			CurrentEndpoint: currentEndpoint,
			CurrentRepoID:   currentRepoID,
		}
	}

	return nil
}

// ValidateTeamContextMarker checks if the team context checkout matches the current endpoint.
// Returns nil if OK, CheckoutMarkerMismatch if mismatched.
func ValidateTeamContextMarker(checkoutPath, currentEndpoint string) error {
	marker, err := LoadCheckoutMarker(checkoutPath)
	if err != nil {
		return fmt.Errorf("load checkout marker: %w", err)
	}
	if marker == nil {
		return nil // no marker, can't validate
	}

	if marker.Endpoint != "" && currentEndpoint != "" && marker.Endpoint != currentEndpoint {
		return CheckoutMarkerMismatch{
			CheckoutPath:    checkoutPath,
			MarkerType:      "team-context",
			MarkerEndpoint:  marker.Endpoint,
			CurrentEndpoint: currentEndpoint,
		}
	}

	return nil
}
