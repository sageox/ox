package contextuse_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contextuse"
)

// The derived categories must stay the session.ScoreCategory literals so a
// derived category can be compared with a manual score and its threshold.
func TestCategoriesMatchSessionScoreCategories(t *testing.T) {
	valid := map[string]bool{}
	for _, c := range session.ValidScoreCategories() {
		valid[string(c)] = true
	}
	for _, c := range []string{
		contextuse.CategoryNone, contextuse.CategoryMinor,
		contextuse.CategoryModerate, contextuse.CategorySignificant,
	} {
		assert.True(t, valid[c], "category %q is not a session.ScoreCategory", c)
	}
	assert.Equal(t, string(session.ScoreNone), contextuse.CategoryNone)
	assert.Equal(t, string(session.ScoreMinor), contextuse.CategoryMinor)
	assert.Equal(t, string(session.ScoreModerate), contextuse.CategoryModerate)
	assert.Equal(t, string(session.ScoreSignificant), contextuse.CategorySignificant)
}
