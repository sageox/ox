package gitutil

import (
	"slices"
	"testing"
)

func TestValidateCloneURL(t *testing.T) {
	hosts := []string{"sageox.ai", "sageox.io"}
	tests := []struct {
		name       string
		url        string
		hosts      []string
		allowLocal bool
		wantErr    bool
	}{
		{"https trusted host", "https://git.sageox.ai/team/ctx.git", hosts, false, false},
		{"https subdomain", "https://x.y.sageox.io/r.git", hosts, false, false},
		{"https any host when no allowlist", "https://github.com/o/r.git", nil, false, false},
		{"https untrusted host with allowlist", "https://github.com/o/r.git", hosts, false, true},
		{"ext transport RCE rejected even with allowLocal", "ext::sh -c 'touch /tmp/pwned'", nil, true, true},
		{"git scheme", "git://evil/r.git", nil, false, true},
		{"ssh scheme", "ssh://git@host/r.git", nil, false, true},
		{"file scheme rejected in production", "file:///etc/passwd", nil, false, true},
		{"file scheme allowed for local tests", "file:///tmp/x.bare", nil, true, false},
		{"bare local path rejected in production", "/tmp/x", nil, false, true},
		{"bare local path allowed for local tests", "/tmp/x", nil, true, false},
		{"http remote rejected", "http://github.com/o/r.git", nil, false, true},
		{"http localhost allowed", "http://localhost/r.git", nil, false, false},
		{"http 127.0.0.1 allowed", "http://127.0.0.1:8080/r.git", nil, false, false},
		{"leading dash flag injection", "--upload-pack=touch /tmp/x", nil, true, true},
		{"empty", "", nil, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCloneURL(tt.url, tt.hosts, tt.allowLocal)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCloneURL(%q, allowLocal=%v) err=%v, wantErr=%v", tt.url, tt.allowLocal, err, tt.wantErr)
			}
		})
	}
}

func TestHardenedCloneArgs(t *testing.T) {
	prod := HardenedCloneArgs(false)
	if !slices.Contains(prod, "protocol.ext.allow=never") {
		t.Error("production args must disable ext transport")
	}
	if !slices.Contains(prod, "protocol.file.allow=never") {
		t.Error("production args must disable file transport")
	}
	test := HardenedCloneArgs(true)
	if slices.Contains(test, "protocol.file.allow=never") {
		t.Error("test override must allow file transport")
	}
	if !slices.Contains(test, "protocol.ext.allow=never") {
		t.Error("ext transport must stay disabled even under file-transport test override")
	}
}
