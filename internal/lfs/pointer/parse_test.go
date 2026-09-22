package pointer_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/lfs/pointer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const versionLine = "version https://git-lfs.github.com/spec/v1"

// Empty artifacts and extension-bearing pointers must remain readable after
// parser extraction; oversized or malformed pointers must never request a blob.
func TestParse_ArtifactPointerContract(t *testing.T) {
	t.Setenv("OX_LFS_MAX_OBJECT_SIZE", "100")
	oid := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name    string
		content string
		size    int64
		err     string
	}{
		{name: "ordinary artifact", content: versionLine + "\noid " + oid + "\nsize 42\n", size: 42},
		{name: "empty artifact", content: versionLine + "\noid " + oid + "\nsize 0\n", size: 0},
		{name: "maximum size inclusive", content: versionLine + "\noid " + oid + "\nsize 100", size: 100},
		{name: "extension keys", content: versionLine + "\next-0-custom extra\noid " + oid + "\nsize 42\n", size: 42},
		{name: "size before object ID", content: versionLine + "\nsize 42\noid " + oid, size: 42},
		{name: "surrounding whitespace", content: "\n" + versionLine + "\noid " + oid + "\nsize 42\n\n", size: 42},
		{name: "not enough lines", content: versionLine + "\noid " + oid, err: "expected at least 3 lines"},
		{name: "empty input", err: "expected at least 3 lines"},
		{name: "wrong version", content: "version https://example.com/v1\noid " + oid + "\nsize 42", err: "missing version line"},
		{name: "missing object ID", content: versionLine + "\next-0-custom extra\nsize 42", err: "missing oid"},
		{name: "empty object ID", content: versionLine + "\noid \nsize 42", err: "missing oid"},
		{name: "missing size", content: versionLine + "\noid " + oid + "\next-0-custom extra", err: "missing or invalid size"},
		{name: "negative size", content: versionLine + "\noid " + oid + "\nsize -1", err: "missing or invalid size"},
		{name: "non-numeric size", content: versionLine + "\noid " + oid + "\nsize invalid", err: "parse size"},
		{name: "integer overflow", content: versionLine + "\noid " + oid + "\nsize 9223372036854775808", err: "parse size"},
		{name: "oversized artifact", content: versionLine + "\noid " + oid + "\nsize 101", err: "exceeds maximum 100"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actualOID, size, err := pointer.Parse(tc.content)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				assert.Empty(t, actualOID, "invalid pointers must not return a usable object ID")
				assert.Zero(t, size)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, oid, actualOID)
			assert.Equal(t, tc.size, size)
		})
	}
}

// Bad limit overrides must retain the default safety bound, while legitimate
// overrides must apply to the parser as well as the reported configuration.
func TestObjectSizeLimit_InvalidOverrideRetainsSafetyBound(t *testing.T) {
	for _, tc := range []struct {
		name, override string
		limit          int64
	}{
		{name: "unset", limit: pointer.DefaultMaxObjectSize},
		{name: "zero", override: "0", limit: pointer.DefaultMaxObjectSize},
		{name: "negative", override: "-1", limit: pointer.DefaultMaxObjectSize},
		{name: "non-numeric", override: "5GiB", limit: pointer.DefaultMaxObjectSize},
		{name: "overflow", override: "9223372036854775808", limit: pointer.DefaultMaxObjectSize},
		{name: "custom smaller", override: "42", limit: 42},
		{name: "custom larger", override: "6442450944", limit: 6442450944},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OX_LFS_MAX_OBJECT_SIZE", tc.override)
			assert.Equal(t, tc.limit, pointer.MaxObjectSize())
			oid := "sha256:" + strings.Repeat("b", 64)
			_, size, err := pointer.Parse(fmt.Sprintf("%s\noid %s\nsize %d", versionLine, oid, tc.limit))
			require.NoError(t, err)
			assert.Equal(t, tc.limit, size)
			_, _, err = pointer.Parse(fmt.Sprintf("%s\noid %s\nsize %d", versionLine, oid, tc.limit+1))
			require.ErrorContains(t, err, "exceeds maximum")
		})
	}
}

// Both LF and CRLF files must be recognized without classifying arbitrary
// artifact content containing the phrase git-lfs as a pointer version line.
func TestVersionLine_Recognition(t *testing.T) {
	for _, tc := range []struct {
		line string
		want bool
	}{
		{line: versionLine, want: true},
		{line: versionLine + "\r", want: true},
		{line: "version https://example.com/spec/v1"},
		{line: "git-lfs artifact content"},
		{line: "prefix " + versionLine},
		{line: ""},
	} {
		t.Run(tc.line, func(t *testing.T) {
			assert.Equal(t, tc.want, pointer.IsVersionLine(tc.line))
		})
	}
}
