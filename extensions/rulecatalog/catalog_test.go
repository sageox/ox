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

func mustDigest(t *testing.T) string {
	t.Helper()
	digest, err := Digest()
	require.NoError(t, err)
	return digest
}
