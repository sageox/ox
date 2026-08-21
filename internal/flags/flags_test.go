package flags_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sageox/ox/internal/flags"
)

// bp returns a pointer to a bool value (test helper for CLIFeatures *bool fields).
func bp(b bool) *bool { return &b }

func TestDefaults(t *testing.T) {
	d := flags.Defaults()
	if !d.CodeDBEnabled {
		t.Error("CodeDBEnabled should default true")
	}
	if !d.WhisperEnabled {
		t.Error("WhisperEnabled should default true")
	}
	if !d.DistillEnabled {
		t.Error("DistillEnabled should default true")
	}
	if d.AutoDistill {
		t.Error("AutoDistill should default false")
	}
	if d.TUIEnabled {
		t.Error("TUIEnabled should default false")
	}
	if d.AttestEnabled {
		t.Error("AttestEnabled should default false")
	}
	if d.DisableFileDeleteTools {
		t.Error("DisableFileDeleteTools should default false")
	}
	if d.DisableShellExecTools {
		t.Error("DisableShellExecTools should default false")
	}
	if d.PrimeAppend != "" {
		t.Error("PrimeAppend should default empty")
	}
}

func TestResolveNoProviders(t *testing.T) {
	got := flags.Resolve(context.Background())
	want := flags.Defaults()
	if got != want {
		t.Errorf("Resolve() with no providers = %+v, want %+v", got, want)
	}
}

func TestEnvProviderUnset(t *testing.T) {
	os.Unsetenv("FEATURE_MEMORY")
	os.Unsetenv("FEATURE_TUI")
	os.Unsetenv("FEATURE_ATTEST")

	f := flags.Resolve(context.Background(), flags.EnvProvider{})
	// unset env vars should not change defaults
	if !f.DistillEnabled {
		t.Error("DistillEnabled should remain true when FEATURE_MEMORY unset")
	}
	if f.TUIEnabled {
		t.Error("TUIEnabled should remain false when FEATURE_TUI unset")
	}
	if f.AttestEnabled {
		t.Error("AttestEnabled should remain false when FEATURE_ATTEST unset")
	}
}

func TestEnvProviderDisablesFeature(t *testing.T) {
	t.Setenv("FEATURE_MEMORY", "false")

	f := flags.Resolve(context.Background(), flags.EnvProvider{})
	if f.DistillEnabled {
		t.Error("DistillEnabled should be false when FEATURE_MEMORY=false")
	}
}

func TestEnvProviderEnablesFeature(t *testing.T) {
	t.Setenv("FEATURE_TUI", "true")

	f := flags.Resolve(context.Background(), flags.EnvProvider{})
	if !f.TUIEnabled {
		t.Error("TUIEnabled should be true when FEATURE_TUI=true")
	}
}

func TestEnvProviderEnablesAttest(t *testing.T) {
	t.Setenv("FEATURE_ATTEST", "yes")

	f := flags.Resolve(context.Background(), flags.EnvProvider{})
	if !f.AttestEnabled {
		t.Error("AttestEnabled should be true when FEATURE_ATTEST=yes")
	}
}

func TestEnvProviderRecognisesVariants(t *testing.T) {
	for _, val := range []string{"1", "yes", "true"} {
		t.Setenv("FEATURE_MEMORY", val)
		f := flags.Resolve(context.Background(), flags.EnvProvider{})
		if !f.DistillEnabled {
			t.Errorf("DistillEnabled should be true for FEATURE_MEMORY=%q", val)
		}
	}
}

// TestEnvProviderCaseInsensitive verifies env vars are parsed case-insensitively,
// consistent with internal/auth/feature.go.
func TestEnvProviderCaseInsensitive(t *testing.T) {
	for _, val := range []string{"TRUE", "True", "YES", "Yes"} {
		t.Setenv("FEATURE_TUI", val)
		f := flags.Resolve(context.Background(), flags.EnvProvider{})
		if !f.TUIEnabled {
			t.Errorf("TUIEnabled should be true for FEATURE_TUI=%q", val)
		}
	}
}

func TestDaemonProviderNilSettings(t *testing.T) {
	p := flags.DaemonProvider{CachedSettings: nil}
	f := flags.Resolve(context.Background(), p)
	if f != flags.Defaults() {
		t.Error("DaemonProvider with nil cache should not change defaults")
	}
}

func TestDaemonProviderStaleCache(t *testing.T) {
	stale := &flags.CLISettingsResponse{
		Features: flags.CLIFeatures{
			CodeDB:  bp(false), // server wanted codedb off
			Whisper: bp(true),
			Distill: bp(true),
		},
		FetchedAt: time.Now().Add(-3 * time.Hour), // older than 2× max age
	}
	p := flags.DaemonProvider{CachedSettings: stale}
	f := flags.Resolve(context.Background(), p)
	// stale cache should be ignored — defaults apply
	if !f.CodeDBEnabled {
		t.Error("stale cache should be ignored; CodeDBEnabled should be default true")
	}
}

func TestDaemonProviderFreshCache(t *testing.T) {
	fresh := &flags.CLISettingsResponse{
		Features: flags.CLIFeatures{
			CodeDB:  bp(false), // server disabled codedb
			Whisper: bp(true),
			Distill: bp(true),
			Attest:  bp(true),
		},
		Killswitches: flags.CLIKillswitches{
			DisableFileDeleteTools: true,
		},
		PrimeAppend: "Always use snake_case.",
		FetchedAt:   time.Now(),
	}
	p := flags.DaemonProvider{CachedSettings: fresh}
	f := flags.Resolve(context.Background(), p)
	if f.CodeDBEnabled {
		t.Error("CodeDBEnabled should be false per remote settings")
	}
	if !f.AttestEnabled {
		t.Error("AttestEnabled should be true per remote settings")
	}
	if !f.DisableFileDeleteTools {
		t.Error("DisableFileDeleteTools kill switch should be active")
	}
	if f.PrimeAppend != "Always use snake_case." {
		t.Errorf("PrimeAppend = %q, want %q", f.PrimeAppend, "Always use snake_case.")
	}
}

// TestRemoteSettingsOmittedFieldsPreserveDefaults verifies that omitted JSON
// fields in CLIFeatures remain nil (no opinion), preventing accidental override
// of default-on flags during rolling upgrades.
func TestRemoteSettingsOmittedFieldsPreserveDefaults(t *testing.T) {
	// simulate a server response that only includes "codedb" — other fields are absent
	raw := `{"features":{"codedb":false},"killswitches":{},"fetched_at":"` +
		time.Now().Format(time.RFC3339) + `"}`
	var resp flags.CLISettingsResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	patch := flags.RemoteSettingsToPatch(&resp)
	if patch.CodeDBEnabled == nil || *patch.CodeDBEnabled != false {
		t.Error("CodeDBEnabled should be explicitly false")
	}
	// omitted fields must be nil so they don't override defaults
	if patch.WhisperEnabled != nil {
		t.Error("WhisperEnabled should be nil for omitted field")
	}
	if patch.DistillEnabled != nil {
		t.Error("DistillEnabled should be nil for omitted field")
	}
	if patch.AutoDistill != nil {
		t.Error("AutoDistill should be nil for omitted field")
	}
	if patch.TUIEnabled != nil {
		t.Error("TUIEnabled should be nil for omitted field")
	}
	if patch.AttestEnabled != nil {
		t.Error("AttestEnabled should be nil for omitted field")
	}

	// resolve should preserve defaults for omitted fields
	f := flags.Resolve(context.Background(), flags.DaemonProvider{
		CachedSettings: &resp,
	})
	if f.CodeDBEnabled {
		t.Error("CodeDBEnabled should be false (explicitly set)")
	}
	if !f.WhisperEnabled {
		t.Error("WhisperEnabled should remain default true (omitted)")
	}
	if !f.DistillEnabled {
		t.Error("DistillEnabled should remain default true (omitted)")
	}
	if f.AttestEnabled {
		t.Error("AttestEnabled should remain default false (omitted)")
	}
}

func TestEnvProviderOverridesRemoteAttestRollout(t *testing.T) {
	t.Setenv("FEATURE_ATTEST", "false")
	remote := flags.DaemonProvider{CachedSettings: &flags.CLISettingsResponse{
		Features:  flags.CLIFeatures{Attest: bp(true)},
		FetchedAt: time.Now(),
	}}

	f := flags.Resolve(context.Background(), remote, flags.EnvProvider{})
	if f.AttestEnabled {
		t.Error("FEATURE_ATTEST=false should override a server-side enable")
	}
}

func TestRemoteSettingsToPatchNil(t *testing.T) {
	if flags.RemoteSettingsToPatch(nil) != nil {
		t.Error("RemoteSettingsToPatch(nil) should return nil")
	}
}

func TestGlobalInitAndGet(t *testing.T) {
	t.Setenv("FEATURE_TUI", "true")
	flags.Init(context.Background(), flags.EnvProvider{})
	if !flags.Get().TUIEnabled {
		t.Error("Get() should reflect Init() result")
	}
	// reset to defaults for other tests
	flags.Init(context.Background())
}

func TestSaveThenLoadCachedSettings(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	ep := "https://sageox.ai"
	want := &flags.CLISettingsResponse{
		Features: flags.CLIFeatures{
			CodeDB:  bp(false),
			Whisper: bp(true),
			Distill: bp(true),
			TUI:     bp(true),
		},
		Killswitches: flags.CLIKillswitches{
			DisableFileDeleteTools: true,
		},
		PrimeAppend: "Always prefer snake_case.",
	}

	if err := flags.SaveCachedSettings(ep, want); err != nil {
		t.Fatalf("SaveCachedSettings: %v", err)
	}

	got, err := flags.LoadCachedSettings(ep)
	if err != nil {
		t.Fatalf("LoadCachedSettings: %v", err)
	}
	if got == nil {
		t.Fatal("LoadCachedSettings returned nil, want non-nil")
	}
	if !reflect.DeepEqual(got.Features, want.Features) {
		t.Errorf("Features = %+v, want %+v", got.Features, want.Features)
	}
	if got.Killswitches != want.Killswitches {
		t.Errorf("Killswitches = %+v, want %+v", got.Killswitches, want.Killswitches)
	}
	if got.PrimeAppend != want.PrimeAppend {
		t.Errorf("PrimeAppend = %q, want %q", got.PrimeAppend, want.PrimeAppend)
	}
}

func TestLoadCachedSettingsMissingFile(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	got, err := flags.LoadCachedSettings("https://sageox.ai")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing file, got: %+v", got)
	}
}

// TestLoadCachedSettingsCorruptJSON verifies that corrupt JSON in the cache file
// is treated as empty (returns nil, nil) rather than causing a hard error.
func TestLoadCachedSettingsCorruptJSON(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)

	ep := "https://sageox.ai"

	// save valid settings first to create the directory structure
	valid := &flags.CLISettingsResponse{
		Features: flags.CLIFeatures{CodeDB: bp(true)},
	}
	if err := flags.SaveCachedSettings(ep, valid); err != nil {
		t.Fatalf("SaveCachedSettings: %v", err)
	}

	// now overwrite with corrupt data
	cacheDir := filepath.Join(tmp, "sageox", "cli-settings")
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected cache file to exist")
	}
	corruptPath := filepath.Join(cacheDir, entries[0].Name())
	if err := os.WriteFile(corruptPath, []byte("{not valid json!!!"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, gotErr := flags.LoadCachedSettings(ep)
	if gotErr != nil {
		t.Errorf("expected nil error for corrupt cache, got: %v", gotErr)
	}
	if got != nil {
		t.Errorf("expected nil settings for corrupt cache, got: %+v", got)
	}
}

func TestLoadCachedSettingsWrongEndpoint(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	epA := "https://sageox.ai"
	epB := "https://staging.sageox.ai"

	r := &flags.CLISettingsResponse{
		Features: flags.CLIFeatures{CodeDB: bp(false)},
	}
	if err := flags.SaveCachedSettings(epA, r); err != nil {
		t.Fatalf("SaveCachedSettings: %v", err)
	}

	got, err := flags.LoadCachedSettings(epB)
	if err != nil {
		t.Fatalf("LoadCachedSettings: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for different endpoint, got: %+v", got)
	}
}

func TestSaveCachedSettingsIdempotent(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	epA := "https://sageox.ai"
	epB := "https://staging.sageox.ai"

	rA1 := &flags.CLISettingsResponse{
		Features:    flags.CLIFeatures{CodeDB: bp(true)},
		PrimeAppend: "first write",
	}
	rA2 := &flags.CLISettingsResponse{
		Features:    flags.CLIFeatures{CodeDB: bp(false)},
		PrimeAppend: "second write wins",
	}
	rB := &flags.CLISettingsResponse{
		Features: flags.CLIFeatures{Whisper: bp(false)},
	}

	// save both endpoints so we can confirm epB survives epA's second write
	if err := flags.SaveCachedSettings(epA, rA1); err != nil {
		t.Fatalf("SaveCachedSettings epA first: %v", err)
	}
	if err := flags.SaveCachedSettings(epB, rB); err != nil {
		t.Fatalf("SaveCachedSettings epB: %v", err)
	}
	if err := flags.SaveCachedSettings(epA, rA2); err != nil {
		t.Fatalf("SaveCachedSettings epA second: %v", err)
	}

	gotA, err := flags.LoadCachedSettings(epA)
	if err != nil || gotA == nil {
		t.Fatalf("LoadCachedSettings epA: err=%v, got=%v", err, gotA)
	}
	if gotA.PrimeAppend != "second write wins" {
		t.Errorf("epA PrimeAppend = %q, want %q", gotA.PrimeAppend, "second write wins")
	}
	if gotA.Features.CodeDB == nil || *gotA.Features.CodeDB {
		t.Error("epA CodeDB should be false after second write")
	}

	// epB must be unaffected by epA's second write (per-endpoint files make this trivial)
	gotB, err := flags.LoadCachedSettings(epB)
	if err != nil || gotB == nil {
		t.Fatalf("LoadCachedSettings epB: err=%v, got=%v", err, gotB)
	}
	if gotB.Features.Whisper == nil || *gotB.Features.Whisper {
		t.Error("epB Whisper should be false as originally saved")
	}
}

// TestAllNilCoversAllPatchFields uses reflection to verify that allNil checks
// every field in the Patch struct, catching drift when new fields are added.
func TestAllNilCoversAllPatchFields(t *testing.T) {
	patchType := reflect.TypeFor[flags.Patch]()
	flagsType := reflect.TypeFor[flags.Flags]()
	defaults := flags.Defaults()
	defaultsVal := reflect.ValueOf(defaults)

	// set each pointer field to non-nil one at a time and verify the patch
	// is detected as non-nil by the resolver (which uses allNil internally)
	for i := range patchType.NumField() {
		field := patchType.Field(i)
		p := flags.Patch{}
		v := reflect.ValueOf(&p).Elem()
		fv := v.Field(i)

		switch fv.Kind() {
		case reflect.Pointer:
			switch fv.Type().Elem().Kind() {
			case reflect.Bool:
				// find the corresponding Flags field and invert its default
				flagsField, ok := flagsType.FieldByName(field.Name)
				if !ok {
					// Patch and Flags field names may differ — just toggle to true
					b := true
					fv.Set(reflect.ValueOf(&b))
				} else {
					def := defaultsVal.FieldByIndex(flagsField.Index).Bool()
					flipped := !def
					fv.Set(reflect.ValueOf(&flipped))
				}
			case reflect.String:
				s := "test"
				fv.Set(reflect.ValueOf(&s))
			default:
				t.Errorf("unexpected pointer type for field %s: %v", field.Name, fv.Type())
				continue
			}
		default:
			t.Errorf("Patch field %s is not a pointer type — all Patch fields should be pointers", field.Name)
			continue
		}

		// resolve with a provider that returns this single-field patch —
		// if allNil incorrectly returns true, the field won't be applied
		ctx := context.Background()
		provider := &testPatchProvider{patch: &p}
		result := flags.Resolve(ctx, provider)

		if result == defaults {
			t.Errorf("Patch with only %s set was not applied — allNil may be missing this field", field.Name)
		}
	}
}

// testPatchProvider is a test helper that returns a fixed Patch.
type testPatchProvider struct {
	patch *flags.Patch
}

func (p *testPatchProvider) Patch(_ context.Context) (*flags.Patch, flags.Source, error) {
	return p.patch, flags.SourceEnv, nil
}
