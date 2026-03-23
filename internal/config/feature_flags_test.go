package config

import "testing"

func TestFeatureWhisperEnabled(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "unset returns false", value: "", want: false},
		{name: "true returns true", value: "true", want: true},
		{name: "1 returns true", value: "1", want: true},
		{name: "yes returns false", value: "yes", want: false},
		{name: "false returns false", value: "false", want: false},
		{name: "0 returns false", value: "0", want: false},
		{name: "TRUE returns false", value: "TRUE", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value == "" {
				t.Setenv("FEATURE_WHISPER", "")
			} else {
				t.Setenv("FEATURE_WHISPER", tt.value)
			}
			if got := FeatureWhisperEnabled(); got != tt.want {
				t.Errorf("FeatureWhisperEnabled() = %v, want %v (FEATURE_WHISPER=%q)", got, tt.want, tt.value)
			}
		})
	}
}
