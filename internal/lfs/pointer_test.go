package lfs

import (
	"testing"
)

func TestFormatPointer(t *testing.T) {
	oid := "sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393"
	size := int64(12345)

	got := FormatPointer(oid, size)

	want := "version https://git-lfs.github.com/spec/v1\n" +
		"oid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393\n" +
		"size 12345\n"

	if got != want {
		t.Errorf("FormatPointer() =\n%q\nwant:\n%q", got, want)
	}
}

func TestParsePointerRoundTrip(t *testing.T) {
	oid := "sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393"
	size := int64(98765)

	content := FormatPointer(oid, size)
	gotOID, gotSize, err := ParsePointer(content)
	if err != nil {
		t.Fatalf("ParsePointer() error: %v", err)
	}
	if gotOID != oid {
		t.Errorf("OID = %q, want %q", gotOID, oid)
	}
	if gotSize != size {
		t.Errorf("Size = %d, want %d", gotSize, size)
	}
}

func TestParsePointerErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"empty", ""},
		{"no version", "oid sha256:abc\nsize 100\n"},
		{"wrong version", "version something\noid sha256:abc\nsize 100\n"},
		{"missing oid", "version https://git-lfs.github.com/spec/v1\nsize 100\n"},
		{"missing size", "version https://git-lfs.github.com/spec/v1\noid sha256:abc\n"},
		{"bad size", "version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize notanumber\n"},
		{"too few lines", "version https://git-lfs.github.com/spec/v1\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ParsePointer(tt.content)
			if err == nil {
				t.Error("ParsePointer() expected error, got nil")
			}
		})
	}
}
