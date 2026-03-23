package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/endpoint"
)

// EndpointInfo holds information about an endpoint from a repo marker
type EndpointInfo struct {
	Endpoint string
	RepoID   string
	InitAt   string
}

// DiscoverEndpoints scans .sageox/ for .repo_* marker files and returns
// unique endpoints found. Returns endpoints sorted by their first appearance.
func DiscoverEndpoints(sageoxDir string) ([]EndpointInfo, error) {
	entries, err := os.ReadDir(sageoxDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	// track unique endpoints and their first occurrence
	endpointMap := make(map[string]EndpointInfo)
	var order []string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasPrefix(entry.Name(), ".repo_") {
			continue
		}

		markerPath := filepath.Join(sageoxDir, entry.Name())
		data, err := os.ReadFile(markerPath)
		if err != nil {
			continue
		}

		var marker struct {
			RepoID   string `json:"repo_id"`
			Endpoint string `json:"endpoint"`
			InitAt   string `json:"init_at"`
		}
		if err := json.Unmarshal(data, &marker); err != nil {
			continue
		}

		ep := marker.Endpoint
		if ep == "" {
			continue
		}

		// normalize endpoint for comparison (strips subdomain prefixes + trailing slash)
		ep = endpoint.NormalizeEndpoint(ep)

		if _, exists := endpointMap[ep]; !exists {
			endpointMap[ep] = EndpointInfo{
				Endpoint: ep,
				RepoID:   marker.RepoID,
				InitAt:   marker.InitAt,
			}
			order = append(order, ep)
		}
	}

	// build result in discovery order
	result := make([]EndpointInfo, 0, len(order))
	for _, ep := range order {
		result = append(result, endpointMap[ep])
	}

	return result, nil
}

// SelectEndpoint prompts the user to select an endpoint when multiple are available.
// If only one endpoint exists, returns it without prompting.
// If flagEndpoint is provided, validates it exists and returns it.
// Returns the selected endpoint URL.
func SelectEndpoint(endpoints []EndpointInfo, defaultEndpoint, flagEndpoint string) (string, error) {
	if len(endpoints) == 0 {
		return "", fmt.Errorf("no endpoints found in .sageox/ markers")
	}

	// if --endpoint flag was provided, use it
	if flagEndpoint != "" {
		// validate that the endpoint exists in discovered endpoints
		flagEndpoint = endpoint.NormalizeEndpoint(flagEndpoint)
		for _, ep := range endpoints {
			if endpoint.NormalizeEndpoint(ep.Endpoint) == flagEndpoint {
				return ep.Endpoint, nil
			}
		}
		return "", fmt.Errorf("endpoint %q not found in .sageox/ markers", flagEndpoint)
	}

	// if only one endpoint, use it without prompting
	if len(endpoints) == 1 {
		return endpoints[0].Endpoint, nil
	}

	// sort endpoints for consistent display
	sorted := make([]EndpointInfo, len(endpoints))
	copy(sorted, endpoints)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Endpoint < sorted[j].Endpoint
	})

	// find which endpoint is the default
	defaultIdx := -1
	normalizedDefault := endpoint.NormalizeEndpoint(defaultEndpoint)
	for i, ep := range sorted {
		if endpoint.NormalizeEndpoint(ep.Endpoint) == normalizedDefault {
			defaultIdx = i
			break
		}
	}

	// build options for selection menu
	options := make([]SelectOption[string], len(sorted))
	for i, ep := range sorted {
		label := ep.Endpoint
		if i == defaultIdx {
			label += " (default)"
		}
		options[i] = SelectOption[string]{Label: label, Value: ep.Endpoint}
	}

	// display interactive selection
	fmt.Println()
	selected, err := SelectOneValue("Multiple endpoints found. Select one:", options, defaultIdx)
	if err != nil {
		return "", fmt.Errorf("selection canceled: %w", err)
	}

	return selected, nil
}

// HasMultipleEndpoints is a quick check for whether endpoint selection is needed.
func HasMultipleEndpoints(sageoxDir string) bool {
	endpoints, err := DiscoverEndpoints(sageoxDir)
	if err != nil {
		return false
	}
	return len(endpoints) > 1
}
