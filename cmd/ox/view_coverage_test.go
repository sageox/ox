package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveEndpoint_DefaultWhenNoEndpoints(t *testing.T) {
	t.Parallel()

	// non-existent directory means DiscoverEndpoints will fail,
	// so resolveEndpoint falls back to defaultEndpoint
	got, err := resolveEndpoint("/nonexistent/path", "https://sageox.ai", "")
	assert.NoError(t, err)
	assert.Equal(t, "https://sageox.ai", got)
}

func TestResolveEndpoint_DefaultWithEmptyFlagAndNoDiscovery(t *testing.T) {
	t.Parallel()

	// when discovery fails and no flag, returns default
	got, err := resolveEndpoint("/nonexistent/path", "https://default.sageox.ai", "")
	assert.NoError(t, err)
	assert.Equal(t, "https://default.sageox.ai", got)
}

func TestResolveEndpoint_FlagWithNoDiscovery(t *testing.T) {
	t.Parallel()

	// when discovery returns no endpoints and flag is provided,
	// the flag endpoint is returned (len(endpoints)==0, flagEndpoint!="")
	got, err := resolveEndpoint("/nonexistent/path", "https://default.sageox.ai", "https://custom.sageox.ai")
	assert.NoError(t, err)
	assert.Equal(t, "https://custom.sageox.ai", got)
}

func TestResolveEndpoint_EmptyEndpointSageoxDir(t *testing.T) {
	t.Parallel()

	// empty .sageox dir (no markers) returns default
	dir := t.TempDir()
	got, err := resolveEndpoint(dir, "https://default.sageox.ai", "")
	assert.NoError(t, err)
	assert.Equal(t, "https://default.sageox.ai", got)
}
