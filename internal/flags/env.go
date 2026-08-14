package flags

import (
	"context"
	"os"
	"strings"
)

// EnvProvider reads FEATURE_* environment variables and maps them onto flag
// fields. Only variables that are explicitly set (non-empty) contribute a
// patch — unset variables leave the corresponding field nil (no opinion).
//
// This preserves backward compatibility with the existing FEATURE_* conventions
// in internal/auth/feature.go without requiring changes to those call sites.
type EnvProvider struct{}

func (EnvProvider) Patch(_ context.Context) (*Patch, Source, error) {
	p := &Patch{
		DistillEnabled: envBoolPtr("FEATURE_MEMORY"),
		TUIEnabled:     envBoolPtr("FEATURE_TUI"),
		AttestEnabled:  envBoolPtr("FEATURE_ATTEST"),
		// FEATURE_AUTH and FEATURE_CLOUD are account-level; not mapped to Flags.
		// FEATURE_POST_MVP gates multiple unrelated features; callers continue to
		// use auth.IsPostMVPEnabled() directly until those features are broken out.
	}

	// return nil if no env var was set — provider has no opinion
	if allNil(p) {
		return nil, SourceEnv, nil
	}
	return p, SourceEnv, nil
}

// envBoolPtr returns a *bool if the named env var is explicitly set, nil if unset.
// Recognizes "true", "1", "yes" as true; anything else as false.
func envBoolPtr(name string) *bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	if v == "" {
		return nil
	}
	b := v == "true" || v == "1" || v == "yes"
	return &b
}

// allNil reports whether every field of p is nil.
func allNil(p *Patch) bool {
	return p.CodeDBEnabled == nil &&
		p.WhisperEnabled == nil &&
		p.DistillEnabled == nil &&
		p.AutoDistill == nil &&
		p.TUIEnabled == nil &&
		p.AttestEnabled == nil &&
		p.DisableFileDeleteTools == nil &&
		p.DisableShellExecTools == nil &&
		p.PrimeAppend == nil
}
