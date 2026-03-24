package config

import "testing"

func TestFeatureMurmurEnabled(t *testing.T) {
	tests := []struct {
		value string
		unset bool
		want  bool
	}{
		{value: "true", want: true},
		{value: "1", want: true},
		{value: "false", want: false},
		{value: "0", want: false},
		{value: "", want: false},
		{unset: true, want: false},
	}

	for _, tt := range tests {
		name := tt.value
		if tt.unset {
			name = "<unset>"
		}
		t.Run(name, func(t *testing.T) {
			if tt.unset {
				t.Setenv("FEATURE_MURMUR", "")
			} else {
				t.Setenv("FEATURE_MURMUR", tt.value)
			}
			if got := FeatureMurmurEnabled(); got != tt.want {
				t.Errorf("FeatureMurmurEnabled() = %v, want %v (FEATURE_MURMUR=%q)", got, tt.want, tt.value)
			}
		})
	}
}
