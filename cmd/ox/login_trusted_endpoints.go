package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/spf13/cobra"
)

// Trusted endpoints list (ox-v5de): first time `ox login` resolves an
// endpoint that hasn't been seen before, prompt the user to confirm
// before any device-flow polling, token persistence, or sync starts.
// Subsequent logins to a known endpoint are silent.
//
// Threat the prompt addresses: SAGEOX_ENDPOINT=https://attacker.example
// (typo, malicious shell config, hijacked terminal) silently directs
// the device flow at the attacker. The user typed `ox login` expecting
// to hit sageox.ai but ends up authenticating against a foreign host.
// The prompt makes the host change visible — even a "yes" answer
// creates an audit trail in the trusted endpoints file.
//
// Production endpoints (sageox.ai and *.sageox.ai) are pre-trusted —
// the prompt only fires for new hosts.

// trustedEndpointsFile is the persistence path. Stored under the user's
// XDG data dir so it survives upgrades and lives next to credentials.
func trustedEndpointsFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "sageox", "trusted-endpoints.json"), nil
}

// trustedEndpointsRecord is the on-disk representation. List, not map,
// to keep order stable and make manual inspection straightforward.
type trustedEndpointsRecord struct {
	Endpoints []string `json:"endpoints"`
}

// loadTrustedEndpoints returns the list of endpoint URLs the user has
// already approved. Missing file = empty list, not an error.
func loadTrustedEndpoints() ([]string, error) {
	path, err := trustedEndpointsFile()
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
	var rec trustedEndpointsRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	return rec.Endpoints, nil
}

// saveTrustedEndpoint appends ep to the persisted list. Idempotent —
// duplicates are quietly dropped. Mode 0600 because the file's content
// is not secret per se, but it's clearly user-state and the rest of
// ox's auth-adjacent files all use 0600.
func saveTrustedEndpoint(ep string) error {
	current, _ := loadTrustedEndpoints()
	for _, existing := range current {
		if strings.EqualFold(existing, ep) {
			return nil
		}
	}
	current = append(current, ep)
	rec := trustedEndpointsRecord{Endpoints: current}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	path, err := trustedEndpointsFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// isPreTrustedEndpoint returns true for endpoints that the ox binary
// knows are always safe (sageox.ai + *.sageox.ai). These never trigger
// the confirm prompt.
func isPreTrustedEndpoint(ep string) bool {
	normalized := strings.ToLower(strings.TrimSpace(ep))
	if normalized == "" {
		return false
	}
	if normalized == endpoint.Default {
		return true
	}
	// strip scheme for suffix check
	host := normalized
	if idx := strings.Index(host, "://"); idx != -1 {
		host = host[idx+3:]
	}
	// strip path
	if idx := strings.Index(host, "/"); idx != -1 {
		host = host[:idx]
	}
	// strip port
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		// only strip if it looks like a port (digits)
		port := host[idx+1:]
		isPort := len(port) > 0
		for _, c := range port {
			if c < '0' || c > '9' {
				isPort = false
				break
			}
		}
		if isPort {
			host = host[:idx]
		}
	}
	return host == "sageox.ai" || strings.HasSuffix(host, ".sageox.ai")
}

// confirmNewEndpointTrust is the ox-v5de gate. It returns nil to allow
// the login to proceed; non-nil to abort.
//
// Sequence:
//  1. Pre-trusted (sageox.ai family): allow silently.
//  2. Already on the user's trusted list: allow silently.
//  3. New endpoint: print a clear "first-use of this host" prompt with
//     the resolved URL, require a "yes" answer, then persist.
//
// OX_TRUST_ENDPOINT=1 short-circuits the prompt — useful for automation
// (CI, headless installs). Logged loudly so the override is visible.
func confirmNewEndpointTrust(cmd *cobra.Command, ep string) error {
	if ep == "" || isPreTrustedEndpoint(ep) {
		return nil
	}
	known, _ := loadTrustedEndpoints()
	for _, k := range known {
		if strings.EqualFold(k, ep) {
			return nil
		}
	}

	if os.Getenv("OX_TRUST_ENDPOINT") == "1" {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"WARNING: trusting new endpoint %s without prompt (OX_TRUST_ENDPOINT=1)\n", ep)
		return saveTrustedEndpoint(ep)
	}

	fmt.Fprintf(cmd.OutOrStdout(),
		"First-use confirmation: this is the first time `ox login` has been asked to talk\n"+
			"to %s.\n\n"+
			"If you did not expect this host (typo, malicious shell config, hijacked terminal),\n"+
			"answer No now. After Yes, this host is trusted for future logins.\n\n"+
			"Continue with login to %s? [yes/No]: ", ep, ep)

	scanner := bufio.NewScanner(cmd.InOrStdin())
	if !scanner.Scan() {
		return fmt.Errorf("login aborted: no answer to first-use confirmation")
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	if answer != "yes" && answer != "y" {
		return fmt.Errorf("login aborted: first-use confirmation declined for %s", ep)
	}
	if err := saveTrustedEndpoint(ep); err != nil {
		return fmt.Errorf("persist trusted endpoint: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Endpoint %s trusted for future logins.\n\n", ep)
	return nil
}
