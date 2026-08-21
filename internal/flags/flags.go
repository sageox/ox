// Package flags provides a layered feature flag resolver for the ox CLI.
//
// Flags are resolved from multiple sources in ascending priority order:
//
//	defaults → daemon (remote settings) → env vars
//
// Each source returns a [Patch] — only the fields it has an explicit opinion
// on. Nil fields in a Patch mean "no opinion" and won't override prior values.
// This lets sources be added or removed without changing merge semantics.
//
// Usage:
//
//	// At CLI startup (zero network calls):
//	flags.Init(ctx, flags.DaemonProvider{CachedSettings: s}, flags.EnvProvider{})
//
//	// Anywhere in the process:
//	if flags.Get().DistillEnabled { ... }
package flags

import (
	"context"
	"sync"
)

// Flags is the fully resolved set of feature flags for the current process.
// All fields have safe defaults via [Defaults].
type Flags struct {
	// Feature gates. Mature capabilities default on; experimental capabilities
	// default off until a server rollout or environment override enables them.
	CodeDBEnabled  bool
	WhisperEnabled bool
	DistillEnabled bool
	AutoDistill    bool
	TUIEnabled     bool
	AttestEnabled  bool

	// Kill switches — default false (not activated).
	// Any source setting these true disables the capability.
	DisableFileDeleteTools bool
	DisableShellExecTools  bool

	// PrimeAppend is injected after team context in ox agent prime output.
	// Set by the remote settings endpoint to push org-wide prime additions
	// without requiring a CLI release or changes to CLAUDE.md.
	PrimeAppend string
}

// Patch is a partial flag override from a single source.
// Nil fields mean "no opinion" — they are skipped during merge.
// NOTE: When adding fields, also update allNil() in env.go and applyPatch() in resolve.go.
type Patch struct {
	CodeDBEnabled  *bool
	WhisperEnabled *bool
	DistillEnabled *bool
	AutoDistill    *bool
	TUIEnabled     *bool
	AttestEnabled  *bool

	DisableFileDeleteTools *bool
	DisableShellExecTools  *bool

	PrimeAppend *string
}

// Source identifies where a Patch came from, used by ox status display.
type Source int

const (
	SourceDefault Source = iota // hardcoded safe defaults
	SourceDaemon                // daemon IPC cache of /api/v1/cli/settings
	SourceEnv                   // FEATURE_* environment variable overrides
)

func (s Source) String() string {
	switch s {
	case SourceDefault:
		return "default"
	case SourceDaemon:
		return "daemon"
	case SourceEnv:
		return "env"
	default:
		return "unknown"
	}
}

// Provider supplies a partial set of flag overrides from one source.
// Returning (nil, source, nil) means this provider has no opinion right now.
type Provider interface {
	Patch(ctx context.Context) (*Patch, Source, error)
}

// Defaults returns the baseline Flags used when no provider has an opinion.
// Experimental gates and kill switches are off; string fields are empty.
func Defaults() Flags {
	return Flags{
		CodeDBEnabled:  true,
		WhisperEnabled: true,
		DistillEnabled: true,
		AutoDistill:    false, // off until remote settings explicitly enables it
		TUIEnabled:     false, // off until remote settings explicitly enables it
		AttestEnabled:  false, // experimental; hidden and unregistered until enabled
	}
}

// global holds the process-wide resolved flags, set by Init.
var (
	globalMu sync.RWMutex
	global   = Defaults()
)

// Init resolves flags from the given providers and stores the result globally.
// Providers are applied in the order given; later providers have higher priority.
// Safe to call multiple times (e.g., after daemon contact).
// Cost: no network calls — providers must not perform I/O during Patch().
func Init(ctx context.Context, providers ...Provider) {
	f := Resolve(ctx, providers...)
	globalMu.Lock()
	global = f
	globalMu.Unlock()
}

// Get returns the current process-wide resolved flags.
// Returns Defaults() if Init has not been called.
func Get() Flags {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return global
}
