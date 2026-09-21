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
