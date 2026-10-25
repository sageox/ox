package theme

import (
	"testing"

	lipgloss "charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenCatalogMatchesGeneratedAdaptiveColors(t *testing.T) {
	generated := map[string]compat.AdaptiveColor{
		"Primary":   ColorPrimary,
		"Secondary": ColorSecondary,
		"Accent":    ColorAccent,
		"Success":   ColorSuccess,
		"Warning":   ColorWarning,
		"Error":     ColorError,
		"Info":      ColorInfo,
		"Dim":       ColorDim,
		"Public":    ColorPublic,
		"Private":   ColorPrivate,
	}

	for _, token := range Tokens {
		color, ok := generated[token.Name]
		if !ok {
			assert.Equal(t, CategoryWordmark, token.Category,
				"non-wordmark token %s has no generated adaptive color", token.Name)
			continue
		}
		require.NotEmpty(t, token.LightHex, token.Name+" light value")
		require.NotEmpty(t, token.DarkHex, token.Name+" dark value")
		assert.Equal(t, lipgloss.Color(token.LightHex), color.Light, token.Name+" light")
		assert.Equal(t, lipgloss.Color(token.DarkHex), color.Dark, token.Name+" dark")
	}
}
