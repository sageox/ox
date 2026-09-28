package flags_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sageox/ox/internal/flags"
)

// Missing remote fields must not accidentally release an experimental command.
func TestTraceRolloutAndLocalOverride(t *testing.T) {
	for _, tc := range []struct {
		name, payload, env string
		want               bool
	}{
		{"default", `{}`, "", false},
		{"null", `{"features":{"trace":null}}`, "", false},
		{"remote on", `{"features":{"trace":true}}`, "", true},
		{"remote off", `{"features":{"trace":false}}`, "", false},
		{"local on", `{"features":{"trace":false}}`, "1", true},
		{"local yes", `{}`, "yes", true},
		{"local true", `{}`, "TRUE", true},
		{"local off", `{"features":{"trace":true}}`, "0", false},
		{"invalid fails closed", `{}`, "maybe", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FEATURE_TRACE", tc.env)
			var settings flags.CLISettingsResponse
			if err := json.Unmarshal([]byte(tc.payload), &settings); err != nil {
				t.Fatal(err)
			}
			settings.FetchedAt = time.Now()
			got := flags.Resolve(context.Background(), flags.DaemonProvider{CachedSettings: &settings}, flags.EnvProvider{})
			if got.TraceEnabled != tc.want {
				t.Fatalf("TraceEnabled = %v, want %v", got.TraceEnabled, tc.want)
			}
		})
	}
}

// An absent env value must leave a remote opt-in intact, including cache upgrades.
func TestTraceAbsentSourcesHaveNoOpinion(t *testing.T) {
	if flags.Defaults().TraceEnabled {
		t.Fatal("trace must default off")
	}
	patch := flags.RemoteSettingsToPatch(&flags.CLISettingsResponse{})
	if patch.TraceEnabled != nil {
		t.Fatal("omitted remote trace must have no opinion")
	}
	t.Setenv("FEATURE_TRACE", "")
	patch, _, err := (flags.EnvProvider{}).Patch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if patch != nil && patch.TraceEnabled != nil {
		t.Fatal("unset trace env must have no opinion")
	}
}
