package orchestrators

import (
	"context"
	"regexp"
	"strings"

	"github.com/sageox/ox/pkg/agentx"
)

var semverRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// versionFromCommand runs a command and extracts a semver-like version string.
func versionFromCommand(env agentx.Environment, name string, args ...string) string {
	out, err := env.Exec(context.Background(), name, args...)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(out))
	if m := semverRe.FindString(text); m != "" {
		return m
	}
	return ""
}
