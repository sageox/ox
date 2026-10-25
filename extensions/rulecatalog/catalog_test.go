package rulecatalog

import (
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

func TestSelectRendersTargetRootAndStableFlatNames(t *testing.T) {
	t.Parallel()

	target := adapterprotocol.SkillTarget{
		Key: "droid-rules", Root: ".factory/rules",
		Format: adapterprotocol.RuleFormatMarkdownV1,
	}
	files, err := Select(target)
	require.NoError(t, err)
	require.Equal(t, []string{"ox-cli.md", "ox-cli-use-team-context.md"},
		[]string{files[0].Path, files[1].Path})
	require.Contains(t, string(files[1].Content), "`.factory/rules/`")
	require.NotContains(t, string(files[1].Content), "{{RULE_ROOT}}")
	for _, file := range files {
		require.True(t, strings.HasPrefix(string(file.Content), "---\ndescription: "))
	}
}

func TestSelectRejectsUnknownFormat(t *testing.T) {
	t.Parallel()
	_, err := Select(adapterprotocol.SkillTarget{Format: "unknown"})
	require.ErrorContains(t, err, "unsupported rule target format")
}

func TestDigestAndLegacyDescriptionsAreStableAndDefensive(t *testing.T) {
	t.Parallel()
	digest, err := Digest()
	require.NoError(t, err)
	require.Len(t, digest, 64)
	require.Equal(t, digest, mustDigest(t))

	descriptions := LegacyDescriptions()
	require.Equal(t, []string{PrimaryDescription, TeamContextDescription}, descriptions)
	descriptions[0] = "mutated by caller"
	require.Equal(t, PrimaryDescription, LegacyDescriptions()[0])
}

// TestCatalogIsLineEndingPinned: the catalog is embedded from the checkout, so
// a Windows clone that materialized these files with CRLF would make a
// Windows-built ox write different bytes AND report a different Digest than
// every other build — which the skills lockfile reads as drift that no repair
// can settle. Without normalization the failure is invisible on Linux and macOS
// and permanent on Windows.
func TestCatalogIsLineEndingPinned(t *testing.T) {
	t.Parallel()

	t.Run("no rendered rule carries CRLF", func(t *testing.T) {
		files, err := Select(adapterprotocol.SkillTarget{
			Key: "droid-rules", Root: ".factory/rules",
			Format: adapterprotocol.RuleFormatMarkdownV1,
		})
		require.NoError(t, err)
		require.NotEmpty(t, files)
		for _, file := range files {
			require.NotContains(t, string(file.Content), "\r\n",
				"rule %s would be written with CRLF", file.Path)
		}
	})

	t.Run("normalizeEOL rewrites CRLF and leaves LF alone", func(t *testing.T) {
		tests := []struct {
			name string
			in   string
			want string
		}{
			{"crlf frontmatter", "---\r\ndescription: x\r\n---\r\n", "---\ndescription: x\n---\n"},
			{"already lf", "---\ndescription: x\n", "---\ndescription: x\n"},
			{"lone cr is not a line ending", "a\rb", "a\rb"},
			{"empty", "", ""},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				require.Equal(t, tt.want, string(normalizeEOL([]byte(tt.in))))
			})
		}
	})
}

func mustDigest(t *testing.T) string {
	t.Helper()
	digest, err := Digest()
	require.NoError(t, err)
	return digest
}
