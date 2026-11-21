package livesession

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckUploadURL(t *testing.T) {
	tests := []struct {
		url string
		ok  bool
	}{
		{"https://bucket.s3.us-west-2.amazonaws.com/key?X-Amz-Signature=abc", true},
		{"http://127.0.0.1:9000/key", true},
		{"http://localhost:9000/key", true},
		{"http://bucket.s3.amazonaws.com/key", false},
		{"http://10.0.0.5/key", false},
		{"https://user:pass@bucket.example.test/key", false},
		{"ftp://bucket.example.test/key", false},
		{"https:///key", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := checkUploadURL(tt.url) == nil; got != tt.ok {
			t.Errorf("checkUploadURL(%q) ok=%v, want %v", tt.url, got, tt.ok)
		}
	}
}

func TestForbiddenUploadHeaders(t *testing.T) {
	for _, h := range []string{"Authorization", "cookie", "Proxy-Authorization", "Ox-Signature"} {
		if !forbiddenUploadHeader(h) {
			t.Errorf("%s may reach storage", h)
		}
	}
	for _, h := range []string{"Content-Type", "x-amz-checksum-sha256", "x-amz-server-side-encryption"} {
		if forbiddenUploadHeader(h) {
			t.Errorf("%s was refused", h)
		}
	}
}

// A marker that could not be written must not stay behind empty: it would
// suppress the notice for good.
func TestNoticeMarkerIsRemovedWhenItsWriteFails(t *testing.T) {
	dir := t.TempDir()
	prev := writeNoticeMarker
	writeNoticeMarker = func(*os.File, string) error { return os.ErrInvalid }
	oweMediaNotice(dir)
	writeNoticeMarker = prev
	if _, err := os.Stat(filepath.Join(dir, mediaNoticeFile)); err == nil {
		t.Fatalf("empty marker left behind")
	}
	oweMediaNotice(dir)
	if !TakeMediaNotice(dir) {
		t.Fatalf("a later upload could not owe the notice")
	}
}
