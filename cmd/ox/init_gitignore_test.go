//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateSageoxGitignore(t *testing.T) {
	tmpDir := t.TempDir()
	gitignorePath := filepath.Join(tmpDir, ".gitignore")

	err := createSageoxGitignore(gitignorePath)
	require.NoError(t, err, "createSageoxGitignore failed")

	// verify file exists
	require.FileExists(t, gitignorePath, ".gitignore was not created")

	// verify content has required entries
	content, err := os.ReadFile(gitignorePath)
	require.NoError(t, err, "failed to read .gitignore")

	for _, required := range requiredGitignoreEntries {
		assert.Contains(t, string(content), required, "expected .gitignore to contain %s", required)
	}
}

func TestMergeGitignoreEntries_EmptyContentAddsAll(t *testing.T) {
	result, changed := mergeGitignoreEntries("")
	assert.True(t, changed, "expected changed=true when merging into empty content")

	for _, required := range requiredGitignoreEntries {
		assert.Contains(t, result, required, "result missing required entry: %s", required)
	}
}

func TestMergeGitignoreEntries_AllPresent(t *testing.T) {
	content := sageoxGitignoreContent
	result, changed := mergeGitignoreEntries(content)
	assert.False(t, changed, "expected changed=false when all entries already present")
	assert.Equal(t, content, result, "expected content to be unchanged")
}

func TestMergeGitignoreEntries_MissingOne(t *testing.T) {
	content := `logs/
cache/
session.jsonl
sessions/
!README.md
!config.json
!discovered.jsonl
# missing !offline/`

	result, changed := mergeGitignoreEntries(content)
	assert.True(t, changed, "expected changed=true when entry is missing")

	assert.Contains(t, result, "!offline/", "result should contain missing !offline/ entry")
}

func TestMergeGitignoreEntries_ConflictingEntry(t *testing.T) {
	content := `logs/
cache/
discovered.jsonl
# this conflicts with !discovered.jsonl`

	result, changed := mergeGitignoreEntries(content)
	assert.True(t, changed, "expected changed=true when conflict exists")

	// conflicting "discovered.jsonl" should be removed
	lines := strings.Split(result, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		assert.NotEqual(t, "discovered.jsonl", trimmed, "conflicting entry 'discovered.jsonl' should have been removed")
	}

	// required "!discovered.jsonl" should be present
	assert.Contains(t, result, "!discovered.jsonl", "required entry '!discovered.jsonl' should be present")
}

func TestMergeGitignoreEntries_PreservesCommentsInContent(t *testing.T) {
	content := `# User comment
logs/
# Another comment
cache/
session.jsonl
sessions/
.needs-doctor
.needs-doctor-agent
agent_instances/
config.local.toml
ledger
teams/
!README.md
!config.json
!discovered.jsonl
!offline/`

	result, changed := mergeGitignoreEntries(content)
	assert.False(t, changed, "expected changed=false when all required entries present")

	assert.Contains(t, result, "# User comment", "user comments should be preserved")
	assert.Contains(t, result, "# Another comment", "user comments should be preserved")
}

func TestMergeGitignoreEntries_PreservesUserEntriesInContent(t *testing.T) {
	content := `logs/
cache/
session.jsonl
sessions/
.needs-doctor
.needs-doctor-agent
agent_instances/
config.local.toml
ledger
teams/
!README.md
!config.json
!discovered.jsonl
!offline/
# user's custom entries
*.swp
.DS_Store`

	result, changed := mergeGitignoreEntries(content)
	assert.False(t, changed, "expected changed=false when all required entries present")

	assert.Contains(t, result, "*.swp", "user's custom entries should be preserved")
	assert.Contains(t, result, ".DS_Store", "user's custom entries should be preserved")
}

func TestMergeGitignoreEntries_MultipleConflictsResolved(t *testing.T) {
	content := `logs/
cache/
discovered.jsonl
config.json
README.md
offline/`

	result, changed := mergeGitignoreEntries(content)
	assert.True(t, changed, "expected changed=true when multiple conflicts exist")

	// all conflicting entries should be removed
	conflicts := []string{"discovered.jsonl", "config.json", "README.md", "offline/"}
	lines := strings.Split(result, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, conflict := range conflicts {
			if trimmed == conflict {
				t.Errorf("conflicting entry '%s' should have been removed", conflict)
			}
		}
	}

	// all required keep entries should be present
	requiredKeeps := []string{"!discovered.jsonl", "!config.json", "!README.md", "!offline/"}
	for _, required := range requiredKeeps {
		assert.Contains(t, result, required, "required keep entry '%s' should be present", required)
	}
}
