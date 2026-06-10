package gitutil

import "testing"

func TestValidateHTTPSHost(t *testing.T) {
	allow := map[string]bool{"github.com": true, "objects.githubusercontent.com": true}
	tests := []struct {
		name    string
		url     string
		allow   map[string]bool
		wantErr bool
	}{
		{"allowed host", "https://github.com/o/r/releases/x", allow, false},
		{"allowed cdn host", "https://objects.githubusercontent.com/a/b", allow, false},
		{"disallowed host", "https://attacker.example.com/evil.tar.gz", allow, true},
		{"http rejected", "http://github.com/o/r", allow, true},
		{"any host when no allowlist", "https://anywhere.test/x", nil, false},
		{"empty", "", allow, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateHTTPSHost(tt.url, tt.allow)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateHTTPSHost(%q) err=%v, wantErr=%v", tt.url, err, tt.wantErr)
			}
		})
	}
}
