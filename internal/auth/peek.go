package auth

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sageox/ox/internal/endpoint"
)

// PeekTokenForEndpoint is exclusively for observational previews. It neither
// refreshes nor migrates credentials and creates no directories or lock files.
// Atomic auth-store replacement makes a single read a coherent old/new snapshot.
func PeekTokenForEndpoint(ep string) (*StoredToken, error) {
	ep = endpoint.NormalizeEndpoint(ep)
	if token, err := envTokenOrError(ep); err != nil || token != nil {
		return token, err
	}
	path, err := GetAuthFilePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var store AuthStore
	if err = json.Unmarshal(data, &store); err != nil {
		return nil, err
	}
	if store.Tokens == nil {
		return nil, fmt.Errorf("legacy credentials require normal login before observational preview")
	}
	normalizeTokenKeys(&store)
	return store.Tokens[ep], nil
}
