package main

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// flexVersionRe matches version patterns more broadly than agentx's strict X.Y.Z:
// handles X.Y, X.Y.Z, vX.Y.Z, and prefixed output like "tool v1.2".
var flexVersionRe = regexp.MustCompile(`v?(\d+\.\d+(?:\.\d+)?)`)

// detectAgentVersionFallback attempts version detection with a flexible regex
// when agentx's strict X.Y.Z pattern fails. Some CLIs output "X.Y" or
// prefixed versions that agentx doesn't match.
func detectAgentVersionFallback(agentType string) string {
	binary := agentTypeToBinary(agentType)
	if binary == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil {
		return ""
	}
	return extractFlexibleVersion(string(out))
}

// extractFlexibleVersion finds the first version-like pattern in text.
func extractFlexibleVersion(text string) string {
	m := flexVersionRe.FindStringSubmatch(strings.TrimSpace(text))
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

// agentTypeToBinary maps canonical agent type slugs to CLI binary names.
func agentTypeToBinary(agentType string) string {
	switch agentType {
	case "claude":
		return "claude"
	case "":
		return ""
	default:
		// most agents use their type as the binary name
		return agentType
	}
}
