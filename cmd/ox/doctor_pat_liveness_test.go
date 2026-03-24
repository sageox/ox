package main

import (
	"context"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckSlugGitPATLiveness_Registered(t *testing.T) {
	check := GetDoctorCheck(CheckSlugGitPATLiveness)
	require.NotNil(t, check, "git-pat-liveness check should be registered")
	assert.Equal(t, "Authentication", check.Category)
	assert.Equal(t, FixLevelAuto, check.FixLevel)
	assert.Equal(t, FixLevelAuto, check.FixLevel, "PAT liveness auto-repairs via credential sync")
}

func TestValidatePATLiveness_NoCredentials(t *testing.T) {
	ctx := context.Background()

	result := gitserver.ValidatePATLiveness(ctx, nil)
	assert.True(t, result.Skipped)
	assert.Contains(t, result.Reason, "no credentials")
}

func TestValidatePATLiveness_NoRepos(t *testing.T) {
	ctx := context.Background()

	creds := &gitserver.GitCredentials{
		Token:     "some-token",
		ServerURL: "https://git.sageox.ai",
	}
	result := gitserver.ValidatePATLiveness(ctx, creds)
	assert.True(t, result.Skipped)
	assert.Contains(t, result.Reason, "no repo URL")
}

func TestValidatePATLiveness_NetworkError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	creds := &gitserver.GitCredentials{
		Token:     "some-token",
		ServerURL: "https://192.0.2.1:1",
		Repos: map[string]gitserver.RepoEntry{
			"test": {URL: "https://192.0.2.1:1/test/repo.git"},
		},
	}
	result := gitserver.ValidatePATLiveness(ctx, creds)
	assert.True(t, result.Skipped)
	assert.Contains(t, result.Reason, "network unreachable")
}
