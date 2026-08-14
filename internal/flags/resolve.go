package flags

import "context"

// Resolve merges patches from all providers in order (lowest to highest priority)
// and returns the final Flags. Starts from [Defaults] so every field has a
// safe value even if no provider has an opinion on it.
func Resolve(ctx context.Context, providers ...Provider) Flags {
	f := Defaults()
	for _, p := range providers {
		patch, _, err := p.Patch(ctx)
		if err != nil || patch == nil {
			continue
		}
		applyPatch(&f, patch)
	}
	return f
}

// applyPatch overlays non-nil fields from p onto f.
func applyPatch(f *Flags, p *Patch) {
	if p.CodeDBEnabled != nil {
		f.CodeDBEnabled = *p.CodeDBEnabled
	}
	if p.WhisperEnabled != nil {
		f.WhisperEnabled = *p.WhisperEnabled
	}
	if p.DistillEnabled != nil {
		f.DistillEnabled = *p.DistillEnabled
	}
	if p.AutoDistill != nil {
		f.AutoDistill = *p.AutoDistill
	}
	if p.TUIEnabled != nil {
		f.TUIEnabled = *p.TUIEnabled
	}
	if p.AttestEnabled != nil {
		f.AttestEnabled = *p.AttestEnabled
	}
	if p.DisableFileDeleteTools != nil {
		f.DisableFileDeleteTools = *p.DisableFileDeleteTools
	}
	if p.DisableShellExecTools != nil {
		f.DisableShellExecTools = *p.DisableShellExecTools
	}
	if p.PrimeAppend != nil {
		f.PrimeAppend = *p.PrimeAppend
	}
}

// boolPtr is a convenience helper for creating *bool literals in Patch structs.
func boolPtr(b bool) *bool { return &b }

// strPtr is a convenience helper for creating *string literals in Patch structs.
func strPtr(s string) *string { return &s }
