package auth

import (
	"fmt"
)

// RequireAuth checks if authentication is required and if the user is authenticated
// Returns an error if authentication is required but user is not authenticated
func RequireAuth() error {
	if !IsAuthRequired() {
		return nil
	}

	authenticated, err := IsAuthenticated()
	if err != nil {
		return fmt.Errorf("failed to check authentication status: %w", err)
	}

	if !authenticated {
		return fmt.Errorf("authentication required: please run 'ox login' to authenticate with sageox.ai")
	}

	return nil
}
