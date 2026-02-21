package agents

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/sageox/ox/pkg/agentx"
)

var semverRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// versionFromCommand runs a command and extracts a semver-like version string from its output.
func versionFromCommand(ctx context.Context, env agentx.Environment, name string, args ...string) string {
	out, err := env.Exec(ctx, name, args...)
	if err != nil {
		return ""
	}
	return extractVersion(string(out))
}

// extractVersion finds the first semver-like pattern (X.Y.Z) in text.
func extractVersion(text string) string {
	text = strings.TrimSpace(text)
	if m := semverRe.FindString(text); m != "" {
		return m
	}
	return ""
}

// packageJSON is a minimal struct for reading version from package.json files.
type packageJSON struct {
	Version string `json:"version"`
}

// versionFromPackageJSON reads a package.json file and returns the version field.
func versionFromPackageJSON(env agentx.Environment, path string) string {
	data, err := env.ReadFile(path)
	if err != nil {
		return ""
	}
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return ""
	}
	return pkg.Version
}
